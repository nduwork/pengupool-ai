package experiment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/judge"
)

func TestQuestions(t *testing.T) {
	if n := Questions("should I run the tests now? yes."); n != 1 {
		t.Fatalf("got %d", n)
	}
	if n := Questions("no questions here."); n != 0 {
		t.Fatalf("got %d", n)
	}
}

func TestTable(t *testing.T) {
	yes, no := true, false
	rows := []Row{
		{Task: "a", Arm: "with", Matched: &yes, Interruptions: 0, Passed: &yes},
		{Task: "a", Arm: "without", Matched: &no, Interruptions: 2, Passed: nil},
	}
	table := Table(rows)
	for _, want := range []string{"| a | with | matched | 0 | pass |", "| a | without | missed | 2 | ? |"} {
		if !strings.Contains(table, want) {
			t.Fatalf("table missing %q:\n%s", want, table)
		}
	}
}

func TestRunTaskBothArms(t *testing.T) {
	oldR, oldRel := RunAgent, Relate
	defer func() { RunAgent, Relate = oldR, oldRel }()
	var prompts []string
	RunAgent = func(prompt, workdir string, timeout time.Duration, maxSteps int) (string, error) {
		prompts = append(prompts, prompt)
		return "I ran the tests. All good?", nil
	}
	Relate = func(words, line, model string) (judge.Relation, error) {
		return judge.Relation{Relation: "supports", Confidence: 0.9}, nil
	}
	outdir := t.TempDir()
	task := Task{ID: "run-tests", Prompt: "run the tests",
		Compass: []string{"Run the test suite."}, Check: "true"}
	rows := RunTask(task, outdir, "", time.Minute, 3)
	if len(rows) != 2 || rows[0].Arm != "without" || rows[1].Arm != "with" {
		t.Fatalf("arms: %+v", rows)
	}
	if rows[1].Matched == nil || !*rows[1].Matched {
		t.Fatalf("matched: %+v", rows[1])
	}
	if rows[1].Interruptions != 1 {
		t.Fatalf("interruptions: %+v", rows[1])
	}
	if rows[1].Passed == nil || !*rows[1].Passed {
		t.Fatalf("passed: %+v", rows[1])
	}
	if !strings.Contains(prompts[1], "Owner's Compass") || strings.Contains(prompts[0], "Compass") {
		t.Fatalf("prompts differ only by prefix: %q", prompts)
	}
	if _, err := os.Stat(filepath.Join(outdir, "run-tests-with.md")); err != nil {
		t.Fatal("transcript not written")
	}
	// One bad arm never kills the experiment.
	RunAgent = func(string, string, time.Duration, int) (string, error) {
		return "", errors.New("boom")
	}
	rows = RunTask(task, outdir, "", time.Minute, 3)
	if len(rows) != 2 || rows[0].Error != "boom" {
		t.Fatalf("error arm: %+v", rows)
	}
}

func TestMainWritesResults(t *testing.T) {
	oldR, oldRel := RunAgent, Relate
	defer func() { RunAgent, Relate = oldR, oldRel }()
	RunAgent = func(prompt, workdir string, timeout time.Duration, maxSteps int) (string, error) {
		return "done", nil
	}
	Relate = func(words, line, model string) (judge.Relation, error) {
		return judge.Relation{Relation: "unrelated"}, nil
	}
	dir := t.TempDir()
	tasks := `[{"id": "t1", "prompt": "do it", "check": "true"}]`
	tasksPath := filepath.Join(dir, "tasks.json")
	if err := os.WriteFile(tasksPath, []byte(tasks), 0o644); err != nil {
		t.Fatal(err)
	}
	outdir := filepath.Join(dir, "results")
	rows, err := Main(tasksPath, outdir, "", time.Minute, 3, []string{"t1"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("main: %+v %v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(outdir, "results.json")); err != nil {
		t.Fatal("results.json not written")
	}
}
