package pool

import (
	"context"
	"testing"
)

// The owner's answer, typed into the asking member, answers its ask; it is not the team's new task.
func TestTypedAnswerSetsNoTask(t *testing.T) {
	p, st := twoTeams(t)
	s, _ := p.session("leadaaaaaaaa")
	goal := func() string { d := st.State().Domain; return findTeam(&d, "team1").Goal }
	p.placeTurn(context.Background(), s, turn{Prompt: "Answer to your ask a1: yes, push both", Delivered: "ask", Tools: 2}, "pushed")
	if g := goal(); g == "Answer to your ask a1: yes, push both" {
		t.Fatalf("a typed answer became the task: %q", g)
	}
	p.placeTurn(context.Background(), s, turn{Prompt: "redeploy on Cloud Run", Tools: 2}, "done")
	if g := goal(); g != "redeploy on Cloud Run" {
		t.Fatalf("the owner's own instruction did not set the task: %q", g)
	}
}
