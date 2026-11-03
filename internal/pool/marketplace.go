package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Specialist hub's catalog is PenguPool's own file, .pengupool/catalog.json: the helpers it lists
// are skill repositories pinned to a commit. PenguPool keeps a clone of the hub under
// <store>/marketplaces and installs a helper by cloning its repository at the pin (an ordinary Skill
// library git source). The format, whole:
//
//	{
//	  "name": "pengupool-agent-hub",              // optional
//	  "description": "...",                       // optional
//	  "agents": [{
//	    "name": "pr-review-agent",                // required, unique (case-insensitive)
//	    "description": "...",                     // optional
//	    "group": "development",                   // optional: becomes the helper's group on install
//	    "capabilities": ["code-review"],          // optional: what the helper is for
//	    "harness": "claude",                      // optional: its preferred agent
//	    "requires": ["claude"],                   // optional: any one of these agents must be installed
//	    "repo": "https://github.com/o/r.git",     // required: the helper's Git repository
//	    "path": "agents/pr-review-agent",         // optional: the helper's folder in repo
//	    "ref": "main",                            // optional: the branch the pin is on, or to follow
//	    "sha": "<40 hex>"                         // optional: the pinned commit; none follows the latest of ref
//	  }]
//	}
//
// One bad entry never stops the rest: an entry with no name, no repo, a "path" with "." or ".." in
// it, or a "sha" that is not a full 40-hex commit is skipped, counted, and logged once with its name and the reason. So is
// every entry whose name another entry has too (case-insensitive): a duplicate is ambiguous, so none
// of that name is read, and a helper installed under it stays as it is until the hub fixes it. The
// other entries load. A file that is not valid JSON is refused whole.

const (
	marketFile = ".pengupool/catalog.json"
	// ponytail: the hub's old file, read when the new one is missing so a clone that has not pulled yet
	// and older hub commits keep working; drop it once every host is on this version.
	legacyMarketFile = ".claude-plugin/marketplace.json"
)

// Marketplace is PenguPool's reading of the catalog: the entries with a Git source it can install.
type Marketplace struct {
	Name        string
	Description string
	Skills      []MarketEntry
	Skipped     int      // entries PenguPool cannot install: a mistake in the entry, or (old file) bundled or not pinned to a commit
	Legacy      bool     // read from the old .claude-plugin/marketplace.json
	Problems    []string // why each entry that was skipped for a mistake was
}

type MarketEntry struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Harness      string            `json:"harness,omitempty"`
	Requires     []string          `json:"requires,omitempty"`
	Repo         string            `json:"repo"`
	Path         string            `json:"path,omitempty"` // git-subdir: the helper's folder in Repo
	Ref          string            `json:"ref,omitempty"`
	Commit       string            `json:"commit"`
	Category     string            `json:"category,omitempty"` // the catalog's grouping; becomes the helper's group on install
	Credits      []protocol.Credit `json:"credits,omitempty"`  // display only
}

// catalogEntry is one helper in .pengupool/catalog.json.
type catalogEntry struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Group        string          `json:"group"`
	Capabilities []string        `json:"capabilities"`
	Harness      string          `json:"harness"`
	Requires     []string        `json:"requires"`
	Repo         string          `json:"repo"`
	Path         string          `json:"path"`
	Ref          string          `json:"ref"`
	SHA          string          `json:"sha"`
	Credits      json.RawMessage `json:"credits"` // display only: read leniently by credits()
}

// credits keeps the well-formed items, each a name and an https link; anything else is dropped, so a
// malformed display-only field never costs the entry or the catalog.
func credits(raw json.RawMessage) []protocol.Credit {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []protocol.Credit
	for _, it := range items {
		var c protocol.Credit
		if json.Unmarshal(it, &c) == nil && c.Name != "" && strings.HasPrefix(c.URL, "https://") {
			out = append(out, c)
		}
	}
	return out
}

