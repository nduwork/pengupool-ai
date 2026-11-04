package pool

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/hub"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

type sourceIn struct {
	Kind   string `json:"kind"` // local | clone | repo | worktree (a new worktree of the repository at Path)
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
	Dest   string `json:"dest,omitempty"`
	RepoID string `json:"repo_id,omitempty"`

	// worktree: Branch is made from From (empty: the repository's HEAD) when NewBranch, else checked out.
	Branch    string `json:"branch,omitempty"`
	NewBranch bool   `json:"new_branch,omitempty"`
	From      string `json:"from,omitempty"`
}

type launchIn struct {
	Name    string   `json:"name"`
	Harness string   `json:"harness"`
	Command []string `json:"command,omitempty"`
	Model   string   `json:"model,omitempty"`
	Via     string   `json:"via,omitempty"` // "ollama": Claude Code through `ollama launch claude`
	Source  sourceIn `json:"source"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
}

// launch starts a main session from a local directory, a clone, or a registered repository.
// The session is not part of a team; a team is formed from a task or by adding sessions.
func (p *Pool) launch(ctx context.Context, in launchIn) (any, error) {
	// A PenguPool session is always an agent runtime; plain terminals belong in the user's terminal app.
	if in.Harness == "" {
		in.Harness = harness.Claude
	}
	if err := harness.Dropped(in.Harness); err != nil {
		return nil, invalid("%v", err)
	}
	if !harness.IsAgent(in.Harness) {
		return nil, invalid("a PenguPool session runs an agent (%s), not %q", agentIDs(), in.Harness)
	}
	repo, err := p.resolveSource(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = p.uniqueName(repo.Name + "-" + in.Harness)
	}
	id, cwd, trust := protocol.NewID(), repo.Path, ""
	if in.Source.Kind == "worktree" {
		if cwd, err = p.addWorktree(ctx, repo, in.Source, name, id); err != nil {
			return nil, err
		}
		trust = repo.Path
	}
	s, err := p.spawn(ctx, spawnReq{
		ID: id, Name: name, Harness: in.Harness, Command: in.Command, Model: in.Model, Via: in.Via,
		Cwd: cwd, TrustFrom: trust, Cols: in.Cols, Rows: in.Rows,
	})
	if err != nil {
		// The worktree this launch just made goes with it, and so does its new branch while that has
		// nothing of its own (safe -d: a branch from a From that HEAD does not contain stays). Any
		// pretrust entry for the folder stays in the agent's list: harmless, as the folder is gone.
		if trust != "" {
			_ = removeWorktree(repo.Path, cwd, map[bool]string{true: in.Source.Branch}[in.Source.NewBranch], true)
		}
		return nil, err
	}
	return s.View(), nil
}

// addWorktree makes a new session's worktree in PenguPool's own folder, so the repository's folder
// gets no siblings: on a new branch from src.From (the repository's HEAD when empty), or on an
// existing branch no other worktree has checked out. git's own refusal is the error. Both start from
// one of the repository's own local branches: the folder is pretrusted when the repository is, so it
// must not start from a fork's or a remote's commits (and their agent settings) the owner never saw.
func (p *Pool) addWorktree(ctx context.Context, repo protocol.Repo, src sourceIn, name, id string) (string, error) {
	if !repo.Git {
		return "", invalid("%s is not a Git repository", repo.Path)
	}
	local := func(b string) bool {
		return b != "" && !strings.HasPrefix(b, "-") && exec.CommandContext(ctx, "git", "-C", repo.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+b).Run() == nil
	}
	if src.NewBranch && (src.Branch == "" || strings.HasPrefix(src.Branch, "-") || src.From != "" && !local(src.From)) || !src.NewBranch && !local(src.Branch) {
		return "", invalid("choose one of the repository's branches")
	}
	wt := filepath.Join(p.st.Dir(), "worktrees", "sessions", slug(name)+"-"+idTail(id))
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", err
	}
	args := []string{"-C", repo.Path, "worktree", "add", wt, src.Branch}
	if src.NewBranch {
		args = []string{"-C", repo.Path, "worktree", "add", "-b", src.Branch, wt}
		if src.From != "" {
			args = append(args, src.From)
		}
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return "", invalid("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	return wt, nil
}

type spawnReq struct {
	ID, Name, Harness, Model, Cwd, Resume string
	Effort                                string
	Via                                   string // the launcher it runs through (see ollama.go)
	SkillID                               string // a helper's Skill library entry, loaded for this session only
	TrustFrom                             string // the repository a worktree Cwd was made from (see pretrustFolder)
	Command                               []string
	ReadDirs                              []string // a helper may read these outside its Cwd (see harness.Params)
	Cols, Rows                            int
	Decorate                              func(*store.Session)
}

// spawn builds the harness command and starts a worker.
func (p *Pool) spawn(ctx context.Context, r spawnReq) (store.Session, error) {
	if r.Harness == "" {
		r.Harness = harness.Claude
	}
	if r.ID == "" {
		r.ID = protocol.NewID()
	}
	if r.Cols <= 0 || r.Rows <= 0 {
		r.Cols, r.Rows = 120, 32
	}
	if st, err := os.Stat(r.Cwd); err != nil || !st.IsDir() {
		return store.Session{}, invalid("workspace %s is not a directory", r.Cwd)
	}
	dec, model, dirs := r.Decorate, r.Model, slices.Clone(r.ReadDirs)
	r.Decorate = func(s *store.Session) { // kept for a restart or a restore
		s.LaunchModel, s.LaunchReadDirs, s.Launched = model, dirs, true
		if dec != nil {
			dec(s)
		}
	}
	if p.testSpawn != nil {
		return p.testSpawn(r)
	}
	if r.Harness == harness.Muse { // a Muse installed or upgraded since the daemon started gets its hooks first
		harness.EnsureMusePlugin(p.st.Dir(), p.exe, p.env, p.logf)
	}
	if (r.SkillID != "" || r.TrustFrom != "") && r.Resume == "" { // a helper or a worktree starts in a folder PenguPool made: trust it, so no dialog waits for the owner
		if err := pretrustFolder(r.Harness, r.Cwd, r.TrustFrom); err != nil {
			p.logf("pool: trust %s for %s: %v", r.Cwd, r.Harness, err)
		}
	}
	plain := r.Harness == harness.Shell
	wrap := ""
	switch r.Via {
	case "":
	case ViaOllama:
		if r.Harness != harness.Claude {
			return store.Session{}, invalid("only Claude Code runs on Ollama, not %s", r.Harness)
		}
		var err error
		if wrap, err = p.ollamaWrap(ctx); err != nil {
			return store.Session{}, err
		}
		r.Model = "" // Ollama's model, named in the wrap
	default:
		return store.Session{}, invalid("unknown launcher %q", r.Via)
	}
	cmd, env, err := harness.Spec(r.Harness, harness.Params{
		SessionID: r.ID, Name: r.Name, Cwd: r.Cwd, Exe: p.exe, StoreDir: p.st.Dir(), Wrap: wrap,
		Model: r.Model, Effort: r.Effort, Resume: r.Resume, Command: r.Command, Env: p.env,
		SkillRepo: p.skillRepo(r.SkillID), ReadDirs: r.ReadDirs,
	})
	if plain {
		env = append([]string(nil), p.env...) // a plain shell is no session: none of the session markers
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "harness_unavailable") {
			return store.Session{}, protocol.Errorf(protocol.CodeHarnessUnavailable, "%v", err)
		}
		return store.Session{}, invalid("%v", err)
	}
	runlog := p.runLogPath(r.ID, r.Name)
	s, err := p.hub.Spawn(ctx, hub.SpawnSpec{
		ID: r.ID, Name: r.Name, Cwd: r.Cwd, Harness: r.Harness, Command: cmd, Env: env,
		Cols: r.Cols, Rows: r.Rows,
		Decorate: func(s *store.Session) {
			s.RunLog, s.Via = runlog, r.Via
			// Starting until the agent's SessionStart hook says it is up: a first-run dialog (folder
			// trust, login) must never receive a typed team message.
			if !plain {
				s.Status = "starting"
				s.Branch = gitBranch(r.Cwd)
			}
			if r.Decorate != nil {
				r.Decorate(s)
			}
		},
	})
	if err != nil {
		return s, err
	}
	p.logLine(s.ID, fmt.Sprintf("session: started %s in %s", r.Harness, r.Cwd))
	return s, nil
}

// resolveSource registers (or finds) the work repository a launch names.
func (p *Pool) resolveSource(ctx context.Context, src sourceIn) (protocol.Repo, error) {
	switch src.Kind {
	case "repo":
		d := p.st.State().Domain
		if r := findRepo(&d, src.RepoID); r != nil {
			return *r, nil
		}
		return protocol.Repo{}, notFound("no repository %s", src.RepoID)
	case "clone":
		dest, err := p.clone(ctx, src.URL, src.Dest)
		if err != nil {
			return protocol.Repo{}, err
		}
		return p.registerRepo(dest, "clone", src.URL)
	case "local", "", "worktree":
		path, err := absDir(src.Path)
		if err != nil {
			return protocol.Repo{}, err
		}
		return p.registerRepo(path, "local", "")
	}
	return protocol.Repo{}, invalid("unknown source kind %q", src.Kind)
}

func absDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if p == "" || !filepath.IsAbs(p) {
		return "", invalid("choose an absolute workspace path")
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	if err != nil {
		return "", protocol.Errorf(protocol.CodeNotFound, "workspace %s: %v", p, err)
	}
	if !st.IsDir() {
		return "", invalid("%s is not a directory", p)
	}
	return p, nil
}

var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^-].*$`)

