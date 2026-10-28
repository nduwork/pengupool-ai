package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/pool"
)

// cmdDoctor: `doctor` checks the machine PenguPool runs on and says what to fix. It changes nothing
// unless --fix is given and the owner confirms; the one thing it can fix is the v1 PenguPool entries
// left in Claude's own settings. What it removes is kept in PenguPool's shared record (not beside the
// file, where a dotfiles repo could commit it), and `--restore` puts it back.
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fix := fs.Bool("fix", false, "remove the v1 PenguPool entries from Claude's settings, after asking")
	yes := fs.Bool("yes", false, "with --fix or --restore, do not ask")
	restore := fs.Bool("restore", false, "list what --fix removed, or with an id put that back into Claude's settings")
	home := fs.String("home", "", "the home directory to check (default: yours)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*fix || *restore) && os.Getenv("PENGUPOOL_SESSION_ID") != "" {
		return errors.New("doctor --fix and --restore change the owner's Claude settings: the owner runs them, not an agent session")
	}
	h := *home
	if h == "" {
		var err error
		if h, err = os.UserHomeDir(); err != nil {
			return err
		}
	}
	if *restore {
		return doctorRestore(h, fs.Args(), *yes)
	}
	warned, dirty := false, false
	show := func(cs []doctorCheck) {
		for _, c := range cs {
			mark := "ok  "
			if c.Warn {
				mark, warned = "warn", true
			}
			fmt.Printf("%s  %s: %s\n", mark, c.Name, c.Detail)
		}
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(h, ".claude", name)
		found, cleaned, removed, err := v1Entries(path)
		if err != nil {
			show([]doctorCheck{{"Claude settings", fmt.Sprintf("%s: %v", path, err), true}})
			continue
		}
		if len(found) == 0 {
			continue
		}
		dirty = true
		show([]doctorCheck{{"Claude settings", fmt.Sprintf("%s holds PenguPool v1 entries (they run in every Claude session): %s", path, strings.Join(found, "; ")), true}})
		if !*fix {
			continue
		}
		if !*yes && !confirm(fmt.Sprintf("remove them from %s (they are kept in PenguPool's record)? [y/N] ", path)) {
			continue
		}
		// Recorded first: with no host to take the record, nothing is removed, so nothing is lost.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = rpc(ctx, poolStore(), "doctor.record", map[string]any{"file": name, "entries": forRecord(removed, h)})
		cancel()
		if err != nil {
			show([]doctorCheck{{"Claude settings", fmt.Sprintf("%s not changed: PenguPool could not keep a record of what it removes (%v); start PenguPool and run this again", path, err), true}})
			continue
		}
		if err := writeSettings(path, cleaned); err != nil {
			show([]doctorCheck{{"Claude settings", fmt.Sprintf("%s: %v", path, err), true}})
			continue
		}
		fmt.Printf("done  removed from %s; kept in PenguPool's record (`pengupool doctor --restore` lists it)\n", path)
	}
	if !dirty {
		show([]doctorCheck{{"Claude settings", "no PenguPool v1 entries", false}})
	}
	show(doctorChecks(h, os.Getenv))
	if warned && dirty && !*fix {
		fmt.Println("\nRun `pengupool doctor --fix` to clean Claude's settings (it asks first and keeps what it removes in PenguPool's record).")
	}
	return nil
}

type doctorCheck struct {
	Name, Detail string
	Warn         bool
}

// doctorChecks are the checks that only read: v1 installed, the keychain, the agent CLIs, and what
// reaches the Compass.
func doctorChecks(home string, getenv func(string) string) []doctorCheck {
	var cs []doctorCheck
	if out, err := runShort("uv", "tool", "list"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "pengupool ") {
				cs = append(cs, doctorCheck{"PenguPool v1", "installed (" + strings.TrimSpace(l) + "); remove it with `uv tool uninstall pengupool`", true})
			}
		}
	}
	if getenv("SSH_CONNECTION") != "" {
		cs = append(cs, doctorCheck{"Keychain", "this shell is an SSH login: a daemon started from it cannot read the decision model's key from the macOS keychain (security exit 36). Start PenguPool from the desktop app", true})
	} else {
		cs = append(cs, doctorCheck{"Keychain", "this shell is not an SSH login (the running daemon's own environment is not checked)", false})
	}
	for _, c := range []string{"claude", "pi", "omp", "codex", "ollama"} {
		path, err := exec.LookPath(c)
		if err != nil {
			cs = append(cs, doctorCheck{c, "not installed (only needed to run agents on it)", false})
			continue
		}
		v, err := runShort(path, "--version")
		if err != nil {
			cs = append(cs, doctorCheck{c, path + ": --version failed: " + err.Error(), true})
			continue
		}
		cs = append(cs, doctorCheck{c, strings.TrimSpace(strings.SplitN(v, "\n", 2)[0]) + " (" + path + ")", false})
	}
	cs = append(cs, doctorCheck{"Compass", "your rules reach it only when typed in the app's terminal or in `pengupool attach`; prompts typed through the phone, Claude's own remote control, or recalled with the arrow keys do not", false})
	return cs
}

