package main

import (
	"context"
	"fmt"

	"github.com/nduwork/pengupool-ai/internal/client"
	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// sessionRow is the CLI's view of a session; it is the protocol's own representation, so the CLI
// never invents a second schema.
type sessionRow = protocol.SessionView

// dialDaemon connects to a running daemon and completes the hello handshake.
func dialDaemon(ctx context.Context, dir string, kind protocol.ClientKind, name string) (*client.Conn, error) {
	c, err := client.Dial(ctx, hubSocket(dir), kind, name)
	if err != nil {
		return nil, fmt.Errorf("%w\nis the daemon running? start it with: pengupool daemon", err)
	}
	if _, err := c.Hello(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// listSessions asks the daemon for every session it knows.
func listSessions(ctx context.Context, c *client.Conn) ([]sessionRow, error) {
	reply, err := c.Call(ctx, &protocol.Message{Type: protocol.TypeSessionList})
	if err != nil {
		return nil, err
	}
	return reply.Sessions, nil
}

// resolveSession maps a user-typed id prefix or name to exactly one session.
func resolveSession(ctx context.Context, c *client.Conn, target string) (sessionRow, error) {
	if target == "" {
		return sessionRow{}, fmt.Errorf("no session given")
	}
	all, err := listSessions(ctx, c)
	if err != nil {
		return sessionRow{}, err
	}
	var matches []sessionRow
	for _, s := range all {
		if isPrefix(s, target) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return sessionRow{}, fmt.Errorf("no session matches %q", target)
	case 1:
		return matches[0], nil
	default:
		names := ""
		for _, m := range matches {
			names += "\n  " + shortID(m.ID) + "  " + m.Name
		}
		return sessionRow{}, fmt.Errorf("%q matches %d sessions:%s", target, len(matches), names)
	}
}
