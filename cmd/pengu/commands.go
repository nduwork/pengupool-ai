package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nduwork/pengupool-ai/internal/chat"
	"github.com/nduwork/pengupool-ai/internal/compass"
	"github.com/nduwork/pengupool-ai/internal/experience"
	"github.com/nduwork/pengupool-ai/internal/experiment"
	"github.com/nduwork/pengupool-ai/internal/ingest"
	"github.com/nduwork/pengupool-ai/internal/owner"
	"github.com/nduwork/pengupool-ai/internal/review"
	"github.com/nduwork/pengupool-ai/internal/rules"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
	"github.com/nduwork/pengupool-ai/internal/voice"
)

func readAllStdin() (string, error) {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func runRule(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu rule \"words\"")
	}
	rule, err := rules.Extract(args[0])
	if err != nil {
		return err
	}
	if rule == "" {
		fmt.Println("No standing rule in that.")
		return nil
	}
	if !stdinIsTerminal() {
		return fmt.Errorf("a rule is saved only after you confirm it at a terminal")
	}
	fmt.Printf("Save to the Compass: %s [y/N] ", rule)
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil || (answer != "y" && answer != "Y") {
		fmt.Println("not saved")
		return nil
	}
	line, err := compass.Add(rule, "typed", 1.0)
	if err != nil {
		return err
	}
	fmt.Println("saved", line.ID)
	return nil
}

func runNew(args []string) error {
	var agent, task string
	trust := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			i++
			if i < len(args) {
				agent = args[i]
			}
		case "--task":
			i++
			if i < len(args) {
				task = args[i]
			}
		case "--trust-workspace":
			trust = true
		default:
			return fmt.Errorf("usage: pengu new --agent claude|muse [--task T] [--trust-workspace]")
		}
	}
	if agent == "" {
		return fmt.Errorf("usage: pengu new --agent claude|muse [--task T] [--trust-workspace]")
	}
	if trust && agent != "muse" {
		return fmt.Errorf("--trust-workspace is muse only")
	}
	row, err := session.New(agent, task, "", trust)
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s  tmux:%s\n", row.ID, row.Agent, row.Tmux)
	return nil
}

func runLs() error {
	rows, err := session.Ls()
	if err != nil {
		return err
	}
	for _, r := range rows {
		alive := "dead"
		if r.Alive {
			alive = "alive"
		}
		task := r.Task
		if len(task) > 60 {
			task = task[:60]
		}
		fmt.Printf("%s  %s  %s  %s\n", r.ID, r.Agent, alive, task)
	}
	return nil
}

func runStop(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu stop <id>")
	}
	ok, err := session.Stop(args[0])
	if err != nil {
		return err
	}
	fmt.Println(map[bool]string{true: "stopped", false: "no such session"}[ok])
	return nil
}

func runSend(args []string) error {
	var from, to, text string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from":
			i++
			if i < len(args) {
				from = args[i]
			}
		case "--to":
			i++
			if i < len(args) {
				to = args[i]
			}
		case "--text":
			i++
			if i < len(args) {
				text = args[i]
			}
		default:
			return fmt.Errorf("usage: pengu send --from ID --to ID --text TEXT")
		}
	}
	if from == "" || to == "" || text == "" {
		return fmt.Errorf("usage: pengu send --from ID --to ID --text TEXT")
	}
	e, err := chat.Send(from, to, text)
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s -> %s\n", e.ID, e.From, e.To)
	return nil
}

func runInbox(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu inbox <id>")
	}
	entries, err := chat.Inbox(args[0])
	if err != nil {
		return err
	}
	for _, e := range entries {
		fmt.Printf("%s  from %s: %s\n", e.ID, e.From, e.Text)
	}
	return nil
}

func runAsk(args []string) error {
	var sid, question string
	var options []string
	expires := 300
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			i++
			if i < len(args) {
				sid = args[i]
			}
		case "--question":
			i++
			if i < len(args) {
				question = args[i]
			}
		case "--option":
			i++
			if i < len(args) {
				options = append(options, args[i])
			}
		case "--expires":
			i++
			if i < len(args) {
				n, err := strconv.Atoi(args[i])
				if err != nil {
					return fmt.Errorf("bad --expires: %s", args[i])
				}
				expires = n
			}
		default:
			return fmt.Errorf("usage: pengu ask --session ID --question Q --option A [--option B] [--expires N]")
		}
	}
	if sid == "" || question == "" || len(options) == 0 {
		return fmt.Errorf("usage: pengu ask --session ID --question Q --option A [--option B] [--expires N]")
	}
	q, err := owner.CreateQuestion(sid, question, options, float64(expires))
	if err != nil {
		return err
	}
	fmt.Printf("question %s waiting for owner...\n", q.ID)
	opt, problem := owner.WaitAnswer(q.ID, float64(expires))
	switch problem {
	case "":
		fmt.Println(opt)
		return nil
	case "timeout":
		return fmt.Errorf("still waiting: question %s is pending (owner has not answered)", q.ID)
	default:
		return fmt.Errorf("no answer: %s", problem)
	}
}

func runOwnerServe(args []string) error {
	bind := "127.0.0.1"
	port := owner.DefaultPort
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bind":
			i++
			if i < len(args) {
				bind = args[i]
			}
		case "--port":
			i++
			if i < len(args) {
				n, err := strconv.Atoi(args[i])
				if err != nil {
					return fmt.Errorf("bad --port: %s", args[i])
				}
				port = n
			}
		default:
			return fmt.Errorf("usage: pengu owner-serve [--bind ADDR] [--port N]")
		}
	}
	srv, err := owner.Serve(bind, port)
	if err != nil {
		return err
	}
	fmt.Printf("owner API on %s:%d (localhost; tailnet ACL is an owner action)\n", bind, port)
	return srv.ListenAndServe()
}

