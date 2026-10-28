package pool

import (
	"cmp"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The floor: what waits for the owner, whatever the team's autonomy, the agent or memory says,
// unless a confirmed (confident) Compass line of the owner's allows that exact action, which triage
// checks with the decision model before it lets one pass (floorWaiver). A fixed list. It is enforced where the tool is called (guardFloor matches the actual
// command or path); the same list matched against the agent's whole reply, before any go-ahead, is
// the early warning, since an agent's text can describe a push innocently. A false
// positive costs one owner question; a false negative lets triage wave through something on it, so
// the patterns lean wide. A match whose "prose" group is set is a noun in passing ("release notes"),
// not the act. Text is folded first (floorFold), so look-alike dashes and spaces match too.

// gitCmd is "git", any global options (-C <dir>, -c k=v, --no-pager) and the subcommand's leading space.
const gitCmd = `\bgit(\s+(-[cC]\s+\S+|--?[a-z][\w-]*(=\S+)?))*\s+`

var floor = []struct {
	what string
	re   *regexp.Regexp
}{
	{"rm -rf", regexp.MustCompile(`(?i)\brm\s+(-[a-z]*[rf]|--recursive|--force)|\bfind\b[^\n]*\s-(delete\b|exec\w*\s+(\S*/)?(rm|shred|unlink)\b)|\bdelete\s+from\b|` + gitCmd + `(checkout\s+--\s+\.|restore\s+\.)`)},
	{"overwriting or destroying data", regexp.MustCompile(`(?i)\bdd\b[^\n]*\sof=|\btruncate\s+-|\bshred\b|` + gitCmd + `(update-ref\b[^\n]*\s-d|filter-branch)\b|\bmv\b[^\n]*\s/dev/null\b`)},
	{"permission grants", regexp.MustCompile(`(?i)\bsudo\b|\bchmod\b[^\n]*\b777\b`)},
	{"git reset --hard", regexp.MustCompile(`(?i)\breset\s+--hard\b|\bhard\s+reset\b`)},
	{"git clean", regexp.MustCompile(`(?i)` + gitCmd + `clean\b`)},
	{"stash drop", regexp.MustCompile(`(?i)\bstash\s+(drop|clear)\b`)},
	{"deleting a branch", regexp.MustCompile(`(?i)` + gitCmd + `branch(\s+--?[a-z-]+)*?\s+(-[a-z]*d[a-z]*|--delete)\b`)},
	{"force push", regexp.MustCompile(`(?i)\bpush\b[^\n]*\s(--force(-with-lease)?|-f)\b`)},
	{"push or merge to a shared branch", regexp.MustCompile(`(?i)` + gitCmd + `(push|merge|send-pack)\b|\bhub\s+(push|merge)\b`)},
	{"pull request", regexp.MustCompile(`(?i)\bgh\s+pr\s+(create|merge)\b`)},
	{"release, deploy or publish", regexp.MustCompile(`(?i)\bgh\s+release\b|\b(npm|yarn|pnpm|cargo|twine|gem)\s+(publish|push|upload)\b|\b(docker|podman)\s+(image\s+)?push\b|\bdocker\s+buildx\s+build\b[^\n]*\s--push\b`)},
	{"kubernetes", regexp.MustCompile(`(?i)\b(kubectl|helm|pulumi)\b`)},
	{"terraform", regexp.MustCompile(`(?i)\bterr(aform|agrunt)\b|\b(tf|tofu)\s+(apply|destroy|import)\b`)},
	{"drop table", regexp.MustCompile(`(?i)\bdrop\s+(table|database|schema|column)\b|\btruncate\s+table\b`)},
	{"cloud change", regexp.MustCompile(`(?i)\b(aws|gcloud|az)\b[^\n]{0,60}?\b(cp|rm|mv|sync|deploy|create|delete|put|update|apply|mb|rb|terminate\S*|run-instances|invoke|stop|start|deallocate|scale|resize)\b|\bgsutil\b`)},
	{"uploading data", regexp.MustCompile(`(?i)\bscp\b|\brsync\b|\bs3\s+(cp|sync|mv)\b|\b(curl|wget)\b[^\n]*(\s(?-i:-[a-zA-Z]*X)\s*['"]?(post|put|patch|delete)|\s--request[= ]\s*['"]?(post|put|patch|delete)|\s(?-i:-[a-zA-Z]*[dFT])|\s--(data\S*|json|form\S*|upload-file|post-data|post-file|method)\b)`)},
	{"gh api write", regexp.MustCompile(`(?i)\bgh\s+api\b[^\n]*(\s(?-i:-[a-zA-Z]*X)\s*['"]?(post|put|patch|delete)|\s--method[= ]\s*['"]?(post|put|patch|delete)|\s(?-i:-[fF])|\s--(field|raw-field|input)\b)`)},
	{"sending data over the network", regexp.MustCompile(`(?i)\b(nc|ncat|netcat)\b[^\n]*<|\bpython[\d.]*\b[^\n]*\s-c\b[^\n]*\b(requests|urllib|httpx)\b[^\n]*(\bpost|\bput\b|\bpatch\b|data=|json=|files=)`)},
}

// floorProse are the items that name an act in words, not in a command: matched against what an
// agent says (and a tool's own text), never against the content of a file it writes.
var floorProse = []struct {
	what string
	re   *regexp.Regexp
}{
	{"force push", regexp.MustCompile(`(?i)\bforce[- ]?push|\bpush\b[^\n]*\s(--force(-with-lease)?|-f)\b`)},
	{"push or merge to a shared branch", regexp.MustCompile(`(?i)` + gitCmd + `(push|merge)\b|\b(push|merg|commit|land)\w*\s+(\S+\s+){0,4}?(to|into|onto|on)\s+(the\s+)?(origin/)?(main|master|trunk|develop|staging|production|prod|release)\b|\bpush\w*\b[^.\n]{0,20}\btags?\b|\btag\b[^.\n]{0,30}\bpush`)},
	{"pull request", regexp.MustCompile(`(?i)\bgh\s+pr\s+(create|merge)\b|\b(open|create|merg|submit|rais|fil|put\s+up|send|make|push)\w*\s+(\S+\s+){0,3}?(pull\s+request|PR)s?\b`)},
	{"deleting outside the worktree", regexp.MustCompile(`(?i)\b(delet|remov|wip)\w*\b[^.\n]{0,60}\boutside\b[^.\n]{0,30}\b(worktree|workspace|repo|repository)`)},
	{"migration", regexp.MustCompile(`(?i)\b(run|apply|execut|roll)\w*\s+(\S+\s+){0,3}?migrations?\b|\b(database|db|schema|data)\s+migrations?\b|\bmigrat\w*\s+(\S+\s+){0,2}?(database|db|schema|data|prod\w*)\b`)},
	{"sending email or messages outside", regexp.MustCompile(`(?i)\b(send|sent|post|email|e-mail|messag(e|ing)|notify|notifying|tweet)\w*\b[^.\n]{0,40}(\b(email|e-mail|slack|discord|sms|tweet|customers?|clients?|mailing list|newsletter)\b|#[a-z0-9_-]+|\bin\s+the\s+channel\b)|\bemail\s+(the|a|an|to|them|him|her)\b`)},
	{"credentials", regexp.MustCompile(`(?i)\b(credentials?|secrets?|passwords?|api[ _-]?keys?|access[ _-]?keys?|private[ _-]?keys?|ssh[ _-]?keys?|tokens?)\b|\.env\b`)},
	{"payments", regexp.MustCompile(`(?i)\b(payments?|billing|invoices?|refunds?|purchas(e|es|ed|ing)|credit\s+cards?|charg(e|ing)\s+(the|a|their)\s+(card|customer))\b`)},
	{"permission grants", regexp.MustCompile(`(?i)\b(grant|revok)\w*\b[^.\n]{0,40}\b(access|permissions?|roles?|rights|privileges?)\b|\biam\b|\bsudo\b|\bchmod\b[^\n]*\b777\b|\badd\w*\b[^.\n]{0,30}\bas\s+(an?\s+)?(admin|owner|collaborator)\b`)},
	{"release, deploy or publish", regexp.MustCompile(`(?i)\b(releas(e|es|ed|ing)|deploy(s|ed|ing)?|publish(es|ed|ing)?)\b(?P<prose>\s+notes?\b)?|\bgh\s+release\b|\b(npm|yarn|pnpm|cargo|twine|gem)\s+(publish|push|upload)\b|\b(docker|podman)\s+(image\s+)?push\b|\bdocker\s+buildx\s+build\b[^\n]*\s--push\b|\bship(ping|s)?\s+(it|this|that|them)\b|\broll(s|ed|ing)?\s+(\S+\s+)?out\b|\bgo(es|ing)?\s+live\b|\b(prod|production)\b`)},
	{"uploading data", regexp.MustCompile(`(?i)\bupload(s|ed|ing)?\b`)},
}

// floorWords is the floor in plain words, for agents and triage's go-ahead.
const floorWords = "deleting anything outside the worktree, rm -rf, shredding or overwriting files, git reset --hard, git clean, dropping a stash, deleting a branch, force-pushing, " +
	"any git push, merging to main or another shared branch, opening or merging a pull request, releasing, deploying, publishing or anything touching production, " +
	"kubectl, helm, migrations, terraform, dropping tables, changing cloud resources, sending email or messages outside the team, uploading data, gh api writes, " +
	"and anything touching credentials, secrets, tokens, API keys, payments or permission grants"

// floorFold makes look-alikes plain before matching: NFKC (full-width letters, ligatures,
// non-breaking spaces), every Unicode dash and minus to "-", every other space to " ", and
// invisible format characters (zero-width spaces and joiners) dropped. Case is folded by the
// patterns' (?i), except where a flag's case matters (curl -X, -d, -F, -T).
func floorFold(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x2010 && r <= 0x2015, r == 0x2212, r == 0xFE58, r == 0xFE63, r == 0xFF0D:
			return '-'
		case r != '\n' && unicode.IsSpace(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, norm.NFKC.String(s))
}

// floorHit is the first floor item the text names, or "".
func floorHit(text string) string {
	text = floorFold(text)
	return cmp.Or(firstHit(floor, text), firstHit(floorProse, text))
}

// commandHit is the first command-shaped floor item the text names, or "": what a script that is
// written and then run would do.
func commandHit(text string) string { return firstHit(floor, floorFold(text)) }

func firstHit(items []struct {
	what string
	re   *regexp.Regexp
}, text string) string {
	for _, f := range items {
		prose := f.re.SubexpIndex("prose")
		for _, m := range f.re.FindAllStringSubmatchIndex(text, -1) {
			if prose > 0 && m[2*prose] >= 0 {
				continue
			}
			return f.what
		}
	}
	return ""
}

// A shell command is judged as the shell will read it, not as typed, and against the command-shaped
// floor only (commandHit). shellFold removes the quotes and backslash escapes (a quoted letter, a \
// before one). Each command substitution ($(...) or backticks) is checked as a command of its own,
// and stands for a plain word in the rest. A command whose real words cannot be seen is a hit itself:
// a substitution, $VAR or ${VAR} where a command starts, a $'...' string, eval, a pipe into a shell,
// sh -c with a substitution or $ in it, xargs handing its input to a shell or to a command that
// takes a subcommand (git, gh, docker...). $VAR/bin/tool where a command starts is judged as tool. A
// $VAR among a command's arguments (echo $HOME) is plain. ponytail: a name match; a script run from a
// file, or text built by other means, gets past it.
const cmdPos = `(?:^|[;&|({!)]|\b(?:env|sudo|exec|command|nohup|time|do|then|else|elif)\s)\s*(?:\w+=\S*\s+)*`

const xargsFlags = `xargs\b(?:\s+(?:-\S+|\{\}|\d+))*\s+`

var (
	// Wrappers that run the command after their flags (timeout 5 $x, find -exec $x), and a trap whose
	// body is built from a substitution or variable, hand the real words over unseen too.
	// A $IFS reference (it can stand for the space between words) and a brace expansion ({git,push})
	// hide the words too.
	indirectRe = regexp.MustCompile(`(?m)\$['"]|\$\{?IFS\b|(?:^|[^$])\{(?:[^\s{}\\]|\\.)*,(?:[^\s{}\\]|\\.)*\}|\btrap\b[^\n;&|]*[$\x00]|` + cmdPos + `(?:source|\.)\s+[^\n;&|]*(?:/dev/(?:stdin|fd)|[<>]\(|[$\x00])|\bfind\b[^\n;&|]*\s-exec(?:dir)?\s+["']?[$\x00]|\b(?:timeout|nice|ionice|watch|setsid|stdbuf|flock|chroot|runuser|unbuffer|caffeinate|doas|ssh|parallel)\b(?:\s+(?:-\S+|\d+[smhd]?))*\s+["']?[$\x00]|` + cmdPos + `["']?(?:eval\b|[\w.@+/-]*[$\x00])|\|\s*(?:ba|z|da|k)?sh\b|\b(?:ba|z|da|k)?sh\s+-\w*c\b[^\n]*[$\x00]|` +
		xargsFlags + `(?:sh|bash|zsh|dash|ksh|env|sudo|eval|exec|ssh)\b|` +
		xargsFlags + `(?:git|gh|hub|npm|yarn|pnpm|docker|podman|kubectl|helm|aws|gcloud|az|terraform)\s*(?:$|[;&|)])`)
	varPathRe = regexp.MustCompile(`(` + cmdPos + `)["']?\$\{?[A-Za-z_]\w*\}?/(?:[\w.@+-]+/)*([\w.@+-]+)`)
	escapeRe  = regexp.MustCompile(`(?s)\\(.)`)
	quoteOut  = strings.NewReplacer("'", "", `"`, "")
	// envReadRe: any word that names a credentials file (.env, .env.local; not .env.example), a cloud
	// credentials file or a private key, whatever command takes it: cat, sed, tr, od, awk, < .env.
	envReadRe = regexp.MustCompile(`(?i)(?:^|[\s<>=(])(\S*?(?:\.env(?:\.[\w-]+)?|\.aws/credentials|\.ssh/id_\w+|\.netrc))(?:[\s;&|)<>]|$)`)
)

// substitutions cuts the command substitutions out of cmd: what is left has \x00 where each was, and
// bodies are their commands. ok is false for one that never closes.
func substitutions(cmd string) (rest string, bodies []string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(cmd); i++ {
		switch {
		case strings.HasPrefix(cmd[i:], "$("):
			depth, j := 1, i+2
			for ; j < len(cmd) && depth > 0; j++ {
				if cmd[j] == '(' {
					depth++
				} else if cmd[j] == ')' {
					depth--
				}
			}
			if depth > 0 {
				return "", nil, false
			}
			bodies = append(bodies, cmd[i+2:j-1])
			b.WriteByte(0)
			i = j - 1
		case cmd[i] == '`':
			j := strings.IndexByte(cmd[i+1:], '`')
			if j < 0 {
				return "", nil, false
			}
			bodies = append(bodies, cmd[i+1:i+1+j])
			b.WriteByte(0)
			i += j + 1
		default:
			b.WriteByte(cmd[i])
		}
	}
	return b.String(), bodies, true
}

var (
	// assignRe is NAME=value where a statement starts; literalVars checks that the statement ends there.
	assignRe = regexp.MustCompile(`(?m)(?:^|[;\n])[ \t]*(?:export[ \t]+)?([A-Za-z_]\w*)=('[^'\n]*'|"[^"\n$\x60\\]*"|[^\s;&|<>()'"$\x60\\*?\[\]{}\x00]*)`)
	varRefRe = regexp.MustCompile(`\$([A-Za-z_]\w*)|\$\{([A-Za-z_]\w*)\}`)
	stmtEnd  = regexp.MustCompile(`^[ \t]*(?:$|[;\n]|&&|\|\|)`)
	// mayNotRun: before it, something that makes a statement conditional or scoped (a && or ||, a
	// pipe, a group, a branch or a loop); a set after it may not happen, and the name then comes from
	// the environment.
	mayNotRun = regexp.MustCompile(`&&|\|\||[|({]|\b(?:if|then|else|elif|case|while|until|do|select|function)\b`)
)

// literalVars writes the value in for $NAME and ${NAME} where the command itself set NAME to a
// literal, in a statement of its own and before the use (P=/x/bin/tool; $P run is /x/bin/tool run),
// so the floor judges the command the shell will run. A name the command mentions any other way
// (read NAME, for NAME in, local NAME, a value with $ or a substitution, a prefix NAME=v cmd, which
// the shell expands after its words) is left as it is: that is still indirection.
func literalVars(cmd string) string {
	// A comment, an escape, a CR or a byte past ASCII can make the shell read a set differently
	// (# hides it, a no-break space is no separator): nothing is written in then.
	if strings.ContainsAny(cmd, "#\\\r") || strings.IndexFunc(cmd, func(r rune) bool { return r > 0x7e }) >= 0 {
		return cmd
	}
	type set struct {
		end       int
		name, val string
		literal   bool
	}
	var sets []set
	for _, m := range assignRe.FindAllStringSubmatchIndex(cmd, -1) {
		name, val := cmd[m[2]:m[3]], cmd[m[4]:m[5]]
		literal := stmtEnd.MatchString(cmd[m[5]:]) && !mayNotRun.MatchString(cmd[:m[0]])
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') {
			val = val[1 : len(val)-1]
		} else if val == "~" || strings.HasPrefix(val, "~/") {
			if home := os.Getenv("HOME"); home != "" {
				val = home + val[1:]
			} else {
				literal = false
			}
		}
		sets = append(sets, set{m[5], name, val, literal})
	}
	if len(sets) == 0 {
		return cmd
	}
	refs := varRefRe.FindAllStringSubmatchIndex(cmd, -1)
	refName := func(r []int) string {
		if r[2] >= 0 {
			return cmd[r[2]:r[3]]
		}
		return cmd[r[4]:r[5]]
	}
	// Trusted: every set of the name is literal, and the command mentions it nowhere else.
	trusted := map[string]bool{}
	for _, s := range sets {
		if ok, seen := trusted[s.name]; !seen || ok {
			trusted[s.name] = s.literal
		}
	}
	for name, ok := range trusted {
		if !ok {
			continue
		}
		mentions := len(regexp.MustCompile(`\b`+name+`\b`).FindAllStringIndex(cmd, -1))
		for _, s := range sets {
			if s.name == name {
				mentions--
			}
		}
		for _, r := range refs {
			if refName(r) == name {
				mentions--
			}
		}
		trusted[name] = mentions == 0
	}
	var b strings.Builder
	val, last, si := map[string]string{}, 0, 0
	for _, r := range refs {
		for ; si < len(sets) && sets[si].end <= r[0]; si++ {
			val[sets[si].name] = sets[si].val
		}
		name := refName(r)
		v, ok := val[name]
		if !ok || !trusted[name] {
			continue
		}
		b.WriteString(cmd[last:r[0]])
		b.WriteString(v)
		last = r[1]
	}
	b.WriteString(cmd[last:])
	return b.String()
}

