package pool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/harness"
	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// Session actions the map and the session list offer: rename, describe role, compact, fresh
// context, restart and resume, close, resume a previous conversation, and grouping by drag.

type renameIn struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

func (p *Pool) sessionRename(_ context.Context, in renameIn) (any, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, invalid("a name cannot be empty")
	}
	s, ok := p.session(in.SessionID)
	if !ok {
		return nil, notFound("no session %s", in.SessionID)
	}
	err := p.update(func(st *store.State) error {
		if i := sessionIndex(st, in.SessionID); i >= 0 {
			st.Sessions[i].Name = name
		}
		if t := teamOf(&st.Domain, in.SessionID); t != nil {
			for i := range t.Members {
				if t.Members[i].SessionID == in.SessionID {
					t.Members[i].Name = name
				}
			}
		}
		return nil
	})
	if err == nil && s.State == "running" && s.Harness == harness.Claude {
		// Claude keeps its own display name too; it is typed only while the agent is idle, so a
		// rename during a turn waits for the turn's end.
		if s.Status == "idle" {
			_ = p.typeInto(in.SessionID, "/rename "+name+"\r")
		} else {
			p.mu.Lock()
			if p.renames == nil {
				p.renames = map[string]string{}
			}
			p.renames[in.SessionID] = name
			p.mu.Unlock()
		}
	}
	return nil, err
}

// pendingRename types a rename that arrived during a turn, now that the session is idle.
func (p *Pool) pendingRename(id string) {
	p.mu.Lock()
	name, ok := p.renames[id]
	delete(p.renames, id)
	p.mu.Unlock()
	if ok {
		go func() { _ = p.typeInto(id, "/rename "+name+"\r") }() // the Stop hook returns first
	}
}

type teamRenameIn struct {
	TeamID string `json:"team_id"`
	Name   string `json:"name"`
}

// teamRename renames a group; members see the new name in their next team context.
func (p *Pool) teamRename(_ context.Context, in teamRenameIn) (any, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, invalid("a name cannot be empty")
	}
	return nil, p.update(func(st *store.State) error {
		t := findTeam(&st.Domain, in.TeamID)
		if t == nil {
			return notFound("no team %s", in.TeamID)
		}
		t.Name = name
		return nil
	})
}

type describeIn struct {
	SessionID string `json:"session_id"`
	Summary   string `json:"summary"`
}

// sessionDescribe records the session's role; it is shown on the map and injected into team context.
func (p *Pool) sessionDescribe(_ context.Context, in describeIn) (any, error) {
	return nil, p.update(func(st *store.State) error {
		i := sessionIndex(st, in.SessionID)
		if i < 0 {
			return notFound("no session %s", in.SessionID)
		}
		st.Sessions[i].Summary = strings.TrimSpace(in.Summary)
		if t := teamOf(&st.Domain, in.SessionID); t != nil {
			for k := range t.Members {
				if t.Members[k].SessionID == in.SessionID && in.Summary != "" {
					t.Members[k].Assignment = strings.TrimSpace(in.Summary)
				}
			}
		}
		return nil
	})
}

type slashIn struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"` // compact | clear
}

// sessionSlash runs a harness slash command the owner asked for (never a team message): the
// agent's own compact and fresh-context commands.
func (p *Pool) sessionSlash(_ context.Context, in slashIn) (any, error) {
	s, ok := p.session(in.SessionID)
	if !ok || s.State != "running" {
		return nil, conflict("the session is not running")
	}
	a, _ := harness.Find(s.Harness)
	c := map[string]string{"compact": a.Compact, "clear": a.Clear}[in.Command]
	if c == "" {
		return nil, invalid("%s has no %s command", firstNonEmpty(s.Harness, "this session"), in.Command)
	}
	if s.Status == "working" {
		return nil, conflict("%s is working; wait until it is idle", s.Name)
	}
	if err := p.typeInto(in.SessionID, c+"\r"); err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "%v", err)
	}
	p.logLine(in.SessionID, "owner: "+c)
	return nil, nil
}