// entry is the helper this entry installs, or why it cannot be one.
func (c catalogEntry) entry() (MarketEntry, string) {
	e := MarketEntry{Name: c.Name, Description: c.Description, Category: strings.TrimSpace(c.Group), Capabilities: c.Capabilities,
		Harness: c.Harness, Requires: c.Requires, Repo: c.Repo, Path: strings.Trim(c.Path, "/"), Ref: c.Ref, Commit: c.SHA, Credits: credits(c.Credits)}
	if parts := strings.Split(e.Path, "/"); e.Path != "" && (slices.Contains(parts, "..") || slices.Contains(parts, ".")) {
		return e, "its path must not contain . or .."
	}
	return e, ""
}

// legacyPlugin is the part of an old .claude-plugin/marketplace.json plugin entry PenguPool reads.
type legacyPlugin struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Keywords    []string        `json:"keywords"`
	Tags        []string        `json:"tags"`
	Category    string          `json:"category"`
	Source      json.RawMessage `json:"source"`
}

type legacySource struct {
	Source string `json:"source"` // github | url | git-subdir | npm ...
	Repo   string `json:"repo"`   // github: owner/name
	URL    string `json:"url"`
	Path   string `json:"path"` // git-subdir: folder inside the repository
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
}

func (c legacyPlugin) entry() (MarketEntry, bool) {
	var src legacySource
	if json.Unmarshal(c.Source, &src) != nil { // a string: a plugin directory inside the catalog
		return MarketEntry{}, false
	}
	e := MarketEntry{Name: c.Name, Description: c.Description, Capabilities: c.Keywords, Category: strings.TrimSpace(c.Category), Ref: src.Ref, Commit: src.SHA}
	switch src.Source {
	case "github":
		e.Repo = "https://github.com/" + src.Repo + ".git"
	case "url":
		e.Repo = src.URL
	case "git-subdir":
		e.Repo, e.Path = src.URL, strings.Trim(src.Path, "/")
		if parts := strings.Split(e.Path, "/"); e.Path == "" || slices.Contains(parts, "..") || slices.Contains(parts, ".") {
			return MarketEntry{}, false
		}
	default:
		return MarketEntry{}, false
	}
	for _, t := range c.Tags {
		if h, ok := strings.CutPrefix(t, "harness:"); ok {
			e.Harness = h
		}
		if r, ok := strings.CutPrefix(t, "requires:"); ok {
			e.Requires = append(e.Requires, strings.Split(r, ",")...)
		}
	}
	return e, e.Commit != ""
}