// clone runs git clone with structured arguments and no prompts into a destination that must not
// exist yet.
func (p *Pool) clone(ctx context.Context, raw, dest string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") {
		return "", invalid("a clone needs a Git URL that does not start with '-'")
	}
	if !scpLike.MatchString(raw) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "git") || u.Host == "" {
			return "", invalid("only https, ssh and git URLs can be cloned")
		}
	}
	dest = strings.TrimSpace(dest)
	if strings.HasPrefix(dest, "~/") {
		home, _ := os.UserHomeDir()
		dest = filepath.Join(home, dest[2:])
	}
	if dest == "" || !filepath.IsAbs(dest) {
		return "", invalid("a clone needs an absolute destination path")
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", conflict("destination %s already exists; PenguPool never overwrites it", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	args := []string{"clone", "--", raw, dest}
	gh := p.ghPath()
	if _, isGH := githubSlug(raw); isGH && gh != "" {
		// Private GitHub repositories clone with the signed-in account, for this command only.
		args = append([]string{"-c", "credential.https://github.com.helper=", "-c", "credential.https://github.com.helper=!" + gh + " auth git-credential"}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(append([]string(nil), p.env...),
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dest) // only what this clone created: dest did not exist before
		return "", protocol.Errorf(protocol.CodeUnavailable, "git clone failed: %s", strings.TrimSpace(string(out)))
	}
	if _, isGH := githubSlug(raw); isGH {
		p.useGHCredentials(ctx, dest)
	}
	return dest, nil
}

func (p *Pool) registerRepo(path, kind, u string) (protocol.Repo, error) {
	var out protocol.Repo
	err := p.update(func(st *store.State) error {
		for _, r := range st.Domain.Repos {
			if r.Path == path {
				out = r
				return nil
			}
		}
		out = protocol.Repo{
			ID: protocol.NewID(), Name: filepath.Base(path), Path: path, Kind: kind, URL: u,
			Git: isGit(path), NodeID: st.NodeID, CreatedAt: now(),
		}
		st.Domain.Repos = append(st.Domain.Repos, out)
		return nil
	})
	return out, err
}

type repoAddIn struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
	Dest string `json:"dest,omitempty"`
}

