package pool

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// The Skill library: registered skill repositories. A registration starts no session; a helper is
// placed from the map's Skill agent pool. A skill's memory and logs live in agents.db (memory.go).

//go:embed all:skilltemplate
var skillTemplate embed.FS

const (
	maxSkillBody  = 8 << 10
	maxSkillLines = 500
)

type skillAddIn struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	URL          string   `json:"url,omitempty"`    // git source
	Commit       string   `json:"commit,omitempty"` // pin for a git source (default: the cloned HEAD)
	Subdir       string   `json:"subdir,omitempty"` // git source: the skill is this folder of the clone
	Method       string   `json:"method,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Harness      string   `json:"harness,omitempty"`
	Requires     []string `json:"requires,omitempty"`
	Group        string   `json:"group,omitempty"`  // set only when the helper has none yet
	Category     string   `json:"-"`                // the catalog's category (marketplace installs)
	Create       bool     `json:"create,omitempty"` // scaffold a new skill repository at Path
	Auto         bool     `json:"-"`                // installed by the library sync, not the owner: off the map
	Task         string   `json:"task,omitempty"`   // skill name for a new repository
}

func (p *Pool) skillAdd(ctx context.Context, in skillAddIn) (_ any, err error) {
	path := strings.TrimSpace(in.Path)
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return nil, invalid("a specialist needs an absolute path")
	}
	path = filepath.Clean(path)
	source, commit := "local", ""
	switch {
	case in.Create:
		task := slug(firstNonEmpty(in.Task, in.Name))
		if err := scaffold(path, filepath.Base(path), task, firstNonEmpty(in.Description, in.Method, task)); err != nil {
			return nil, err
		}
	case in.URL != "":
		p.cloneMu.RLock() // the orphan clean-up waits until the clone is registered
		defer p.cloneMu.RUnlock()
		dest, cerr := p.clone(ctx, in.URL, path)
		if cerr != nil {
			return nil, cerr
		}
		defer func() { // clone made dest new: a failed add leaves nothing behind
			if err != nil {
				os.RemoveAll(dest)
			}
		}()
		if in.Commit != "" {
			if out, err := exec.CommandContext(ctx, "git", "-C", dest, "-c", "advice.detachedHead=false", "checkout", "--detach", in.Commit, "--").CombinedOutput(); err != nil {
				return nil, invalid("pin %s: %s", in.Commit, strings.TrimSpace(string(out)))
			}
		}
		source = "git"
		commit = headCommit(dest)
		if in.Subdir != "" {
			if !filepath.IsLocal(filepath.FromSlash(in.Subdir)) {
				return nil, invalid("the skill's folder %q must be inside its repository", in.Subdir)
			}
			path = filepath.Join(dest, filepath.FromSlash(in.Subdir))
			if _, err := absDir(path); err != nil {
				return nil, invalid("%s has no folder %s at %s", in.URL, in.Subdir, commit)
			}
		}
	default:
		if within(path, filepath.Join(p.st.Dir(), "skills")) {
			p.cloneMu.RLock() // a delete removing this clone finishes first
			defer p.cloneMu.RUnlock()
		}
		if _, err := absDir(path); err != nil {
			return nil, err
		}
	}
	sk := protocol.Skill{
		ID: protocol.NewID(), Key: agentKey(ctx, path), Name: strings.TrimSpace(firstNonEmpty(in.Name, filepath.Base(path))), Path: path,
		Method: in.Method, Description: in.Description, Capabilities: normTags(in.Capabilities),
		Harness: in.Harness, Requires: normTags(in.Requires), Group: groupName(in.Group), Category: in.Category, Source: source, Commit: commit, CreatedAt: now(),
		OnMap: !in.Auto,
	}
	if err := checkRequires(sk.Requires); err != nil {
		return nil, err
	}
	files, profiles, err := scanSkills(path)
	if err != nil {
		return nil, err
	}
	sk.Files, sk.Profiles = files, profiles
	if len(sk.Capabilities) == 0 {
		for _, f := range files {
			sk.Capabilities = append(sk.Capabilities, f.Name)
		}
	}
	err = p.update(func(st *store.State) error {
		// PenguPool's own clones are registered by the Specialist hub; a folder of
		// them added by hand may be one a delete is removing.
		if in.URL == "" && !in.Create && within(path, filepath.Join(p.st.Dir(), "skills")) {
			return invalid("%s is one of PenguPool's own clones and no specialist's; install the specialist from the Specialist hub instead", path)
		}
		for _, o := range st.Domain.Skills {
			if o.Key != "" && o.Key == sk.Key {
				return conflict("%s is already the specialist %s (one agent per repository)", sk.Key, o.Name)
			}
			if o.Path == path {
				return conflict("%s is already the specialist %s", path, o.Name)
			}
			if strings.EqualFold(o.Name, sk.Name) {
				return conflict("there is already a specialist named %s", sk.Name)
			}
		}
		st.Domain.Skills = append(st.Domain.Skills, sk)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !in.Auto && p.db != nil { // the owner adds a helper they deleted: the sync keeps it again
		if k := deletedKey(p.catalogKey(ctx, path)); p.db.Setting(k) != "" {
			_ = p.db.SetSetting(k, "")
		}
	}
	if !in.Auto { // the sync locks every clone once it is done
		p.lockClonesIdle(ctx)
	}
	return map[string]any{"skill": sk}, nil
}

// agentKey is a repository's identity beyond this machine: its normalised origin remote
// ("github.com/owner/repo"). Without one there is none (a folder name would match unrelated
// repositories); such an agent is revived by its path only.
func agentKey(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return normRemote(strings.TrimSpace(string(out))) + subdirKey(ctx, dir)
}

// subdirKey is "/<folder>" for a skill that is a folder inside its repository (several helpers
// can share one catalog repository), "" for a skill at the repository root.
func subdirKey(ctx context.Context, dir string) string {
	out, _ := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-prefix").Output()
	if s := strings.Trim(strings.TrimSpace(string(out)), "/"); s != "" {
		return "/" + s
	}
	return ""
}

// normRemote turns https://, ssh:// and scp-style Git URLs into "host/owner/repo".
func normRemote(u string) string {
	u = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if at := strings.Index(u, "@"); at >= 0 && strings.Contains(u[at:], ":") { // git@host:owner/repo
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	if at := strings.LastIndex(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") { // user@ in a URL
		u = u[at+1:]
	}
	return strings.ToLower(u)
}

type skillUpdateIn struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Method       string   `json:"method,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Harness      string   `json:"harness,omitempty"`
	Requires     []string `json:"requires"`
	Group        *string  `json:"group,omitempty"` // absent: unchanged; "": ungrouped
	OnMap        *bool    `json:"on_map,omitempty"`
}

