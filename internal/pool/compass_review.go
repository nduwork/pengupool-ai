package pool

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
)

// The Compass review: what the owner is asked to tidy, never done on its own. Two kinds of item:
//
//   - broad: an allow line whose pattern covers far more than the owner chose (a bare `git` or `rm`,
//     `gh pr`, `gh release`…); listed first. The owner removes it (new answers are then kept per
//     subcommand) or edits it to say it stays.
//   - merge: allow/deny lines kept as a whole command before 0.11.4 ("The owner allows Bash for
//     `cd x && go test ./...`") that the command patterns of today ("go test") cover. The owner
//     applies each one; the lines' evidence moves to the pattern's line.
//
// Exact-text lines (scripts, floor items, anything commandKeys keeps exact) are never proposed.

// broadExtra are the broad commands the floor does not name; every other broad pattern is derived
// from the floor itself (broadWhy), so the two lists cannot drift.
var broadExtra = map[string]string{
	"rm": "removes files", "mv": "moves and overwrites files", "chmod": "changes permissions", "chown": "changes owners",
	"kill": "stops processes", "pkill": "stops processes", "killall": "stops processes",
	"psql": "runs any SQL", "mysql": "runs any SQL", "sqlite3": "runs any SQL", "redis-cli": "runs any Redis command", "mongosh": "runs any database command",
	"unlink": "removes files", "rmdir": "removes folders", "chgrp": "changes groups", "chflags": "changes file flags", "setfacl": "changes permissions", "xattr": "changes file attributes",
	"launchctl": "starts and stops services", "systemctl": "starts and stops services", "crontab": "schedules commands", "diskutil": "erases disks", "security": "reads and changes the keychain",
	"cp": "overwrites files", "tee": "overwrites files", "install": "overwrites files", "ln": "replaces files with links",
}

// broadProbes are what a command or pair is followed by to see whether the floor would be reached through it.
var broadProbes = []string{"", " push", " merge", " publish", " apply", " delete", " release", " pr merge", " api -X POST x", " -rf x", " -delete", " of=x", " -s 0",
	" -d x", " build --push", " -- .", " .", " drop", " --hard", " 777", " < x"}

type reviewLine struct {
	UID  string `json:"uid"`
	Body string `json:"body"`
}

type reviewItem struct {
	Kind     string       `json:"kind"` // broad | merge
	Way      string       `json:"way"`  // allow | deny
	Why      string       `json:"why"`
	Lines    []reviewLine `json:"lines"`
	Patterns []string     `json:"patterns,omitempty"` // merge: the patterns the lines become
}

// broadWhy says why a Bash pattern (a command, or a command and its subcommand) is broad, "" when it is not.
// ponytail: probes, not the floor's regexps; a floor item no probe reaches is missed, add its probe.
func broadWhy(pattern string) string {
	if w, ok := broadExtra[pattern]; ok {
		return "`" + pattern + "` " + w
	}
	if !keyRe.MatchString(pattern) || strings.HasPrefix(pattern, "=") {
		return ""
	}
	for _, s := range broadProbes {
		if what := firstHit(floor, floorFold(pattern+s)); what != "" {
			return "`" + pattern + "` can reach " + what
		}
	}
	return ""
}

// permTag splits a line's tag "perm:<way>:<tool>:<pattern>"; ok is false for any other line.
func permTag(tag string) (way, tool, pattern string, ok bool) {
	rest, isPerm := strings.CutPrefix(tag, "perm:")
	if !isPerm {
		return
	}
	way, rest, _ = strings.Cut(rest, ":")
	tool, pattern, ok = strings.Cut(rest, ":")
	return way, tool, pattern, ok && (way == "allow" || way == "deny")
}

// wholeCommandPatterns are the patterns a line kept as a whole command (before 0.11.4) becomes: one
// per statement, nil when any statement must stay exact text.
func wholeCommandPatterns(pattern string) []string {
	if strings.HasPrefix(pattern, "=") || keyRe.MatchString(pattern) {
		return nil
	}
	var out []string
	for _, k := range commandKeys(pattern) {
		if k.exact != "" {
			return nil
		}
		out = append(out, cmp2(k.pair, k.word))
	}
	return out
}

