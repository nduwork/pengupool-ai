package pool

import (
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
)

// Two sessions with one name started in the same minute get their own run log (and, the same way, their
// own worktree folder and branch): the names use the id's random tail, not its time-ordered head.
func TestIDTailNames(t *testing.T) {
	p := testPool(t)
	a, b := protocol.NewID(), protocol.NewID()
	if p.runLogPath(a, "web") == p.runLogPath(b, "web") {
		t.Fatal("two sessions share a run log")
	}
	if idTail("abc") != "abc" || idTail(a) != a[len(a)-8:] {
		t.Fatal("idTail")
	}
}
