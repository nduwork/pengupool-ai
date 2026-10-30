package vt

import (
	"fmt"
	"strings"
	"testing"
)

// history is a screen's scrollback as plain text, oldest first.
func history(t *testing.T, s Screen) []string {
	t.Helper()
	sc := s.(*screen)
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var out []string
	for _, l := range sc.emu.Scrollback().Lines() {
		var b strings.Builder
		for _, c := range l {
			if c.Width > 0 {
				b.WriteString(cellText(&c))
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

func writeLines(s Screen, n int, line func(i int) string) {
	for i := range n {
		s.Write([]byte(line(i) + "\r\n"))
	}
}

// TestSnapshotHistoryReattach: a viewer that attaches after 50 lines gets the same scrollback and
// screen as the host, colours and all, and so does a viewer that attaches after it.
func TestSnapshotHistoryReattach(t *testing.T) {
	src := New(20, 5)
	writeLines(src, 50, func(i int) string { return fmt.Sprintf("\x1b[3%dmline %02d\x1b[0m", i%8, i) })
	want := history(t, src)
	if len(want) != 46 || want[0] != "line 00" {
		t.Fatalf("host history = %d lines, first %q", len(want), want[0])
	}
	for range 2 {
		dst := New(20, 5)
		dst.Write(src.Snapshot())
		if got := history(t, dst); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("viewer history:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if d := diffGrids(grid(t, src), grid(t, dst)); d != "" {
			t.Fatalf("screen after history: %s", d)
		}
		src = dst
	}
}

// TestSnapshotHistoryResize: a resize keeps the history, and the snapshot at the new size carries it.
func TestSnapshotHistoryResize(t *testing.T) {
	src := New(20, 5)
	writeLines(src, 30, func(i int) string { return fmt.Sprintf("line %02d", i) })
	want := history(t, src)
	src.Resize(30, 8)
	if got := history(t, src); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("history after resize: %q, want %q", got, want)
	}
	dst := New(30, 8)
	dst.Write(src.Snapshot())
	if got := history(t, dst); len(got) < len(want) || strings.Join(got[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("viewer history after resize: %q, want it to start with %q", got, want)
	}
}

// TestSnapshotHistoryAltScreen: history survives a snapshot taken while a program is on the
// alternate screen, and the viewer ends up on the alternate screen.
func TestSnapshotHistoryAltScreen(t *testing.T) {
	src := New(20, 5)
	writeLines(src, 10, func(i int) string { return fmt.Sprintf("line %02d", i) })
	src.Write([]byte("\x1b[?1049hfull-screen app"))
	dst := New(20, 5)
	dst.Write(src.Snapshot())
	if got := history(t, dst); len(got) != len(history(t, src)) || got[0] != "line 00" {
		t.Fatalf("viewer history = %q", got)
	}
	if !dst.Modes().AltScreen || !strings.Contains(dst.Text(), "full-screen app") {
		t.Fatalf("viewer not on the alternate screen: %q", dst.Text())
	}
}

// TestHistoryCaps: the host keeps HistoryLines lines (the memory cap), a snapshot carries at most
// maxSnapshotHistory bytes of them (the newest), and a follower screen keeps none.
func TestHistoryCaps(t *testing.T) {
	src := New(200, 5)
	n := HistoryLines + 500
	writeLines(src, n, func(i int) string { return fmt.Sprintf("%05d ", i) + strings.Repeat("\x1b[31mx\x1b[32my", 95) })
	h := history(t, src) // colour on every cell: ~1 KiB a line encoded, so the budget bites first
	if len(h) != HistoryLines || !strings.HasPrefix(h[len(h)-1], fmt.Sprintf("%05d", n-5)) {
		t.Fatalf("host keeps %d lines, last %.6q", len(h), h[len(h)-1])
	}
	snap := src.Snapshot()
	if len(snap) > maxSnapshotHistory+200*5*16 { // history plus a 5-row colour screen
		t.Fatalf("snapshot is %d bytes, past the history budget", len(snap))
	}
	dst := New(200, 5)
	dst.Write(snap)
	got := history(t, dst)
	if len(got) == 0 || len(got) >= HistoryLines || got[len(got)-1] != h[len(h)-1] {
		t.Fatalf("viewer got %d lines; want the newest that fit, ending with the host's last", len(got))
	}
	f := NewVisible(20, 5)
	writeLines(f, 50, func(i int) string { return "x" })
	if l := len(history(t, f)); l > 1 {
		t.Fatalf("a follower keeps %d history lines", l)
	}
}
