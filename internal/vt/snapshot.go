package vt

import (
	"bytes"
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

// Snapshot returns the bytes that rebuild the visible screen on a cleared, same-size client.
//
// Layout: set the alternate screen to match, emit every tracked DEC private mode plus the
// XTMODKEYS and kitty keyboard sequences, emit the scroll region when it is not the whole screen,
// then per row a cursor position, an erase of that row and the row's cells, and finally a closed
// pen and hyperlink, the cursor position and cursor visibility. Every row is rewritten from column
// one and cleared to its end, so a client showing stale content ends up with exactly this screen.
// \x1b[2J is never emitted: on the normal screen it would destroy the client's scrollback.
func (s *screen) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	cols, rows := s.emu.Width(), s.emu.Height()
	var b bytes.Buffer
	b.Grow(cols*rows + cols*8 + 64)

	if s.emu.IsAltScreen() {
		b.WriteString("\x1b[?1049h")
	} else {
		b.WriteString("\x1b[?1049l")
	}
	appendModes(&b, s.mode)
	appendScrollRegion(&b, s.mode)
	// Start from a known pen: some of the sequences above reach an SGR handler on terminals that
	// do not know them, and this snapshot must not depend on what the client had set.
	b.WriteString(ansi.ResetStyle)

	var pen uv.Style
	var link uv.Link
	for y := range rows {
		appendCursorPosition(&b, y+1, 1)
		// Erase the row before painting it: the erase is inclusive of the cell under the cursor,
		// so erasing after a row that reached the last column would eat the last cell. With the
		// pen at its default the row is cleared to blank cells, exactly what the cells past the
		// row's content must look like.
		if !pen.IsZero() {
			b.WriteString(ansi.ResetStyle)
			pen = uv.Style{}
		}
		if link.URL != "" {
			b.WriteString(ansi.ResetHyperlink())
			link = uv.Link{}
		}
		b.WriteString(ansi.EraseLineRight)
		last := lastPaintedCell(s.emu, y, cols)
		for x := 0; x <= last; {
			c := s.emu.CellAt(x, y)
			if c == nil {
				x++
				continue
			}
			if c.Width <= 0 {
				x++ // trail half of a wide cell: nothing to write, the leading cell covers it
				continue
			}
			if !c.Style.Equal(&pen) {
				b.WriteString(uv.StyleDiff(&pen, &c.Style))
				pen = c.Style
			}
			if !c.Link.Equal(&link) {
				if link.URL != "" {
					b.WriteString(ansi.ResetHyperlink())
				}
				if c.Link.URL != "" {
					b.WriteString(ansi.SetHyperlink(c.Link.URL, c.Link.Params))
				}
				link = c.Link
			}
			b.WriteString(cellText(c))
			x += c.Width
		}
	}

	// The cursor position and visibility below must not be painted with the last cell's pen or
	// carry its hyperlink into whatever the client writes next.
	if !pen.IsZero() {
		b.WriteString(ansi.ResetStyle)
	}
	if link.URL != "" {
		b.WriteString(ansi.ResetHyperlink())
	}

	cursor := s.emu.CursorPosition()
	appendCursorPosition(&b, cursor.Y+1, cursor.X+1)
	if s.emu.CursorHidden() {
		b.WriteString(ansi.HideCursor)
	} else {
		b.WriteString(ansi.ShowCursor)
	}

	return b.Bytes()
}

// lastPaintedCell returns the last column of a row that carries anything a cleared client would
// lose, or -1 for a row that is blank all the way across. Cells past it stay blank and are left
// alone by the erase that opened the row.
func lastPaintedCell(emu *xvt.Emulator, y, cols int) int {
	for x := cols - 1; x >= 0; x-- {
		c := emu.CellAt(x, y)
		if c == nil {
			continue
		}
		if c.IsZero() || (c.Width == 1 && c.Content == " " && c.Style.IsZero() && c.Link.IsZero()) {
			continue // an unwritten or default-blank cell
		}
		return x
	}
	return -1
}

// appendModes writes every tracked mode, set and reset alike, so the client ends up with this
// terminal's input semantics no matter what it had before. The two keyboard sequences are distinct
// and are emitted as such: XTMODKEYS is CSI > Pp ; Pv m, the kitty flags are CSI > flags u.
func appendModes(b *bytes.Buffer, m Modes) {
	privateMode(b, 1, m.ApplicationCursor) // DECCKM
	privateMode(b, 1000, m.MouseX10)
	privateMode(b, 1002, m.MouseButton)
	privateMode(b, 1003, m.MouseAny)
	privateMode(b, 1005, m.MouseUTF8)
	privateMode(b, 1006, m.MouseSGR)
	privateMode(b, 1004, m.FocusEvents)
	privateMode(b, 2004, m.BracketedPaste)
	privateMode(b, 2026, m.SyncOutput)
	if m.KeyboardFlags != 0 {
		// Kitty keyboard protocol: push the flags so a client that has none set ends up with them.
		b.WriteString("\x1b[>")
		b.WriteString(strconv.FormatUint(uint64(m.KeyboardFlags), 10))
		b.WriteByte('u')
	}
	if m.ModifyOtherKeys != 0 {
		// XTMODKEYS: modifyOtherKeys (resource 1), 0 is the default so there is nothing to say then.
		b.WriteString("\x1b[>1;")
		b.WriteString(strconv.Itoa(m.ModifyOtherKeys))
		b.WriteByte('m')
	}
}

// appendScrollRegion writes DECSTBM when the scroll region is not the whole screen. It is emitted
// with the modes, before any row is painted, because it also homes the cursor and changes where
// the client's own output scrolls.
func appendScrollRegion(b *bytes.Buffer, m Modes) {
	if m.ScrollTop == 0 {
		return
	}
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(m.ScrollTop))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(m.ScrollBottom))
	b.WriteByte('r')
}

func privateMode(b *bytes.Buffer, mode int, set bool) {
	b.WriteString("\x1b[?")
	b.WriteString(strconv.Itoa(mode))
	if set {
		b.WriteByte('h')
	} else {
		b.WriteByte('l')
	}
}

// appendCursorPosition writes CUP, one-based as the wire wants it.
func appendCursorPosition(b *bytes.Buffer, row, col int) {
	b.WriteString("\x1b[")
	b.WriteString(strconv.Itoa(row))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(col))
	b.WriteByte('H')
}