// groupName trims a group and caps it at 40 characters.
func groupName(g string) string {
	r := []rune(strings.TrimSpace(g))
	return string(r[:min(len(r), 40)])
}

func (p *Pool) skillUpdate(ctx context.Context, in skillUpdateIn) (any, error) {
	var out protocol.Skill
	return &out, p.update(func(st *store.State) error {
		sk := findSkill(&st.Domain, in.ID)
		if sk == nil {
			return notFound("no skill entry %s", in.ID)
		}
		if in.Name != "" {
			sk.Name = in.Name
		}
		if in.Requires == nil && (in.Method != "" || in.Description != "" || in.Harness != "") {
			return invalid("send requires with the other fields (method, description, harness)")
		}
		if in.Requires != nil { // the Overview's form, which always sends requires; a switch alone sends none
			if err := checkRequires(in.Requires); err != nil {
				return err
			}
			sk.Method, sk.Description, sk.Harness, sk.Requires = in.Method, in.Description, in.Harness, normTags(in.Requires)
		}
		if in.Capabilities != nil {
			sk.Capabilities = normTags(in.Capabilities)
		}
		if in.Group != nil {
			sk.Group = groupName(*in.Group)
		}
		if in.OnMap != nil {
			sk.OnMap = *in.OnMap
		}
		out = *sk
		return nil
	})
}

// skillRescan reloads SKILL.md files. A git source must be clean: a dirty source update is refused.
func (p *Pool) skillRescan(ctx context.Context, in idIn) (any, error) {
	d := p.st.State().Domain
	sk := findSkill(&d, in.ID)
	if sk == nil {
		return nil, notFound("no skill entry %s", in.ID)
	}
	if sk.Source == "git" {
		out, err := exec.CommandContext(ctx, "git", "-C", sk.Path, "status", "--porcelain").Output()
		if err == nil && realChanges(string(out)) != "" {
			return nil, conflict("%s has uncommitted changes; a dirty source update is refused", sk.Path)
		}
	}
	files, profiles, err := scanSkills(sk.Path)
	if err != nil {
		return nil, err
	}
	var res protocol.Skill
	return &res, p.update(func(st *store.State) error {
		s := findSkill(&st.Domain, in.ID)
		if s == nil {
			return notFound("no skill entry %s", in.ID)
		}
		s.Files, s.Profiles = files, profiles
		if s.Source == "git" {
			s.Commit = headCommit(s.Path)
		}
		res = *s
		return nil
	})
}

