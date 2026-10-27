package pool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Backup keeps the record safe in two places:
//
//   - on this machine, a snapshot a day in <store>/backups: agents-<day>.db (SQLite's own copy of
//     agents.db, its WAL included) and state-<day>.json (the teams, tasks and messages), the last
//     14 days kept;
//   - with GitHub on, a private repository (default <login>/pengupool-memory; the owner can pick
//     any private repository the account can push to, or name a new one), created on the first
//     push. Every machine and account shares its one file, pengupool-agents.db: the Compass and its
//     signals, helper experience (its journal entries; no team has a record), and the tombstones
//     of those rows, as SQLite. Each round fetches it, merges it row by row into agents.db and
//     back (agentdb.Merge), commits and pushes, so what is learned anywhere reaches everywhere. The
//     gates stay on their machine. Nothing else is written there but the README of
//     a repository PenguPool created. Run when PenguPool starts and every few minutes.

// backupReadme is the README of a backup repository PenguPool created.
const backupReadme = "# PenguPool record\n\nShared by every machine and account that backs up here; written by PenguPool, do not edit. `pengupool-agents.db` is one SQLite file (open it read-only with any SQLite tool) of the owner's Compass (`compass`) with the signals behind it (`signals`, `signal_effects`), each helper's journal, one entry per finished job (`experience`), and the tombstones of deleted rows (`tombstones`). Gates stay on the machine that recorded them.\n"

const (
	backupKeep     = 14
	backupRepoName = "pengupool-memory"
	backupFile     = "pengupool-agents.db"
	compactSize    = 50 << 20             // the repository this big, or ...
	compactAge     = 180 * 24 * time.Hour // ... this long since its base commit, asks to be compacted
	compactKeep    = 30                   // days of commits a compaction keeps
)