func shellFold(cmd string) string { return quoteOut.Replace(escapeRe.ReplaceAllString(cmd, "$1")) }

// shellFloor is the floor item a shell command names, or "".
func shellFloor(cmd string) string { return shellCheck(stripHeredocs(cmd), 0) }

func shellCheck(cmd string, depth int) string {
	cmd = strings.ReplaceAll(cmd, "\\\n", "") // a backslash-newline is a line continuation: the shell reads the words joined
	rest, bodies, ok := substitutions(cmd)
	if !ok || depth > 4 {
		return "shell indirection"
	}
	for _, b := range bodies {
		if hit := shellCheck(b, depth+1); hit != "" {
			return hit
		}
	}
	rest = literalVars(rest)
	rest = varPathRe.ReplaceAllString(rest, "${1}${2}")
	rest = zshEquals.ReplaceAllString(rest, "$1")
	if globHides(rest) {
		return "shell indirection"
	}
	// The quotes come out of a word too (e''val), so the test runs on the text as typed and as read.
	if indirectRe.MatchString(rest) || indirectRe.MatchString(shellFold(rest)) {
		return "shell indirection"
	}
	rest = shellFold(strings.ReplaceAll(rest, "\x00", "x"))
	for _, m := range envReadRe.FindAllStringSubmatch(rest, -1) {
		if f := strings.ToLower(m[1]); !strings.HasSuffix(f, ".example") && !strings.HasSuffix(f, ".sample") && !strings.HasSuffix(f, ".template") {
			return "credentials"
		}
	}
	return commandHit(rest)
}