// typeInto writes owner-initiated bytes to a session's PTY through the host, without the lease.
func (p *Pool) typeInto(id, text string) error {
	if p.testKeys != nil {
		return p.testKeys(id, text)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, filepath.Join(p.st.Dir(), store.RunDir, "hub.sock"), protocol.KindCLI, "pengupool action")
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Call(ctx, c.HelloMessage()); err != nil {
		return err
	}
	att := &protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionAttach, SessionID: id, Channel: 7, Kind: string(protocol.KindCLI)}
	if err := c.Send(att); err != nil {
		return err
	}
	for {
		f, err := c.Next(ctx)
		if err != nil {
			return err
		}
		if f.Header.Type != protocol.FrameControl {
			continue
		}
		m, err := protocol.DecodeControl(f)
		if err != nil {
			return err
		}
		if m.Type == protocol.TypeError {
			return errors.New(m.Message)
		}
		if m.ID == att.ID && m.Type == protocol.TypeSessionAttached {
			break
		}
	}
	// Claude's TUI takes a pasted command and its Enter as one paste; send Enter separately.
	body := strings.TrimSuffix(text, "\r")
	for _, chunk := range []string{body, "\r"} {
		if chunk == "" {
			continue
		}
		if err := c.WriteFrame(protocol.Frame{Header: protocol.Header{Type: protocol.FrameInput, Channel: 7, Length: uint32(len(chunk))}, Payload: []byte(chunk)}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = c.Send(&protocol.Message{ID: protocol.NewID(), Type: protocol.TypeSessionDetach, SessionID: id, Channel: 7})
	return nil
}

// sessionRestart stops the agent and resumes its conversation under the same session id.
func (p *Pool) sessionRestart(ctx context.Context, in sessionIn) (any, error) {
	s, ok := p.session(in.SessionID)
	if !ok {
		return nil, notFound("no session %s", in.SessionID)
	}
	if s.State == "running" {
		if err := p.hub.Kill(ctx, s.ID); err != nil {
			return nil, err
		}
		for i := 0; i < 50; i++ {
			if cur, _ := p.session(s.ID); cur.State != "running" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return p.resume(ctx, in)
}

// sessionClose stops a session and takes it off the map. A team helper in its own worktree simply
// closes: it leaves the team, and its worktree goes when that loses nothing, else stays on disk. A
// team's main worker leaves the team first.
func (p *Pool) sessionClose(ctx context.Context, in sessionIn) (any, error) {
	d := p.st.State().Domain
	if t := teamOf(&d, in.SessionID); t != nil {
		for _, m := range t.Members {
			if m.SessionID == in.SessionID && m.State == "active" && (m.Worktree != "" || m.SkillID != "") && m.Role != "main" {
				return p.closeHelperRPC(ctx, closeIn{TeamID: t.ID, SessionID: m.SessionID, KeepWorktree: true, Reason: "closed on the map"})
			}
		}
		if _, err := p.teamGroup(ctx, groupIn{SessionID: in.SessionID, alone: true}); err != nil {
			return nil, err
		}
	}
	if s, ok := p.session(in.SessionID); ok && s.State == "running" {
		if err := p.hub.Kill(ctx, in.SessionID); err != nil {
			return nil, err
		}
	}
	return p.forget(ctx, in)
}

type groupIn struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id,omitempty"` // empty: the top level
	// alone takes only this session off its team (a close): its children move up to its parent, and
	// a main worker's team ends.
	alone bool
	// Ungroup takes the session off its team, which goes on without it: the session stays running,
	// ungrouped, and its children move up to its parent.
	Ungroup bool `json:"ungroup,omitempty"`
}

// teamGroup is the map's drag: put a session under another, or on the empty canvas. The session
// moves with everything under it, in the same shape: under a session of another team (or of none,
// which forms one) the subtree joins there, a main worker bringing its whole team; on the canvas a
// subtree becomes a team of its own, rooted at the moved session. One harness per team is not
// required; no loops. The owner's drag is the approval of the formation change; it is recorded, and
// the roster updated, like any other.
//
// The rules left are the ones that keep the roster whole: a session cannot join itself or its own
// descendant (no cycles), only agent sessions group (a plain terminal cannot receive team messages),
// and the main worker is the team's one root (move its team onto another session instead). A refused
// move says why, to the owner and in the daemon's log.
func (p *Pool) teamGroup(ctx context.Context, in groupIn) (any, error) {
	moved, from, to, err := p.regroup(ctx, in)
	if err != nil {
		p.logf("pool: move %s refused: %v", in.SessionID, err)
		return nil, err
	}
	if from == to {
		return nil, nil
	}
	for _, id := range moved {
		s, _ := p.session(id)
		if from != "" {
			p.recordFormation(from, protocol.FormationChange{Op: "remove", SessionID: id, Reason: "regrouped on the map"}, "remove "+s.Name)
		}
		if to != "" {
			p.recordFormation(to, protocol.FormationChange{Op: "add", SessionID: id}, "add "+s.Name)
		}
	}
	return nil, nil
}

// regroup changes the state for teamGroup: the sessions that changed team, from which team to which.
func (p *Pool) regroup(_ context.Context, in groupIn) (moved []string, from, to string, err error) {
	lan := p.st.Config().Network.LAN
	err = p.update(func(st *store.State) error {
		si := sessionIndex(st, in.SessionID)
		if si < 0 {
			return notFound("no session %s", in.SessionID)
		}
		src := &st.Sessions[si]
		if in.ParentID == in.SessionID {
			return invalid("a session cannot be grouped under itself")
		}
		cur := teamOf(&st.Domain, in.SessionID)
		if cur != nil {
			from = cur.ID
		}
		if in.alone {
			if cur != nil {
				leaveAlone(st, cur, in.SessionID)
			}
			return nil
		}
		if in.Ungroup {
			switch {
			case cur == nil:
				to = from
			case teamRole(cur, in.SessionID) == "main":
				return invalid("%s is the main worker of %s, the team's root: move the team onto another session, or close it", src.Name, cur.Name)
			default:
				to = ""
				leaveKeeping(st, cur, in.SessionID)
				moved = []string{in.SessionID}
			}
			return nil
		}
		var target *protocol.Team
		if in.ParentID != "" {
			pi := sessionIndex(st, in.ParentID)
			if pi < 0 {
				return notFound("no session %s", in.ParentID)
			}
			parent := &st.Sessions[pi]
			// Any two agents may share a team: messages go through PenguPool, which delivers each in
			// the recipient's own way. A plain terminal cannot take part.
			if !harness.IsAgent(src.Harness) || !harness.IsAgent(parent.Harness) {
				return invalid("only agent sessions group (%s, %s)", firstNonEmpty(src.Harness, "shell"), firstNonEmpty(parent.Harness, "shell"))
			}
			target = teamOf(&st.Domain, in.ParentID)
			if cur != nil && target != nil && cur.ID == target.ID {
				// A move inside the team: its subtree comes along by its parent links; refuse a loop.
				to = cur.ID
				for p, n := in.ParentID, 0; p != "" && n <= len(cur.Members); p, n = effectiveParent(cur, p), n+1 {
					if p == in.SessionID {
						return invalid("that would put %s under its own descendant", src.Name)
					}
				}
				for i := range cur.Members {
					if cur.Members[i].SessionID == in.SessionID && cur.Members[i].State == "active" {
						if cur.Members[i].Role == "main" {
							return invalid("the main worker is the team's root; move the team onto a session of another team instead")
						}
						cur.Members[i].Parent = in.ParentID
					}
				}
				return nil
			}
		}
		// The subtree that moves, parents first; a session on no team moves alone.
		sub := []protocol.Member{{SessionID: src.ID, Name: src.Name, State: "active", Assignment: src.Summary}}
		// A session that left a team (or whose team ended) still works in its worktree: it brings the
		// worktree back from the closed record that kept it.
		old, hadWorktree := closedWorktree(st, src.ID)
		if cur == nil && hadWorktree {
			sub[0].SkillID, sub[0].RepoID, sub[0].Worktree, sub[0].Branch, sub[0].BaseCommit = old.SkillID, old.RepoID, old.Worktree, old.Branch, old.BaseCommit
		}
		if cur != nil {
			sub = subtree(cur, in.SessionID)
			if len(sub) == 0 {
				return notFound("%s is not active on its team", src.Name)
			}
		}
		if in.ParentID == "" {
			switch {
			case cur == nil, sub[0].Role == "main":
				to = from
				return nil // already a root
			}
		}
		// Take the subtree off its team; what is left ends when it is one member or none.
		if cur != nil {
			in := map[string]bool{}
			for _, m := range sub {
				in[m.SessionID] = true
			}
			out := cur.Members[:0]
			for _, m := range cur.Members {
				if !in[m.SessionID] || m.State != "active" {
					out = append(out, m)
				}
			}
			cur.Members = out
			if cur.AnchorID != "" && in[cur.AnchorID] {
				cur.AnchorID = ""
			}
			if activeMembers(cur) <= 1 {
				dissolve(st, cur)
			}
		}
		// A new team only now: appending to the teams may move them, and cur with them.
		if in.ParentID == "" {
			target = newTeam(st, &st.Sessions[si], lan)
			// The new team's root keeps its worktree, branch and repository: it stays a worktree session.
			r := &target.Members[0]
			r.SkillID, r.RepoID, r.Worktree, r.Branch, r.BaseCommit = sub[0].SkillID, sub[0].RepoID, sub[0].Worktree, sub[0].Branch, sub[0].BaseCommit
			if r.Worktree != "" && r.RepoID == "" && cur != nil {
				r.RepoID = cur.RepoID
			}
			// The team works in the repository its root's worktree is in (placeHelper and the one-team-
			// per-repository check read the team's).
			if target.RepoID == "" {
				target.RepoID = r.RepoID
			}
			sub = sub[1:]
		} else {
			if target = teamOf(&st.Domain, in.ParentID); target == nil {
				target = newTeam(st, &st.Sessions[sessionIndex(st, in.ParentID)], lan)
			}
			sub[0].Parent = in.ParentID
		}
		to = target.ID
		if cur == nil && hadWorktree {
			dropClosed(st, src.ID) // the live record carries the worktree now
		}
		for _, m := range sub {
			if m.Worktree != "" && m.RepoID == "" && cur != nil {
				m.RepoID = cur.RepoID // the worktree stays in the repository it was made in
			}
			m.Role, m.Coordinator, m.State = "helper", false, "active"
			if m.RepoID != "" && m.RepoID != target.RepoID && !slices.Contains(target.Repos, m.RepoID) {
				target.Repos = append(target.Repos, m.RepoID) // the team's context names every repository it works in
			}
			target.Members = append(target.Members, m)
			if j := sessionIndex(st, m.SessionID); j >= 0 {
				st.Sessions[j].TeamID, st.Sessions[j].Role = target.ID, "helper"
			}
		}
		moved = append(moved, src.ID)
		for _, m := range sub {
			if m.SessionID != src.ID {
				moved = append(moved, m.SessionID)
			}
		}
		return nil
	})
	return moved, from, to, err
}

// newTeam forms a team rooted at a session, which becomes its main worker.
func newTeam(st *store.State, root *store.Session, lan bool) *protocol.Team {
	st.Domain.Teams = append(st.Domain.Teams, protocol.Team{
		ID: protocol.NewID(), Name: root.Name, Harness: root.Harness, State: "active", IOSAccess: !lan, CreatedAt: now(),
		Members: []protocol.Member{{SessionID: root.ID, Name: root.Name, Role: "main", Coordinator: true, State: "active", Assignment: root.Summary}},
	})
	t := &st.Domain.Teams[len(st.Domain.Teams)-1]
	for _, r := range st.Domain.Repos {
		if r.Path == root.Cwd {
			t.RepoID = r.ID
		}
	}
	root.TeamID, root.Role = t.ID, "main"
	return t
}

// subtree is a member and every active member under it, parents first, each with its parent named
// (a helper with no parent hangs under the main worker).
func subtree(t *protocol.Team, id string) []protocol.Member {
	var out []protocol.Member
	for _, m := range t.Members {
		if m.SessionID == id && m.State == "active" {
			out = append(out, m)
		}
	}
	seen := map[string]bool{id: true}
	for i := 0; i < len(out); i++ {
		for _, m := range t.Members {
			if m.State == "active" && !seen[m.SessionID] && effectiveParent(t, m.SessionID) == out[i].SessionID {
				seen[m.SessionID] = true
				m.Parent = out[i].SessionID
				out = append(out, m)
			}
		}
	}
	return out
}

// effectiveParent is a member's parent on the tree: its own while that is an active member, else the
// main worker's (a helper with none, or whose parent was closed, hangs there on the map).
func effectiveParent(t *protocol.Team, id string) string {
	active := func(id string) bool {
		return slices.ContainsFunc(t.Members, func(m protocol.Member) bool { return m.SessionID == id && m.State == "active" })
	}
	for _, m := range t.Members {
		if m.SessionID == id && m.State == "active" {
			if m.Role == "main" {
				return ""
			}
			if m.Parent != "" && active(m.Parent) {
				return m.Parent
			}
		}
	}
	for _, m := range t.Members {
		if m.Role == "main" && m.State == "active" && m.SessionID != id {
			return m.SessionID
		}
	}
	return ""
}

// leaveAlone takes one session off its team: its children move up to its parent, and a main worker's
// team ends.
func leaveAlone(st *store.State, t *protocol.Team, id string) {
	var me protocol.Member
	for _, m := range t.Members {
		if m.SessionID == id && m.State == "active" {
			me = m
		}
	}
	if me.Role == "main" {
		dissolve(st, t)
		return
	}
	out := t.Members[:0]
	for _, m := range t.Members {
		if m.SessionID == id {
			continue
		}
		if m.Parent == id {
			m.Parent = me.Parent
		}
		out = append(out, m)
	}
	t.Members = out
	if j := sessionIndex(st, id); j >= 0 {
		st.Sessions[j].TeamID, st.Sessions[j].Role = "", ""
	}
	if activeMembers(t) <= 1 {
		dissolve(st, t)
	}
}

// closedWorktree is the worktree record kept for a session that is on no team: its latest closed
// member record that has one (left through leaveKeeping, or closed when its team ended).
func closedWorktree(st *store.State, id string) (protocol.Member, bool) {
	for i := len(st.Domain.Teams) - 1; i >= 0; i-- {
		ms := st.Domain.Teams[i].Members
		for j := len(ms) - 1; j >= 0; j-- {
			if ms[j].SessionID == id && ms[j].State != "active" && ms[j].Worktree != "" {
				return ms[j], true
			}
		}
	}
	return protocol.Member{}, false
}

// dropClosed removes the closed worktree records of a session that has joined a team again, so a
// team never holds two records of one session.
func dropClosed(st *store.State, id string) {
	for i := range st.Domain.Teams {
		out := st.Domain.Teams[i].Members[:0]
		for _, m := range st.Domain.Teams[i].Members {
			if !(m.SessionID == id && m.State != "active" && m.Worktree != "") {
				out = append(out, m)
			}
		}
		st.Domain.Teams[i].Members = out
	}
}

// teamRole is a member's role on the team, "" when it is not an active member.
func teamRole(t *protocol.Team, id string) string {
	for _, m := range t.Members {
		if m.SessionID == id && m.State == "active" {
			return m.Role
		}
	}
	return ""
}

// leaveKeeping takes a non-main member off its team like leaveAlone, but a worktree session stays
// on the roster as closed, so the worktree and branch it was working in are still on the record.
func leaveKeeping(st *store.State, t *protocol.Team, id string) {
	var keep protocol.Member
	for _, m := range t.Members {
		if m.SessionID == id && m.State == "active" && m.Worktree != "" {
			keep = m
		}
	}
	leaveAlone(st, t, id)
	if keep.SessionID != "" {
		keep.State, keep.Parent = "closed", ""
		t.Members = append(t.Members, keep)
	}
}

func activeMembers(t *protocol.Team) int {
	n := 0
	for _, m := range t.Members {
		if m.State == "active" {
			n++
		}
	}
	return n
}

func parentOf(t *protocol.Team, id string) string {
	for _, m := range t.Members {
		if m.SessionID == id {
			return m.Parent
		}
	}
	return ""
}

// dissolve ends a team: every member is closed and off the team. A worktree member's closed record
// keeps its worktree, branch and repository, so the session brings them along when it joins a team again.
func dissolve(st *store.State, t *protocol.Team) {
	t.State = "closed"
	for i := range t.Members {
		t.Members[i].State = "closed"
		if j := sessionIndex(st, t.Members[i].SessionID); j >= 0 {
			st.Sessions[j].TeamID, st.Sessions[j].Role = "", ""
		}
	}
}

// PreviousSession is a past harness conversation that can be resumed as a session.
type PreviousSession struct {
	Harness    string `json:"harness"`
	ID         string `json:"id"`         // conversation id
	Transcript string `json:"transcript"` // file
	Cwd        string `json:"cwd"`
	Title      string `json:"title"`
	Modified   string `json:"modified"`
	Live       bool   `json:"live"` // already a running session here
}

type previousIn struct {
	Cwd   string `json:"cwd,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// sessionPrevious lists resumable Claude and OMP conversations, newest first.
func (p *Pool) sessionPrevious(_ context.Context, in previousIn) (any, error) {
	home, _ := os.UserHomeDir()
	live := map[string]bool{}
	for _, s := range p.st.State().Sessions {
		if s.State == "running" && s.ConvID != "" {
			live[s.ConvID] = true
		}
	}
	type file struct {
		harness, path string
		mod           time.Time
	}
	var files []file
	collect := func(h, root string) {
		_ = filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() {
				if strings.Count(strings.TrimPrefix(path, root), string(os.PathSeparator)) > 1 {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".jsonl") {
				if info, err := e.Info(); err == nil && info.Size() > 0 {
					files = append(files, file{h, path, info.ModTime()})
				}
			}
			return nil
		})
	}
	collect(harness.Claude, filepath.Join(home, ".claude", "projects"))
	collect(harness.OMP, filepath.Join(home, ".omp", "agent", "sessions"))
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	var out []PreviousSession
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		ps := readTranscriptHead(f.harness, f.path)
		if ps.ID == "" || (in.Cwd != "" && ps.Cwd != in.Cwd) {
			continue
		}
		ps.Modified = store.RFC3339(f.mod)
		ps.Live = live[ps.ID]
		out = append(out, ps)
	}
	return out, nil
}

// readTranscriptHead takes the conversation id, cwd and first user prompt from a transcript.
func readTranscriptHead(h, path string) PreviousSession {
	ps := PreviousSession{Harness: h, Transcript: path}
	f, err := os.Open(path)
	if err != nil {
		return ps
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for n := 0; sc.Scan() && n < 200; n++ {
		var rec struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			ID        string `json:"id"`
			Cwd       string `json:"cwd"`
			IsMeta    bool   `json:"isMeta"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if ps.ID == "" {
			if h == harness.Claude && rec.SessionID != "" {
				ps.ID = rec.SessionID
			}
			if h == harness.OMP && rec.Type == "session" && rec.ID != "" {
				ps.ID = rec.ID
			}
		}
		if ps.Cwd == "" && rec.Cwd != "" {
			ps.Cwd = rec.Cwd
		}
		if ps.Title == "" && rec.Message.Role == "user" && !rec.IsMeta {
			t := strings.TrimSpace(contentText(rec.Message.Content))
			if t != "" && !strings.HasPrefix(t, "<") {
				if len(t) > 120 {
					t = t[:120] + "…"
				}
				ps.Title = t
			}
		}
		if ps.ID != "" && ps.Cwd != "" && ps.Title != "" {
			break
		}
	}
	if h == harness.OMP && ps.ID == "" {
		ps.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	return ps
}

type adoptIn struct {
	Harness    string `json:"harness"`
	ID         string `json:"id"`
	Transcript string `json:"transcript,omitempty"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
}

// sessionAdopt resumes a previous conversation as a new session (optionally under a parent).
func (p *Pool) sessionAdopt(ctx context.Context, in adoptIn) (any, error) {
	if in.Harness != harness.Claude && in.Harness != harness.OMP {
		return nil, invalid("resume a Claude or OMP conversation")
	}
	for _, s := range p.st.State().Sessions {
		if s.State == "running" && s.ConvID == in.ID {
			return nil, conflict("%s is already running as %s", in.ID, s.Name)
		}
	}
	repo, err := p.resolveSource(ctx, sourceIn{Kind: "local", Path: in.Cwd})
	if err != nil {
		return nil, err
	}
	resume := in.ID
	if in.Harness == harness.OMP && in.Transcript != "" {
		resume = in.Transcript
	}
	name := firstNonEmpty(strings.TrimSpace(in.Name), p.uniqueName(repo.Name+"-"+in.Harness))
	s, err := p.spawn(ctx, spawnReq{Name: name, Harness: in.Harness, Cwd: repo.Path, Resume: resume,
		Decorate: func(x *store.Session) { x.ConvID, x.Transcript = in.ID, in.Transcript }})
	if err != nil {
		return nil, err
	}
	if in.ParentID != "" {
		if _, err := p.teamGroup(ctx, groupIn{SessionID: s.ID, ParentID: in.ParentID}); err != nil {
			p.logf("pool: adopt %s under %s: %v", s.ID, in.ParentID, err)
		}
	}
	return s.View(), nil
}

// uniqueName returns base, or base-2, base-3… when another session already has it.
func (p *Pool) uniqueName(base string) string {
	taken := map[string]bool{}
	for _, s := range p.st.State().Sessions {
		taken[s.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		n := base + "-" + itoa(i)
		if !taken[n] {
			return n
		}
	}
}

func itoa(i int) string {
	b := []byte{}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Worktree is one `git worktree` of a repository.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Main   bool   `json:"main"`
}

type pathIn struct {
	Path string `json:"path"`
}

// repoWorktrees lists a repository's worktrees for the Agent pool drawer.
func (p *Pool) repoWorktrees(ctx context.Context, in pathIn) (any, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", in.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return []Worktree{}, nil
	}
	var wts []Worktree
	var cur Worktree
	for _, line := range strings.Split(string(out)+"\n", "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "":
			if cur.Path != "" {
				cur.Main = len(wts) == 0
				wts = append(wts, cur)
				cur = Worktree{}
			}
		}
	}
	return wts, nil
}

// repoPull fast-forwards a repository; it never merges or rewrites.
func (p *Pool) repoPull(ctx context.Context, in pathIn) (any, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", in.Path, "pull", "--ff-only")
	cmd.Env = append(append([]string(nil), p.env...), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeUnavailable, "git pull --ff-only: %s", strings.TrimSpace(string(out)))
	}
	return map[string]string{"output": strings.TrimSpace(string(out))}, nil
}

type worktreeRmIn struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// worktreeRemove removes a worktree only when nothing would be lost and no session runs there.
func (p *Pool) worktreeRemove(_ context.Context, in worktreeRmIn) (any, error) {
	for _, s := range p.st.State().Sessions {
		if s.State == "running" && (s.Cwd == in.Path || strings.HasPrefix(s.Cwd, in.Path+"/")) {
			return nil, conflict("%s is running in that worktree", s.Name)
		}
	}
	main, _ := exec.Command("git", "-C", in.Repo, "rev-parse", "HEAD").Output()
	ok, why := worktreeState(in.Repo, in.Path, "", strings.TrimSpace(string(main)))
	if !ok {
		return nil, conflict("the worktree has %s; integrate or preserve that work first", why)
	}
	return map[string]string{"removed": in.Path}, removeWorktree(in.Repo, in.Path, "", false)
}