type skillDeleteIn struct {
	ID string `json:"id"`
	// ConfirmOwnerWork is the owner_work answer the owner saw and confirmed: the clone goes only
	// while it holds exactly that work.
	ConfirmOwnerWork string `json:"confirm_owner_work,omitempty"`
	DryRun           bool   `json:"dry_run,omitempty"` // only say what would go (the app's confirmation)
}

// skillDelete deletes a helper for good: its memory and experience in agents.db, its
// entry, and the clone PenguPool made for it, in that order, so a failure leaves something to
// retry. The owner's own checkout is never deleted, only unregistered. A clone holding the owner's
// work (changes, ignored files, unpushed commits, stashes) goes only once the owner confirms what
// was found. Its catalog key is remembered, so the library sync does not install it again. Closed
// teams keep their member rows (names) as history.
func (p *Pool) skillDelete(ctx context.Context, in skillDeleteIn) (any, error) {
	if !in.DryRun {
		if !p.syncMu.TryLock() { // the sync moves, updates and removes clones
			return nil, conflict("a library sync is running; delete the specialist when it ends")
		}
		defer p.syncMu.Unlock()
	}
	return p.deleteHelper(ctx, in)
}

// deleteHelper is skillDelete for a caller that holds the sync lock (the library sync itself).
func (p *Pool) deleteHelper(ctx context.Context, in skillDeleteIn) (any, error) {
	if !in.DryRun {
		p.cloneMu.Lock() // no clone is registered or cleaned up while its folder goes
		defer p.cloneMu.Unlock()
	}
	st := p.st.State()
	sk := findSkill(&st.Domain, in.ID)
	if sk == nil {
		return nil, notFound("no skill entry %s", in.ID)
	}
	name, folder, kept := sk.Name, "", ""
	if root, own := p.ownClone(ctx, sk.Path); !own {
		kept = "its folder stays on disk: it is your own checkout, not a clone PenguPool made"
		if _, err := os.Stat(sk.Path); err != nil {
			kept = "its folder was already gone"
		}
	} else if o := slices.IndexFunc(st.Domain.Skills, func(o protocol.Skill) bool { return o.ID != sk.ID && within(o.Path, root) }); o >= 0 {
		kept = "its clone stays: the specialist " + st.Domain.Skills[o].Name + " uses it too"
	} else {
		folder = root
	}
	if err := deletable(&st, sk.ID, folder); err != nil {
		return nil, err
	}
	if in.DryRun {
		n := 0
		if p.db != nil {
			n, _ = p.db.AgentTotals(agentOf(*sk))
		}
		return map[string]any{"name": name, "folder": folder, "folder_kept": kept, "entries": n}, nil
	}
	if folder != "" {
		if work := p.ownersWorkFound(ctx, folder); work != "" {
			if msg := fmt.Sprintf("%s holds work of yours that would be lost:\n%s", folder, work); msg != strings.TrimPrefix(in.ConfirmOwnerWork, protocol.CodeOwnerWork+": ") {
				return nil, protocol.Errorf(protocol.CodeOwnerWork, "%s", msg) // new or changed since the owner looked
			}
		}
	}
	out := map[string]any{"deleted": name, "folder_removed": false, "entries": 0}
	if p.db != nil {
		n, err := p.db.DeleteAgentRecord(agentOf(*sk))
		if err != nil {
			return nil, fmt.Errorf("%s: its journal could not be deleted; nothing was: %w", name, err)
		}
		out["entries"] = n
		for _, k := range p.catalogKeys(ctx, *sk) {
			if err := p.db.SetSetting(deletedKey(k), now()); err != nil {
				return nil, fmt.Errorf("%s: its journal is deleted, its entry is kept: %w", name, err)
			}
		}
	}
	if err := p.update(func(st *store.State) error {
		if err := deletable(st, in.ID, folder); err != nil { // nothing started it since
			return err
		}
		st.Domain.Skills = slices.DeleteFunc(st.Domain.Skills, func(o protocol.Skill) bool { return o.ID == in.ID })
		return nil
	}); err != nil {
		return nil, err
	}
	if kept != "" {
		out["folder_kept"] = kept
	}
	if folder != "" {
		_ = setWritable(folder, true) // a helper's clone is read-only
		if err := os.RemoveAll(folder); err != nil {
			p.logf("pool: delete %s: its clone %s: %v", name, folder, err)
			out["folder_kept"] = "its clone could not be removed: " + err.Error()
		} else {
			out["folder_removed"] = true
			// The category folder it sat in goes too once empty; <store>/skills itself stays.
			if cat := filepath.Dir(folder); within(cat, filepath.Join(p.st.Dir(), "skills")) && realPath(cat) != realPath(filepath.Join(p.st.Dir(), "skills")) {
				removeIfEmpty(cat)
			}
		}
	}
	p.logf("pool: deleted the specialist %s (%s): %v journal entries; folder removed: %v", name, in.ID, out["entries"], out["folder_removed"])
	return out, nil
}

