package pool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/harness"
)

// pretrustFolder marks a helper's own folder as trusted in its agent's own list, so the agent starts
// without its folder-trust dialog. Only the folder PenguPool made for the helper, only an entry that
// is missing, and nothing else in the file changes. Agents without such a list are left alone.
// from is the repository a worktree was made from: its folder carries the repository's own agent
// settings and hooks, so it is trusted only when the owner has already trusted that repository (or a
// folder above it) in the agent; "" for a folder with nothing of a repository in it.
func pretrustFolder(kind, dir, from string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // the agents key their lists by the real path
	}
	if real, err := filepath.EvalSymlinks(from); from != "" && err == nil {
		from = real
	}
	switch kind {
	case harness.Claude:
		return pretrustClaude(filepath.Join(home, ".claude.json"), dir, from)
	case harness.Codex:
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return pretrustCodex(filepath.Join(codexHome, "config.toml"), dir, from)
	}
	return nil
}

// pretrustClaude sets projects[dir].hasTrustDialogAccepted in Claude Code's ~/.claude.json. The file
// is Claude's own and busy: it is read and written back in one quick pass, and if it changed under us
// the pass runs again. A missing file is left for Claude to create.
func pretrustClaude(path, dir, from string) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // a link to a dotfiles repository stays a link: the rename is over its target
	}
	for try := 0; try < 3; try++ {
		before, err := os.Stat(path)
		if err != nil {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // numbers stay as written
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		projects, _ := doc["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
		}
		entry, _ := projects[dir].(map[string]any)
		if entry != nil && entry["hasTrustDialogAccepted"] == true {
			return nil
		}
		if from != "" && !claudeTrusts(projects, from) {
			return nil // the owner has not trusted the repository: its dialog is the owner's to answer
		}
		if entry == nil {
			entry = map[string]any{}
		}
		entry["hasTrustDialogAccepted"] = true
		projects[dir] = entry
		doc["projects"] = projects
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.pengupool-*")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(out)
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("%s: %v %v", tmp.Name(), werr, cerr)
		}
		_ = os.Chmod(tmp.Name(), before.Mode().Perm())
		if after, err := os.Stat(path); err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
			_ = os.Remove(tmp.Name()) // Claude wrote it meanwhile: start over from what it wrote
			continue
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			_ = os.Remove(tmp.Name())
			return err
		}
		return nil
	}
	return fmt.Errorf("%s kept changing", path)
}

// claudeTrusts: the folder, or one above it, is trusted in Claude's list.
func claudeTrusts(projects map[string]any, dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if e, _ := projects[d].(map[string]any); e != nil && e["hasTrustDialogAccepted"] == true {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// pretrustCodex appends [projects."dir"] trust_level = "trusted" to Codex's config.toml unless the file
// already says anything about that folder, in any TOML spelling (a basic or literal key, a dotted key,
// an inline table): whatever the owner set stands, even "untrusted". Without a TOML parser it is
// cautious: the folder's name anywhere in the file, quoted, counts as already set.
func pretrustCodex(path, dir, from string) error {
	if strings.IndexFunc(dir, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("folder name not written to a TOML key: %q", dir)
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(old)
	if tomlNames(text, dir) {
		return nil
	}
	if from != "" && !codexTrusts(text, from) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	block := `[projects."` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(dir) + "\"]\ntrust_level = \"trusted\"\n"
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		block = "\n" + block
	}
	if len(old) > 0 {
		block = "\n" + block
	}
	_, werr := f.WriteString(block)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// tomlNames: the file has dir as a quoted string, written either way TOML allows.
func tomlNames(text, dir string) bool {
	basic := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(dir)
	return strings.Contains(text, `"`+basic+`"`) || !strings.ContainsAny(dir, `'`) && strings.Contains(text, `'`+dir+`'`)
}

// codexTrusts: the file has a [projects."dir"] (or 'dir') table with trust_level "trusted" for the
// folder or one above it.
func codexTrusts(text, dir string) bool {
	header := regexp.MustCompile(`^\s*\[\s*projects\s*\.\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)')\s*\]`)
	level := regexp.MustCompile(`^\s*trust_level\s*=\s*["']trusted["']`)
	trusted := map[string]bool{}
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		if m := header.FindStringSubmatch(line); m != nil {
			cur = m[2]
			if m[1] != "" {
				cur = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(m[1])
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			cur = ""
			continue
		}
		if cur != "" && level.MatchString(line) {
			trusted[cur] = true
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		if trusted[d] {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}
