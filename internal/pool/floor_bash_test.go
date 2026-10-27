package pool

import (
	"cmp"
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestShellFloorAgainstBash compares shellFloor with what a real bash does with the same text. Every
// command the floor names is shadowed by a function that only prints "RAN <name> <args>", PATH is
// empty (nothing outside bash can run) and the working directory is empty and temporary, so the
// candidates are harmless. A candidate is a bypass when bash ran a floor command and shellFloor
// returned "". PP_FUZZ=<n> sets the number of random candidates (default 1500).
func TestShellFloorAgainstBash(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) { fuzzFloor(t, shell) })
	}
}

func fuzzFloor(t *testing.T, shell string) {
	n := 1500
	if v, err := strconv.Atoi(os.Getenv("PP_FUZZ")); err == nil {
		n = v
	}
	run := fuzzRunner(t, shell, strings.Fields("git gh rm dd truncate shred mv sudo chmod find hub npm yarn pnpm cargo twine gem docker podman kubectl helm pulumi terraform terragrunt tf tofu aws gcloud az gsutil scp rsync curl wget nc python python3"), nil)
	if run == nil {
		t.Skip("no " + shell)
	}
	seeds := []string{
		"git push origin main", "git push --force", "git reset --hard", "git clean -fd", "git branch -D x", "git merge main",
		"rm -rf x", "rm -r -f x", "gh pr create --fill", "gh pr merge 3", "gh release create v1", "gh api -X POST repos/o/r/issues",
		"kubectl apply -f x", "helm install x y", "terraform apply", "docker push img", "npm publish", "curl -X POST https://x",
		"curl -d a=b https://x", "scp f h:", "rsync -a f h:", "dd if=a of=b", "truncate -s0 f", "shred f", "sudo ls", "chmod 777 f",
		"aws s3 cp a b", "gcloud compute instances delete x", "find . -delete", "find . -exec rm {} ;",
	}
	rng := rand.New(rand.NewSource(1))
	var bypass = map[string]string{}
	check := func(cmd string) {
		if shellFloor(cmd) != "" {
			return
		}
		for _, line := range strings.Split(run(cmd), "\n") {
			if hit := commandHit(strings.TrimPrefix(line, "RAN ")); strings.HasPrefix(line, "RAN ") && hit != "" {
				bypass[cmd] = line
				return
			}
		}
	}
	for i := 0; i < n; i++ {
		cmd := seeds[rng.Intn(len(seeds))]
		for j := rng.Intn(3) + 1; j > 0; j-- {
			cmd = mutations[rng.Intn(len(mutations))](cmd, rng)
		}
		check(cmd)
	}
	for cmd, ran := range bypass {
		t.Errorf("shellFloor(%q) missed; bash ran %q", cmd, ran)
	}
}

// fuzzStubDir holds an executable stub for every command the fuzzers name; the shells run with it
// first on PATH, so a wrapper (env, xargs, nice, timeout), a path (stubdir/git) or a glob finds the
// stub, and nothing real runs.
var fuzzStubDir string

// fuzzRunner returns a function that runs a command in the shell (bash or zsh) with the stubs and
// reports what the stubs printed, or nil when the shell is not installed. fns are stubs defined as
// shell functions too, for the builtins (cd, echo) a PATH lookup does not reach.
func fuzzRunner(t *testing.T, shell string, names, fns []string) func(string) string {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		return nil
	}
	stubs := t.TempDir()
	script := "#!/bin/sh\nprintf 'RAN %s' \"${0##*/}\"; for a in \"$@\"; do printf ' %s' \"$a\"; done; echo\n"
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(stubs, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fuzzStubDir = stubs
	var fn strings.Builder
	for _, n := range fns {
		fn.WriteString(n + `() { printf 'RAN %s' "${FUNCNAME:-$0}"; printf ' %s' "$@"; builtin echo; }; `)
	}
	dir := t.TempDir()
	for _, f := range []string{"push", "merge", "clean", "branch", "reset", "pr", "create", "release", "apply", "publish", "-rf", "-f", "-r", "--hard", "--force", "POST", "-X"} {
		_ = os.WriteFile(filepath.Join(dir, f), nil, 0o644) // files a glob in a word can expand to
	}
	args := []string{"--noprofile", "--norc", "-c"}
	if shell == "zsh" {
		args = []string{"-f", "-c"}
	}
	return func(cmd string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, bin, append(args, fn.String()+cmd)...)
		c.Dir, c.Env = dir, []string{"PATH=" + stubs + ":/usr/bin:/bin", "HOME=" + dir}
		out, _ := c.Output()
		return string(out)
	}
}

