package pool

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Agents talk only to their neighbours on their team's tree: their parent and their children (the
// members at the top talk to each other). A message for anyone further goes to the parent, which
// decides whether to pass it on, so a talk between two children never reaches the top. The host
// enforces it on every path (`pengupool send`, Claude's SendMessage, and a cross-session message
// arriving at a session). The one exception is the owner's: typing @<session> in a session's own
// prompt opens a two-way channel between that session and the one named, for a while. Nothing an
// agent writes can open one.

const crossTeamFor = 2 * time.Hour

type sessionPair struct{ a, b string }

func pairOf(x, y string) sessionPair {
	if x > y {
		x, y = y, x
	}
	return sessionPair{x, y}
}

// ponytail: grants live in memory, so a host restart closes every channel; the owner @s again.
func (p *Pool) grantCrossTeam(x, y string) {
	p.mu.Lock()
	p.crossOK[pairOf(x, y)] = time.Now().Add(crossTeamFor)
	p.mu.Unlock()
}

func (p *Pool) crossTeamOpen(x, y string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.crossOK[pairOf(x, y)]
	return ok && time.Now().Before(exp)
}

// sameTeam: both sessions are active members of one team.
func sameTeam(d *protocol.Domain, x, y string) bool {
	t := teamOf(d, x)
	if t == nil {
		return false
	}
	for _, m := range t.Members {
		if m.State == "active" && m.SessionID == y {
			return true
		}
	}
	return false
}

// treeParent is the member's nearest active ancestor on the team's tree ("" at the top).
func treeParent(t *protocol.Team, id string) string {
	seen := map[string]bool{id: true}
	for up := parentOf(t, id); up != "" && !seen[up]; up = parentOf(t, up) {
		seen[up] = true
		for _, m := range t.Members {
			if m.SessionID == up && m.State == "active" {
				return up
			}
		}
	}
	return ""
}

// isAncestor: anc is an active member above id on the team's tree, following the recorded parents
// (a closed member between them does not break the chain); a cycle in the record is no.
func isAncestor(t *protocol.Team, anc, id string) bool {
	if t == nil || anc == "" || anc == id {
		return false
	}
	seen := map[string]bool{id: true}
	for up := parentOf(t, id); up != "" && !seen[up]; up = parentOf(t, up) {
		if up == anc {
			return slices.ContainsFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == anc && m.State == "active" })
		}
		seen[up] = true
	}
	return false
}

// adjacent: x and y are on one team and next to each other on its tree.
func adjacent(d *protocol.Domain, x, y string) bool {
	if x == y || !sameTeam(d, x, y) {
		return false
	}
	t := teamOf(d, x)
	px, py := treeParent(t, x), treeParent(t, y)
	return px == y || py == x || px == "" && py == ""
}

// contacts are the active members the session may message, in team order.
func contacts(d *protocol.Domain, id string) []protocol.Member {
	t := teamOf(d, id)
	if t == nil {
		return nil
	}
	var out []protocol.Member
	for _, m := range t.Members {
		if m.State == "active" && adjacent(d, id, m.SessionID) {
			out = append(out, m)
		}
	}
	return out
}

// mayTalk: x may message y, and y may hear x.
func (p *Pool) mayTalk(x, y string) bool {
	if x == "" || y == "" || x == y {
		return false
	}
	d := p.st.State().Domain
	return adjacent(&d, x, y) || p.crossTeamOpen(x, y)
}

// sessionNamed finds a running session by its name (any case) or an id prefix of 8 or more.
func sessionNamed(st store.State, name string) (store.Session, bool) {
	var hit []store.Session
	for _, s := range st.Sessions {
		if s.State == "running" && (strings.EqualFold(s.Name, name) || s.ID == name || len(name) >= 8 && strings.HasPrefix(s.ID, name)) {
			hit = append(hit, s)
		}
	}
	if len(hit) != 1 {
		return store.Session{}, false
	}
	return hit[0], true
}

// sessionBySocket is the pool session listening on a Claude messaging socket.
func (p *Pool) sessionBySocket(sock string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ib := range p.msgSock {
		if ib.Socket != "" && ib.Socket == sock {
			return id
		}
	}
	return ""
}

var mentionRe = regexp.MustCompile(`(?:^|[\s(])@([A-Za-z0-9][\w.-]*)`)

// ownerMentions opens a channel for each running session the owner names with @ in a prompt they
// typed into session s. The caller has checked the prompt is the owner's.
func (p *Pool) ownerMentions(s store.Session, prompt string) {
	st := p.st.State()
	for _, m := range mentionRe.FindAllStringSubmatch(prompt, -1) {
		to, ok := sessionNamed(st, strings.TrimRight(m[1], ".-"))
		if !ok || to.ID == s.ID || p.mayTalk(s.ID, to.ID) && !p.crossTeamOpen(s.ID, to.ID) {
			continue // unknown, itself, or already a neighbour
		}
		p.grantCrossTeam(s.ID, to.ID)
		line := "team: the owner opened a channel between " + s.Name + " and " + to.Name + " for 2 hours"
		p.logLine(s.ID, line)
		p.logLine(to.ID, line)
	}
}

var fromSockRe = regexp.MustCompile(`\bfrom="uds:([^"]+)"`)

// outsideSender is the session a cross-session message came from, by its message socket; "" when
// it is not a PenguPool session's.
func (p *Pool) outsideSender(prompt string) string {
	if m := fromSockRe.FindStringSubmatch(prompt); m != nil {
		return p.sessionBySocket(m[1])
	}
	return ""
}

// blockOutside refuses a cross-session message from a session the receiver may not hear. It is
// dropped before the agent sees it (the prompt never enters its context) and the attempt is logged.
func (p *Pool) blockOutside(s store.Session, prompt string) (HookOut, bool) {
	from, _, ok := outsideMessage(prompt)
	if !ok {
		return HookOut{}, false
	}
	if p.mayTalk(p.outsideSender(prompt), s.ID) {
		return HookOut{}, false
	}
	p.logLine(s.ID, "team: blocked a message from "+from+" (not this session's parent or child; the owner can open a channel with @"+s.Name+")")
	return hookJSON(map[string]any{
		"decision": "block",
		"reason":   "PenguPool blocked a message from " + from + ": agents talk only to their parent and children on their team. To open a channel, type @" + s.Name + " in " + from + "'s prompt (or @" + from + " here).",
	}), true
}
