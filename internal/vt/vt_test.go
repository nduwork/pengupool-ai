package vt

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

const (
	testCols = 40
	testRows = 12
)

// recording is a byte stream shaped like a real session's output: SGR colour runs, absolute and
// relative cursor movement, erases, wide CJK, a combining mark, a hyperlink, a row that fills the
// screen exactly, a pile of input modes and an alternate-screen excursion.
func recording(leaveAltScreen bool) string {
	var b strings.Builder
	b.WriteString("\x1b[?25l")
	b.WriteString("\x1b[1;31mred bold\x1b[0m \x1b[32mgreen\x1b[0m \x1b[38;5;208morange\x1b[0m \x1b[48;2;10;20;30mon navy\x1b[0m\r\n")
	b.WriteString("plain second line\r\n")
	b.WriteString("\x1b[3;5H\x1b[4munderlined\x1b[0m")
	b.WriteString("\x1b[2;1Hsecond line rewritten\x1b[K")
	b.WriteString("\x1b[4;1H日本語 wide CJK")
	b.WriteString("\x1b[5;1Hcafe\u0301 combining")
	b.WriteString("\x1b[6;1H\x1b]8;;https://example.com\x07a link\x1b]8;;\x07")
	b.WriteString("\x1b[7;1H\x1b[7mreverse video\x1b[27m\x1b[42m\x1b[K") // erase with a non-default background
	b.WriteString("\x1b[8;1H" + strings.Repeat("F", testCols))           // exactly one full row
	b.WriteString("\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h\x1b[?1005h\x1b[?1004h\x1b[?2004h")
	b.WriteString("\x1b[?1h\x1b[>5u\x1b[?1000l\x1b[?2026h")
	b.WriteString("\x1b[?1049h\x1b[2;2Halternate screen\x1b[3;1H\x1b[44malt background\x1b[0m")
	if leaveAltScreen {
		b.WriteString("\x1b[?1049l")
	}
	b.WriteString("\x1b[10;3Hstatus line")
	b.WriteString("\x1b[?25h")
	// The keyboard and scrolling state that has no home in x/vt's own mode map: XTMODKEYS, the
	// kitty flags and a scroll region that is not the whole screen.
	b.WriteString("\x1b[>1;2m\x1b[=5u\x1b[2;8r")
	return b.String()
}

// TestSnapshotRoundTrip feeds a recording to one screen, snapshots it, replays the snapshot into a
// fresh screen of the same size, and demands that the new screen shows the same cells, the same
// cursor and the same modes.
func TestSnapshotRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		alt    bool
		prefix string
	}{
		{name: "normal screen", alt: false, prefix: "\x1b[?1049l"},
		{name: "alternate screen", alt: true, prefix: "\x1b[?1049h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := New(testCols, testRows)
			if _, err := src.Write([]byte(recording(!tc.alt))); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := src.Modes().AltScreen; got != tc.alt {
				t.Fatalf("source AltScreen = %v, want %v", got, tc.alt)
			}

			snap := src.Snapshot()
			if len(snap) == 0 {
				t.Fatal("Snapshot returned nothing")
			}
			if !strings.HasPrefix(string(snap), tc.prefix) {
				t.Fatalf("snapshot starts with %q, want the prefix %q", head(snap, 16), tc.prefix)
			}
			// A snapshot must never clear the display: on the normal screen that would throw away
			// the client's scrollback.
			if i := bytes.Index(snap, []byte("\x1b[2J")); i >= 0 {
				t.Fatalf("snapshot clears the display at byte %d: %q", i, snap)
			}
			if i := bytes.Index(snap, []byte("\x1b[3J")); i >= 0 {
				t.Fatalf("snapshot clears the scrollback at byte %d: %q", i, snap)
			}

			dst := New(testCols, testRows)
			if _, err := dst.Write(snap); err != nil {
				t.Fatalf("replaying the snapshot: %v", err)
			}

			if d := diffGrids(grid(t, src), grid(t, dst)); d != "" {
				t.Fatalf("snapshot did not reproduce the screen: %s\nsnapshot: %q", d, snap)
			}
			if want, got := cursorOf(src), cursorOf(dst); want != got {
				t.Fatalf("cursor = %v, want %v", got, want)
			}
			if want, got := src.Modes(), dst.Modes(); want != got {
				t.Fatalf("modes = %+v, want %+v", got, want)
			}

			// Applying the same snapshot twice must change nothing: a client that resynchronizes
			// while a snapshot is already on screen stays put.
			dst.Write(snap)
			if d := diffGrids(grid(t, src), grid(t, dst)); d != "" {
				t.Fatalf("replaying the snapshot twice changed the screen: %s", d)
			}
		})
	}
}