// mutations rewrite a command so bash still reads it as the same words, or as a different spelling
// of them.
var mutations = []func(string, *rand.Rand) string{
	// quote or escape one letter
	func(c string, r *rand.Rand) string {
		return at(c, r, func(ch string) string { return []string{"'" + ch + "'", `"` + ch + `"`, `\` + ch}[r.Intn(3)] })
	},
	// an empty quote pair inside a word
	func(c string, r *rand.Rand) string {
		return at(c, r, func(ch string) string { return []string{`''`, `""`}[r.Intn(2)] + ch })
	},
	// $'..' with an octal or hex escape
	func(c string, r *rand.Rand) string {
		return at(c, r, func(ch string) string {
			if ch == " " || ch == "$" || ch == "'" || ch == `"` || ch == `\` || ch == "`" {
				return ch
			}
			return []string{`$'\x`, `$'\`}[r.Intn(2)] + map[bool]string{true: strconv.FormatInt(int64(ch[0]), 16), false: strconv.FormatInt(int64(ch[0]), 8)}[r.Intn(2) == 0] + `'`
		})
	},
	// a space as $IFS or ${IFS}
	func(c string, r *rand.Rand) string {
		return strings.Replace(c, " ", []string{"${IFS}", "$IFS", "${IFS%?}"}[r.Intn(3)], 1)
	},
	// the first word through a variable
	func(c string, r *rand.Rand) string {
		w, rest, _ := strings.Cut(c, " ")
		return "x=" + w + "; " + []string{"$x", "${x}", `"$x"`}[r.Intn(3)] + " " + rest
	},
	// the first word split over two variables
	func(c string, r *rand.Rand) string {
		w, rest, _ := strings.Cut(c, " ")
		if len(w) < 2 {
			return c
		}
		k := 1 + r.Intn(len(w)-1)
		return "a=" + w[:k] + "; b=" + w[k:] + "; " + []string{"$a$b", "${a}${b}", `"$a$b"`}[r.Intn(3)] + " " + rest
	},
	// a whole command in a variable, word-split
	func(c string, r *rand.Rand) string {
		return "c='" + strings.ReplaceAll(c, "'", "") + "'; " + []string{"$c", "${c}", "eval $c"}[r.Intn(3)]
	},
	// brace expansion
	func(c string, r *rand.Rand) string {
		w, rest, _ := strings.Cut(c, " ")
		return "{" + w + "," + strings.Fields(rest + " x")[0] + "}" + " " + rest
	},
	func(c string, r *rand.Rand) string { return "{" + strings.ReplaceAll(c, " ", ",") + "}" },
	// wrappers that run a command
	func(c string, r *rand.Rand) string {
		w := []string{"time " + c, "command " + c, "builtin " + c, "exec " + c, "env " + c, "nohup " + c, "! " + c, "FOO=1 " + c, "\\" + c, "{ " + c + "; }", "(" + c + ")", "if true; then " + c + "; fi",
			"for i in 1; do " + c + "; done", "while true; do " + c + "; break; done", "case x in x) " + c + ";; esac", "f() { " + c + "; }; f", "true && " + c, "false || " + c,
			c + " &", "echo | " + c, c + " | cat", "echo x; " + c, c + "\n", "\n" + c, "true;\n" + c, c + " # x", "echo $(" + c + ")", "echo `" + c + "`", "<(" + c + ")", "cat <(" + c + ")",
			"echo x > /dev/null; " + c, "{\n" + c + "\n}", "test -z '' && { " + c + "; }", "x=$(" + c + ")", "echo ${x:-$(" + c + ")}", "cat <<EOF\n$(" + c + ")\nEOF", `echo "$(` + c + `)"`,
			"bash -c '" + strings.ReplaceAll(c, "'", "") + "'", "eval '" + strings.ReplaceAll(c, "'", "") + "'", "echo '" + strings.ReplaceAll(c, "'", "") + "' | sh", "sh <<< '" + strings.ReplaceAll(c, "'", "") + "'",
			"xargs " + c + " <<< x", "alias a='" + strings.ReplaceAll(c, "'", "") + "'; a", "trap '" + strings.ReplaceAll(c, "'", "") + "' EXIT",
			"x='" + strings.ReplaceAll(c, "'", "") + "'; trap \"$x\" EXIT", "x='" + strings.ReplaceAll(c, "'", "") + "'; source /dev/stdin <<< \"$x\"", ". <(echo '" + strings.ReplaceAll(c, "'", "") + "')",
			"x=" + strings.Fields(c)[0] + "; find . -maxdepth 0 -exec $x " + strings.Join(strings.Fields(c)[1:], " ") + " ;", "x=" + strings.Fields(c)[0] + "; timeout 5 $x " + strings.Join(strings.Fields(c)[1:], " "),
			"x=" + strings.Fields(c)[0] + "; nice -n 1 $x " + strings.Join(strings.Fields(c)[1:], " "), "a=(" + c + `); "${a[@]}"`, "a=(" + c + `); ${a[@]}`, "x='" + strings.ReplaceAll(c, "'", "") + `'; "${x:0}"`,
			"set -- " + c + "; $@", "set -- " + c + `; "$@"`, "read -r l <<< '" + strings.ReplaceAll(c, "'", "") + "'; $l", "printf '%s' '" + strings.ReplaceAll(c, "'", "") + "' | bash",
		}
		return w[r.Intn(len(w))]
	},
	// line continuation or tab inside the command
	func(c string, r *rand.Rand) string { return at(c, r, func(ch string) string { return "\\\n" + ch }) },
	func(c string, r *rand.Rand) string { return strings.Replace(c, " ", "\t", 1) },
	// the command from a printf or echo substitution
	func(c string, r *rand.Rand) string {
		w, rest, _ := strings.Cut(c, " ")
		return []string{`$(echo ` + w + `) `, "$(printf " + w + ") ", "`echo " + w + "` ", `"$(echo ` + w + `)" `}[r.Intn(4)] + rest
	},
	// a flag moved or split: -rf as -r -f
	func(c string, r *rand.Rand) string { return strings.Replace(c, " -f", " -f ", 1) },
	// a path-qualified command, a glob in a word
	func(c string, r *rand.Rand) string { return fuzzStubDir + "/" + c },
	// the command word globbed (a path, so the glob finds the stub), or a later word globbed onto a file in the directory
	func(c string, r *rand.Rand) string {
		w, rest, _ := strings.Cut(c, " ")
		if len(w) < 2 {
			return c
		}
		i := r.Intn(len(w))
		g := w[:i] + []string{"?", "[" + w[i:i+1] + "]", "*"}[r.Intn(3)] + w[i+1:]
		return fuzzStubDir + "/" + g + " " + rest
	},
	func(c string, r *rand.Rand) string {
		f := strings.Fields(c)
		if len(f) < 2 {
			return c
		}
		k := 1 + r.Intn(len(f)-1)
		if len(f[k]) < 2 {
			return c
		}
		i := r.Intn(len(f[k]))
		f[k] = f[k][:i] + []string{"?", "[" + f[k][i:i+1] + "]", "*"}[r.Intn(3)] + f[k][i+1:]
		return strings.Join(f, " ")
	},
	// zsh: =git is the path of git
	func(c string, r *rand.Rand) string { return "=" + c },
	func(c string, r *rand.Rand) string { return "./" + c },
	func(c string, r *rand.Rand) string { return "cd . && " + c },
}

// at applies f to one non-space character of c.
func at(c string, r *rand.Rand, f func(string) string) string {
	i := r.Intn(len(c))
	if c[i] == ' ' {
		return c
	}
	return c[:i] + f(string(c[i])) + c[i+1:]
}

// TestCommandKeysAgainstBash holds commandKeys to what bash runs: when every key of a command is a
// plain one (no exact text), each command bash ran must be one a key covers, a command and, where the
// key has one, its subcommand. Payloads that are not on the floor are shadowed too (tee, cp, tar ...).
func TestCommandKeysAgainstBash(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) { fuzzKeys(t, shell) })
	}
}

