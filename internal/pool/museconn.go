package pool

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nduwork/pengupool-ai/internal/gateway"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// The Muse app's connection, seen from both ends: a device asks muse.status whether its token and
// address work; the owner's Settings asks muse.connection why a device does not connect.

// museRefusedMax bounds the refused-request log and the revoked hashes kept to explain it.
const museRefusedMax = 20

// museRefusal is one refused device-API request. Never a token, a caller's hash, a body or a URL.
type museRefusal struct {
	At     string `json:"at"`
	Reason string `json:"reason"` // wrong_host, unknown_token, revoked_token, bad_code or pairing_paused
	Host   string `json:"host,omitempty"`
	Device string `json:"device,omitempty"` // the device's name, when the token named one
}

// museRevokedHash is a token hash that was paired and has since been revoked or replaced.
type museRevokedHash struct{ hash, name string }

// NoteMuseRefused records a device-API request the gateway refused: reason is wrong_host or
// unknown_token; tokenHash (the SHA-256 of the token sent) only tells a revoked token from an
// unknown one and is not kept. The log lives in memory: a host restart clears it.
func (p *Pool) NoteMuseRefused(reason, host, tokenHash, device string) {
	if len(host) > 253 {
		host = host[:253]
	}
	p.museMu.Lock()
	if reason == "unknown_token" && tokenHash != "" {
		for _, r := range p.museRevoked {
			if r.hash == tokenHash {
				reason, device = "revoked_token", r.name
			}
		}
	}
	p.museMu.Unlock()
	p.noteMuseRefused(reason, host, device)
}

func (p *Pool) noteMuseRefused(reason, host, device string) {
	p.museMu.Lock()
	defer p.museMu.Unlock()
	p.museRefused = append(p.museRefused, museRefusal{At: now(), Reason: reason, Host: host, Device: device})
	if n := len(p.museRefused); n > museRefusedMax {
		p.museRefused = append([]museRefusal(nil), p.museRefused[n-museRefusedMax:]...)
	}
}

// noteRevokedLocked remembers a revoked or replaced token's hash (museMu held).
func (p *Pool) noteRevokedLocked(hash, name string) {
	if hash == "" {
		return
	}
	p.museRevoked = append(p.museRevoked, museRevokedHash{hash, name})
	if n := len(p.museRevoked); n > museRefusedMax {
		p.museRevoked = append([]museRevokedHash(nil), p.museRevoked[n-museRefusedMax:]...)
	}
}

// museStatusOut answers a device's connection check. The gateway adds the host's epoch.
type museStatusOut struct {
	Device      string `json:"device"`
	HostVersion string `json:"host_version"`
	Waiting     int    `json:"waiting"` // the asks this device may see
	PairedAt    string `json:"paired_at"`
}

func (p *Pool) museStatus(_ context.Context, c gateway.Caller, _ json.RawMessage) (any, error) {
	if c.Device == "" {
		return nil, protocol.Errorf(protocol.CodeUnauthorized, "no device")
	}
	out := museStatusOut{Device: c.Device, HostVersion: p.version}
	for _, d := range p.st.Config().MuseDevices {
		if d.Name == c.Device {
			out.PairedAt = firstNonEmpty(d.RepairedAt, d.CreatedAt)
		}
	}
	for _, a := range p.museAsks() {
		if p.museVisible(a.sessionID) && p.museCard(a) != nil {
			out.Waiting++
		}
	}
	return out, nil
}

// museServeView is Serve as a device reaches it: state ok or problem, the address a code carries,
// the first failing check, and the watchdog's last round.
type museServeView struct {
	State   string            `json:"state"`
	URL     string            `json:"url,omitempty"`
	Problem string            `json:"problem,omitempty"`
	Check   string            `json:"check,omitempty"` // the failing check's id
	Watch   *serveWatchResult `json:"watch,omitempty"`
}

// museConnection is the owner's connection panel: Serve, the Tailscale checklist (the pairing
// button's blocker), the paired devices, and the requests refused since the host started. Owner
// only (not in museAllowed).
func (p *Pool) museConnection(ctx context.Context, c gateway.Caller, raw json.RawMessage) (any, error) {
	checks := p.tailscaleChecks()
	serve := museServeView{State: "ok"}
	if p.st.Config().Network.Tailscale == "" {
		serve = museServeView{State: "problem", Problem: "Tailscale Serve isn't set up.", Check: "serve"}
	} else {
		serve.URL = p.museBaseURL()
		for _, ch := range checks {
			if !ch.OK {
				serve.State, serve.Problem, serve.Check = "problem", strings.TrimSpace(ch.Label+": "+ch.Detail), ch.ID
				break
			}
		}
	}
	p.serveWatchMu.Lock()
	if !p.serveWatch.At.IsZero() {
		w := p.serveWatch
		serve.Watch = &w
	}
	p.serveWatchMu.Unlock()
	devices, _ := p.museDevices(ctx, c, raw)
	p.museMu.Lock()
	refused := append([]museRefusal{}, p.museRefused...)
	p.museMu.Unlock()
	return map[string]any{"serve": serve, "checks": checks, "devices": devices, "refused": refused}, nil
}

// serveWatchResult is one round of the Serve watchdog.
type serveWatchResult struct {
	At         time.Time `json:"at"`
	Problem    string    `json:"problem,omitempty"`     // why the round stopped early: Tailscale isn't running
	Repointed  bool      `json:"repointed,omitempty"`   // a stale PenguPool route was pointed back at this host
	RenamedTo  string    `json:"renamed_to,omitempty"`  // the saved Serve name followed a tailnet rename
	HealFailed string    `json:"heal_failed,omitempty"` // the re-point's own error
}