// key identifies the helper an entry installs: its repository, plus its folder for git-subdir.
func (e MarketEntry) key() string {
	if e.Path != "" {
		return repoKey(e.Repo) + "/" + e.Path
	}
	return repoKey(e.Repo)
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// problem is why an entry cannot be installed, "" when it can.
func problem(e MarketEntry) string {
	switch {
	case e.Name == "":
		return "it has no name"
	case e.Repo == "" || strings.HasPrefix(e.Repo, "-") || strings.HasPrefix(e.Repo, "https://github.com/-"):
		return "it has no repository"
	case e.Commit != "" && !shaRe.MatchString(e.Commit): // no sha: the helper follows the latest of its ref
		return "its sha must be a full 40-hex commit"
	}
	return ""
}

// repoKey makes Git URLs comparable: git@github.com:o/r.git, https://github.com/o/r and
// ssh://git@github.com/o/r all become github.com/o/r.
func repoKey(raw string) string {
	s := strings.TrimSpace(raw)
	if scpLike.MatchString(s) {
		_, hostPath, _ := strings.Cut(s, "@")
		host, path, _ := strings.Cut(hostPath, ":")
		s = host + "/" + path
	} else if u, err := url.Parse(s); err == nil && u.Host != "" {
		s = u.Hostname() + u.Path
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git"))
}

// gitIn runs git non-interactively: credentials come from the owner's helper or agent, never a prompt.
func (p *Pool) gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	return p.gitEnv(ctx, dir, nil, args...)
}

// gitEnv is gitIn with more environment (a commit's dates).
func (p *Pool) gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(append([]string(nil), p.env...), env...),
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", protocol.Errorf(protocol.CodeUnavailable, "git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// mainMarketplace is the one catalog PenguPool reads: the Specialist hub, a private repository
// (cloned and fetched with the signed-in GitHub account, like any private one).
const mainMarketplace = "https://github.com/nduwork/pengupool-agent-hub.git"

// catalogURL is the hub in use: the owner's setting, else the default.
func (p *Pool) catalogURL() string { return firstNonEmpty(p.st.Config().CatalogRepo, mainMarketplace) }

func (p *Pool) marketDir() string { return p.marketDirFor(p.catalogURL()) }

// marketDirFor is where the clone of a catalog lives under the store.
func (p *Pool) marketDirFor(url string) string {
	return filepath.Join(p.st.Dir(), "marketplaces", strings.NewReplacer("/", "-", ":", "-").Replace(repoKey(url)))
}

// hubAccess says what a refused clone or fetch of the (private) hub most likely means.
func hubAccess(err error, hub string) error {
	if err == nil {
		return nil
	}
	m := strings.ToLower(err.Error())
	for _, w := range []string{"repository not found", "authentication failed", "could not read username", "permission denied", "error: 403", "terminal prompts disabled"} {
		if strings.Contains(m, w) {
			return protocol.Errorf(protocol.CodeUnavailable, "the Specialist hub (%s) is private and GitHub refused this account: sign in with an account that has access (Settings → GitHub), then Refresh. %v", repoKey(hub), err)
		}
	}
	return err
}

type marketSetIn struct {
	Repo string `json:"repo"` // "" returns to the default hub
}

// marketplaceSet points PenguPool at another hub repository (or back at the default). Its clone is
// made on the next refresh; helpers already installed stay as they are.
func (p *Pool) marketplaceSet(_ context.Context, in marketSetIn) (any, error) {
	repo := strings.TrimSpace(in.Repo)
	if repo == mainMarketplace || repoKey(repo) == repoKey(mainMarketplace) {
		repo = ""
	}
	if repo != "" {
		if strings.HasPrefix(repo, "-") || strings.ContainsAny(repo, " \t\n") || !(strings.Contains(repo, "://") || strings.Contains(repo, "@") || strings.HasPrefix(repo, "/")) {
			return nil, invalid("%q is not a Git repository address (https://…, git@host:owner/name or a local path)", in.Repo)
		}
	}
	cfg := p.st.Config()
	cfg.CatalogRepo = repo
	return nil, p.st.SaveConfig(cfg)
}

// ensureMarket clones the catalog on first use.
func (p *Pool) ensureMarket(ctx context.Context) (string, error) {
	return p.ensureMarketFor(ctx, p.catalogURL())
}

func (p *Pool) ensureMarketFor(ctx context.Context, url string) (string, error) {
	dir := p.marketDirFor(url)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	_, err := p.clone(ctx, url, dir)
	return dir, hubAccess(err, url)
}

// readCatalog is readMarketplace that says in the log, once per process, when the old file was the one
// read and which entries were skipped and why.
func (p *Pool) readCatalog(dir string) (*Marketplace, error) {
	m, err := readMarketplace(dir)
	if m == nil {
		return m, err
	}
	once := func(msg string) {
		if _, seen := p.logged.LoadOrStore(msg, true); !seen {
			p.logf("pool: library sync: %s", msg)
		}
	}
	if m.Legacy {
		once("the hub has no " + marketFile + "; read " + legacyMarketFile + " instead")
	}
	for _, w := range m.Problems {
		once(w)
	}
	return m, err
}

func readMarketplace(dir string) (*Marketplace, error) {
	b, err := os.ReadFile(filepath.Join(dir, marketFile))
	legacy := false
	if errors.Is(err, fs.ErrNotExist) {
		b, err = os.ReadFile(filepath.Join(dir, legacyMarketFile))
		legacy = true
	}
	if err != nil {
		return nil, invalid("the repository has no %s", marketFile)
	}
	file := map[bool]string{false: marketFile, true: legacyMarketFile}[legacy]
	var raw struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Agents      json.RawMessage `json:"agents"`
		Plugins     json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, invalid("%s: %v", file, err)
	}
	m := &Marketplace{Name: raw.Name, Description: raw.Description, Legacy: legacy}
	type cand struct {
		e   MarketEntry
		why string
	}
	var cands []cand
	if legacy { // the old file's "plugins"; a new-format file ignores them
		var plugins []legacyPlugin
		if err := json.Unmarshal(raw.Plugins, &plugins); raw.Plugins != nil && err != nil {
			return nil, invalid("%s: %v", file, err)
		}
		for _, c := range plugins {
			if e, ok := c.entry(); ok {
				cands = append(cands, cand{e: e})
			} else {
				m.Skipped++ // bundled in the catalog, or not a Git source PenguPool installs: not a mistake
			}
		}
	} else {
		// Each entry is decoded on its own: a wrongly typed field skips that entry, not the hub.
		var agents []json.RawMessage
		if err := json.Unmarshal(raw.Agents, &agents); raw.Agents != nil && err != nil {
			return nil, invalid("%s: %v", file, err)
		}
		for _, a := range agents {
			var c catalogEntry
			if err := json.Unmarshal(a, &c); err != nil {
				var named struct{ Name string }
				_ = json.Unmarshal(a, &named)
				cands = append(cands, cand{MarketEntry{Name: named.Name}, "it is not a valid entry: " + err.Error()})
				continue
			}
			e, why := c.entry()
			cands = append(cands, cand{e, why})
		}
	}
	// A name the hub gives twice is ambiguous: neither entry is trusted, so a stray copy cannot stand in
	// for the real helper (the sync matches an installed helper by name too). The installed one stays as it is.
	names := map[string]int{}
	for _, c := range cands {
		names[strings.ToLower(c.e.Name)]++
	}
	for _, c := range cands {
		why := c.why
		if why == "" {
			if c.e.Name != "" && names[strings.ToLower(c.e.Name)] > 1 {
				why = "the name is used more than once, so none of them is read"
			} else {
				why = problem(c.e)
			}
		}
		if why != "" {
			m.Skipped++
			m.Problems = append(m.Problems, fmt.Sprintf("the hub's entry %q is skipped: %s (%s)", c.e.Name, why, file))
			continue
		}
		m.Skills = append(m.Skills, c.e)
	}
	return m, nil
}