// TestSnapshotOverwritesStaleClient applies a snapshot on top of unrelated content, the state a
// client is in when it reconnects to a session it has been showing something else for.
func TestSnapshotOverwritesStaleClient(t *testing.T) {
	src := New(testCols, testRows)
	src.Write([]byte(recording(true)))

	dst := New(testCols, testRows)
	var stale strings.Builder
	for i := range testRows {
		stale.WriteString(fmt.Sprintf("\x1b[%d;1Hstale line %d left over from before", i+1, i))
	}
	stale.WriteString("\x1b[6;20H広い文字")
	dst.Write([]byte(stale.String()))

	if _, err := dst.Write(src.Snapshot()); err != nil {
		t.Fatalf("replaying the snapshot: %v", err)
	}
	if d := diffGrids(grid(t, src), grid(t, dst)); d != "" {
		t.Fatalf("snapshot did not overwrite the stale screen: %s", d)
	}
	if want, got := cursorOf(src), cursorOf(dst); want != got {
		t.Fatalf("cursor = %v, want %v", got, want)
	}
}

// TestSnapshotShape pins the layout of a snapshot: one cursor-position and one erase per row, no
// newline is ever written (a newline at the bottom of the screen would scroll), and the cursor is
// put back afterwards.
func TestSnapshotShape(t *testing.T) {
	s := New(20, 3)
	s.Write([]byte("hi"))
	snap := s.Snapshot()

	if got := bytes.Count(snap, []byte("\x1b[K")); got != 3 {
		t.Fatalf("snapshot erases %d rows, want one erase per row (3): %q", got, snap)
	}
	for i, want := range []string{"\x1b[1;1H", "\x1b[2;1H", "\x1b[3;1H"} {
		if !bytes.Contains(snap, []byte(want)) {
			t.Fatalf("snapshot has no cursor position for row %d (%q): %q", i, want, snap)
		}
	}
	if bytes.ContainsAny(snap, "\r\n") {
		t.Fatalf("snapshot contains a line break: %q", snap)
	}
	if !bytes.HasSuffix(snap, []byte("\x1b[1;3H\x1b[?25h")) {
		t.Fatalf("snapshot does not end with the cursor position and visibility: %q", snap)
	}
}

func TestNewClampsSize(t *testing.T) {
	s := New(0, 0)
	if s.Cols() != 1 || s.Rows() != 1 {
		t.Fatalf("New(0, 0) is %dx%d, want 1x1", s.Cols(), s.Rows())
	}
	s.Resize(-4, -7)
	if s.Cols() != 1 || s.Rows() != 1 {
		t.Fatalf("Resize(-4, -7) left %dx%d, want 1x1", s.Cols(), s.Rows())
	}
}

func TestResize(t *testing.T) {
	s := New(20, 5)
	s.Write([]byte("\x1b[5;1Hbottom"))
	s.Resize(30, 10)
	if s.Cols() != 30 || s.Rows() != 10 {
		t.Fatalf("size = %dx%d, want 30x10", s.Cols(), s.Rows())
	}
	dst := New(30, 10)
	dst.Write(s.Snapshot())
	if d := diffGrids(grid(t, s), grid(t, dst)); d != "" {
		t.Fatalf("snapshot after resize: %s", d)
	}
}