var (
	quotedRe  = regexp.MustCompile(`'[^']*'|"[^"]*"|\\.`)
	stmtSplit = regexp.MustCompile(`[;&|\n(){}]`)
	globChars = "*?["
	// globPrefix are words that run the command after them.
	globPrefix = map[string]bool{"time": true, "env": true, "command": true, "exec": true, "nohup": true, "sudo": true, "nice": true, "timeout": true, "xargs": true, "!": true,
		"if": true, "then": true, "else": true, "do": true, "while": true, "until": true}
	// globFloor are the commands the floor is about: a glob in their words can expand to a flag or a
	// subcommand it names (rm -[r]f, git pu[s]h, when a file by that name is there).
	globFloor = map[string]bool{"git": true, "gh": true, "hub": true, "rm": true, "dd": true, "truncate": true, "shred": true, "mv": true, "chmod": true, "docker": true, "podman": true,
		"kubectl": true, "helm": true, "pulumi": true, "terraform": true, "terragrunt": true, "tf": true, "tofu": true, "npm": true, "yarn": true, "pnpm": true, "cargo": true,
		"twine": true, "gem": true, "aws": true, "gcloud": true, "az": true, "gsutil": true, "scp": true, "rsync": true, "curl": true, "wget": true, "nc": true}
)

