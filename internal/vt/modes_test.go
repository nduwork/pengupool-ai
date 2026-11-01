package vt

import (
	"fmt"
	"testing"
)

// TestModesTracking walks every mode the client has to mirror: each one must read back set after
// DECSET and reset after DECRST.
func TestModesTracking(t *testing.T) {
	cases := []struct {
		name string
		mode int
		get  func(Modes) bool
	}{
		{"application cursor", 1, func(m Modes) bool { return m.ApplicationCursor }},
		{"bracketed paste", 2004, func(m Modes) bool { return m.BracketedPaste }},
		{"focus events", 1004, func(m Modes) bool { return m.FocusEvents }},
		{"mouse x10", 1000, func(m Modes) bool { return m.MouseX10 }},
		{"mouse button", 1002, func(m Modes) bool { return m.MouseButton }},
		{"mouse any", 1003, func(m Modes) bool { return m.MouseAny }},
		{"mouse utf8", 1005, func(m Modes) bool { return m.MouseUTF8 }},
		{"mouse sgr", 1006, func(m Modes) bool { return m.MouseSGR }},
		{"synchronized output", 2026, func(m Modes) bool { return m.SyncOutput }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(20, 4)
			if tc.get(s.Modes()) {
				t.Fatal("mode is set before anything was written")
			}
			s.Write([]byte(fmt.Sprintf("\x1b[?%dh", tc.mode)))
			if !tc.get(s.Modes()) {
				t.Fatalf("?%dh did not set the mode", tc.mode)
			}
			s.Write([]byte(fmt.Sprintf("\x1b[?%dl", tc.mode)))
			if tc.get(s.Modes()) {
				t.Fatalf("?%dl did not reset the mode", tc.mode)
			}
		})
	}
}

// TestModesAltScreen covers the screen-switching modes and RIS.
func TestModesAltScreen(t *testing.T) {
	s := New(20, 4)
	if s.Modes().AltScreen {
		t.Fatal("starts in the alternate screen")
	}
	for _, mode := range []int{1049, 1047} {
		s.Write([]byte(fmt.Sprintf("\x1b[?%dh", mode)))
		if !s.Modes().AltScreen {
			t.Fatalf("?%dh did not switch to the alternate screen", mode)
		}
		s.Write([]byte(fmt.Sprintf("\x1b[?%dl", mode)))
		if s.Modes().AltScreen {
			t.Fatalf("?%dl did not switch back to the normal screen", mode)
		}
	}

	// A full reset drops every mode, tracked ones included.
	s.Write([]byte("\x1b[?2004h\x1b[?1h\x1b[?1000h\x1b[?1049h"))
	s.Write([]byte("\x1bc")) // RIS
	if m := s.Modes(); m.AltScreen || m.BracketedPaste || m.ApplicationCursor || m.MouseX10 {
		t.Fatalf("ESC c left modes set: %+v", m)
	}
}

func TestModesCursorVisibility(t *testing.T) {
	s := New(20, 4)
	if !s.Modes().CursorVisible {
		t.Fatal("the cursor starts visible")
	}
	s.Write([]byte("\x1b[?25l"))
	if s.Modes().CursorVisible {
		t.Fatal("?25l did not hide the cursor")
	}
	s.Write([]byte("\x1b[?25h"))
	if !s.Modes().CursorVisible {
		t.Fatal("?25h did not show the cursor")
	}
}

// TestModesKeyboardFlags checks the kitty keyboard protocol flags, which no handler in x/vt
// claims: CSI = flags u sets them, CSI > flags u pushes, CSI < n u pops, and none of the three may
// reach the SGR handler.
func TestModesKeyboardFlags(t *testing.T) {
	s := New(20, 4)
	if got := s.Modes().KeyboardFlags; got != 0 {
		t.Fatalf("KeyboardFlags = %d, want 0", got)
	}
	for _, flags := range []uint16{5, 1, 31, 0} {
		s.Write([]byte(fmt.Sprintf("\x1b[=%du", flags)))
		if got := s.Modes().KeyboardFlags; got != flags {
			t.Fatalf("CSI = %d u left KeyboardFlags = %d", flags, got)
		}
	}

	// Set modes 2 (OR the flags in) and 3 (clear them).
	s.Write([]byte("\x1b[=1u\x1b[=2;2u"))
	if got := s.Modes().KeyboardFlags; got != 3 {
		t.Fatalf("CSI = 2 ; 2 u left KeyboardFlags = %d, want 3", got)
	}
	s.Write([]byte("\x1b[=2;3u"))
	if got := s.Modes().KeyboardFlags; got != 1 {
		t.Fatalf("CSI = 2 ; 3 u left KeyboardFlags = %d, want 1", got)
	}

	// Push and pop: pushing saves the current flags, popping restores them.
	s.Write([]byte("\x1b[=7u\x1b[>4u\x1b[>2u"))
	if got := s.Modes().KeyboardFlags; got != 2 {
		t.Fatalf("after two pushes KeyboardFlags = %d, want 2", got)
	}
	s.Write([]byte("\x1b[<u"))
	if got := s.Modes().KeyboardFlags; got != 4 {
		t.Fatalf("after one pop KeyboardFlags = %d, want 4", got)
	}
	s.Write([]byte("\x1b[<u"))
	if got := s.Modes().KeyboardFlags; got != 7 {
		t.Fatalf("after popping the whole stack KeyboardFlags = %d, want 7", got)
	}
	// Popping past the bottom resets every flag, as the kitty spec says.
	s.Write([]byte("\x1b[>4u\x1b[<3u"))
	if got := s.Modes().KeyboardFlags; got != 0 {
		t.Fatalf("after popping past the bottom KeyboardFlags = %d, want 0", got)
	}

	// The same byte stream, with a colour run around the flag sequence: the flags must change and
	// the colour must survive, i.e. the sequence never reaches the SGR handler.
	s = New(20, 4)
	s.Write([]byte("\x1b[31mred\x1b[>1ured still\x1b[0m"))
	if got := s.Modes().KeyboardFlags; got != 1 {
		t.Fatalf("KeyboardFlags = %d, want 1", got)
	}
	g := grid(t, s)
	for x := range 10 {
		if g[0][x].Style.Fg == nil {
			t.Fatalf("cell (0,%d) lost the red foreground at %q", x, text(g[0]))
		}
	}
}

