package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/session"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// TestPsFailureIsNotAForeignWorker is the identity rule that has no happy path to test: when the
// process table cannot be read, a pid is unknown — never "not ours". Nothing may be removed and
// nothing may be signalled on a look that failed, or a broken ps would cost a live worker its socket.
func TestPsFailureIsNotAForeignWorker(t *testing.T) {
	fakeDir := t.TempDir()
	// A ps that always fails, the way a broken or missing ps does.
	if err := os.WriteFile(filepath.Join(fakeDir, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write the broken ps: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	storeDir := t.TempDir()
	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	const id = "session-whose-process-table-cannot-be-read"
	handle := session.HandshakePath(storeDir, id)
	if err := session.WriteHandshake(handle, session.Handshake{
		SessionID: id, Pid: os.Getpid(), Socket: session.SocketPath(storeDir, id),
		Protocol: protocol.Version, Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}
	nodeID := st.NodeID()
	if err := st.Update(func(s *store.State) error {
		s.Sessions = append(s.Sessions, store.Session{
			ID: id, NodeID: nodeID, Name: "unreadable", Cwd: t.TempDir(), State: stateRunning,
			Pid: os.Getpid(), Cols: 80, Rows: 24, StartedAt: time.Now().UTC(),
		})
		return nil
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	h, err := New(Options{Store: st, Executable: "/bin/true"})
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	defer h.Close()

	if chk := h.checkWorker(os.Getpid(), id, time.Time{}); chk.status != procUnknown {
		t.Errorf("a failed ps was read as %q (%s), want unknown", chk.status, chk.detail)
	}
	if err := h.Kill(context.Background(), id); protocol.CodeOf(err) != protocol.CodeUnavailable {
		t.Errorf("Kill = %v, want unavailable: it must refuse rather than guess", err)
	}
	if got, ok := h.lookup(id); !ok || got.State != stateRunning {
		t.Errorf("the record was settled on an unreadable process table: %+v", got)
	}
	if _, err := os.Stat(handle); err != nil {
		t.Errorf("the handshake was removed without proof the worker is gone: %v", err)
	}
}