// deletedKey is the setting that remembers a deleted helper's catalog key: the library sync does
// not install it again until the owner adds it.
func deletedKey(key string) string { return "deleted:" + key }

// catalogKeys are the catalog keys a helper goes by: its origin's, and those of the catalog entries
// the marketplace matches to it (read from the catalog clone, never fetched here).
func (p *Pool) catalogKeys(ctx context.Context, sk protocol.Skill) []string {
	var keys []string
	if k := p.catalogKey(ctx, sk.Path); k != "" {
		keys = append(keys, k)
	}
	if _, err := os.Stat(p.marketDir()); err == nil {
		if v, err := p.marketplaceList(ctx, struct{}{}); err == nil {
			for _, e := range v.(marketView).Skills {
				if e.SkillID == sk.ID && !slices.Contains(keys, e.key()) {
					keys = append(keys, e.key())
				}
			}
		}
	}
	return keys
}

// catalogKey is how the marketplace matches an entry to a registered helper: its origin's
// repository plus its folder in it (MarketEntry.key); "" without an origin.
func (p *Pool) catalogKey(ctx context.Context, path string) string {
	origin, err := p.gitIn(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return repoKey(origin) + subdirKey(ctx, path)
}

// removeIfEmpty removes dir when nothing but Finder's .DS_Store files is left in it.
func removeIfEmpty(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil || slices.ContainsFunc(ents, func(e os.DirEntry) bool { return e.IsDir() || e.Name() != ".DS_Store" }) {
		return false
	}
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return os.Remove(dir) == nil
}

// deletable refuses to delete a helper that is on an active team, or runs, or
// whose clone (folder) a running session works in.
func deletable(st *store.State, id, folder string) error {
	sk := findSkill(&st.Domain, id)
	if sk == nil {
		return notFound("no skill entry %s", id)
	}
	for _, t := range st.Domain.Teams {
		if t.State == "active" && slices.ContainsFunc(t.Members, func(m protocol.Member) bool { return m.SkillID == id }) {
			return conflict("%s is on the active team %s; close the team first", sk.Name, t.Name)
		}
	}
	for _, s := range st.Sessions {
		if s.State == "running" && s.SkillID == id {
			return conflict("%s is running in the session %s; end it first", sk.Name, s.Name)
		}
		if s.State == "running" && folder != "" && within(s.Cwd, folder) {
			return conflict("the session %s works in %s; end it first", s.Name, folder)
		}
	}
	return nil
}

// scanSkills reads skills/<name>/SKILL.md and agents/*.md. A file with a problem is listed with
// the problem, not silently dropped.
func scanSkills(root string) ([]protocol.SkillFile, []string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, protocol.Errorf(protocol.CodeNotFound, "skill repository %s: %v", root, err)
	}
	defer r.Close()
	var files []protocol.SkillFile
	ents, _ := fs.ReadDir(r.FS(), "skills")
	names := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		rel := "skills/" + e.Name() + "/SKILL.md"
		b, err := r.ReadFile(rel)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				files = append(files, protocol.SkillFile{Name: e.Name(), Problem: "unreadable: " + err.Error()})
			}
			continue
		}
		f := parseSkill(b)
		if f.Name == "" {
			f.Name = e.Name()
		}
		if f.Problem == "" && f.Name != e.Name() {
			f.Problem = fmt.Sprintf("name %q does not match its folder %q", f.Name, e.Name())
		}
		if names[f.Name] && f.Problem == "" {
			f.Problem = "duplicate skill name"
		}
		names[f.Name] = true
		files = append(files, f)
	}
	var profiles []string
	agents, _ := fs.ReadDir(r.FS(), "agents")
	for _, e := range agents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			profiles = append(profiles, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, profiles, nil
}

