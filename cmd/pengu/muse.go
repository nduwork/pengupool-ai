package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/musehost"
)

// Muse Code as a driven harness: `muse-run` speaks MSP to `muse serve`
// (approvals decided on the wire), `muse-exec` is the one-shot headless
// worker. muse-run registers the pengu MCP server into the Muse session
// so the owner's Compass rides along.

// museRun drives one MSP turn and streams its text.
func museRun(args []string) error {
	fs := flag.NewFlagSet("muse-run", flag.ContinueOnError)
	cwd := fs.String("cwd", "", "workspace root (default: current directory)")
	mode := fs.String("approval-mode", "onRequest", "allowAll|promptUnmatched|onRequest|denyUnmatched")
	provider := fs.String("provider", "", "echo|meta|local (default: muse settings)")
	model := fs.String("model", "", "model id")
	yes := fs.Bool("yes", false, "approve every wire approval without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: pengu muse-run [--cwd DIR] [--approval-mode M] [--yes] \"prompt\"")
	}
	dir := *cwd
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	ctx := context.Background()
	in := bufio.NewReader(os.Stdin)
	tty := stdinIsTerminal()
	host, err := musehost.Start(ctx, musehost.Options{Provider: *provider, Model: *model, Dir: dir}, musehost.Handlers{
		OnText: func(d string) { fmt.Print(d) },
		Approve: func(a musehost.Approval) (string, error) {
			if *yes {
				return pickChoice(a.Choices, "allow", "approve", "accept"), nil
			}
			if !tty {
				return "", nil // musehost denies
			}
			fmt.Fprintf(os.Stderr, "\nmuse asks approval %s — allow? [y/N] ", a.ID)
			line, _ := in.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(line)) == "y" {
				return pickChoice(a.Choices, "allow", "approve", "accept"), nil
			}
			return "", nil
		},
		Answer: func(u musehost.UserInput) (json.RawMessage, error) {
			return nil, fmt.Errorf("interactive input not supported here")
		},
	})
	if err != nil {
		return err
	}
	defer host.Close()
	config := map[string]any{}
	if self, err := os.Executable(); err == nil {
		config["mcpServers"] = map[string]any{
			"pengu": map[string]any{"command": self, "args": []string{"mcp"}, "transport": "stdio"},
		}
	}
	sid, err := host.StartSession(ctx, *mode, dir, config)
	if err != nil {
		return err
	}
	status, err := host.RunTurn(ctx, sid, fs.Arg(0))
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "muse turn %s (session %s)\n", status, sid)
	return nil
}

func pickChoice(choices []string, wants ...string) string {
	for _, c := range choices {
		l := strings.ToLower(c)
		for _, w := range wants {
			if strings.Contains(l, w) {
				return c
			}
		}
	}
	return ""
}

// museExec runs one headless prompt (`muse exec --json`).
func museExec(args []string) error {
	fs := flag.NewFlagSet("muse-exec", flag.ContinueOnError)
	provider := fs.String("provider", "", "echo|meta (default: meta)")
	model := fs.String("model", "", "model id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: pengu muse-exec [--provider P] [--model M] \"prompt\"")
	}
	var extra []string
	if *provider != "" {
		extra = append(extra, "--provider", *provider)
	}
	if *model != "" {
		extra = append(extra, "--model", *model)
	}
	out, err := musehost.Exec(context.Background(), "", fs.Arg(0), extra...)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