// globHides: an unquoted glob where the floor reads, which the shell expands to what it finds on disk
// (an agent can write the file): in a command's own word (/usr/bin/g?t), or in the words after a floor
// command (git pu[s]h, rm -[r]f; git: the next three, gh: four). A glob among a plain command's arguments (ls *.go) is not.
// ponytail: a glob later in a floor command's words (git -C r pu[s]h) gets past it.
func globHides(cmd string) bool {
	if strings.ContainsAny(cmd, globChars) && shellOnString.MatchString(cmd) {
		return true // a shell handed a string with a glob in it: the glob is read at run time, inside the quotes
	}
	hasGlob := func(w string) bool { return strings.ContainsAny(quotedRe.ReplaceAllString(w, "q"), globChars) }
	for _, st := range stmtSplit.Split(cmd, -1) {
		f := strings.Fields(st)
		for len(f) > 0 && (assignWordRe.MatchString(f[0]) || globPrefix[shellFold(f[0])]) {
			if assignWordRe.MatchString(f[0]) && hasGlob(f[0]) {
				return true // x=pu[s]h; git $x: the value is read as a glob where it is used
			}
			wrapper := !assignWordRe.MatchString(f[0])
			f = f[1:]
			for wrapper && len(f) > 0 && (strings.HasPrefix(f[0], "-") || f[0] != "" && strings.Trim(f[0], "0123456789smhd") == "") {
				f = f[1:] // the wrapper's own flags and numbers: nice -n 1, timeout 5, env -i
			}
		}
		if len(f) == 0 {
			continue
		}
		if hasGlob(f[0]) && !(f[0] == "[" || f[0] == "[[") {
			return true
		}
		real := shellFold(f[0])
		base := real[strings.LastIndexByte(real, '/')+1:]
		if base == "find" { // find -dele[t]e: its predicates are words that start with a dash
			for _, w := range f[1:] {
				if strings.HasPrefix(w, "-") && hasGlob(w) {
					return true
				}
			}
		}
		if !globFloor[base] {
			continue
		}
		words := f[1:]
		switch base {
		case "git":
			words = words[:min(len(words), 3)]
		case "gh":
			words = words[:min(len(words), 4)]
		}
		for _, w := range words {
			if hasGlob(w) {
				return true
			}
		}
	}
	return false
}