func cmp2(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// compassReview computes the items from the owner's lines: broad ones first (a bare word before a
// pair), then merges.
func (p *Pool) compassReviewItems() ([]reviewItem, error) {
	lines, err := p.db.Lines(p.owner(), "", "", 0)
	if err != nil {
		return nil, err
	}
	var bare, pairs, merges []reviewItem
	pat := map[string]string{}          // a broad item's first line → its pattern, for a stable order
	single := map[string][]reviewLine{} // way|pattern → the whole-command lines that become exactly it
	for _, l := range lines {
		way, tool, pattern, ok := permTag(l.Tag)
		if !ok || !strings.EqualFold(tool, "Bash") {
			continue
		}
		rl := reviewLine{l.UID, l.Body}
		if why := broadWhy(pattern); why != "" && way == "allow" && l.Source != "owner" {
			it := reviewItem{Kind: "broad", Way: way, Why: why, Lines: []reviewLine{rl}}
			pat[l.UID] = pattern
			if !strings.Contains(pattern, " ") {
				bare = append(bare, it)
			} else {
				pairs = append(pairs, it)
			}
			continue
		}
		pats := wholeCommandPatterns(pattern)
		switch {
		case len(pats) == 1:
			k := way + "|" + pats[0]
			single[k] = append(single[k], rl)
		case len(pats) > 1:
			why := "a whole command that today's patterns cover"
			for _, pt := range pats {
				if w := broadWhy(pt); w != "" {
					why += "; note " + w
				}
			}
			merges = append(merges, reviewItem{Kind: "merge", Way: way, Why: why, Lines: []reviewLine{rl}, Patterns: pats})
		}
	}
	for k, ls := range single {
		way, pat, _ := strings.Cut(k, "|")
		why := fmt.Sprintf("%d lines for whole commands that `%s` covers", len(ls), pat)
		if w := broadWhy(pat); w != "" {
			why += "; note " + w
		}
		merges = append(merges, reviewItem{Kind: "merge", Way: way, Why: why, Lines: ls, Patterns: []string{pat}})
	}
	slices.SortFunc(merges, func(a, b reviewItem) int {
		return strings.Compare(a.Way+strings.Join(a.Patterns, " ")+a.Lines[0].UID, b.Way+strings.Join(b.Patterns, " ")+b.Lines[0].UID)
	})
	byPattern := func(a, b reviewItem) int {
		return strings.Compare(pat[a.Lines[0].UID]+a.Lines[0].UID, pat[b.Lines[0].UID]+b.Lines[0].UID)
	}
	slices.SortFunc(bare, byPattern)
	slices.SortFunc(pairs, byPattern)
	return append(append(bare, pairs...), merges...), nil
}

func (p *Pool) compassReview(_ context.Context, _ struct{}) (any, error) {
	if _, err := p.agentDB(); err != nil {
		return nil, err
	}
	items, err := p.compassReviewItems()
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []reviewItem{}
	}
	return map[string]any{"items": items}, nil
}

type compassApplyIn struct {
	From     []string `json:"from"`
	Patterns []string `json:"patterns"`
}

// compassApplyMerge applies one merge the owner accepted. The item is computed again and must match
// what the owner saw, so a stale or invented request changes nothing. Each pattern gets its line (an
// existing one stays); the old lines' evidence moves to the most specific pattern's line and they go.
func (p *Pool) compassApplyMerge(_ context.Context, in compassApplyIn) (any, error) {
	db, err := p.agentDB()
	if err != nil {
		return nil, err
	}
	defer p.lockKey("compass")()
	items, err := p.compassReviewItems()
	if err != nil {
		return nil, err
	}
	var it *reviewItem
	for i := range items {
		if items[i].Kind != "merge" || !slices.Equal(items[i].Patterns, in.Patterns) || len(items[i].Lines) != len(in.From) {
			continue
		}
		match := true
		for _, l := range items[i].Lines {
			match = match && slices.Contains(in.From, l.UID)
		}
		if match {
			it = &items[i]
		}
	}
	if it == nil {
		return nil, conflict("the Compass changed; review it again")
	}
	// The evidence goes to the most specific pattern: a pair, else the first that is not `cd`.
	keepAt := slices.IndexFunc(it.Patterns, func(pt string) bool { return strings.Contains(pt, " ") })
	if keepAt < 0 {
		keepAt = max(slices.IndexFunc(it.Patterns, func(pt string) bool { return pt != "cd" }), 0)
	}
	var keep string
	for i, pat := range it.Patterns {
		tag := "perm:" + it.Way + ":Bash:" + pat
		uid := ""
		if have, _ := db.Lines(p.owner(), "", tag, 1); len(have) > 0 {
			uid = have[0].UID
		} else {
			verb := map[string]string{"allow": "allows", "deny": "does not allow"}[it.Way]
			if uid = p.newDecision(agentdb.Line{Tag: tag, Body: fmt.Sprintf("The owner %s Bash for `%s`", verb, pat)}); uid == "" {
				return nil, invalid("the line for %s was not kept", pat)
			}
		}
		if i == keepAt {
			keep = uid
		}
	}
	for _, l := range it.Lines {
		if err := db.MergeLines(keep, l.UID, ""); err != nil {
			return nil, err
		}
	}
	if err := ownLine(db, keep); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}
