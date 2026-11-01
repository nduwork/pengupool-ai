package vt

import (
	"bytes"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// TestSnapshotWideCJK checks that a wide character and the placeholder cell behind it survive a
// round trip: the leading cell carries the grapheme and the width, the trailing cell is a
// zero-width placeholder in both screens.
func TestSnapshotWideCJK(t *testing.T) {
	src := New(10, 2)
	src.Write([]byte("日本語"))
	g := grid(t, src)
	for i, want := range []string{"日", "本", "語"} {
		lead, trail := i*2, i*2+1
		if g[0][lead].Content != want || g[0][lead].Width != 2 {
			t.Fatalf("cell (%d,0) = %+v, want %q with width 2", lead, g[0][lead], want)
		}
		if g[0][trail].Width != 0 {
			t.Fatalf("cell (%d,0) = %+v, want the zero-width trail half of %q", trail, g[0][trail], want)
		}
	}

	dst := New(10, 2)
	dst.Write(src.Snapshot())
	if d := diffGrids(g, grid(t, dst)); d != "" {
		t.Fatalf("wide characters did not survive the snapshot: %s", d)
	}
	if got := text(grid(t, dst)[0]); got != "日本語    " {
		t.Fatalf("row 0 = %q, want %q", got, "日本語    ")
	}
}

// TestSnapshotCombiningMark checks that a grapheme cluster keeps its combining mark: a snapshot
// that emitted only the base character would show the same text but lose the accent.
func TestSnapshotCombiningMark(t *testing.T) {
	src := New(10, 2)
	src.Write([]byte("cafe\u0301"))
	g := grid(t, src)
	if got := g[0][3].Content; !strings.Contains(got, "e") || !strings.Contains(got, "\u0301") {
		t.Fatalf("cell (3,0) = %q, want the e with its combining mark", got)
	}
	if g[0][3].Width != 1 {
		t.Fatalf("cell (3,0) width = %d, want 1", g[0][3].Width)
	}

	dst := New(10, 2)
	dst.Write(src.Snapshot())
	if d := diffGrids(g, grid(t, dst)); d != "" {
		t.Fatalf("combining mark did not survive the snapshot: %s", d)
	}
}

// TestSnapshotColors checks that attributes and colours are re-emitted with the cells.
func TestSnapshotColors(t *testing.T) {
	src := New(10, 2)
	src.Write([]byte("\x1b[1;31mbold red\x1b[0m \x1b[48;5;236mbg\x1b[0m"))

	dst := New(10, 2)
	dst.Write(src.Snapshot())
	sg, dg := grid(t, src), grid(t, dst)

	if dg[0][0].Style.IsZero() {
		t.Fatalf("cell (0,0) = %+v, want the bold red style", dg[0][0])
	}
	if dg[0][0].Style.Attrs&uv.AttrBold == 0 {
		t.Fatalf("cell (0,0) lost its bold attribute: %+v", dg[0][0].Style)
	}
	if dg[0][0].Style.Fg == nil {
		t.Fatalf("cell (0,0) lost its foreground colour: %+v", dg[0][0].Style)
	}
	if dg[0][9].Style.Bg == nil {
		t.Fatalf("cell (9,0) lost its background colour: %+v", dg[0][9].Style)
	}
	if d := diffGrids(sg, dg); d != "" {
		t.Fatalf("styles did not survive the snapshot: %s", d)
	}
}

// TestSnapshotFullRow checks the row that reaches the last column. Erasing after painting such a
// row would eat its last cell, so the snapshot must clear the row before it paints it.
func TestSnapshotFullRow(t *testing.T) {
	src := New(testCols, 3)
	src.Write([]byte(strings.Repeat("F", testCols)))

	dst := New(testCols, 3)
	dst.Write(src.Snapshot())
	g := grid(t, dst)
	if got := g[0][testCols-1].Content; got != "F" {
		t.Fatalf("last cell of a full row = %q, want \"F\"", got)
	}
	if d := diffGrids(grid(t, src), g); d != "" {
		t.Fatalf("a full row did not survive the snapshot: %s", d)
	}
}

// TestSnapshotClosesPenAndHyperlink checks that a snapshot does not leak the pen or the hyperlink
// it painted its last row with into whatever the client writes next.
func TestSnapshotClosesPenAndHyperlink(t *testing.T) {
	src := New(20, 3)
	src.Write([]byte("\x1b[1;31mred\x1b]8;;https://example.com\x07linked\x1b]8;;\x07more"))
	// Leave the pen and the link open on the last painted row, the state that used to leak.
	src.Write([]byte("\x1b[1;1H\x1b[1;33m\x1b]8;;https://example.com\x07linked again"))

	dst := New(20, 3)
	dst.Write(src.Snapshot())
	dst.Write([]byte("\x1b[3;1Hplain"))

	c := grid(t, dst)[2][0]
	if !c.Style.IsZero() {
		t.Fatalf("the snapshot left the pen open: cell (0,2) = %+v", c.Style)
	}
	if c.Link.URL != "" {
		t.Fatalf("the snapshot left the hyperlink open: cell (0,2) = %+v", c.Link)
	}
}

// TestSnapshotEmitsScrollRegionAndKeyboard checks that the state with no home in x/vt's own mode
// map travels in the snapshot: the scroll region, XTMODKEYS and the kitty flags, each as its own
// sequence.
func TestSnapshotEmitsScrollRegionAndKeyboard(t *testing.T) {
	src := New(20, 6)
	src.Write([]byte("\x1b[2;5r\x1b[>1;2m\x1b[=5u"))

	snap := src.Snapshot()
	for _, want := range []string{"\x1b[2;5r", "\x1b[>1;2m", "\x1b[>5u"} {
		if !bytes.Contains(snap, []byte(want)) {
			t.Fatalf("snapshot %q does not carry %q", snap, want)
		}
	}

	dst := New(20, 6)
	dst.Write(snap)
	if want, got := src.Modes(), dst.Modes(); want != got {
		t.Fatalf("modes = %+v, want %+v", got, want)
	}
}

// TestSnapshotKeepsKeyboardStack checks that the kitty flag stack, not only its top, survives a
// snapshot: after pushes of 1 and 3, a pop on the restored screen must give 1 as it does on the
// original.
func TestSnapshotKeepsKeyboardStack(t *testing.T) {
	src := New(20, 4)
	src.Write([]byte("\x1b[>1u\x1b[>3u"))
	dst := New(20, 4)
	dst.Write(src.Snapshot())
	src.Write([]byte("\x1b[<u"))
	dst.Write([]byte("\x1b[<u"))
	if want, got := src.Modes().KeyboardFlags, dst.Modes().KeyboardFlags; want != 1 || got != want {
		t.Fatalf("after a pop the restored screen has flags %d, the original %d (want 1)", got, want)
	}
}

// text renders the printable content of a grid row.
func text(row []uv.Cell) string {
	var b strings.Builder
	for i := range row {
		if row[i].Width <= 0 {
			continue
		}
		if row[i].Content == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(row[i].Content)
	}
	return b.String()
}
