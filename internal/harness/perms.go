package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// Agent permissions: with the owner's consent per agent, PenguPool writes rules into that agent's own
// global configuration so PenguPool's team commands run without a permission prompt, in and outside
// PenguPool. Only these fixed commands, never a floor action, experience import, kill or a whole
// command group; the host still checks every call: who may message whom, an ask only from its own
// session, an answer only in a turn the owner's own prompt opened, a team change only as a request
// the owner approves, a Compass wording only from the session PenguPool asked, once, checked as any line.
var CLIAllowed = [][]string{
	{"send"}, {"search"}, {"experience", "search"}, {"experience", "show"},
	{"ask"}, {"answer"}, {"decide"}, {"team", "request", "add"}, {"team", "request", "remove"}, {"compass", "word"},
}

// PermissionAgents are the agents PenguPool writes rules for. The others, with why:
var PermissionAgents = []string{Claude, Codex}

// PermissionNotWritten says why an agent gets no rules.
var PermissionNotWritten = map[string]string{
	OMP:  "OMP asks nothing in its default approval mode, and its command patterns replace a user's list rather than extend it.",
	Pi:   "pi has no permission prompts.",
	Muse: "Muse has no documented format for a saved command rule.",
}

// ClaudeRules are Claude Code Bash rules: this executable shell-quoted exactly as agents are told to
// run it, then the subcommand.
func ClaudeRules(exe string) []string {
	q := shellQuote(exe)
	out := make([]string, len(CLIAllowed))
	for i, c := range CLIAllowed {
		out[i] = "Bash(" + q + " " + strings.Join(c, " ") + " *)"
	}
	return out
}

// CodexRules are Codex prefix rules, one line each: argv tokens, so the path is one token, unquoted.
func CodexRules(exe string) []string {
	out := make([]string, len(CLIAllowed))
	for i, c := range CLIAllowed {
		toks := []string{strconv.Quote(exe)}
		for _, w := range c {
			toks = append(toks, strconv.Quote(w))
		}
		out[i] = `prefix_rule(pattern=[` + strings.Join(toks, ", ") + `], decision="allow")`
	}
	return out
}

// PermissionFile is where an agent's rules go: Claude's user settings, or PenguPool's own file in
// Codex's user rules folder (Codex keeps its own saved rules in default.rules there).
func PermissionFile(agent string, env []string) string {
	home := envOr(env, "HOME", "")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	switch agent {
	case Claude:
		return filepath.Join(envOr(env, "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")), "settings.json")
	case Codex:
		return filepath.Join(envOr(env, "CODEX_HOME", filepath.Join(home, ".codex")), "rules", "pengupool.rules")
	}
	return ""
}

func envOr(env []string, k, def string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], k+"="); ok && v != "" {
			return v
		}
	}
	return def
}

// PermissionWrite is what PenguPool wrote for one agent (kept in its config).
type PermissionWrite = store.AgentPermission

// WritePermissions adds the agent's rules for exe to its file and reports what it wrote.
func WritePermissions(agent, file, exe string) (PermissionWrite, error) {
	switch agent {
	case Claude:
		return addClaude(file, ClaudeRules(exe))
	case Codex:
		lines := CodexRules(exe)
		body := "# Written by PenguPool AI with the owner's consent: its team commands run without a prompt.\n" +
			"# Turn it off in PenguPool's Settings; edits here are replaced.\n" + strings.Join(lines, "\n") + "\n"
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return PermissionWrite{}, err
		}
		if err := writeAtomic(file, []byte(body), 0o644); err != nil {
			return PermissionWrite{}, err
		}
		return PermissionWrite{File: file, Lines: lines, Created: []string{"file"}}, nil
	}
	return PermissionWrite{}, fmt.Errorf("PenguPool writes no rules for %s", agent)
}

// RemovePermissions removes what w recorded and nothing else.
func RemovePermissions(agent string, w PermissionWrite) error {
	switch agent {
	case Claude:
		return removeClaude(w)
	case Codex:
		if err := os.Remove(w.File); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return nil
}

// MissingPermissions are the recorded lines no longer in the file: changed outside PenguPool.
func MissingPermissions(agent string, w PermissionWrite) ([]string, error) {
	b, err := os.ReadFile(w.File)
	if errors.Is(err, os.ErrNotExist) {
		return w.Lines, nil
	}
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	if agent == Claude {
		_, _, allow, err := claudeParts(b, w.File)
		if err != nil {
			return nil, err
		}
		for _, r := range allow {
			var s string
			if json.Unmarshal(r, &s) == nil {
				have[s] = true
			}
		}
	} else {
		for _, l := range strings.Split(string(b), "\n") {
			have[strings.TrimSpace(l)] = true
		}
	}
	var miss []string
	for _, l := range w.Lines {
		if !have[l] {
			miss = append(miss, l)
		}
	}
	return miss, nil
}

// kv is one member of a JSON object, its value kept raw so nothing else in the file changes.
type kv struct {
	k string
	v json.RawMessage
}

// objKVs reads a JSON object in order.
func objKVs(b []byte) ([]kv, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []kv
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, kv{t.(string), v})
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("text after the JSON object")
	}
	return out, nil
}

