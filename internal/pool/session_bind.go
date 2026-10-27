package pool

import (
	"encoding/json"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// descendsFrom reports whether pid is ancestor or one of its descendants. known is false when the
// process tree cannot be read (an unsupported OS, or the process is gone): the caller then cannot tell.
func descendsFrom(pid, ancestor int) (yes, known bool) {
	for i := 0; i < 64 && pid > 1; i++ {
		if pid == ancestor {
			return true, true
		}
		next := parentPID(pid)
		if next < 0 {
			return false, false
		}
		pid = next
	}
	return false, true
}

// bindSession keeps an agent to its own session. An agent's CLI call names its session in the
// request (session_id, or from for a message), and the unix socket is open to every process of the
// owner's account, so another agent could name a session that is not its own (the anchor's, a member
// of another team). The daemon reads the calling process from the socket and takes the request only
// when that process descends from the named session's worker. A caller that is not an agent (the
// owner's token, the phone, the terminal) is not checked; the phone gets only its own requests (RPC).
// A unix caller whose process cannot be found (it connected, forked and exited) is refused where the
// process tree can be read.
// ponytail: a session on another node (its worker's pid is not ours) is taken as before, and so is
// any request where the process tree cannot be read at all (an unsupported OS); a process an agent
// reparents away from its worker (setsid, double fork) is refused.
func (p *Pool) bindSession(c gateway.Caller, params json.RawMessage) error {
	if c.Owner || c.Phone || c.Terminal {
		return nil
	}
	if c.Unix && c.PeerPID <= 0 && procTreeReadable {
		return protocol.Errorf(protocol.CodeForbidden, "the calling process is not known")
	}
	if c.PeerPID <= 0 {
		return nil
	}
	var in struct {
		SessionID string `json:"session_id"`
		From      string `json:"from"`
	}
	if json.Unmarshal(params, &in) != nil {
		return nil
	}
	for _, id := range []string{in.SessionID, in.From} {
		if id == "" {
			continue
		}
		s, ok := p.session(id)
		if !ok || s.Pid <= 0 || !p.onThisHost(s.NodeID) {
			continue
		}
		yes, known := descendsFrom(c.PeerPID, s.Pid)
		if yes || !known && !procTreeReadable {
			continue
		}
		return protocol.Errorf(protocol.CodeForbidden, "this request does not come from session %s", id)
	}
	return nil
}

// onThisHost: the node is the local one (no node id counts as local).
func (p *Pool) onThisHost(nodeID string) bool {
	if nodeID == "" {
		return true
	}
	for _, n := range p.st.State().Nodes {
		if n.ID == nodeID {
			return n.Kind == "local"
		}
	}
	return false
}
