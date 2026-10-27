package pool

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/store"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// originFile writes the backup file as of rev in the bare repository to a temp file.
func originFile(t *testing.T, origin, rev string) string {
	t.Helper()
	b, err := exec.Command("git", "--git-dir", origin, "show", rev+":"+backupFile).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), backupFile)
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func dbInt(t *testing.T, file, q string) (n int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// backupMachine is a PenguPool with its own database, backing up to me/pengupool-memory.
func backupMachine(t *testing.T, env []string) *Pool {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.Config()
	cfg.Backup.GitHub, cfg.Backup.Repo = true, "me/pengupool-memory"
	_ = st.SaveConfig(cfg)
	return New(Options{Store: st, Exe: "pengupool", Env: env})
}

func addLine(t *testing.T, p *Pool, body string) {
	t.Helper()
	if _, err := p.db.PutLine(agentdb.Line{Owner: p.owner(), Body: body, Source: "owner"}); err != nil {
		t.Fatal(err)
	}
}

// Two machines share one file: both machines' rows survive, the file holds the shared tables only,
// a commit counts its changes by table, and a round with nothing new commits nothing.
func TestBackupFileTwoMachines(t *testing.T) {
	gh, env := fakeGitHub(t)
	origin := filepath.Join(gh, "me", "pengupool-memory.git")
	ctx := context.Background()
	a, b := backupMachine(t, env), backupMachine(t, env)
	addLine(t, a, "from a")
	addLine(t, b, "from b")
	_, _ = a.db.AddExperience(agentdb.Entry{Helper: "k", Title: "t", Body: "b"})
	for _, p := range []*Pool{a, b, a} {
		if err := p.pushBackup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	f := originFile(t, origin, "main")
	if n := dbInt(t, f, `SELECT count(*) FROM compass`); n != 2 {
		t.Fatalf("compass lines in the file: %d", n)
	}
	if n := dbInt(t, f, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('gates', 'meta')`); n != 0 {
		t.Fatalf("the file holds %d local tables", n)
	}
	if n := dbInt(t, f, `PRAGMA user_version`); n != agentdb.SchemaVersion {
		t.Fatalf("user_version %d", n)
	}
	subjects := gitOut(t, origin, "log", "--format=%s", "main")
	if !strings.Contains(subjects, "compass +1, experience +1") || !strings.Contains(subjects, "compass +1") {
		t.Fatalf("commit messages:\n%s", subjects)
	}
	before := gitOut(t, origin, "rev-list", "--count", "main")
	for _, p := range []*Pool{b, a} {
		if err := p.pushBackup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if after := gitOut(t, origin, "rev-list", "--count", "main"); after != before {
		t.Fatalf("nothing changed, yet %s → %s commits", before, after)
	}
}

// A damaged file stops the round: nothing is pushed, the file is kept aside and Settings says why.
// A file from a newer PenguPool is merged but not written over.
func TestBackupDamagedAndNewerFile(t *testing.T) {
	gh, env := fakeGitHub(t)
	origin := filepath.Join(gh, "me", "pengupool-memory.git")
	ctx := context.Background()
	p := backupMachine(t, env)
	addLine(t, p, "mine")
	if err := p.pushBackup(ctx); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(gh, "work")
	gitOut(t, gh, "clone", "-q", origin, work)
	push := func(msg string) string {
		gitOut(t, work, "commit", "-qam", msg)
		gitOut(t, work, "push", "-q", "origin", "HEAD:main")
		return gitOut(t, origin, "rev-parse", "main")
	}
	good, _ := os.ReadFile(filepath.Join(work, backupFile))
	_ = os.WriteFile(filepath.Join(work, backupFile), append([]byte("garbage"), good[7:]...), 0o644)
	head := push("break it")
	if err := p.backupOnce(ctx); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damaged file: %v", err)
	}
	if got := gitOut(t, origin, "rev-parse", "main"); got != head {
		t.Fatal("a round pushed over a damaged file")
	}
	if kept, _ := filepath.Glob(filepath.Join(p.st.Dir(), store.BackupsDir, "pengupool-agents-*.db")); len(kept) != 1 {
		t.Fatalf("kept aside: %v", kept)
	}
	if st := p.backupView()["status"].(backupStatus); !strings.Contains(st.LastError, "damaged") {
		t.Fatalf("status %+v", st)
	}
	_ = p.backupOnce(ctx) // the same file is kept once
	if kept, _ := filepath.Glob(filepath.Join(p.st.Dir(), store.BackupsDir, "pengupool-agents-*.db")); len(kept) != 1 {
		t.Fatalf("kept again: %v", kept)
	}
	// Restored, then made newer than this PenguPool.
	_ = os.WriteFile(filepath.Join(work, backupFile), good, 0o644)
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(work, backupFile))
	_, err := db.Exec(`PRAGMA user_version = 99`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	head = push("newer")
	addLine(t, p, "never pushed")
	if err := p.backupOnce(ctx); err == nil || !strings.Contains(err.Error(), "update PenguPool") {
		t.Fatalf("newer file: %v", err)
	}
	if got := gitOut(t, origin, "rev-parse", "main"); got != head {
		t.Fatal("a round pushed over a newer file")
	}
}

// A 0.10 machine's shared/ is merged once and removed in the commit that writes the file.
func TestBackupMigratesShared(t *testing.T) {
	gh, env := fakeGitHub(t)
	origin := filepath.Join(gh, "me", "pengupool-memory.git")
	ctx := context.Background()
	p := backupMachine(t, env)
	gitOut(t, gh, "init", "-q", "--bare", "-b", "main", origin) // a 0.10 machine's repository: no file yet
	_ = os.WriteFile(filepath.Join(gh, "me", "pengupool-memory.json"), []byte(`{"viewerPermission":"ADMIN","visibility":"PRIVATE"}`), 0o644)
	work := filepath.Join(gh, "work")
	gitOut(t, gh, "clone", "-q", origin, work)
	gitOut(t, work, "checkout", "-q", "-b", "main")
	_ = os.MkdirAll(filepath.Join(work, "shared"), 0o755)
	_ = os.WriteFile(filepath.Join(work, "shared", "compass.jsonl"), []byte(`{"uid":"u1","owner":"me","body":"from 0.10","kind":"preference","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n"), 0o644)
	gitOut(t, work, "add", "-A")
	gitOut(t, work, "commit", "-qm", "0.10")
	gitOut(t, work, "push", "-q", "origin", "HEAD:main")
	before := gitOut(t, origin, "rev-list", "--count", "main")
	if err := p.pushBackup(ctx); err != nil {
		t.Fatal(err)
	}
	if ls, _ := p.db.Lines(p.owner(), "", "", 0); len(ls) != 1 {
		t.Fatalf("shared/ rows did not come in: %+v", ls)
	}
	files := gitOut(t, origin, "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(files, "shared/") || !strings.Contains(files, backupFile) {
		t.Fatalf("after the migration:\n%s", files)
	}
	if got := gitOut(t, origin, "rev-list", "--count", "main"); got != "2" || before != "1" {
		t.Fatalf("commits %s → %s, want one for both the file and the removal", before, got)
	}
	if n := dbInt(t, originFile(t, origin, "main"), `SELECT count(*) FROM compass WHERE body = 'from 0.10'`); n != 1 {
		t.Fatal("the file lacks the migrated line")
	}
}

// A push another machine beat is fetched, merged and pushed again once.
func TestBackupPushRace(t *testing.T) {
	gh, env := fakeGitHub(t)
	origin := filepath.Join(gh, "me", "pengupool-memory.git")
	p := backupMachine(t, env)
	if err := p.pushBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(origin, "hooks", "pre-receive") // refuses the first push, as a lost race does
	marker := filepath.Join(gh, "refused")
	_ = os.WriteFile(hook, []byte("#!/bin/sh\nif [ ! -f "+marker+" ]; then touch "+marker+"; echo raced >&2; exit 1; fi\n"), 0o755)
	addLine(t, p, "after the race")
	if err := p.pushBackup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the first push was not refused")
	}
	if n := dbInt(t, originFile(t, origin, "main"), `SELECT count(*) FROM compass WHERE body = 'after the race'`); n != 1 {
		t.Fatal("the retry did not push")
	}
}

// Compacting makes a new first commit holding the file as of 30 days ago, replays the last 30 days
// on top and force-pushes; every row survives, another machine carries on, and the prompt clears.
func TestBackupCompact(t *testing.T) {
	gh, env := fakeGitHub(t)
	origin := filepath.Join(gh, "me", "pengupool-memory.git")
	ctx := context.Background()
	a, b := backupMachine(t, env), backupMachine(t, env)
	for i, c := range []struct {
		days int
		body string
	}{{200, "l1"}, {50, "l2"}, {40, "l3"}, {10, "l4"}, {0, "l5"}} {
		d := time.Now().AddDate(0, 0, -c.days).Format(time.RFC3339)
		a.env = append(a.env, "GIT_AUTHOR_DATE="+d, "GIT_COMMITTER_DATE="+d)
		addLine(t, a, c.body)
		if err := a.pushBackup(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 1 { // a base 200 days old: the next round asks
			if st := a.backupView()["status"].(backupStatus); !st.CompactSuggested || !strings.Contains(st.CompactReason, "200 days") {
				t.Fatalf("status %+v", st)
			}
		}
	}
	old := gitOut(t, origin, "rev-parse", "main")
	if _, err := a.backupCompact(ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, origin, "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("commits after compacting: %s", got)
	}
	root := gitOut(t, origin, "rev-list", "--max-parents=0", "main")
	if n := dbInt(t, originFile(t, origin, root), `SELECT count(*) FROM compass`); n != 3 {
		t.Fatalf("the new base holds %d lines, want the 3 of 40 days ago", n)
	}
	if n := dbInt(t, originFile(t, origin, "main"), `SELECT count(*) FROM compass`); n != 5 {
		t.Fatalf("the tip holds %d lines", n)
	}
	if subjects := gitOut(t, origin, "log", "--format=%s", "main"); strings.Count(subjects, "compass +1") != 2 || !strings.Contains(subjects, "Compacted history") {
		t.Fatalf("subjects:\n%s", subjects)
	}
	if gitOut(t, origin, "rev-parse", "main") == old {
		t.Fatal("history not rewritten")
	}
	if st := a.backupView()["status"].(backupStatus); st.CompactSuggested {
		t.Fatalf("still suggested: %+v", st)
	}
	if err := b.pushBackup(ctx); err != nil { // another machine's first round, on the rewritten history
		t.Fatal(err)
	}
	if ls, _ := b.db.Lines(b.owner(), "", "", 0); len(ls) != 5 {
		t.Fatalf("b has %d lines", len(ls))
	}
	if _, err := a.backupCompact(ctx, struct{}{}); err == nil {
		t.Fatal("compacted a history with nothing older than 30 days")
	}
}

func TestCompactReason(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		size int64
		age  time.Duration
		want string
	}{{compactSize - 1, compactAge - day, ""}, {compactSize, day, "the repository is 50 MB"}, {1 << 20, compactAge, "its history starts 180 days ago"}} {
		if got := compactReason(c.size, c.age); got != c.want {
			t.Errorf("%d bytes, %v: %q, want %q", c.size, c.age, got, c.want)
		}
	}
}
