package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nduwork/pengupool-ai/internal/pool"
)

// cmdEval: `eval` replays the owner's exchanges (their Claude Code transcripts, or the bundled
// sample) through the real triage and check-in code on a temporary database, with the configured
// decision model, and prints aggregates. It fails when a bar of the plan is missed.
func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	dir := fs.String("transcripts", "", "a folder of Claude Code transcripts (DIR/**/*.jsonl)")
	sample := fs.Bool("sample", false, "the bundled, invented, labelled sample")
	since := fs.String("since", "", "only exchanges from this date, YYYY-MM-DD")
	limit := fs.Int("limit", 0, "only the newest N exchanges")
	home := fs.String("home", "", "the PenguPool home whose decision model to use (default: $PENGUPOOL_HOME or ~)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*dir == "") == !*sample {
		return errors.New("eval takes --transcripts DIR or --sample")
	}
	cfgDir := storeDir("")
	if *home != "" {
		cfgDir = filepath.Join(*home, "v2")
	}
	cfg, err := readConfig(cfgDir)
	if err != nil {
		return fmt.Errorf("read the configuration in %s: %v", cfgDir, err)
	}
	var eps []pool.Episode
	if *sample {
		eps, err = pool.SampleEpisodes()
	} else {
		var from time.Time
		if *since != "" {
			if from, err = time.Parse(time.DateOnly, *since); err != nil {
				return fmt.Errorf("--since: %v", err)
			}
		}
		eps, err = pool.TranscriptEpisodes(*dir, from, *limit)
	}
	if err != nil {
		return err
	}
	r, err := pool.Eval(cfg, eps)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Print(r)
	}
	if r.Verdict == "FAIL" {
		return errors.New("the evaluation missed a bar")
	}
	return nil
}