type marketIn struct {
	Name string `json:"name,omitempty"` // install: entry name
	Path string `json:"path,omitempty"` // install: destination (default <store>/skills/<name>)
	Auto bool   `json:"-"`              // install: by the library sync, not the owner's click
}

func (p *Pool) marketplaceRefresh(ctx context.Context, _ struct{}) (any, error) {
	if !p.syncMu.TryLock() {
		return nil, conflict("sync already running")
	}
	defer p.syncMu.Unlock()
	dir, err := p.pullCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = p.readCatalog(dir); err != nil {
		return nil, err
	}
	return p.syncLocked(ctx), nil
}

// pullCatalog fast-forwards the catalog clone to its remote and logs when it moved.
func (p *Pool) pullCatalog(ctx context.Context) (string, error) {
	dir, err := p.ensureMarket(ctx)
	if err != nil {
		return "", err
	}
	was, _ := p.gitIn(ctx, dir, "rev-parse", "HEAD")
	if _, err := p.ghGit(ctx, dir, "pull", "--ff-only", "-q"); err != nil {
		return "", hubAccess(err, p.catalogURL())
	}
	if now, _ := p.gitIn(ctx, dir, "rev-parse", "HEAD"); now != was {
		p.logf("pool: library sync: catalog %s→%s", was[:min(len(was), 7)], now[:min(len(now), 7)])
	}
	return dir, nil
}

// refreshLibrary is the host's own Refresh, a minute after it starts and every 6 hours: pull the
// catalog, then sync the library. A sync already running makes it a no-op; a failed pull (offline)
// still syncs from the clone as it is. Only once the catalog has been cloned (the owner opened
// Specialists or the marketplace): a new host installs nothing on its own.
func (p *Pool) refreshLibrary(ctx context.Context) {
	if _, err := os.Stat(p.marketDir()); err != nil {
		return
	}
	if !p.syncMu.TryLock() {
		p.logf("pool: library sync: already running; skipped")
		return
	}
	defer p.syncMu.Unlock()
	if _, err := p.pullCatalog(ctx); err != nil {
		p.logf("pool: library sync: pull the catalog: %v (syncing from the clone as it is)", err)
	}
	p.syncLocked(ctx)
}