func (p *Pool) repoAdd(ctx context.Context, in repoAddIn) (any, error) {
	if in.URL != "" {
		return p.resolveSource(ctx, sourceIn{Kind: "clone", URL: in.URL, Dest: in.Dest})
	}
	return p.resolveSource(ctx, sourceIn{Kind: "local", Path: in.Path})
}

type idIn struct {
	ID string `json:"id"`
}

func (p *Pool) repoRemove(_ context.Context, in idIn) (any, error) {
	return nil, p.update(func(st *store.State) error {
		for _, t := range st.Domain.Teams {
			if t.RepoID == in.ID && t.State == "active" {
				return conflict("team %s still works in this repository", t.Name)
			}
		}
		out := st.Domain.Repos[:0]
		for _, r := range st.Domain.Repos {
			if r.ID != in.ID {
				out = append(out, r)
			}
		}
		st.Domain.Repos = out
		return nil // the directory itself is never touched
	})
}

type sessionIn struct {
	SessionID string `json:"session_id"`
}

// resume relaunches an ended harness session from its transcript under the same domain id.
func (p *Pool) resume(ctx context.Context, in sessionIn) (any, error) {
	s, ok := p.session(in.SessionID)
	if !ok {
		return nil, notFound("no session %s", in.SessionID)
	}
	if s.State == "running" {
		return nil, conflict("session %s is still running", s.Name)
	}
	if err := harness.Dropped(s.Harness); err != nil {
		return nil, invalid("%v", err)
	}
	if !harness.IsAgent(s.Harness) {
		return nil, invalid("only agent sessions can be resumed")
	}
	resumeID := s.ConvID
	if (s.Harness == harness.OMP || s.Harness == harness.Pi) && s.Transcript != "" {
		resumeID = s.Transcript
	}
	if resumeID == "" {
		return nil, invalid("no transcript was recorded for %s", s.Name)
	}
	if d := p.st.State().Domain; s.SkillID != "" && findSkill(&d, s.SkillID) == nil {
		return nil, invalid("%s's helper was deleted; start a new session instead", s.Name)
	}
	keep := s
	p.expireAsks(s.ID) // what it asked before it stopped has no one waiting: it asks again if it still needs to
	ns, err := p.spawn(ctx, spawnReq{
		ID: s.ID, Name: s.Name, Harness: s.Harness, Cwd: s.Cwd, Resume: resumeID, Cols: s.Cols, Rows: s.Rows, SkillID: keep.SkillID, Via: s.Via,
		Model: keep.LaunchModel, ReadDirs: p.resumeReadDirs(keep),
		Decorate: func(n *store.Session) {
			n.TeamID, n.Role, n.SkillID, n.ConvID, n.Transcript = keep.TeamID, keep.Role, keep.SkillID, keep.ConvID, keep.Transcript
			if keep.RunLog != "" {
				n.RunLog = keep.RunLog
			}
		},
	})
	if err != nil {
		return nil, err
	}
	p.logLine(ns.ID, "session: resumed from "+resumeID)
	return ns.View(), nil
}

