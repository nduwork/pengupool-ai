package gateway

import (
	"reflect"
	"strings"
	"testing"
)

// Only an Enter that ends typed text submits a prompt: not an empty one, one inside a paste, ESC Enter
// or one after a backslash; backspace and the line kill edit; the state carries across frames.
func TestTypedTextSubmitsOnlyRealPrompts(t *testing.T) {
	for name, c := range map[string]struct {
		in   []string
		want []string
	}{
		"a prompt":                 {[]string{"hello there\r"}, []string{"hello there"}},
		"empty Enter":              {[]string{"\r", "  \r"}, nil},
		"a menu Enter after keys":  {[]string{"\x1b[B\x1b[B\r"}, nil}, // arrows, then Enter
		"split over frames":        {[]string{"hel", "lo\r"}, []string{"hello"}},
		"backspace":                {[]string{"helx\x7flo\r"}, []string{"hello"}},
		"line kill":                {[]string{"junk\x15fresh\r"}, []string{"fresh"}},
		"interrupt":                {[]string{"junk\x03\r"}, nil},
		"shift enter is a newline": {[]string{"one\x1b\rtwo\r"}, []string{"one two"}},
		"a backslash continues":    {[]string{"one\\\rtwo\r"}, []string{"one two"}},
		"a paste, then Enter":      {[]string{"\x1b[200~line one\nline two\x1b[201~", "\r"}, []string{"line one line two"}},
		"Enters inside a paste":    {[]string{"\x1b[200~a\rb\x1b[201~"}, nil},
		"two prompts":              {[]string{"one\rtwo\r"}, []string{"one", "two"}},
		"Esc then typing":          {[]string{"\x1b", "yes\r"}, []string{"yes"}},
		"Esc then a letter":        {[]string{"\x1byes\r"}, []string{"yes"}},
		"Ctrl+J is a newline":      {[]string{"first\nsecond\r"}, []string{"first second"}},
		"a CSI that never ends":    {[]string{"\x1b[" + strings.Repeat("1;", 40) + "\x15", "ok\r"}, []string{"ok"}},
		"utf-8 backspace":          {[]string{"caf\xc3\xa9\x7f!\r"}, []string{"caf!"}},
	} {
		var tt typedText
		var got []string
		for _, in := range c.in {
			got = append(got, tt.feed([]byte(in))...)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q submitted %q, want %q", name, c.in, got, c.want)
		}
	}
}

// Typing too long to match, and a paste that never ends, give nothing and do not hold later prompts.
func TestTypedTextLimits(t *testing.T) {
	var tt typedText
	if got := tt.feed([]byte(strings.Repeat("x", maxTyped+10) + "\r")); len(got) != 1 || len(got[0]) > maxTyped {
		t.Fatalf("a long line: %d submitted", len(got))
	}
	tt.feed([]byte("\x1b[200~" + strings.Repeat("y", maxTyped+10)))
	if got := tt.feed([]byte("\x1b[201~z\r")); len(got) != 1 {
		t.Fatalf("after a long paste: %q", got)
	}
	if got := tt.feed([]byte("\x1b[200~never ends")); len(got) != 0 {
		t.Fatal("an open paste submitted")
	}
}