// syncLibrary points the Skill library at the marketplace (owner, 2026-10-20: "you should be
// pointing to the market place instead of pointing to individual repos"): every helper the catalog
// carries is installed at its pin, and a helper registered from its own repository with the same
// skills moves to its catalog folder in place, keeping its id, memory and log. Runs when the host
// starts and after each refresh; a sync already running makes it a no-op (nil). A helper PenguPool
// installed (its clone under <store>/skills) whose pin or folder moved is checked out at the new
// pin, its clone follows the catalog's group folder, and its catalog description, skills and group
// are refreshed; a helper in the owner's own folder is never moved A helper the catalog drops stays installed; the owner deletes it.
func (p *Pool) syncLibrary(ctx context.Context) map[string][]string {
	if !p.syncMu.TryLock() {
		p.logf("pool: library sync: already running; skipped")
		return nil
	}
	defer p.syncMu.Unlock()
	return p.syncLocked(ctx)
}

func (p *Pool) syncLocked(ctx context.Context) map[string][]string {
	p.unlockClones() // the sync's Git work, moves and removals need write; read-only again after, also on error
	defer p.lockClones(ctx)
	out := map[string][]string{"added": {}, "moved": {}, "updated": {}, "failed": {}}
	v, err := p.marketplaceList(ctx, struct{}{})
	if err != nil {
		p.logf("pool: library sync: %v", err)
		return out
	}
	cats := map[string]bool{} // the catalog's groups: a helper in one follows its entry
	for _, e := range v.(marketView).Skills {
		if e.Category != "" {
			cats[strings.ToLower(groupName(e.Category))] = true
		}
	}
	for _, e := range v.(marketView).Skills {
		if e.SkillID == "" && p.db != nil && p.db.Setting(deletedKey(e.key())) != "" {
			continue // the owner deleted it: installed again only by the owner
		}
		if e.SkillID != "" {
			if path, updated, err := p.updateHelper(ctx, e, cats); err != nil {
				out["failed"] = append(out["failed"], e.Name+": "+err.Error())
				p.logf("pool: library sync: %s: %v", e.Name, err)
			} else if path != "" {
				out["moved"] = append(out["moved"], e.Name)
				p.logf("pool: library sync: %s moved to %s", e.Name, path)
			} else if updated {
				out["updated"] = append(out["updated"], e.Name)
				p.logf("pool: library sync: %s updated", e.Name)
			}
			continue
		}
		if _, err := p.marketplaceInstall(ctx, marketIn{Name: e.Name, Auto: true}); err != nil {
			out["failed"] = append(out["failed"], e.Name+": "+err.Error())
			p.logf("pool: library sync: %s: %v", e.Name, err)
		} else {
			out["added"] = append(out["added"], e.Name)
			p.logf("pool: library sync: added %s from the Specialist hub", e.Name)
		}
	}
	p.dropOrphans(ctx)
	return out
}

// dropOrphans removes what PenguPool cloned under <store>/skills and no helper points into any more, when it holds none of the owner's work (changes, ignored files,
// commits on no remote), and group folders left empty. A repository without an origin is never removed.
func (p *Pool) dropOrphans(ctx context.Context) {
	p.cloneMu.Lock()
	defer p.cloneMu.Unlock()
	skills := p.st.State().Domain.Skills
	used := func(dir string) bool {
		return slices.ContainsFunc(skills, func(s protocol.Skill) bool { return within(s.Path, dir) })
	}
	var visit func(dir string, top bool)
	visit = func(dir string, top bool) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			sub := filepath.Join(dir, e.Name())
			if !e.IsDir() || used(sub) {
				continue
			}
			if _, err := os.Stat(filepath.Join(sub, ".git")); err != nil {
				if top { // a group folder
					visit(sub, false)
					if removeIfEmpty(sub) {
						p.logf("pool: library sync: removed the empty group folder %s", sub)
					}
				}
				continue
			}
			if _, err := p.gitIn(ctx, sub, "remote", "get-url", "origin"); err != nil {
				continue
			}
			if p.ownersWork(ctx, sub) {
				continue
			}
			if os.RemoveAll(sub) == nil {
				p.logf("pool: library sync: removed %s, a clone no specialist uses", sub)
			}
		}
	}
	visit(filepath.Join(p.st.Dir(), "skills"), true)
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	in := func(path, dir string) bool {
		return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
	}
	return in(path, dir) || in(realPath(path), realPath(dir))
}