// resumeReadDirs is what s was started reading; a session from before that was recorded gets
// startMember's rule.
func (p *Pool) resumeReadDirs(s store.Session) []string {
	if s.Launched {
		return s.LaunchReadDirs
	}
	return p.helperReadDirs(s)
}

// helperReadDirs is what a team helper (any skill member) reads besides its own
// folder: the team's repositories, as startMember gives it at the start.
func (p *Pool) helperReadDirs(s store.Session) []string {
	if s.TeamID == "" || s.SkillID == "" {
		return nil
	}
	d := p.st.State().Domain
	t, sk := findTeam(&d, s.TeamID), findSkill(&d, s.SkillID)
	if t == nil || sk == nil { // as startMember: any skill member, a team-hub one included
		return nil
	}
	var dirs []string
	for _, r := range teamRepos(&d, t) {
		dirs = append(dirs, r.Path)
	}
	return dirs
}

// forget drops an ended session's record. A running session must be stopped first.
func (p *Pool) forget(_ context.Context, in sessionIn) (any, error) {
	return nil, p.update(func(st *store.State) error {
		i := sessionIndex(st, in.SessionID)
		if i < 0 {
			return notFound("no session %s", in.SessionID)
		}
		if st.Sessions[i].State == "running" {
			return conflict("stop the session before removing it")
		}
		if t := teamOf(&st.Domain, in.SessionID); t != nil {
			return conflict("the session is a member of team %s; close it there", t.Name)
		}
		st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
		return nil
	})
}

// openTerminal opens the user's classic terminal app attached to the session.
func (p *Pool) openTerminal(_ context.Context, in sessionIn) (any, error) {
	s, ok := p.session(in.SessionID)
	if !ok {
		return nil, notFound("no session %s", in.SessionID)
	}
	script := filepath.Join(p.st.Dir(), "run", "open-"+idTail(s.ID)+".command")
	body := fmt.Sprintf("#!/bin/sh\nexec %s attach --store %s %s\n", shq(p.exe), shq(p.st.Dir()), s.ID)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return nil, err
	}
	if out, err := exec.Command("open", script).CombinedOutput(); err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "open terminal: %s", strings.TrimSpace(string(out)))
	}
	return map[string]string{"command": strings.TrimSpace(body[strings.Index(body, "exec ")+5:])}, nil
}