var knownSkillKeys = map[string]bool{"name": true, "description": true, "license": true, "compatibility": true,
	"metadata": true, "allowed-tools": true, "triggers": true}

// parseSkill reads a SKILL.md front matter: a small YAML subset (scalars, | and > blocks, lists,
// one level of metadata map) is all the format uses.
func parseSkill(b []byte) protocol.SkillFile {
	sum := sha256.Sum256(b)
	f := protocol.SkillFile{Digest: hex.EncodeToString(sum[:])[:16]}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		f.Problem = "no front matter"
		return f
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		f.Problem = "unterminated front matter"
		return f
	}
	head, body := text[4:4+end], text[4+end+4:]
	fields := map[string]string{}
	meta := map[string]string{}
	lines := strings.Split(head, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		var block []string
		for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || lines[i+1] == "") {
			i++
			block = append(block, strings.TrimSpace(lines[i]))
		}
		switch {
		case k == "metadata":
			for _, l := range block {
				if mk, mv, ok := strings.Cut(l, ":"); ok {
					meta[strings.TrimSpace(mk)] = unquote(strings.TrimSpace(mv))
				}
			}
		case v == "|" || v == ">" || v == "|-" || v == ">-" || (v == "" && len(block) > 0):
			v = strings.TrimSpace(strings.Join(block, " "))
		}
		fields[k] = unquote(v)
		if !knownSkillKeys[k] && f.Problem == "" {
			f.Problem = "unknown metadata key " + k
		}
	}
	f.Name, f.Description = fields["name"], fields["description"]
	f.License, f.Compatibility, f.AllowedTools = fields["license"], fields["compatibility"], fields["allowed-tools"]
	if len(meta) > 0 {
		f.Metadata = meta
	}
	if f.Problem == "" {
		switch {
		case f.Description == "":
			f.Problem = "missing description"
		case len(body) > maxSkillBody:
			f.Problem = fmt.Sprintf("body is %d bytes; the limit is 8 KiB (move detail to references/)", len(body))
		case strings.Count(body, "\n") > maxSkillLines:
			f.Problem = "body is over 500 lines (move detail to references/)"
		}
	}
	return f
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func normTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return r == ',' || r == ' ' }) {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	return out
}

func headCommit(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// scaffold creates a new skill repository from the bundled skill-repo template. It refuses a
// non-empty target.
func scaffold(target, repo, task, description string) error {
	if ents, err := os.ReadDir(target); err == nil && len(ents) > 0 {
		return conflict("%s is not empty; pick another folder", target)
	}
	repl := strings.NewReplacer("{{repo}}", repo, "{{task}}", task, "{{description}}", description,
		"{{date}}", time.Now().Format("2006-01-02"))
	err := fs.WalkDir(skillTemplate, "skilltemplate", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(path, "skilltemplate"), "/")
		rel = strings.ReplaceAll(rel, "__task__", task)
		if rel == "gitignore" {
			rel = ".gitignore"
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := skillTemplate.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".py") && strings.Contains(rel, "scripts/") {
			mode = 0o755
		}
		return os.WriteFile(dst, []byte(repl.Replace(string(b))), mode)
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(target, "memory"), 0o755); err != nil {
		return err
	}
	_ = exec.Command("git", "-C", target, "init", "-q").Run()
	return nil
}

func checkRequires(ids []string) error {
	for _, id := range ids {
		if !harness.IsAgent(strings.ToLower(strings.TrimSpace(id))) {
			return invalid("requires: %q is not an agent (%s)", id, agentIDs())
		}
	}
	return nil
}

// harnessFiles are one agent's own config: name → whether it is a folder.
var harnessFiles = map[string]bool{".claude": true, ".codex": true, ".gemini": true, ".cursor": true, "CLAUDE.md": false, "GEMINI.md": false}

