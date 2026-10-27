package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/store"
)

// The PenguPool Codex rules: prefix_rule allow entries so a Codex session
// runs the PenguPool CLI outside the Seatbelt sandbox without prompting.
// Without them every helper message and Done report waits on a click: the
// socket connect is denied (EPERM) under workspace-write.
//
// The rule file is ours: a managed header, rewritten only when the content
// changes (e.g. the exe path moved). Nothing else under rules/ is touched.

// codexRulesFile is the managed file, honouring CODEX_HOME like Codex does.
func codexRulesFile() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
		if home == "" {
			return ""
		}
		home = filepath.Join(home, ".codex")
	}
	return filepath.Join(home, "rules", "pengupool.rules")
}

// codexLookPath and codexRun are exec entry points, stubbed in tests.
var codexLookPath = exec.LookPath

var codexRun = func(ctx context.Context, args ...string) error {
	if out, err := exec.CommandContext(ctx, "codex", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("codex %s: %v: %s", args[0], err, firstLine(out))
	}
	return nil
}

// codexRule is one allowed argv prefix with its justification. The prefix
// is an argv slice, never split on spaces: an exe path may itself contain
// a space (e.g. '/Applications/PenguPool AI.app/...').
type codexRule struct {
	argv []string
	why  string
}

// codexRulePrefixes are the exact argv prefixes the pool context teaches
// agents (`send --to`, `search`, `experience search|show`, `compass
// word`, and the older `message` form), each with its justification.
// `experience import` is owner-only and stays out. Nothing broader:
// only these run unsandboxed.
func codexRulePrefixes(exe string) []codexRule {
	return []codexRule{
		{[]string{exe, "send"}, "PenguPool team messages and Done reports"},
		{[]string{exe, "message"}, "PenguPool team messages (older form)"},
		{[]string{exe, "experience", "search"}, "PenguPool helper journal search"},
		{[]string{exe, "experience", "show"}, "PenguPool helper journal reads"},
		{[]string{exe, "compass", "word"}, "PenguPool Compass wording when asked"},
		{[]string{exe, "search"}, "PenguPool Compass and journal search"},
	}
}

// codexRules renders the managed rules file for exe.
func codexRules(exe string) string {
	var b strings.Builder
	b.WriteString("# Managed by PenguPool. Do not edit: PenguPool rewrites this file when its own commands change.\n")
	b.WriteString("# It lets Codex sessions run only the PenguPool CLI commands below outside the sandbox, without prompting.\n")
	for _, p := range codexRulePrefixes(exe) {
		q := make([]string, len(p.argv))
		for i, a := range p.argv {
			q[i] = squote(a)
		}
		// The match example is one command line, shell-split on load: the
		// exe element carries its own quotes so a path with a space
		// stays one word.
		example := `"` + p.argv[0] + `"`
		if len(p.argv) > 1 {
			example += " " + strings.Join(p.argv[1:], " ")
		}
		fmt.Fprintf(&b, "\nprefix_rule(\n    pattern = [%s],\n    decision = \"allow\",\n    justification = %s,\n    match = [%s],\n)\n",
			strings.Join(q, ", "), squote(p.why), squote(example))
	}
	return b.String()
}

func squote(s string) string {
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	return `"` + r + `"`
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// EnsureCodexRules writes the PenguPool rules file into the Codex home,
// once per content, and only for the owner's real home
// (store.IsHomeDefault): any other store logs one line and installs
// nothing. It runs at daemon start, only when codex is found. One log
// line when it writes; silence otherwise.
func EnsureCodexRules(storeDir, exe string, logf func(string, ...any)) {
	if !store.IsHomeDefault(storeDir) {
		logf("codex rules: skipped (not the owner's home)")
		return
	}
	if _, err := codexLookPath("codex"); err != nil {
		return // codex is not here: nothing to wire
	}
	path := codexRulesFile()
	if path == "" {
		logf("codex rules: no home")
		return
	}
	want := codexRules(exe)
	if old, err := os.ReadFile(path); err == nil && string(old) == want {
		return // already ours and current
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		logf("codex rules: %v", err)
		return
	}
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		logf("codex rules: %v", err)
		return
	}
	logf("codex rules: wrote %s", path)
}

// CheckCodexRules asks `codex execpolicy check` whether argv is allowed by
// the managed file. It reports the check's decision ("allow" or other);
// an error means the checker could not run.
func CheckCodexRules(ctx context.Context, argv ...string) (string, error) {
	args := append([]string{"execpolicy", "check", "--rules", codexRulesFile(), "--"}, argv...)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("codex execpolicy check: %v: %s", err, firstLine(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, `"decision"`) && strings.Contains(line, `"allow"`) {
			return "allow", nil
		}
	}
	return "other", nil
}
