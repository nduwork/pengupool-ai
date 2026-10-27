package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/nduwork/pengupool-ai/internal/review"
)

// runReview prints a Muse Code review of a diff: the argument's commit,
// stdin, or the worktree diff. Folded in from the old pengu CLI.
func runReview(args []string) error {
	var diff string
	if len(args) > 1 {
		return fmt.Errorf("usage: pengupool review [REV]")
	}
	if len(args) == 1 {
		out, err := exec.Command("git", "show", args[0], "--").Output()
		if err != nil {
			return fmt.Errorf("git show failed: %v", err)
		}
		diff = string(out)
	} else if fi, _ := os.Stdin.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice == 0 {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		if err != nil {
			return err
		}
		diff = string(raw)
	} else {
		out, err := exec.Command("git", "diff", "HEAD", "--").Output()
		if err != nil {
			return fmt.Errorf("git diff failed: %v", err)
		}
		diff = string(out)
	}
	findings, err := review.ReviewDiff(diff, 0)
	if err != nil {
		return err
	}
	fmt.Println(findings)
	return nil
}