// TestModesModifyOtherKeys covers XTMODKEYS (CSI > Pp ; Pv m), whose resource 1 carries the
// modifyOtherKeys level. It shares the "> m" command with nothing else, so it must stay separate
// from the kitty flags.
func TestModesModifyOtherKeys(t *testing.T) {
	s := New(20, 4)
	if got := s.Modes().ModifyOtherKeys; got != 0 {
		t.Fatalf("ModifyOtherKeys = %d, want 0", got)
	}
	s.Write([]byte("\x1b[>1;2m"))
	if got := s.Modes(); got.ModifyOtherKeys != 2 || got.KeyboardFlags != 0 {
		t.Fatalf("CSI > 1 ; 2 m left %+v, want ModifyOtherKeys 2 and no kitty flags", got)
	}
	// Resource 4 is the shorthand some applications use.
	s.Write([]byte("\x1b[>4;1m"))
	if got := s.Modes().ModifyOtherKeys; got != 1 {
		t.Fatalf("CSI > 4 ; 1 m left ModifyOtherKeys = %d, want 1", got)
	}
	// Resource 0 resets.
	s.Write([]byte("\x1b[>0m"))
	if got := s.Modes().ModifyOtherKeys; got != 0 {
		t.Fatalf("CSI > 0 m left ModifyOtherKeys = %d, want 0", got)
	}
}

// TestModesScrollRegion covers DECSTBM, which x/vt applies itself: the tracked region must match
// what it applied, and a resize resets it.
func TestModesScrollRegion(t *testing.T) {
	s := New(20, 6)
	if m := s.Modes(); m.ScrollTop != 0 || m.ScrollBottom != 0 {
		t.Fatalf("a fresh screen reports scroll region %d..%d, want the whole screen", m.ScrollTop, m.ScrollBottom)
	}
	s.Write([]byte("\x1b[2;5r"))
	if m := s.Modes(); m.ScrollTop != 2 || m.ScrollBottom != 5 {
		t.Fatalf("scroll region = %d..%d, want 2..5", m.ScrollTop, m.ScrollBottom)
	}
	// The bottom parameter defaults to the screen, and a full-screen region is the default.
	s.Write([]byte("\x1b[3r"))
	if m := s.Modes(); m.ScrollTop != 3 || m.ScrollBottom != 6 {
		t.Fatalf("scroll region = %d..%d, want 3..6", m.ScrollTop, m.ScrollBottom)
	}
	s.Write([]byte("\x1b[r"))
	if m := s.Modes(); m.ScrollTop != 0 || m.ScrollBottom != 0 {
		t.Fatalf("CSI r left scroll region %d..%d, want the whole screen", m.ScrollTop, m.ScrollBottom)
	}
	// An inverted region is declined by the emulator, so the tracked region must not move either.
	s.Write([]byte("\x1b[5;2r"))
	if m := s.Modes(); m.ScrollTop != 0 || m.ScrollBottom != 0 {
		t.Fatalf("an inverted DECSTBM left scroll region %d..%d", m.ScrollTop, m.ScrollBottom)
	}

	s.Write([]byte("\x1b[2;4r"))
	s.Resize(30, 10)
	if m := s.Modes(); m.ScrollTop != 0 || m.ScrollBottom != 0 {
		t.Fatalf("Resize left scroll region %d..%d, want the whole screen", m.ScrollTop, m.ScrollBottom)
	}
}