// checkSkillRepo is the conforming checklist, shared by the converter and Propose.
func checkSkillRepo(dir string) []string {
	var out []string
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		out = append(out, "AGENTS.md is missing at the repository root (the specialist's persona and how to use its skills)")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "skills"))
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "skills", e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		n++
		f := parseSkill(b)
		switch {
		case f.Problem != "":
			out = append(out, fmt.Sprintf("skills/%s/SKILL.md: %s", e.Name(), f.Problem))
		case f.Name != e.Name():
			out = append(out, fmt.Sprintf("skills/%s/SKILL.md: name %q must equal its folder name", e.Name(), f.Name))
		}
	}
	if n == 0 {
		out = append(out, "no skills/<name>/SKILL.md")
	}
	// Secrets: what a helper is handed and what the catalog publishes, not build output or tests.
	scan := func(path string) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() < 256<<10 {
			if b, err := os.ReadFile(path); err == nil && detectSecret(string(b)) {
				rel, _ := filepath.Rel(dir, path)
				out = append(out, "possible secret in "+rel)
			}
		}
	}
	// Harness-agnostic: PenguPool hands the skills to any agent through its context, so one agent's
	// own config has no place in a helper: its top folder and its skills/ are checked.
	flag := func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if isDir, ok := harnessFiles[e.Name()]; ok && isDir == e.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			if e.IsDir() {
				rel += "/"
			}
			out = append(out, rel+" is specific to one agent; specialists are harness-agnostic")
			if e.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	}
	top, _ := os.ReadDir(dir)
	for _, e := range top {
		_ = flag(filepath.Join(dir, e.Name()), e, nil)
	}
	_ = filepath.WalkDir(filepath.Join(dir, "skills"), flag)
	scan(filepath.Join(dir, "AGENTS.md"))
	_ = filepath.WalkDir(filepath.Join(dir, "skills"), func(path string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			scan(path)
		}
		return nil
	})
	return out
}

// skillCheck runs the checklist on a registered skill.
func (p *Pool) skillCheck(_ context.Context, in struct {
	ID string `json:"id"`
}) (any, error) {
	d := p.st.State().Domain
	sk := findSkill(&d, in.ID)
	if sk == nil {
		return nil, notFound("no skill entry %s", in.ID)
	}
	return map[string]any{"problems": checkSkillRepo(sk.Path)}, nil
}

// ownersWork reports whether a clone holds anything of the owner's: local changes, ignored files
// (a .env, notes) or commits no remote has. A clone git cannot read counts as holding work.
// realChanges drops the lines of `git status --porcelain` output that name only Finder's .DS_Store
// files: opening a helper's folder in Finder (Reveal) must not count as a local change.
func realChanges(porcelain string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(porcelain), "\n") {
		if l = strings.TrimSpace(l); l != "" && filepath.Base(strings.Trim(l[min(3, len(l)):], `"`)) != ".DS_Store" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func (p *Pool) ownersWork(ctx context.Context, dir string) bool {
	return p.ownersWorkFound(ctx, dir) != ""
}

// ownersWorkFound says what of the owner's a clone holds, one finding a line ("" when nothing): the
// first few changed, untracked or ignored files git reports (untracked ones whatever the owner's
// status.showUntrackedFiles says), commits on HEAD or any local branch that no remote has, and stashes.
func (p *Pool) ownersWorkFound(ctx context.Context, dir string) string {
	out, err := p.gitIn(ctx, dir, "status", "--porcelain", "--ignored", "--untracked-files=all")
	if err != nil {
		return "git cannot read it: " + err.Error()
	}
	var found []string
	if ch := realChanges(out); ch != "" {
		lines := strings.Split(ch, "\n")
		if len(lines) > 5 {
			lines = append(lines[:5], fmt.Sprintf("… and %d more", len(lines)-5))
		}
		found = lines
	}
	// HEAD too: PenguPool's clones sit on a detached HEAD at their pin.
	switch n, err := p.gitIn(ctx, dir, "rev-list", "--count", "HEAD", "--branches", "--not", "--remotes"); {
	case err != nil:
		found = append(found, "git cannot read its commits")
	case n == "1":
		found = append(found, "1 unpushed commit")
	case n != "0":
		found = append(found, n+" unpushed commits")
	}
	switch stashes, err := p.gitIn(ctx, dir, "stash", "list"); {
	case err != nil:
		found = append(found, "git cannot read its stashes")
	case stashes != "":
		found = append(found, fmt.Sprintf("%d stashed changes", strings.Count(stashes, "\n")+1))
	}
	return strings.Join(found, "\n")
}