func runShort(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func confirm(q string) bool {
	fmt.Print(q)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "y")
}

// v1Entries finds the PenguPool v1 hooks and status line in a Claude settings file and returns what it
// found, the document without them, and the removed entries alone (hooks by event, and the status
// line) as they were, for the record. A missing file has none. Other tools'
// entries are untouched, anything it cannot read as a hook stays; the document is returned as parsed,
// so key order is not kept.
func v1Entries(path string) (found []string, cleaned, removed map[string]any, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers keep the text they were written with
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, nil, err
	}
	removedHooks := map[string]any{}
	if hooks, ok := doc["hooks"].(map[string]any); ok {
		for event, v := range hooks {
			groups, ok := v.([]any)
			if !ok {
				continue // not a list of hook groups: not ours to read
			}
			var keptGroups []any
			for _, g := range groups {
				group, ok := g.(map[string]any)
				inner, isList := group["hooks"].([]any)
				if !ok || !isList {
					keptGroups = append(keptGroups, g)
					continue
				}
				var kept, gone []any
				for _, hk := range inner {
					entry, _ := hk.(map[string]any)
					if cmd, _ := entry["command"].(string); pool.IsV1Command(cmd) {
						found = append(found, "hook "+event)
						gone = append(gone, hk)
						continue
					}
					kept = append(kept, hk)
				}
				if len(gone) > 0 { // the group's own keys (a matcher) and only the hooks removed from it
					g2 := map[string]any{}
					for k, v := range group {
						g2[k] = v
					}
					g2["hooks"] = gone
					prev, _ := removedHooks[event].([]any)
					removedHooks[event] = append(prev, g2)
				}
				if len(kept) > 0 {
					group["hooks"] = kept
					keptGroups = append(keptGroups, group)
				}
			}
			if len(keptGroups) > 0 {
				hooks[event] = keptGroups
			} else {
				delete(hooks, event)
			}
		}
		if len(hooks) == 0 {
			delete(doc, "hooks")
		}
	}
	if sl, ok := doc["statusLine"].(map[string]any); ok {
		if cmd, _ := sl["command"].(string); pool.IsV1Command(cmd) {
			found = append(found, "statusLine")
			// Only v1's own part is kept: for the wrapper, what comes before the owner's line (restore
			// wraps the line the settings hold then); the owner's command is never recorded.
			orig := map[string]any{}
			for k, v := range sl {
				orig[k] = v
			}
			if prefix, _, wrapped := strings.Cut(cmd, " -- "); wrapped && strings.Contains(cmd, " || eval ") {
				orig["command"], orig["wraps"] = prefix, true
			}
			removed = map[string]any{"statusLine": orig}
			// v1 wrapped the owner's own line as "... -- <quoted line> || eval <quoted line>"; give that
			// line back. Without it (v1's standalone form) there is none to give back.
			if _, line, ok := strings.Cut(cmd, " || eval "); ok {
				sl["command"] = unquoteWord(strings.TrimSpace(line))
			} else {
				delete(doc, "statusLine")
			}
		}
	}
	if len(removedHooks) > 0 {
		if removed == nil {
			removed = map[string]any{}
		}
		removed["hooks"] = removedHooks
	}
	return found, doc, removed, nil
}

// unquoteWord undoes Python's shlex.quote for one word: bare, or inside single quotes with each
// embedded quote written '"'"'.
func unquoteWord(w string) string {
	if len(w) >= 2 && w[0] == '\'' && w[len(w)-1] == '\'' {
		return strings.ReplaceAll(w[1:len(w)-1], `'"'"'`, "'")
	}
	return w
}