func fuzzKeys(t *testing.T, shell string) {
	n := 1500
	if v, err := strconv.Atoi(os.Getenv("PP_FUZZ")); err == nil {
		n = v
	}
	run := fuzzRunner(t, shell, strings.Fields("git gh rm dd truncate shred mv sudo chmod find hub npm yarn pnpm cargo twine gem docker podman kubectl helm pulumi terraform terragrunt tf tofu aws gcloud az gsutil scp rsync curl wget nc python python3 "+
		"ls cat echo go make grep head tail wc sort uniq diff cd pwd true false tee cp ln install tar open jq sed awk rg"), strings.Fields("cd echo true false pwd"))
	if run == nil {
		t.Skip("no " + shell)
	}
	allowed := []string{"ls -la", "cat f", "go test ./...", "git status", "echo hi", "grep -rn x .", "cd sub", "head -5 f", "git log --oneline", "make test"}
	payload := []string{"rm f", "curl https://x", "tee f", "cp a b", "git push", "kubectl get po", "tar xf a", "mv a b", "chmod +x f", "ln -s a b", "git config alias.x !ls", "sed -i s/a/b/ f", "awk 1 f",
		"git checkout .", "docker ps", "xargs ls", "env ls", "nice ls", "rg --pre ls x", "git commit -m x", "echo x > f", "cat f > g", "go run x.go", "npm test", "python3 x.py"}
	seps := []string{"; ", " && ", " || ", " | ", "\n", " & ", " ;; ", " |& "}
	rng := rand.New(rand.NewSource(2))
	bypass := map[string]string{}
	for i := 0; i < n; i++ {
		a, p := allowed[rng.Intn(len(allowed))], payload[rng.Intn(len(payload))]
		var cmd string
		switch rng.Intn(6) {
		case 0, 1, 2:
			cmd = a + seps[rng.Intn(len(seps))] + p
		case 3:
			cmd = a + " $(" + p + ")"
		case 4:
			cmd = a + " `" + p + "`"
		default:
			cmd = a + []string{" > f; ", " 2>&1; ", " >/dev/null; ", " 2>/dev/null && ", " <<< x; ", " < f; ", " >> f; ", " &>/dev/null; "}[rng.Intn(8)] + p
		}
		for j := rng.Intn(3); j > 0; j-- {
			cmd = mutations[rng.Intn(len(mutations))](cmd, rng)
		}
		keys := commandKeys(cmd)
		plain := len(keys) > 0
		for _, k := range keys {
			plain = plain && k.exact == ""
		}
		if !plain {
			continue
		}
		covered := func(line string) bool {
			// A variable's value is split into words by bash, not into statements: "make test;" is the
			// word make and the argument test; (commandKeys reads a literal value in as statements,
			// which is the stricter reading).
			f := strings.Fields(strings.ReplaceAll(line, ";", " "))
			// A key is a word as typed (a path, or =name in zsh); the stub prints the name it ran.
			name := func(w string) string { return filepath.Base(strings.TrimPrefix(w, "=")) }
			for _, k := range keys {
				kw := strings.Fields(cmp.Or(k.pair, k.word))
				if len(kw) == 2 && len(f) > 1 && name(kw[0]) == f[0] && kw[1] == f[1] || len(kw) == 1 && name(kw[0]) == f[0] {
					return true
				}
			}
			return false
		}
		for _, line := range strings.Split(run(cmd), "\n") {
			if rest, ok := strings.CutPrefix(line, "RAN "); ok && !covered(rest) {
				bypass[cmd] = line
				break
			}
		}
	}
	for cmd, ran := range bypass {
		t.Errorf("commandKeys(%q) are plain, yet bash ran %q", cmd, ran)
	}
}