// TestSnapshotTinyScreen covers the degenerate one-cell screen, where writing wraps and scrolls
// with no room to spare.
func TestSnapshotTinyScreen(t *testing.T) {
	src := New(1, 1)
	src.Write([]byte("ab"))
	dst := New(1, 1)
	dst.Write(src.Snapshot())
	if d := diffGrids(grid(t, src), grid(t, dst)); d != "" {
		t.Fatalf("one-cell screen: %s", d)
	}
	if got := grid(t, dst)[0][0].Content; got != "b" {
		t.Fatalf("cell (0,0) = %q, want \"b\"", got)
	}
	if want, got := cursorOf(src), cursorOf(dst); want != got {
		t.Fatalf("cursor = %v, want %v", got, want)
	}
}

// TestConcurrentUse exercises Write, Snapshot, Resize, Modes and the response channel from several
// goroutines at once; it is the -race guard for the whole package.
func TestConcurrentUse(t *testing.T) {
	s := New(80, 24)
	chunk := []byte("\x1b[31mred\x1b[0m wide 日本 e\u0301 \x1b[2;3Hmoved\r\n\x1b[?1049h alt\x1b[?1049l\x1b[?1000h\x1b[?1000l\x1b[c\x1b[?1000$p")

	var writers, others sync.WaitGroup
	stop := make(chan struct{})
	// The chunk provokes two query replies per write, and the emulator blocks inside Write until
	// they are consumed: the responses channel has to be drained for the writers to finish.
	others.Add(1)
	go func() {
		defer others.Done()
		for {
			select {
			case <-s.Responses():
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		writers.Wait()
		close(stop)
		others.Wait()
	}()

	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 200 {
				if _, err := s.Write(chunk); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	writers.Add(1)
	go func() {
		defer writers.Done()
		for range 200 {
			if len(s.Snapshot()) == 0 {
				t.Error("Snapshot returned nothing")
				return
			}
		}
	}()
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := range 200 {
			s.Resize(60+i%40, 20+i%10)
		}
	}()
	writers.Add(1)
	go func() {
		defer writers.Done()
		for range 200 {
			if c := s.Cols(); c < 1 {
				t.Errorf("Cols() = %d", c)
				return
			}
			_ = s.Rows()
			_, _ = s.Cursor()
			_ = s.Modes()
		}
	}()
}

// grid copies the visible cells of a screen.
func grid(t *testing.T, s Screen) [][]uv.Cell {
	t.Helper()
	sc, ok := s.(*screen)
	if !ok {
		t.Fatalf("screen has type %T, want *vt.screen", s)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	w, h := sc.emu.Width(), sc.emu.Height()
	g := make([][]uv.Cell, h)
	for y := range h {
		g[y] = make([]uv.Cell, w)
		for x := range w {
			if c := sc.emu.CellAt(x, y); c != nil {
				g[y][x] = *c
			}
		}
	}
	return g
}

// cursorOf reports the cursor position in a form that compares with ==.
func cursorOf(s Screen) [2]int {
	col, row := s.Cursor()
	return [2]int{col, row}
}

func sameCell(a, b uv.Cell) bool {
	if a.Width != b.Width {
		return false
	}
	if a.Width <= 0 {
		// Trail half of a wide cell: the leading cell carries the content and the style.
		return true
	}
	ax, bx := a.Content, b.Content
	if ax == "" {
		ax = " "
	}
	if bx == "" {
		bx = " "
	}
	return ax == bx && a.Style.Equal(&b.Style) && a.Link.Equal(&b.Link)
}

func diffGrids(want, got [][]uv.Cell) string {
	if len(want) != len(got) {
		return fmt.Sprintf("row count %d, want %d", len(got), len(want))
	}
	for y := range want {
		if len(want[y]) != len(got[y]) {
			return fmt.Sprintf("row %d is %d cells wide, want %d", y, len(got[y]), len(want[y]))
		}
		for x := range want[y] {
			if !sameCell(want[y][x], got[y][x]) {
				return fmt.Sprintf("cell (%d,%d) = %+v, want %+v", x, y, got[y][x], want[y][x])
			}
		}
	}
	return ""
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}