// shellOnString: a shell run on a string (sh -c '...', sh <<< '...') or eval.
var shellOnString = regexp.MustCompile(`\b(?:ba|z|da|k)?sh\b[^\n;&|]*(?:\s-\w*c\b|<<<)|\beval\b`)

// zshEquals: in zsh =git is the path of git, a command word of its own spelling.
var zshEquals = regexp.MustCompile(`(` + cmdPos + `)=`)

// heredocRe starts a command substitution that only echoes a heredoc, $(cat <<'EOF' ...): Claude
// Code's way to pass a commit message. Its text is a message, not a command, so it is cut out before
// the rest is checked; an unquoted delimiter is cut only when the body expands nothing.
var heredocRe = regexp.MustCompile(`\$\(\s*cat\s+<<-?\s*(['"]?)(\w+)['"]?[ \t]*\n`)

func stripHeredocs(cmd string) string {
	for from := 0; ; {
		m := heredocRe.FindStringSubmatchIndex(cmd[from:])
		if m == nil {
			return cmd
		}
		start, bodyAt, quote, word := from+m[0], from+m[1], cmd[from+m[2]:from+m[3]], cmd[from+m[4]:from+m[5]]
		end := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(word) + `[ \t]*\n?[ \t]*\)`).FindStringIndex(cmd[bodyAt:])
		if end == nil || quote == "" && strings.ContainsAny(cmd[bodyAt:bodyAt+end[0]], "$`") {
			from = bodyAt
			continue
		}
		cmd = cmd[:start] + "msg" + cmd[bodyAt+end[1]:]
		from = start
	}
}
