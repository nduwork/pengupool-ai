package pool

import (
	"context"
	"strings"
	"testing"

	"github.com/nduwork/pengupool-ai/internal/protocol"
	"github.com/nduwork/pengupool-ai/internal/store"
)

// A proposed task whose repository was removed is refused on repropose, never a nil dereference
// that would crash the host.
func TestReproposeRemovedRepo(t *testing.T) {
	p := testPool(t)
	_ = p.st.Update(func(s *store.State) error {
		s.Domain.Tasks = append(s.Domain.Tasks, protocol.Task{ID: "task1", Title: "t", Objective: "o", RepoID: "gone", State: "proposed"})
		return nil
	})
	_, err := p.taskRepropose(context.Background(), idIn{ID: "task1"})
	if err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("repropose = %v, want a 'gone' error", err)
	}
}