type marketView struct {
	URL         string            `json:"url"`
	Default     string            `json:"default"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Error       string            `json:"error,omitempty"`
	Skipped     int               `json:"skipped,omitempty"`
	Skills      []marketEntryView `json:"skills"`
}

type marketEntryView struct {
	MarketEntry
	SkillID         string `json:"skill_id,omitempty"`         // the registered skill with this repository
	InstalledCommit string `json:"installed_commit,omitempty"` // its checkout's HEAD
}

// marketplaceList reads the catalog and matches entries to registered skills by repository
// and folder, so a skill registered from a local checkout shows as installed. A helper the catalog
// moved to another folder of the same repository (same name, PenguPool's clone) is still that entry.
func (p *Pool) marketplaceList(ctx context.Context, _ struct{}) (any, error) {
	type reg struct {
		id, commit, repo, name, path string
	}
	byRepo := map[string]reg{}
	var regs []reg
	for _, sk := range p.st.State().Domain.Skills {
		if origin, err := p.gitIn(ctx, sk.Path, "remote", "get-url", "origin"); err == nil {
			head, _ := p.gitIn(ctx, sk.Path, "rev-parse", "HEAD")
			r := reg{sk.ID, head, repoKey(origin), sk.Name, sk.Path}
			if k := r.repo + subdirKey(ctx, sk.Path); byRepo[k].id == "" {
				byRepo[k] = r
			}
			regs = append(regs, r)
		}
	}
	v := marketView{URL: p.catalogURL(), Default: mainMarketplace, Name: repoKey(p.catalogURL()), Skills: []marketEntryView{}}
	dir, err := p.ensureMarket(ctx)
	var m *Marketplace
	if err == nil {
		m, err = p.readCatalog(dir)
	}
	if err != nil {
		v.Error = err.Error()
		return v, nil
	}
	v.Name, v.Description, v.Skipped = m.Name, m.Description, m.Skipped
	for _, e := range m.Skills {
		r, ok := byRepo[e.key()]
		if !ok {
			for _, c := range regs {
				if _, own := p.ownClone(ctx, c.path); own && strings.EqualFold(c.name, e.Name) && c.repo == repoKey(e.Repo) {
					r = c
					break
				}
			}
		}
		v.Skills = append(v.Skills, marketEntryView{MarketEntry: e, SkillID: r.id, InstalledCommit: r.commit})
	}
	return v, nil
}

// marketplaceInstall clones an entry's repository at its pinned commit, or the latest of its ref when
// it has no sha, into the Skill library;
// a git-subdir entry registers its folder of that clone.
func (p *Pool) marketplaceInstall(ctx context.Context, in marketIn) (any, error) {
	dir, err := p.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	m, err := p.readCatalog(dir)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(m.Skills, func(e MarketEntry) bool { return e.Name == in.Name })
	if i < 0 {
		return nil, notFound("no entry %s in %s", in.Name, m.Name)
	}
	e := m.Skills[i]
	dest := firstNonEmpty(in.Path, p.installDest(e))
	pin := e.Commit
	if pin == "" && e.Ref != "" {
		pin = "origin/" + e.Ref // no sha: the latest of its ref (no ref: the clone's default branch)
	}
	res, err := p.skillAdd(ctx, skillAddIn{Name: e.Name, Path: dest, URL: e.Repo, Commit: pin, Subdir: e.Path,
		Description: e.Description, Capabilities: e.Capabilities, Harness: e.Harness, Requires: e.Requires, Group: groupName(e.Category), Category: e.Category, Credits: e.Credits, Auto: in.Auto})
	if err == nil && !in.Auto && p.db != nil && p.db.Setting(deletedKey(e.key())) != "" {
		_ = p.db.SetSetting(deletedKey(e.key()), "") // the owner installs a helper they deleted: the sync keeps it again
	}
	return res, err
}

// installDest is where PenguPool clones a catalog helper: its group folder, as the marketplace lays
// out agents/<category>/<name>.
func (p *Pool) installDest(e MarketEntry) string {
	if e.Category != "" {
		return filepath.Join(p.st.Dir(), "skills", slug(e.Category), slug(e.Name))
	}
	return filepath.Join(p.st.Dir(), "skills", slug(e.Name))
}

// catalogFolder: a helper kept in the marketplace repository itself must sit in its group's folder,
// agents/<category>/<name>; the catalog's check rejects anything else. Helpers in their own
// repositories are exempt.
func catalogFolder(origin, sub, group, name string) error {
	if repoKey(origin) != repoKey(mainMarketplace) {
		return nil
	}
	if group == "" {
		return conflict("give %s a group first; the Specialist hub keeps it in agents/<group>/%s", name, name)
	}
	cat := slug(group)
	if cat != strings.ToLower(group) {
		return conflict("use a one-word group for %s (letters, digits and dashes, like %s); it names its Specialist hub folder", name, cat)
	}
	if want := "agents/" + cat + "/" + name; strings.TrimPrefix(sub, "/") != want {
		return conflict("move %s to %s in the Specialist hub first", name, want)
	}
	return nil
}

// updateHelper brings an installed catalog helper to its entry: the catalog's description, skills,
// agent tags and group (unless the owner made the group) always; and, when PenguPool owns the clone
// (under <store>/skills), it is clean and no session of the helper runs (a session uses its folder),
// the pin, the helper's folder in it, and the clone's place in the catalog's group folder; a later
// sync retries. Each step is recorded as it lands on disk, so the record never names a folder that
// is not there. It reports the helper's new path when it moved, and whether the pin moved.
func (p *Pool) updateHelper(ctx context.Context, e marketEntryView, cats map[string]bool) (string, bool, error) {
	d := p.st.State().Domain
	sk := findSkill(&d, e.SkillID)
	if sk == nil {
		return "", false, nil
	}
	if err := p.update(func(st *store.State) error {
		if s := findSkill(&st.Domain, e.SkillID); s != nil {
			s.Description, s.Harness, s.Requires = e.Description, e.Harness, normTags(e.Requires)
			if len(e.Capabilities) > 0 {
				s.Capabilities = normTags(e.Capabilities)
			}
			if g := strings.ToLower(s.Group); g == "" || g == strings.ToLower(groupName(s.Category)) || cats[g] {
				s.Group = groupName(e.Category) // came from the catalog: follows it; an owner-made group stays
			}
			s.Category, s.Credits = e.Category, e.Credits
		}
		return nil
	}); err != nil {
		return "", false, err
	}
	top, own := p.ownClone(ctx, sk.Path)
	if !own {
		return "", false, nil // the owner's own checkout: theirs to move
	}
	sub := filepath.FromSlash(subdirKey(ctx, sk.Path))
	root, ok := strings.CutSuffix(sk.Path, sub) // the clone, as the store names it
	if !ok || realPath(root) != realPath(top) {
		p.logf("pool: library sync: %s: %s is not where its clone %s says; left alone", sk.Name, sk.Path, top)
		return "", false, nil
	}
	if e.Commit == "" { // no sha: follow the latest of its ref
		if _, err := p.ghGit(ctx, root, "fetch", "-q", "origin"); err != nil {
			p.logf("pool: library sync: %s: fetch: %v (left as it is)", sk.Name, err)
			return "", false, nil
		}
		tip, err := p.gitIn(ctx, root, "rev-parse", "--verify", "origin/"+firstNonEmpty(e.Ref, "HEAD")+"^{commit}")
		if err != nil {
			return "", false, err
		}
		e.Commit = tip
	}
	path, dest := filepath.Join(root, filepath.FromSlash(e.Path)), p.installDest(e.MarketEntry)
	repin, relocate := e.InstalledCommit != e.Commit || path != sk.Path, realPath(root) != realPath(dest)
	if !repin && !relocate {
		return "", false, nil
	}
	running := func() bool {
		if slices.ContainsFunc(p.st.State().Sessions, func(s store.Session) bool { return s.SkillID == sk.ID && s.State == "running" }) {
			p.logf("pool: library sync: %s waits while a session of it runs; a later sync updates it", sk.Name)
			return true
		}
		return false
	}
	if running() {
		return "", false, nil
	}
	dirty, err := p.gitIn(ctx, root, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if realChanges(dirty) != "" {
		return "", false, conflict("%s has local changes; not updated", root)
	}
	if repin {
		if _, err := p.ghGit(ctx, root, "fetch", "-q", "origin"); err != nil {
			return "", false, err
		}
		if _, err := p.gitIn(ctx, root, "cat-file", "-e", e.Commit+":"+e.Path); err != nil {
			return "", false, invalid("%s has no folder %s at %s", e.Repo, e.Path, e.Commit[:10])
		}
	}
	setPath := func(from, to string) error { // only over the path this sync read
		var was, now string
		if err := p.update(func(st *store.State) error {
			s := findSkill(&st.Domain, sk.ID)
			if s == nil || s.Path != from {
				return conflict("%s changed during the sync", sk.Name)
			}
			was = agentOf(*s)
			s.Path, s.Key = to, agentKey(ctx, to)
			now = agentOf(*s)
			return nil
		}); err != nil {
			return err
		}
		return p.rekey(was, now)
	}
	cur := sk.Path
	if relocate {
		inside := slices.ContainsFunc(p.st.State().Domain.Skills, func(s protocol.Skill) bool { return s.ID != sk.ID && within(s.Path, root) })
		_, taken := os.Lstat(dest)
		switch {
		case inside:
			p.logf("pool: library sync: %s stays at %s; another specialist is in that clone", sk.Name, root)
		case taken == nil:
			p.logf("pool: library sync: %s stays at %s; %s already exists", sk.Name, root, dest)
		case running(): // a session started during the fetch: nothing has changed on disk yet
			return "", false, nil
		default:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", false, err
			}
			if err := os.Rename(root, dest); err != nil {
				return "", false, err
			}
			moved := dest + sub
			if err := setPath(cur, moved); err != nil {
				if back := os.Rename(dest, root); back != nil {
					p.logf("pool: library sync: %s: moving %s back failed: %v", sk.Name, dest, back)
				}
				return "", false, err
			}
			root, cur = dest, moved
			path = filepath.Join(dest, filepath.FromSlash(e.Path))
		}
	}
	if repin {
		if _, err := p.gitIn(ctx, root, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", e.Commit, "--"); err != nil {
			return pathIf(cur, sk.Path), false, err
		}
		if path != cur {
			if err := setPath(cur, path); err != nil { // back to the commit the record describes
				_, _ = p.gitIn(ctx, root, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", e.InstalledCommit, "--")
				return pathIf(cur, sk.Path), false, err
			}
			cur = path
		}
	}
	if _, err := p.skillRescan(ctx, idIn{ID: sk.ID}); err != nil {
		return pathIf(cur, sk.Path), false, err
	}
	return pathIf(cur, sk.Path), e.InstalledCommit != e.Commit, nil
}

// pathIf is now when it differs from was, else "".
func pathIf(now, was string) string {
	if now == was {
		return ""
	}
	return now
}

// ownClone is the root of the clone holding path, and whether PenguPool made it (a folder under
// <store>/skills). The owner's own checkouts are never moved or removed.
func (p *Pool) ownClone(ctx context.Context, path string) (string, bool) {
	root, err := p.gitIn(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	return root, strings.HasPrefix(realPath(root), realPath(filepath.Join(p.st.Dir(), "skills"))+string(filepath.Separator))
}

func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}
