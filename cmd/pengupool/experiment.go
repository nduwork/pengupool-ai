package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/experiment"
	pengustore "github.com/nduwork/pengupool-ai/internal/pengustore"
)

// runExperiment runs the with-vs-without benchmark. Folded in from the
// old pengu CLI, flags and output unchanged.
func runExperiment(args []string) error {
	tasks := "experiment/tasks.json"
	out := filepath.Join(pengustore.Home(), "experiment", "results")
	var only []string
	maxSteps := 12
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tasks":
			i++
			if i < len(args) {
				tasks = args[i]
			}
		case "--out":
			i++
			if i < len(args) {
				out = args[i]
			}
		case "--only":
			i++
			for ; i < len(args) && !strings.HasPrefix(args[i], "--"); i++ {
				only = append(only, args[i])
			}
			i--
		case "--judge-model":
			i++
			if i < len(args) {
				_ = os.Setenv("PENGUPOOL_JUDGE_MODEL", args[i])
			}
		case "--judge-base-url":
			i++
			if i < len(args) {
				_ = os.Setenv("MUSE_BASE_URL", args[i])
			}
		case "--max-steps":
			i++
			if i < len(args) {
				n, err := strconv.Atoi(args[i])
				if err != nil {
					return fmt.Errorf("bad --max-steps: %s", args[i])
				}
				maxSteps = n
			}
		default:
			return fmt.Errorf("usage: pengupool experiment [--tasks F] [--out D] [--only ID...] [--judge-model M] [--judge-base-url U] [--max-steps N]")
		}
	}
	_, err := experiment.Main(tasks, out, "", 5*time.Minute, maxSteps, only)
	return err
}
