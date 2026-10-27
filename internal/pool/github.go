package pool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// GitHub account, backed by the GitHub CLI: signing in runs gh's own device flow, so the token
// lives where gh keeps it (the macOS Keychain) and PenguPool never sees or stores it. With it,
// the owner can pick any repository the account can reach and clone it (private ones too), and
// propose changes as a pull request — pushed to the repository when the account may write there,
// otherwise to the account's fork. Git credentials are passed per command or set on the cloned
// repository only; the global git config is never touched.

type ghLogin struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	code    string
	uri     string
	message string
}

var gh = &ghLogin{}

// ghPath finds gh on the daemon's PATH or the usual Homebrew locations.
func (p *Pool) ghPath() string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	for _, kv := range p.env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			dirs = append(filepath.SplitList(v), dirs...)
		}
	}
	for _, d := range dirs {
		if st, err := os.Stat(filepath.Join(d, "gh")); err == nil && !st.IsDir() {
			return filepath.Join(d, "gh")
		}
	}
	return ""
}

// ghRun runs gh without prompts and returns its trimmed stdout (also when it fails).
func (p *Pool) ghRun(ctx context.Context, dir string, args ...string) (string, error) {
	return p.ghRunFor(ctx, 2*time.Minute, dir, args...)
}

// ghRunFor is ghRun with its own time limit, for downloads.
func (p *Pool) ghRunFor(ctx context.Context, limit time.Duration, dir string, args ...string) (string, error) {
	bin := p.ghPath()
	if bin == "" {
		return "", protocol.Errorf(protocol.CodeUnavailable, "the GitHub CLI is not installed; install it with `brew install gh`")
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, append(append([]string(nil), p.env...), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		// stdout too: `gh auth status` still reports every account when one of them fails.
		return strings.TrimSpace(out.String()), protocol.Errorf(protocol.CodeUnavailable, "gh %s: %s", args[0], firstNonEmpty(strings.TrimSpace(errb.String()), err.Error()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ghGit runs git with gh as the credential helper for github.com, for this command only.
func (p *Pool) ghGit(ctx context.Context, dir string, args ...string) (string, error) {
	if bin := p.ghPath(); bin != "" {
		args = append([]string{"-c", "credential.https://github.com.helper=", "-c", "credential.https://github.com.helper=!" + bin + " auth git-credential"}, args...)
	}
	return p.gitIn(ctx, dir, args...)
}

type ghAccount struct {
	Login  string `json:"login"`
	Active bool   `json:"active"`
	Scopes string `json:"scopes"`
	State  string `json:"state"`
}

// githubStatus reports gh's accounts on github.com and any sign-in in progress.
// state: no_cli | signed_out | authorizing | signed_in
func (p *Pool) githubStatus() map[string]any {
	gh.mu.Lock()
	code, uri, msg, busy := gh.code, gh.uri, gh.message, gh.cmd != nil
	gh.mu.Unlock()
	out := map[string]any{"state": "signed_out", "accounts": []ghAccount{}, "user_code": code, "verification_uri": uri, "message": msg}
	if p.ghPath() == "" {
		out["state"] = "no_cli"
		return out
	}
	raw, _ := p.ghRun(context.Background(), "", "auth", "status", "--hostname", "github.com", "--json", "hosts")
	var st struct {
		Hosts map[string][]ghAccount `json:"hosts"`
	}
	_ = json.Unmarshal([]byte(raw), &st)
	accounts := st.Hosts["github.com"]
	if accounts == nil {
		accounts = []ghAccount{}
	}
	out["accounts"] = accounts
	for _, a := range accounts {
		if a.Active && a.State == "success" {
			out["state"], out["user"] = "signed_in", map[string]any{"login": a.Login, "avatar_url": "https://github.com/" + url.PathEscape(a.Login) + ".png?size=64"}
		}
	}
	if busy {
		out["state"] = "authorizing"
	}
	return out
}

func (p *Pool) githubStatusRPC(_ context.Context, _ struct{}) (any, error) {
	return p.githubStatus(), nil
}

var ghCodeRe = regexp.MustCompile(`one-time code: ([A-Z0-9]{4}-[A-Z0-9]{4})`)

// githubLogin starts gh's device flow: it shows the one-time code here and opens GitHub in the
// browser; the account is added (or refreshed) once the owner approves it there.
func (p *Pool) githubLogin(_ context.Context, _ struct{}) (any, error) {
	bin := p.ghPath()
	if bin == "" {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "the GitHub CLI is not installed; install it with `brew install gh`")
	}
	gh.mu.Lock()
	if gh.cmd != nil {
		gh.mu.Unlock()
		return p.githubStatus(), nil
	}
	cmd := exec.Command(bin, "auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web", "--skip-ssh-key")
	// GH_BROWSER=true: gh must not open the browser itself; PenguPool does, once the code is shown.
	cmd.Env = append(append([]string(nil), p.env...), "GH_BROWSER=true", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		gh.mu.Unlock()
		return nil, err
	}
	cmd.Stdout = cmd.Stderr
	if err := cmd.Start(); err != nil {
		gh.mu.Unlock()
		return nil, err
	}
	gh.cmd, gh.code, gh.uri, gh.message = cmd, "", "", ""
	gh.mu.Unlock()

	got := make(chan struct{})
	var tail []string
	go func() {
		sc := bufio.NewScanner(stderr)
		once := sync.Once{}
		for sc.Scan() {
			line := sc.Text()
			tail = append(tail, line)
			if m := ghCodeRe.FindStringSubmatch(line); m != nil {
				gh.mu.Lock()
				gh.code, gh.uri = m[1], "https://github.com/login/device"
				gh.mu.Unlock()
				once.Do(func() { close(got) })
			}
		}
		err := cmd.Wait()
		gh.mu.Lock()
		gh.cmd, gh.code, gh.uri = nil, "", ""
		if err != nil {
			gh.message = "sign-in did not finish: " + strings.TrimSpace(strings.Join(tail, " "))
		}
		gh.mu.Unlock()
		once.Do(func() { close(got) })
		p.reposCache.Store(nil)
	}()
	select {
	case <-got:
	case <-time.After(15 * time.Second):
	}
	gh.mu.Lock()
	uri := gh.uri
	gh.mu.Unlock()
	if uri != "" {
		_ = exec.Command("open", uri).Start()
	}
	return p.githubStatus(), nil
}

// githubCancel stops a sign-in in progress.
func (p *Pool) githubCancel(_ context.Context, _ struct{}) (any, error) {
	gh.mu.Lock()
	if gh.cmd != nil && gh.cmd.Process != nil {
		_ = gh.cmd.Process.Kill()
	}
	gh.message = ""
	gh.mu.Unlock()
	return p.githubStatus(), nil
}

type ghUserIn struct {
	Login string `json:"login"`
}

var ghLoginRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)

// githubSwitch makes another signed-in account the active one (for gh and PenguPool alike).
func (p *Pool) githubSwitch(ctx context.Context, in ghUserIn) (any, error) {
	if !ghLoginRe.MatchString(in.Login) {
		return nil, invalid("not a GitHub login: %q", in.Login)
	}
	if _, err := p.ghRun(ctx, "", "auth", "switch", "--hostname", "github.com", "--user", in.Login); err != nil {
		return nil, err
	}
	p.reposCache.Store(nil)
	return p.githubStatus(), nil
}

// githubLogout signs one account out of gh on this machine.
func (p *Pool) githubLogout(ctx context.Context, in ghUserIn) (any, error) {
	if !ghLoginRe.MatchString(in.Login) {
		return nil, invalid("not a GitHub login: %q", in.Login)
	}
	if _, err := p.ghRun(ctx, "", "auth", "logout", "--hostname", "github.com", "--user", in.Login); err != nil {
		return nil, err
	}
	p.reposCache.Store(nil)
	return p.githubStatus(), nil
}

type ghRepo struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
	Fork        bool   `json:"fork"`
	Push        bool   `json:"push"`
	PushedAt    string `json:"pushed_at"`
	CloneURL    string `json:"clone_url"`
}

type ghRepos struct {
	at    time.Time
	repos []ghRepo
}

type refreshIn struct {
	Refresh bool `json:"refresh,omitempty"`
}

// githubRepos lists every repository the active account can reach (its own, collaborations and
// its organizations'), most recently pushed first; cached for two minutes.
func (p *Pool) githubRepos(ctx context.Context, in refreshIn) (any, error) {
	if c := p.reposCache.Load(); c != nil && !in.Refresh && time.Since(c.at) < 2*time.Minute {
		return c.repos, nil
	}
	raw, err := p.ghRun(ctx, "", "api", "--paginate",
		"user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member",
		"--jq", `.[] | {full_name, description: (.description // ""), private, fork, push: .permissions.push, pushed_at, clone_url}`)
	if err != nil {
		return nil, err
	}
	repos := []ghRepo{}
	for _, line := range strings.Split(raw, "\n") {
		var r ghRepo
		if json.Unmarshal([]byte(line), &r) == nil && r.FullName != "" {
			repos = append(repos, r)
		}
	}
	p.reposCache.Store(&ghRepos{at: time.Now(), repos: repos})
	return repos, nil
}

// useGHCredentials makes a cloned github.com repository authenticate through gh, in its own config
// only, so later pulls and pushes by agents work for private repositories too.
func (p *Pool) useGHCredentials(ctx context.Context, dir string) {
	bin := p.ghPath()
	if bin == "" {
		return
	}
	key := "credential.https://github.com.helper"
	_, _ = p.gitIn(ctx, dir, "config", "--local", "--unset-all", key)
	_, _ = p.gitIn(ctx, dir, "config", "--local", "--add", key, "")
	_, _ = p.gitIn(ctx, dir, "config", "--local", "--add", key, "!"+bin+" auth git-credential")
}

var ghSlugRe = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// githubSlug returns owner/name for a github.com remote URL.
func githubSlug(remote string) (string, bool) {
	m := ghSlugRe.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// openPR pushes ref as branch and opens a pull request against base (owner/name): on base itself
// when the active account may push there, otherwise on the account's fork, created if needed.
func (p *Pool) openPR(ctx context.Context, dir, base, ref, branch, title, body string) (string, error) {
	canPush, err := p.ghRun(ctx, "", "api", "repos/"+base, "--jq", ".permissions.push")
	if err != nil {
		return "", err
	}
	head, remote := branch, "https://github.com/"+base+".git"
	if canPush != "true" {
		login, err := p.ghRun(ctx, "", "api", "user", "--jq", ".login")
		if err != nil {
			return "", err
		}
		// Idempotent: an existing fork is reused.
		if _, err := p.ghRun(ctx, "", "repo", "fork", base, "--clone=false", "--remote=false"); err != nil {
			return "", err
		}
		// ponytail: assumes the fork kept the repository's name; a renamed fork needs a forks lookup.
		_, name, _ := strings.Cut(base, "/")
		fork := login + "/" + name
		remote, head = "https://github.com/"+fork+".git", login+":"+branch
		// A new fork can take a few seconds before it accepts pushes.
		for i := 0; i < 10; i++ {
			if _, err = p.ghRun(ctx, "", "api", "repos/"+fork, "--jq", ".full_name"); err == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
	}
	if _, err := p.ghGit(ctx, dir, "push", "-q", remote, ref+":refs/heads/"+branch); err != nil {
		return "", err
	}
	out, err := p.ghRun(ctx, dir, "pr", "create", "--repo", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return "", fmt.Errorf("branch %s is pushed, but the pull request was not opened: %w", head, err)
	}
	if i := strings.LastIndex(out, "\n"); i >= 0 {
		out = out[i+1:]
	}
	return out, nil
}

type prProposeIn struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// githubPropose opens a pull request with a repository's committed work. The working tree is not
// changed: the current commit is pushed under the current branch name, or under a new
// pengupool/… name when on the default branch. A clone of a fork proposes to its parent.
func (p *Pool) githubPropose(ctx context.Context, in prProposeIn) (any, error) {
	if !filepath.IsAbs(in.Path) || !isGit(in.Path) {
		return nil, invalid("%s is not a Git repository", in.Path)
	}
	origin, err := p.gitIn(ctx, in.Path, "remote", "get-url", "origin")
	if err != nil {
		return nil, invalid("the repository has no origin remote")
	}
	slugPath, ok := githubSlug(origin)
	if !ok {
		return nil, invalid("origin %s is not a GitHub repository", origin)
	}
	if dirty, _ := p.gitIn(ctx, in.Path, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return nil, conflict("there are uncommitted changes; commit them first, then propose")
	}
	info, err := p.ghRun(ctx, "", "api", "repos/"+slugPath, "--jq", `[(.parent.full_name // .full_name), (.parent.default_branch // .default_branch)] | join(" ")`)
	if err != nil {
		return nil, err
	}
	base, def, _ := strings.Cut(info, " ")
	head, err := p.gitIn(ctx, in.Path, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	branch, _ := p.gitIn(ctx, in.Path, "rev-parse", "--abbrev-ref", "HEAD")
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title, _ = p.gitIn(ctx, in.Path, "log", "-1", "--format=%s")
	}
	if branch == "HEAD" || branch == def || branch == "" {
		branch = "pengupool/" + slug(title) + "-" + head[:7]
	}
	body := strings.TrimSpace(in.Body)
	if body != "" {
		body += "\n\n"
	}
	body += "Proposed from PenguPool."
	pr, err := p.openPR(ctx, in.Path, base, "HEAD", branch, title, body)
	if err != nil {
		return nil, err
	}
	return map[string]string{"pr": pr, "base": base, "branch": branch}, nil
}