type backupStatus struct {
	LastSnapshot string `json:"last_snapshot,omitempty"`
	LastPush     string `json:"last_push,omitempty"`
	LastChecked  string `json:"last_checked,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	Running      bool   `json:"running"`
	// CompactSuggested: the history is large or old (CompactReason); the owner's click compacts it.
	CompactSuggested bool   `json:"compact_suggested,omitempty"`
	CompactReason    string `json:"compact_reason,omitempty"`
}

func (p *Pool) backupView() map[string]any {
	p.mu.Lock()
	st := p.bstat
	p.mu.Unlock()
	cfg := p.st.Config().Backup
	return map[string]any{"github": cfg.GitHub, "interval_min": backupInterval(cfg) / time.Minute, "repo": cfg.Repo,
		"local_dir": filepath.Join(p.st.Dir(), store.BackupsDir), "status": st}
}

func backupInterval(c store.Backup) time.Duration {
	if c.IntervalMin < 1 || c.IntervalMin > 60 {
		return 5 * time.Minute
	}
	return time.Duration(c.IntervalMin) * time.Minute
}

func (p *Pool) setBackup(fn func(*backupStatus)) {
	p.mu.Lock()
	fn(&p.bstat)
	p.mu.Unlock()
}

// backupLoop runs the backups in the background: shortly after start, then on the interval. It
// never blocks an agent; a failure is kept for Settings and retried on the next round.
func (p *Pool) backupLoop(ctx context.Context) {
	wait := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_ = p.backupOnce(ctx)
		wait = backupInterval(p.st.Config().Backup)
	}
}

// backupOnce takes today's snapshot if it is missing and, with GitHub on, pushes the record.
func (p *Pool) backupOnce(ctx context.Context) error {
	if !p.backupMu.TryLock() {
		return conflict("a backup is already running")
	}
	defer p.backupMu.Unlock()
	p.setBackup(func(s *backupStatus) { s.Running = true })
	err := p.snapshotDaily()
	if err == nil && p.st.Config().Backup.GitHub {
		err = p.pushBackup(ctx)
	}
	p.setBackup(func(s *backupStatus) {
		s.Running, s.LastChecked, s.LastError = false, now(), ""
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if err != nil {
		p.logf("pool: backup: %v", err)
	}
	return err
}

func (p *Pool) snapshotDaily() error {
	if p.db == nil {
		return nil
	}
	dir := filepath.Join(p.st.Dir(), store.BackupsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	day := time.Now().Format("2006-01-02")
	db := filepath.Join(dir, "agents-"+day+".db")
	if _, err := os.Stat(db); err == nil {
		return nil
	}
	if err := p.db.Snapshot(db + ".tmp"); err != nil {
		_ = os.Remove(db + ".tmp")
		return fmt.Errorf("snapshot: %w", err)
	}
	_ = os.Chmod(db+".tmp", 0o600) // the record is the owner's alone
	if err := os.Rename(db+".tmp", db); err != nil {
		return err
	}
	if b, err := json.MarshalIndent(p.st.State(), "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "state-"+day+".json"), b, 0o600)
	}
	for _, pat := range []string{"agents-*.db", "state-*.json"} {
		old, _ := filepath.Glob(filepath.Join(dir, pat))
		slices.Sort(old) // dated names sort by day
		for len(old) > backupKeep {
			_ = os.Remove(old[0])
			old = old[1:]
		}
	}
	p.setBackup(func(s *backupStatus) { s.LastSnapshot = now() })
	return nil
}

var hostSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// pushBackup syncs the backup repository's file (syncShared). The repository is created
// private on the first push. A clone of another repository (the owner chose a new one) is moved
// aside, never deleted: it may hold unpushed rows.
func (p *Pool) pushBackup(ctx context.Context) error {
	repoDir := filepath.Join(p.st.Dir(), "sync", "repo")
	want, err := p.backupRepoSlug(ctx)
	if err != nil {
		return err
	}
	_, cloneErr := os.Stat(filepath.Join(repoDir, ".git"))
	if cloneErr == nil {
		origin, _ := p.gitIn(ctx, repoDir, "config", "--get", "remote.origin.url")
		if slug, _ := githubSlug(origin); !strings.EqualFold(slug, want) {
			aside := fmt.Sprintf("%s-%s-%d", repoDir, firstNonEmpty(hostSafe.ReplaceAllString(slug, "-"), "old"), time.Now().Unix())
			if err := os.Rename(repoDir, aside); err != nil {
				return err
			}
			p.logf("pool: backup: moved the clone of %s aside to %s", firstNonEmpty(slug, origin), aside)
			cloneErr = os.ErrNotExist
		}
	}
	if p.backupFor != want { // once a run: the repository may have turned public or dropped the account
		if cloneErr == nil {
			if exists, err := p.checkBackupRepo(ctx, want); err != nil {
				return err
			} else if !exists {
				return fmt.Errorf("%s is no longer on GitHub; pick another repository in Settings", want)
			}
		}
		login, err := p.ghLogin(ctx)
		if err != nil {
			return err
		}
		p.backupFor, p.backupLogin = want, login
		p.claimOwner(login)
	}
	if cloneErr != nil {
		if err := p.createBackupRepo(ctx, repoDir, want); err != nil {
			return err
		}
	}
	// Twice at most: a push another machine beat is merged again on top of its rows.
	for range 2 {
		if err = p.syncShared(ctx, repoDir); err == nil || !strings.Contains(err.Error(), "rejected") {
			break
		}
	}
	if err != nil {
		return err
	}
	p.setBackup(func(s *backupStatus) { s.LastPush = now() })
	return nil
}

// syncShared brings the clone to origin, merges pengupool-agents.db with agents.db both ways, and
// pushes the result when it changed. The clone is PenguPool's own: nothing in it is the owner's to
// keep.
func (p *Pool) syncShared(ctx context.Context, repoDir string) error {
	branch, err := p.fetchReset(ctx, repoDir)
	if err != nil {
		return err
	}
	p.noteHistory(ctx, repoDir)
	if p.db == nil {
		return nil
	}
	tmp, err := os.MkdirTemp(filepath.Dir(repoDir), "merge-") // beside the clone: the move into it is a rename
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	file, out := filepath.Join(repoDir, backupFile), filepath.Join(tmp, backupFile)
	// A 0.10 machine writes shared/*.jsonl: merged once when it is newer than the file, and removed
	// in the commit that writes the file.
	shared := filepath.Join(repoDir, "shared")
	legacy, _ := filepath.Glob(filepath.Join(shared, "*.jsonl"))
	if len(legacy) > 0 && p.commitTime(ctx, repoDir, "shared") > p.commitTime(ctx, repoDir, backupFile) {
		old := filepath.Join(tmp, "shared.db")
		if err := agentdb.ImportShared(shared, old, p.owner()); err != nil {
			return err
		}
		if _, err := p.db.Merge(old, old+".out"); err != nil {
			return err
		}
	}
	res, err := p.db.Merge(file, out)
	var bad *agentdb.DamagedError
	switch {
	case errors.As(err, &bad):
		aside, _ := p.keepDamaged(file)
		return fmt.Errorf("%s in the backup repository is %s; nothing was pushed (a copy is in %s)", backupFile, bad, aside)
	case err != nil:
		return err
	case res.Newer:
		return fmt.Errorf("%s in the backup repository is from a newer PenguPool; update PenguPool to back up", backupFile)
	}
	if res.Changes != "" {
		if err := os.Rename(out, file); err != nil {
			return err
		}
	}
	paths := []string{backupFile}
	if len(legacy) > 0 {
		if err := os.RemoveAll(shared); err != nil {
			return err
		}
		paths = append(paths, "shared")
	}
	// The repository holds this schema only: an earlier layout's folders go (envs/<host>/).
	if _, err := os.Stat(filepath.Join(repoDir, "envs")); err == nil {
		if err := os.RemoveAll(filepath.Join(repoDir, "envs")); err != nil {
			return err
		}
		paths = append(paths, "envs")
	}
	if b, err := os.ReadFile(filepath.Join(repoDir, "README.md")); err == nil {
		if strings.HasPrefix(string(b), "# PenguPool") && string(b) != backupReadme { // PenguPool's own: kept current
			_ = os.WriteFile(filepath.Join(repoDir, "README.md"), []byte(backupReadme), 0o644)
		}
		paths = append(paths, "README.md")
	}
	if _, err := p.gitIn(ctx, repoDir, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return err
	}
	if out, _ := p.gitIn(ctx, repoDir, "diff", "--cached", "--name-only"); out == "" {
		return nil
	}
	if _, err := p.gitIn(ctx, repoDir, "-c", "user.name=PenguPool", "-c", "user.email=pengupool@localhost", "commit", "-q", "-m", firstNonEmpty(res.Changes, "Move shared/ into "+backupFile)); err != nil {
		return err
	}
	_, err = p.ghGit(ctx, repoDir, "push", "-q", "origin", "HEAD:"+branch)
	return err
}

// fetchReset brings the clone to origin's default branch (main for an empty repository's first
// push) and names it.
func (p *Pool) fetchReset(ctx context.Context, repoDir string) (string, error) {
	branch := "main"
	if ref, err := p.gitIn(ctx, repoDir, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		branch = firstNonEmpty(strings.TrimPrefix(ref, "origin/"), branch)
	}
	if _, err := p.ghGit(ctx, repoDir, "fetch", "-q", "origin"); err != nil {
		return "", err
	}
	if _, err := p.gitIn(ctx, repoDir, "rev-parse", "-q", "--verify", "origin/"+branch); err == nil {
		if _, err := p.gitIn(ctx, repoDir, "reset", "-q", "--hard", "origin/"+branch); err != nil {
			return "", err
		}
	}
	return branch, nil
}

// commitTime is the time (unix seconds) of the last commit that touched path; 0 when none did.
func (p *Pool) commitTime(ctx context.Context, repoDir, path string) int64 {
	out, _ := p.gitIn(ctx, repoDir, "log", "-1", "--format=%ct", "--", path)
	n, _ := strconv.ParseInt(out, 10, 64)
	return n
}

// keepDamaged copies a damaged backup file into the store's backups folder, stamped and keyed by
// its content so the same file is kept once, and says where.
func (p *Pool) keepDamaged(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))[:8]
	dir := filepath.Join(p.st.Dir(), store.BackupsDir)
	if kept, _ := filepath.Glob(filepath.Join(dir, "pengupool-agents-*-"+sum+".db")); len(kept) > 0 {
		return kept[0], nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	aside := filepath.Join(dir, "pengupool-agents-"+time.Now().Format("20060102-150405")+"-"+sum+".db")
	return aside, os.WriteFile(aside, b, 0o600)
}

// compactReason says why the history should be compacted, or "" when it need not be.
func compactReason(size int64, age time.Duration) string {
	switch {
	case size >= compactSize:
		return fmt.Sprintf("the repository is %d MB", size>>20)
	case age >= compactAge:
		return fmt.Sprintf("its history starts %d days ago", int(age.Hours()/24))
	}
	return ""
}

// noteHistory sets whether Settings asks the owner to compact the clone's history: its size (packed
// and loose objects) and the age of its first commit.
func (p *Pool) noteHistory(ctx context.Context, repoDir string) {
	var reason string
	counts, err := p.gitIn(ctx, repoDir, "count-objects", "-v")
	root, err2 := p.gitIn(ctx, repoDir, "log", "--max-parents=0", "--reverse", "--format=%ct", "HEAD")
	if f := strings.Fields(root); err == nil && err2 == nil && len(f) > 0 {
		var kib int64
		for _, l := range strings.Split(counts, "\n") {
			if k, v, ok := strings.Cut(l, ": "); ok && (k == "size" || k == "size-pack") {
				n, _ := strconv.ParseInt(v, 10, 64)
				kib += n
			}
		}
		sec, _ := strconv.ParseInt(f[0], 10, 64)
		reason = compactReason(kib<<10, time.Since(time.Unix(sec, 0)))
	}
	p.setBackup(func(s *backupStatus) { s.CompactSuggested, s.CompactReason = reason != "", reason })
}

// backupCompact rewrites the backup repository's history, on the owner's click: a new first commit
// holding the repository as it was 30 days ago (or the oldest commit when none is older), the
// last 30 days of commits replayed on top with their messages and dates, pushed with
// --force-with-lease so a push that landed meanwhile is not lost. Other machines need nothing:
// every round fetches and resets, and merges from its own database.
func (p *Pool) backupCompact(ctx context.Context, _ struct{}) (any, error) {
	if !p.backupMu.TryLock() {
		return nil, conflict("a backup is running; try again in a moment")
	}
	defer p.backupMu.Unlock()
	repoDir := filepath.Join(p.st.Dir(), "sync", "repo")
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return nil, invalid("nothing to compact: no backup has been made yet")
	}
	branch, err := p.fetchReset(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	tip, err := p.gitIn(ctx, repoDir, "rev-parse", "origin/"+branch)
	if err != nil {
		return nil, invalid("nothing to compact: the repository is empty")
	}
	log, err := p.gitIn(ctx, repoDir, "log", "--first-parent", "--reverse", "--format=%H%x09%ct%x09%aI%x09%s", tip)
	if err != nil {
		return nil, err
	}
	type commit struct{ hash, date, subject string }
	var commits []commit
	base, cutoff := 0, time.Now().AddDate(0, 0, -compactKeep).Unix()
	for i, l := range strings.Split(log, "\n") {
		f := strings.SplitN(l, "\t", 4)
		if len(f) < 4 {
			continue
		}
		if ct, _ := strconv.ParseInt(f[1], 10, 64); ct <= cutoff {
			base = i
		}
		commits = append(commits, commit{f[0], f[2], f[3]})
	}
	if base == 0 {
		return nil, invalid("nothing to compact: no commit is older than %d days", compactKeep)
	}
	parent := ""
	for i, c := range commits[base:] {
		args := []string{"-c", "user.name=PenguPool", "-c", "user.email=pengupool@localhost", "commit-tree", c.hash + "^{tree}", "-m", c.subject}
		env := []string{"GIT_AUTHOR_DATE=" + c.date, "GIT_COMMITTER_DATE=" + c.date}
		if i == 0 { // the new base: the file as of then, made now
			args[len(args)-1] = "Compacted history: the record as of " + c.date
			env[1] = "GIT_COMMITTER_DATE=" + now()
		} else {
			args = append(args, "-p", parent)
		}
		if parent, err = p.gitEnv(ctx, repoDir, env, args...); err != nil {
			return nil, err
		}
	}
	if _, err := p.ghGit(ctx, repoDir, "push", "-q", "--force-with-lease=refs/heads/"+branch+":"+tip, "origin", parent+":refs/heads/"+branch); err != nil {
		return nil, err
	}
	// The old commits stay in the clone until its reflog lets them go.
	for _, args := range [][]string{{"reset", "-q", "--hard", parent}, {"reflog", "expire", "--expire=now", "--all"}, {"gc", "-q", "--prune=now"}} {
		if _, err := p.gitIn(ctx, repoDir, args...); err != nil {
			return nil, err
		}
	}
	p.noteHistory(ctx, repoDir)
	return p.backupView(), nil
}

// backupRepoSlug is the configured backup repository as owner/name: the owner's choice ("name" in
// the signed-in account, or "owner/name"), else <login>/pengupool-memory.
func (p *Pool) backupRepoSlug(ctx context.Context) (string, error) {
	return p.repoInAccount(ctx, firstNonEmpty(p.st.Config().Backup.Repo, backupRepoName))
}

// repoInAccount names repo as owner/name, a bare name being in the signed-in account.
func (p *Pool) repoInAccount(ctx context.Context, repo string) (string, error) {
	if strings.Contains(repo, "/") {
		return repo, nil
	}
	login, err := p.ghLogin(ctx)
	if err != nil {
		return "", err
	}
	return login + "/" + repo, nil
}

// ghLogin is the signed-in GitHub account's login.
func (p *Pool) ghLogin(ctx context.Context) (string, error) {
	login, err := p.ghRun(ctx, "", "api", "user", "--jq", ".login")
	if err != nil || login == "" {
		return "", invalid("sign in to GitHub in Settings first (%v)", err)
	}
	return login, nil
}

// checkBackupRepo tells whether repo (owner/name) exists and, when it does, that PenguPool may back
// up there: the account can push to it and it is private (memory can hold sensitive text).
func (p *Pool) checkBackupRepo(ctx context.Context, repo string) (bool, error) {
	out, err := p.ghRun(ctx, "", "repo", "view", repo, "--json", "viewerPermission,visibility")
	if err != nil {
		if strings.Contains(err.Error(), "Could not resolve to a Repository") {
			return false, nil
		}
		return false, err
	}
	var v struct {
		ViewerPermission string `json:"viewerPermission"`
		Visibility       string `json:"visibility"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return false, fmt.Errorf("gh repo view %s: %w", repo, err)
	}
	if !slices.Contains([]string{"ADMIN", "MAINTAIN", "WRITE"}, v.ViewerPermission) {
		return true, invalid("%s: your GitHub account can't push to it", repo)
	}
	if v.Visibility != "PRIVATE" {
		return true, invalid("%s exists and is not private; PenguPool backs up only to a private repository", repo)
	}
	return true, nil
}

