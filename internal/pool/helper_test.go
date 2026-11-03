package pool

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/agentdb"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

const planKey = "github.com/o/planning-agent"

// helperPool is epicTeam with plan running as the sk-plan helper (keyed planKey).
func helperPool(t *testing.T) (*Pool, *store.Store) {
	t.Helper()
	p, st := twoTeams(t)
	_ = st.Update(func(s *store.State) error {
		for i := range s.Sessions {
			if s.Sessions[i].ID == "planaaaaaaaa" {
				s.Sessions[i].SkillID = "sk-plan"
			}
		}
		s.Domain.Skills[0].Key = planKey
		return nil
	})
	return p, st
}

// soloPlan takes plan off its team, so its turns are its own alone.
func soloPlan(st *store.Store) {
	_ = st.Update(func(s *store.State) error {
		t := &s.Domain.Teams[0]
		t.Members = slices.DeleteFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == "planaaaaaaaa" })
		return nil
	})
}

func placeToolTurn(p *Pool, id, prompt, reply string) {
	p.turnStart(id, prompt)
	p.turnTool(id)
	s, _ := p.session(id)
	p.mu.Lock()
	t := *p.turns[id]
	delete(p.turns, id)
	p.mu.Unlock()
	p.placeTurn(context.Background(), s, t, reply)
}

func putNotes(t *testing.T, p *Pool, agent, text string) {
	t.Helper()
	if _, err := p.db.AddExperience(agentdb.Entry{Helper: agent, Title: text, Body: text}); err != nil {
		t.Fatal(err)
	}
}

// A skill-backed member is a HELPER in its own words and in every teammate's member list.
func TestContextNamesSpecialist(t *testing.T) {
	p, _ := helperPool(t)
	for _, id := range []string{"planaaaaaaaa", "leadaaaaaaaa"} {
		c := p.renderContext(id)
		if !strings.Contains(c, "- plan (session planaaaaaaaa) — HELPER") || strings.Contains(c, "SPECIALIST") {
			t.Fatalf("%s context:\n%s", id, c)
		}
	}
	if c := p.renderContext("planaaaaaaaa"); !strings.Contains(c, "You are **plan**, a HELPER of team **app**.") {
		t.Fatalf("plan context:\n%s", c)
	}
}