// cli is this executable as agents are told to run it: shell-quoted, the same spelling the
// per-launch permission rules match (harness.ClaudeSettings).
func (p *Pool) cli() string {
	if p.exe == "" {
		return "pengupool"
	}
	return shq(p.exe)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func isGit(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (p *Pool) harnessProbe(ctx context.Context, _ struct{}) (any, error) {
	out := map[string]map[string]string{}
	for _, a := range harness.Agents {
		if a.Hidden {
			continue
		}
		k := a.ID
		v, err := harness.Probe(k, p.env)
		r := map[string]string{"version": v}
		if err != nil {
			r["error"] = probeNote(err)
		}
		out[k] = r
	}
	// Claude Code (Ollama): the ollama CLI, and the model it would start.
	bin, err := harness.LookupOllama(p.env)
	r := map[string]string{"version": bin} // installed: the CLI's path (an error alongside: no model to start)
	if err != nil {
		r["error"] = probeNote(err)
	} else if m, err := p.ollamaModel(ctx); err != nil {
		r["error"] = probeNote(err)
	} else {
		r["model"] = m
	}
	out["ollama"] = r
	return out, nil
}

// probeNote is an install check's error as the owner reads it: the message, without its code.
func probeNote(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return strings.TrimPrefix(err.Error(), "harness_unavailable: ")
}

// agentIDs lists the supported agent runtimes for error messages.
func agentIDs() string {
	ids := make([]string, len(harness.Agents))
	for i, a := range harness.Agents {
		ids[i] = a.ID
	}
	return strings.Join(ids, ", ")
}

// skillRepo is the checkout of a Skill library entry, or "" when there is none on disk.
func (p *Pool) skillRepo(id string) string {
	if id == "" {
		return ""
	}
	d := p.st.State().Domain
	if sk := findSkill(&d, id); sk != nil {
		if _, err := os.Stat(filepath.Join(sk.Path, "skills")); err == nil {
			return sk.Path
		}
	}
	return ""
}

// readyLoop readies Codex and Muse sessions. Both run their SessionStart hook only with the first
// prompt, so a session left at "starting" would never receive a team message. It is ready once its
// screen shows the agent's prompt (promptMarkers): a folder-trust dialog, a sign-in or an update
// screen does not show it, and a typed message would answer them.
func (p *Pool) readyLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.readyByPrompt()
			p.watchStartup()
			p.watchHooks(time.Now())
		}
	}
}

// promptMarkers is what each agent shows only at its prompt: Codex's footer (0.164), Muse's input
// rule (1.4.14). ponytail: tied to their wording; a later version that changes it leaves its sessions
// "starting" until their first prompt, as before.
var promptMarkers = map[string]string{
	harness.Codex: "? for shortcuts",
	harness.Muse:  "Voice input (⌥ + v to start)",
}

// museStatusRe is Muse's status line under its prompt (1.4.14): "<model> · <effort> · <folder> · <mode>".
// ponytail: tied to that layout; a Muse that changes it shows "Muse Code · Meta" with no model.
var museStatusRe = regexp.MustCompile(`(?m)^\s*(\S+) · (?:minimal|low|medium|high|xhigh|max) · `)

// museModel keeps the model Muse shows on its own status line: Muse's hooks carry no model, so its
// screen is the evidence. It returns the model, or "" when the line is not on screen.
func (p *Pool) museModel(id, screen string) string {
	m := museStatusRe.FindStringSubmatch(screen)
	if m == nil {
		return ""
	}
	if s, ok := p.session(id); ok && s.Model != m[1] {
		p.setSession(id, func(x *store.Session) { x.Model = m[1] })
	}
	return m[1]
}

// museTurnModel logs the model a Muse turn ran on, from its status line now, else the --model it was
// started with; the session log then says per turn what the agent ran on.
func (p *Pool) museTurnModel(s store.Session) {
	text := ""
	if p.testScreen != nil {
		text, _ = p.testScreen(s.ID)
	} else if p.hub != nil {
		text, _ = p.hub.ScreenText(s.ID)
	}
	switch m := p.museModel(s.ID, text); {
	case m != "":
		p.logLine(s.ID, "turn: model "+m+" (Muse status line)")
	case s.LaunchModel != "":
		p.logLine(s.ID, "turn: model "+s.LaunchModel+" (--model)")
	default:
		p.logLine(s.ID, "turn: model not shown")
	}
}

func (p *Pool) readyByPrompt() {
	screen := p.hub.ScreenText
	if p.testScreen != nil {
		screen = p.testScreen
	}
	for _, s := range p.st.State().Sessions {
		marker, ok := promptMarkers[s.Harness]
		if !ok || s.State != "running" || s.Status != "starting" {
			continue
		}
		text, ok := screen(s.ID)
		if !ok || !strings.Contains(text, marker) {
			continue
		}
		if s.Harness == harness.Muse {
			p.museModel(s.ID, text) // its label shows the model before its first turn
		}
		changed := false
		p.setSession(s.ID, func(x *store.Session) {
			if x.Status == "starting" {
				x.Status, changed = "idle", true
			}
		})
		if !changed { // its SessionStart hook got there first, and flushed
			continue
		}
		p.logLine(s.ID, "session: "+s.Harness+" ready (its prompt is on screen)")
		p.flushQueue(s.ID)
	}
}