// createBackupRepo clones the backup repository repo (owner/name) into dir, creating it private
// when it does not exist yet (in the account or an organization it may create repositories in),
// and records it in the config. Only a repository PenguPool created gets its README.
func (p *Pool) createBackupRepo(ctx context.Context, dir, repo string) error {
	exists, err := p.checkBackupRepo(ctx, repo)
	if err != nil {
		return err
	}
	if !exists {
		if _, err := p.ghRun(ctx, "", "repo", "create", repo, "--private", "--description", "PenguPool backup: the Compass and helper experience"); err != nil {
			return fmt.Errorf("create %s: %w", repo, err)
		}
	}
	_ = os.RemoveAll(dir) // a half-made clone from a failed first push
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if _, err := p.ghRun(ctx, "", "repo", "clone", repo, dir); err != nil {
		return fmt.Errorf("clone %s: %w", repo, err)
	}
	if created := !exists; created {
		_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte(backupReadme), 0o644)
	}
	cfg := p.st.Config()
	cfg.Backup.Repo = repo
	return p.st.SaveConfig(cfg)
}

type backupSetIn struct {
	GitHub      *bool   `json:"github,omitempty"`
	IntervalMin *int    `json:"interval_min,omitempty"`
	Repo        *string `json:"repo,omitempty"` // "name" or "owner/name"; "" for the default
}

