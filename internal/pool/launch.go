package pool

import (
	"context"
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
	Kind   string `json:"kind"` // local | clone | repo
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
	Dest   string `json:"dest,omitempty"`
	RepoID string `json:"repo_id,omitempty"`
}

type launchIn struct {
	Name    string   `json:"name"`
	Harness string   `json:"harness"`
	Command []string `json:"command,omitempty"`
	Model   string   `json:"model,omitempty"`
	Source  sourceIn `json:"source"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
}

// launch starts a main session from a local directory, a clone, or a registered repository.
// The session is not part of a team; a team is formed from a task or by adding sessions.
func (p *Pool) launch(ctx context.Context, in launchIn) (any, error) {
	// A session runs a supported agent, or a plain terminal for any other (no hooks, so no status or journal).
	if in.Harness == "" {
		in.Harness = harness.Claude
	}
	if !harness.IsAgent(in.Harness) && in.Harness != harness.Shell {
		return nil, invalid("a PenguPool session runs an agent (%s) or a terminal, not %q", agentIDs(), in.Harness)
	}
	repo, err := p.resolveSource(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = p.uniqueName(repo.Name + "-" + in.Harness)
	}
	s, err := p.spawn(ctx, spawnReq{
		Name: name, Harness: in.Harness, Command: in.Command, Model: in.Model,
		Cwd: repo.Path, Cols: in.Cols, Rows: in.Rows,
	})
	if err != nil {
		return nil, err
	}
	return s.View(), nil
}

type spawnReq struct {
	ID, Name, Harness, Model, Cwd, Resume string
	Effort                                string
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
	if p.testSpawn != nil {
		return p.testSpawn(r)
	}
	if r.SkillID != "" && r.Resume == "" { // a helper starts in a folder PenguPool made: trust it, so no dialog waits for the owner
		if err := pretrustFolder(r.Harness, r.Cwd, r.TrustFrom); err != nil {
			p.logf("pool: trust %s for %s: %v", r.Cwd, r.Harness, err)
		}
	}
	ollama := p.ollamaFor(r.Harness, r.Model)
	if ollama == nil && strings.HasPrefix(r.Model, ollamaPrefix) {
		return store.Session{}, invalid("name the Ollama model to run %s on, or set one in Settings → Agents", r.Harness)
	}
	cmd, env, err := harness.Spec(r.Harness, harness.Params{
		SessionID: r.ID, Name: r.Name, Cwd: r.Cwd, Exe: p.exe, StoreDir: p.st.Dir(),
		Model: r.Model, Effort: r.Effort, Resume: r.Resume, Command: r.Command, Env: p.env, Ollama: ollama,
		SkillRepo: p.skillRepo(r.SkillID), ReadDirs: r.ReadDirs, Bin: p.binFor(r.Harness),
	})
	if r.Harness == harness.Shell {
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
			s.RunLog = runlog
			// Starting until the agent's SessionStart hook says it is up: a first-run dialog (folder
			// trust, login) must never receive a typed team message.
			// A terminal has no hook to clear it, so it never starts.
			if harness.IsAgent(r.Harness) {
				s.Status = "starting"
			}
			s.Branch = gitBranch(r.Cwd)
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
	case "local", "":
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
		return nil, invalid("%s's helper agent was deleted; start a new session instead", s.Name)
	}
	keep := s
	ns, err := p.spawn(ctx, spawnReq{
		ID: s.ID, Name: s.Name, Harness: s.Harness, Cwd: s.Cwd, Resume: resumeID, Cols: s.Cols, Rows: s.Rows, SkillID: keep.SkillID,
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

// RestoreLost resumes the agent sessions that were running before this host started and whose
// workers did not survive (the machine powered off or crashed): the hub marked them lost. A session
// the owner stopped, or one that exited on its own, is not touched; a terminal cannot be resumed.
func (p *Pool) RestoreLost(ctx context.Context, wasRunning []string) {
	for _, id := range wasRunning {
		s, ok := p.session(id)
		if !ok || s.State != "lost" || !harness.IsAgent(s.Harness) {
			continue
		}
		if _, err := p.resume(ctx, sessionIn{SessionID: id}); err != nil {
			p.logf("pool: restore %s after the host restarted: %v", s.Name, err)
			continue
		}
		p.logf("pool: restored %s after the host restarted", s.Name)
	}
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
	script := filepath.Join(p.st.Dir(), "run", "open-"+s.ID[:8]+".command")
	body := fmt.Sprintf("#!/bin/sh\nexec %s attach --store %s %s\n", shq(p.exe), shq(p.st.Dir()), s.ID)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return nil, err
	}
	if out, err := exec.Command("open", script).CombinedOutput(); err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "open terminal: %s", strings.TrimSpace(string(out)))
	}
	return map[string]string{"command": strings.TrimSpace(body[strings.Index(body, "exec ")+5:])}, nil
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

func (p *Pool) harnessProbe(_ context.Context, _ struct{}) (any, error) {
	out := map[string]map[string]string{}
	for _, a := range harness.Agents {
		k := a.ID
		v, err := harness.Probe(k, p.env, p.binFor(k))
		r := map[string]string{"version": v}
		if err != nil {
			r["error"] = err.Error()
		}
		out[k] = r
	}
	return out, nil
}

// agentIDs lists the supported agent runtimes for error messages.
func agentIDs() string {
	ids := make([]string, len(harness.Agents))
	for i, a := range harness.Agents {
		ids[i] = a.ID
	}
	return strings.Join(ids, ", ")
}

// binFor returns the configured binary override for an agent, or "".
func (p *Pool) binFor(kind string) string {
	if b, ok := p.st.Config().AgentBins[kind]; ok {
		return strings.TrimSpace(b)
	}
	return ""
}

// ollamaPrefix marks a session's model as an Ollama model: "ollama/qwen3-coder".
const ollamaPrefix = "ollama/"

// ollamaFor returns the Ollama model an agent runs on, or nil for its own account: the session's
// pick ("ollama/<model>", "ollama/" for the configured model), or every launch of an agent the
// owner put on Ollama in Settings. A model named at launch replaces the configured one.
func (p *Pool) ollamaFor(kind, model string) *harness.Ollama {
	am := p.st.Config().AgentModels
	a, ok := harness.Find(kind)
	picked, onOllama := strings.CutPrefix(model, ollamaPrefix)
	if onOllama {
		model = picked
	}
	if !ok || !a.Ollama || !onOllama && !slices.Contains(am.Ollama, kind) {
		return nil
	}
	o := &harness.Ollama{URL: strings.TrimRight(firstNonEmpty(am.OllamaURL, "http://127.0.0.1:11434"), "/"), Model: firstNonEmpty(model, am.OllamaModel)}
	if o.Model == "" {
		return nil
	}
	return o
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

// codexLoop readies Codex sessions. Codex runs its SessionStart hook only with the first prompt, so a
// session left at "starting" would never receive a team message. It is ready once its screen shows
// Codex's prompt footer: a folder-trust dialog, a sign-in or an update screen does not show it, and a
// typed message would answer them.
func (p *Pool) codexLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.readyCodex()
			p.watchStartup()
		}
	}
}

// codexPrompt is the footer under Codex's prompt (0.164). ponytail: tied to Codex's wording; a later
// Codex that changes it leaves its sessions "starting" until their first prompt, as before.
const codexPrompt = "? for shortcuts"

func (p *Pool) readyCodex() {
	screen := p.hub.ScreenText
	if p.testScreen != nil {
		screen = p.testScreen
	}
	for _, s := range p.st.State().Sessions {
		if s.Harness != harness.Codex || s.State != "running" || s.Status != "starting" {
			continue
		}
		if text, ok := screen(s.ID); !ok || !strings.Contains(text, codexPrompt) {
			continue
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
		p.logLine(s.ID, "session: codex ready (its prompt is on screen)")
		p.flushQueue(s.ID)
	}
}