func runProposals() error {
	items, err := owner.ListProposals()
	if err != nil {
		return err
	}
	for _, p := range items {
		fmt.Printf("%s  %s  (%s)\n", p.ID, p.Words, p.Status)
	}
	return nil
}

func runLearned(args []string) error {
	var helper, title, task, lesson, outcome, sid string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--helper":
			i++
			if i < len(args) {
				helper = args[i]
			}
		case "--title":
			i++
			if i < len(args) {
				title = args[i]
			}
		case "--task":
			i++
			if i < len(args) {
				task = args[i]
			}
		case "--lesson":
			i++
			if i < len(args) {
				lesson = args[i]
			}
		case "--outcome":
			i++
			if i < len(args) {
				outcome = args[i]
			}
		case "--session":
			i++
			if i < len(args) {
				sid = args[i]
			}
		default:
			return fmt.Errorf("usage: pengu learned --helper H --title T --task TASK --lesson L --outcome worked|failed [--session S]")
		}
	}
	if helper == "" || title == "" || task == "" || lesson == "" || outcome == "" {
		return fmt.Errorf("usage: pengu learned --helper H --title T --task TASK --lesson L --outcome worked|failed [--session S]")
	}
	r, err := experience.Ingest(helper, title, task, lesson, outcome, sid, "")
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", r.Action, r.Entry.ID)
	return nil
}

func runRecall(args []string) error {
	var helper, task string
	k := 5
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--helper":
			i++
			if i < len(args) {
				helper = args[i]
			}
		case "--task":
			i++
			if i < len(args) {
				task = args[i]
			}
		case "--k":
			i++
			if i < len(args) {
				n, err := strconv.Atoi(args[i])
				if err != nil {
					return fmt.Errorf("bad --k: %s", args[i])
				}
				k = n
			}
		default:
			return fmt.Errorf("usage: pengu recall --helper H --task T [--k N]")
		}
	}
	if helper == "" || task == "" {
		return fmt.Errorf("usage: pengu recall --helper H --task T [--k N]")
	}
	entries, err := experience.ForTask(helper, task, k, experience.Threshold, "")
	if err != nil {
		return err
	}
	for _, e := range entries {
		fmt.Printf("%s  %s: %s\n", e.ID, e.Title, e.Lesson)
	}
	return nil
}

func runIngest(args []string) error {
	var text, sid string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--text":
			i++
			if i < len(args) {
				text = args[i]
			}
		case "--session":
			i++
			if i < len(args) {
				sid = args[i]
			}
		default:
			return fmt.Errorf("usage: pengu ingest --text TEXT [--session ID]")
		}
	}
	if text == "" {
		return fmt.Errorf("usage: pengu ingest --text TEXT [--session ID]")
	}
	pid, msg, err := ingest.Ingest(text, sid, "")
	if err != nil {
		return err
	}
	if pid == "" {
		pid = "-"
	}
	fmt.Printf("%s  %s\n", pid, msg)
	return nil
}

func runConfirm(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: pengu confirm <proposal-id>")
	}
	code, out := owner.ConfirmProposal(args[0])
	if code != 200 {
		msg, _ := out["error"].(string)
		return fmt.Errorf("pengu: %s", msg)
	}
	applied, _ := out["applied"].(string)
	id, _ := out["id"].(string)
	fmt.Printf("%s %s\n", applied, id)
	return nil
}

func runVoice(args []string) error {
	var clip, sid string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clip":
			i++
			if i < len(args) {
				clip = args[i]
			}
		case "--session":
			i++
			if i < len(args) {
				sid = args[i]
			}
		default:
			return fmt.Errorf("usage: pengu voice --clip FILE [--session ID]")
		}
	}
	if clip == "" {
		return fmt.Errorf("usage: pengu voice --clip FILE [--session ID]")
	}
	words, err := voice.Transcribe(clip)
	if err != nil {
		return err
	}
	fmt.Printf("heard: %s\n", words)
	pid, msg, err := ingest.Ingest(words, sid, "")
	if err != nil {
		return err
	}
	if pid == "" {
		pid = "-"
	}
	fmt.Printf("%s  %s\n", pid, msg)
	return nil
}

func runReview(args []string) error {
	var diff string
	if len(args) > 1 {
		return fmt.Errorf("usage: pengu review [REV]")
	}
	if len(args) == 1 {
		out, err := exec.Command("git", "show", args[0], "--").Output()
		if err != nil {
			return fmt.Errorf("git show failed: %v", err)
		}
		diff = string(out)
	} else if !stdinIsTerminal() {
		out, err := readAllStdin()
		if err != nil {
			return err
		}
		diff = out
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

func runExperiment(args []string) error {
	tasks := "experiment/tasks.json"
	out := filepath.Join(store.Home(), "experiment", "results")
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
			for ; i < len(args) && len(args[i]) > 0 && args[i][:2] != "--"; i++ {
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
			return fmt.Errorf("usage: pengu experiment [--tasks F] [--out D] [--only ID...] [--judge-model M] [--judge-base-url U] [--max-steps N]")
		}
	}
	_, err := experiment.Main(tasks, out, "", 5*time.Minute, maxSteps, only)
	return err
}
