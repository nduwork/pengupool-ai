package gateway

import (
	"bytes"
	"strings"
)

// typedText follows what the owner types into a session so the pool can tie a prompt to the text that was
// sent. It reads the keystrokes as a terminal sends them: printable text, backspace, line kill, escape
// sequences (arrows, bracketed paste) and Enter. An Enter is a submit only when typed text ends in it:
// not one with nothing typed (a menu, a dialog), not one inside a paste, not Shift+Enter (ESC CR), not
// one after a backslash (a line continuation).
type typedText struct {
	text  []byte
	paste bool
	esc   int // 0: none; 1: after ESC; 2: in a CSI sequence; 3: after ESC O
	csi   []byte
}

const maxTyped = 8 << 10

// feed takes input bytes and returns the text of each prompt they submit.
func (t *typedText) feed(p []byte) (submitted []string) {
	for _, b := range p {
		switch t.esc {
		case 1:
			switch b {
			case '[':
				t.esc, t.csi = 2, t.csi[:0]
				continue
			case 'O':
				t.esc = 3
				continue
			case '\r', '\n': // ESC Enter: a newline inside the prompt
				t.esc = 0
				t.add(' ')
				continue
			}
			t.esc = 0 // Esc pressed alone: the byte after it is typed text, read on below

		case 2:
			if b >= 0x40 && b <= 0x7e { // the sequence's last byte
				seq := string(t.csi) + string(b)
				t.esc = 0
				switch seq {
				case "200~":
					t.paste = true
				case "201~":
					t.paste = false
				}
			} else if len(t.csi) < 32 {
				t.csi = append(t.csi, b)
			} else {
				t.esc = 0 // not a sequence a terminal sends
			}
			continue
		case 3:
			t.esc = 0
			continue
		}
		switch {
		case b == 0x1b:
			t.esc = 1
		case t.paste:
			if b == '\r' || b == '\n' {
				t.add(' ')
			} else if b >= 0x20 && b != 0x7f {
				t.add(b)
			}
		case b == '\n': // Ctrl+J: the agent's input takes it as a newline, not a submit
			t.add(' ')
		case b == '\r':
			if n := len(t.text); n > 0 && t.text[n-1] == '\\' {
				t.text[n-1] = ' ' // a line continuation
				continue
			}
			if s := strings.TrimSpace(string(t.text)); s != "" {
				submitted = append(submitted, s)
			}
			t.text = t.text[:0]
		case b == 0x7f || b == 0x08: // backspace: drop the last character (a whole UTF-8 rune)
			for n := len(t.text); n > 0; n = len(t.text) {
				last := t.text[n-1]
				t.text = t.text[:n-1]
				if last < 0x80 || last >= 0xC0 {
					break
				}
			}
		case b == 0x15 || b == 0x03: // kill the line, interrupt
			t.text = t.text[:0]
		case b >= 0x20:
			t.add(b)
		}
	}
	return submitted
}

func (t *typedText) add(b byte) {
	if len(t.text) >= maxTyped {
		t.paste = false // too long to match a prompt anyway; do not let a paste that never ends hold every later one
		return
	}
	if !(b == ' ' && bytes.HasSuffix(t.text, []byte(" "))) {
		t.text = append(t.text, b)
	}
}