var backupRepoRe = regexp.MustCompile(`^([A-Za-z0-9-]+/)?[A-Za-z0-9._-]+$`)

func (p *Pool) backupGet(_ context.Context, _ struct{}) (any, error) { return p.backupView(), nil }

// backupSet turns the GitHub backup on or off and sets its interval; turning it on pushes at once.
func (p *Pool) backupSet(ctx context.Context, in backupSetIn) (any, error) {
	cfg := p.st.Config()
	if in.GitHub != nil {
		cfg.Backup.GitHub = *in.GitHub
	}
	if in.IntervalMin != nil {
		if *in.IntervalMin < 1 || *in.IntervalMin > 60 {
			return nil, invalid("the interval is 1 to 60 minutes")
		}
		cfg.Backup.IntervalMin = *in.IntervalMin
	}
	moved := false
	if in.Repo != nil {
		r := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(*in.Repo), "https://github.com/"), ".git")
		if r != "" && !backupRepoRe.MatchString(r) {
			return nil, invalid("name the repository as name or owner/name")
		}
		if r != "" && r != cfg.Backup.Repo { // checked now so Settings shows why, not the next push
			var err error
			if r, err = p.repoInAccount(ctx, r); err != nil {
				return nil, err
			}
			if _, err := p.checkBackupRepo(ctx, r); err != nil {
				return nil, err
			}
		}
		moved = r != cfg.Backup.Repo
		cfg.Backup.Repo = r
	}
	// The next push clones the new repository; the old clone is moved aside then, and the old
	// repository is left as it is on GitHub.
	if !p.backupMu.TryLock() {
		return nil, conflict("a backup is running; try again in a moment")
	}
	err := p.st.SaveConfig(cfg)
	p.backupMu.Unlock()
	if err != nil {
		return nil, err
	}
	if (in.GitHub != nil && *in.GitHub) || (moved && cfg.Backup.GitHub) {
		go func() { _ = p.backupOnce(context.Background()) }()
	}
	return p.backupView(), nil
}

type backupRepo struct {
	FullName    string `json:"full_name"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
}

// backupRepos lists the private repositories the GitHub account can push to, for Settings'
// repository picker.
func (p *Pool) backupRepos(ctx context.Context, in refreshIn) (any, error) {
	all, err := p.githubRepos(ctx, in)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "can't list your GitHub repositories; sign in to GitHub in Settings first (%v)", err)
	}
	out := []backupRepo{}
	for _, r := range all.([]ghRepo) {
		if r.Push && r.Private {
			out = append(out, backupRepo{FullName: r.FullName, Private: r.Private, Description: r.Description})
		}
	}
	slices.SortFunc(out, func(a, b backupRepo) int {
		if a.Private != b.Private {
			if a.Private {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName))
	})
	return out, nil
}

// backupNow runs a backup and answers when it is done.
func (p *Pool) backupNow(ctx context.Context, _ struct{}) (any, error) {
	if err := p.backupOnce(ctx); err != nil {
		return nil, err
	}
	return p.backupView(), nil
}