// writeSettings saves doc over path (the file a symlink points to, so a dotfile link survives). The
// write is a rename of a file beside it, so it is whole or not at all. Keys come back in sorted order.
func writeSettings(path string, doc map[string]any) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false) // && and > stay as written
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	tmp := path + ".pengupool-tmp"
	if err := os.WriteFile(tmp, out.Bytes(), perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// doctorRestore lists what `doctor --fix` removed (no id), or puts one record's entries back into
// the settings file it came from (an id, or its unique prefix).
func doctorRestore(home string, args []string, yes bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := rpc(ctx, poolStore(), "doctor.removed", struct{}{})
	if err != nil {
		return fmt.Errorf("PenguPool's record is read through PenguPool: start it and run this again (%w)", err)
	}
	var out struct {
		Removed []struct{ Hash, File, At, Entries string }
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return err
	}
	if len(args) == 0 {
		if len(out.Removed) == 0 {
			fmt.Println("doctor has removed nothing from Claude's settings.")
		}
		for _, r := range out.Removed {
			fmt.Printf("%s  %s  %s\n", r.Hash[:12], r.At, r.File)
		}
		if len(out.Removed) > 0 {
			fmt.Println("\nRun `pengupool doctor --restore ID` to put one back.")
		}
		return nil
	}
	var hit []int
	for i, r := range out.Removed {
		if strings.HasPrefix(r.Hash, args[0]) {
			hit = append(hit, i)
		}
	}
	if len(hit) != 1 {
		return fmt.Errorf("%d records match %q; list them with `pengupool doctor --restore`", len(hit), args[0])
	}
	r := out.Removed[hit[0]]
	path := filepath.Join(home, ".claude", r.File)
	if !yes && !confirm(fmt.Sprintf("put the PenguPool v1 entries recorded %s back into %s? [y/N] ", r.At, path)) {
		return nil
	}
	doc := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var entries map[string]any
	dec := json.NewDecoder(strings.NewReader(r.Entries))
	dec.UseNumber()
	if err := dec.Decode(&entries); err != nil {
		return err
	}
	notes := restoreEntries(doc, entries, home)
	if err := writeSettings(path, doc); err != nil {
		return err
	}
	fmt.Printf("done  restored into %s\n", path)
	for _, n := range notes {
		fmt.Println("note  " + n)
	}
	return nil
}

// forRecord is what is handed over to be kept: the removed entries with the home folder written ~,
// so the user name is not synced and the same entries from two machines are one record.
func forRecord(removed map[string]any, home string) json.RawMessage {
	b, _ := json.Marshal(removed)
	h, _ := json.Marshal(home)
	return bytes.ReplaceAll(b, bytes.Trim(h, `"`), []byte("~"))
}

// pyQuote is Python's shlex.quote, which v1 wrote its status line with (see unquoteWord).
func pyQuote(w string) string {
	if w == "" {
		return "''"
	}
	if strings.Trim(w, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%+=:,./-_") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'"'"'`) + "'"
}

// restoreEntries puts a record's entries back into doc and returns what it left alone. A hook goes
// back unless a hook with the same command is already under that event (the file may still hold the
// group it came from); ~ is this machine's home. A status line that wraps the owner's wraps the
// one doc holds; a standalone one is set only when doc has none.
func restoreEntries(doc, entries map[string]any, home string) (notes []string) {
	expand := func(s string) string { return strings.ReplaceAll(s, "~/", home+"/") }
	if rh, ok := entries["hooks"].(map[string]any); ok {
		hooks, _ := doc["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		for event, v := range rh {
			groups, _ := v.([]any)
			have, _ := hooks[event].([]any)
			present := map[string]bool{}
			for _, g := range have {
				gm, _ := g.(map[string]any)
				inner, _ := gm["hooks"].([]any)
				for _, hk := range inner {
					hm, _ := hk.(map[string]any)
					c, _ := hm["command"].(string)
					present[c], present[expand(c)] = true, true // a ~/ the file wrote itself is the same command
				}
			}
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				inner, _ := gm["hooks"].([]any)
				var add []any
				for _, hk := range inner {
					hm, _ := hk.(map[string]any)
					c, _ := hm["command"].(string)
					c = expand(c)
					if present[c] {
						continue
					}
					present[c] = true
					h2 := map[string]any{}
					for k, v := range hm {
						h2[k] = v
					}
					h2["command"] = c
					add = append(add, h2)
				}
				if len(add) > 0 {
					g2 := map[string]any{}
					for k, v := range gm {
						g2[k] = v
					}
					g2["hooks"] = add
					have = append(have, g2)
				}
			}
			hooks[event] = have
		}
		doc["hooks"] = hooks
	}
	if rs, ok := entries["statusLine"].(map[string]any); ok {
		cur, has := doc["statusLine"].(map[string]any)
		curCmd, _ := cur["command"].(string)
		prefix, _ := rs["command"].(string)
		prefix = expand(prefix)
		switch {
		case has && pool.IsV1Command(curCmd):
		case rs["wraps"] == true && has && curCmd != "":
			cur["command"] = prefix + " -- " + pyQuote(curCmd) + " || eval " + pyQuote(curCmd)
		case rs["wraps"] == true:
			notes = append(notes, "the status line was not restored: there is none to wrap")
		case has:
			b, _ := json.Marshal(cur)
			notes = append(notes, "statusLine left as it is: "+string(b))
		default:
			sl := map[string]any{}
			for k, v := range rs {
				sl[k] = v
			}
			sl["command"] = prefix
			doc["statusLine"] = sl
		}
	}
	return notes
}