func encKVs(kvs []kv) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range kvs {
		if i > 0 {
			b.WriteByte(',')
		}
		k := jsonString(m.k)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func find(kvs []kv, k string) int {
	return slices.IndexFunc(kvs, func(m kv) bool { return m.k == k })
}

// claudeParts parses settings.json strictly: the top object, its permissions object and allow list.
func claudeParts(b []byte, file string) (top, perms []kv, allow []json.RawMessage, err error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil, nil, nil
	}
	if !json.Valid(b) {
		var se *json.SyntaxError
		if e := json.Unmarshal(b, new(any)); errors.As(e, &se) {
			line := bytes.Count(b[:se.Offset], []byte("\n")) + 1
			return nil, nil, nil, fmt.Errorf("%s is not valid JSON (line %d: %v); PenguPool did not change it", file, line, se)
		}
		return nil, nil, nil, fmt.Errorf("%s is not valid JSON; PenguPool did not change it", file)
	}
	if top, err = objKVs(b); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %v; PenguPool did not change it", file, err)
	}
	if i := find(top, "permissions"); i >= 0 {
		if perms, err = objKVs(top[i].v); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: permissions is not an object; PenguPool did not change it", file)
		}
		if j := find(perms, "allow"); j >= 0 {
			if err = json.Unmarshal(perms[j].v, &allow); err != nil {
				return nil, nil, nil, fmt.Errorf("%s: permissions.allow is not a list; PenguPool did not change it", file)
			}
		}
	}
	return top, perms, allow, nil
}

// indentOf keeps a file's indentation: the whitespace starting its second line, "" for one line.
func indentOf(b []byte) string {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return ""
	}
	rest := b[i+1:]
	n := 0
	for n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
		n++
	}
	if n == 0 {
		return "  "
	}
	return string(rest[:n])
}

func addClaude(file string, rules []string) (PermissionWrite, error) {
	if real, err := filepath.EvalSymlinks(file); err == nil {
		file = real // a settings file linked from a dotfiles repo is edited where it lives
	}
	w := PermissionWrite{File: file}
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return w, err
	}
	if errors.Is(err, os.ErrNotExist) {
		w.Created = append(w.Created, "file")
	}
	top, perms, allow, err := claudeParts(b, file)
	if err != nil {
		return w, err
	}
	have := map[string]bool{}
	for _, r := range allow {
		var s string
		if json.Unmarshal(r, &s) == nil {
			have[s] = true
		}
	}
	for _, r := range rules {
		if !have[r] {
			allow = append(allow, jsonString(r))
			w.Lines = append(w.Lines, r)
		}
	}
	if len(w.Lines) == 0 {
		return w, nil // all there already, and the user's: nothing is PenguPool's to record
	}
	av := jsonString(allow)
	if j := find(perms, "allow"); j >= 0 {
		perms[j].v = av
	} else {
		perms = append(perms, kv{"allow", av})
		w.Created = append(w.Created, "permissions.allow")
	}
	if i := find(top, "permissions"); i >= 0 {
		top[i].v = encKVs(perms)
	} else {
		top = append(top, kv{"permissions", encKVs(perms)})
		w.Created = append(w.Created, "permissions")
	}
	return w, writeClaude(file, b, top)
}

func removeClaude(w PermissionWrite) error {
	b, err := os.ReadFile(w.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	top, perms, allow, err := claudeParts(b, w.File)
	if err != nil || perms == nil {
		return err
	}
	mine := map[string]bool{}
	for _, l := range w.Lines {
		mine[l] = true
	}
	allow = slices.DeleteFunc(allow, func(r json.RawMessage) bool {
		var s string
		return json.Unmarshal(r, &s) == nil && mine[s]
	})
	created := func(k string) bool { return slices.Contains(w.Created, k) }
	if j := find(perms, "allow"); j >= 0 {
		if len(allow) == 0 && created("permissions.allow") {
			perms = slices.Delete(perms, j, j+1)
		} else {
			perms[j].v = jsonString(allow)
		}
	}
	i := find(top, "permissions")
	if len(perms) == 0 && created("permissions") {
		top = slices.Delete(top, i, i+1)
	} else {
		top[i].v = encKVs(perms)
	}
	if len(top) == 0 && created("file") {
		return os.Remove(w.File)
	}
	return writeClaude(w.File, b, top)
}

// writeClaude writes the object in the file's own indentation, after a backup of the old file.
func writeClaude(file string, old []byte, top []kv) error {
	var out bytes.Buffer
	ind := "  "
	if len(old) > 0 {
		ind = indentOf(old)
	}
	if ind == "" {
		out.Write(encKVs(top))
	} else if err := json.Indent(&out, encKVs(top), "", ind); err != nil {
		return err
	}
	if len(old) == 0 || bytes.HasSuffix(old, []byte("\n")) {
		out.WriteByte('\n')
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(file); err == nil {
		mode = st.Mode().Perm()
		// The first backup is kept: it stays the user's file as it was before PenguPool wrote to it.
		if _, err := os.Stat(file + ".pengupool-backup"); errors.Is(err, os.ErrNotExist) {
			if err := writeAtomic(file+".pengupool-backup", old, mode); err != nil {
				return err
			}
		}
	} else if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return writeAtomic(file, out.Bytes(), mode)
}

// jsonString encodes v as JSON without HTML escaping, so a user's "&&" stays as they wrote it.
func jsonString(v any) json.RawMessage {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSpace(b.Bytes())
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".pengupool-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
