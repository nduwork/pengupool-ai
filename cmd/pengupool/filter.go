package main

// Terminal-query responder filter (docs/protocol/v1.md section 4).
//
// One attached client holds the size lease and is also the terminal-query responder. While this
// client does not hold the lease, the lease holder answers the queries the session's programs ask
// (DA1/DA2, a DECRQM mode report, a cursor position, a focus event, an in-band resize report): this
// client's own terminal answers them too, because a raw attach writes the query bytes to the real
// terminal, and forwarding that reply as input would inject it into agent input a second time —
// replies such as `\x1b[?1;2c` duplicated, or injected into agent input, leave the harness in the
// wrong keyboard mode. The filter drops exactly those replies from the input stream.
//
// Only replies (terminal → host) are dropped, never requests (host → terminal): a query a program
// writes arrives as PTY output and is never seen here. The filter is stateful across reads, because
// one reply can be split over two reads of the local terminal.

const (
	escByte = 0x1b
	belByte = 0x07
	// queryFilterMaxPending bounds what an unterminated sequence may hold back. Past it the filter
	// gives up and forwards what it has, so a lone ESC typed by the user cannot stall input forever.
	queryFilterMaxPending = 4096
)

// queryFilter removes terminal-query replies from a byte stream.
type queryFilter struct {
	held []byte
}

func newQueryFilter() *queryFilter { return &queryFilter{} }

// push filters one chunk of local-terminal input. A sequence that is not complete yet is held back
// and finished by a later push, rather than half-forwarded.
func (f *queryFilter) push(chunk []byte) []byte {
	buf := chunk
	if len(f.held) > 0 {
		joined := make([]byte, 0, len(f.held)+len(chunk))
		joined = append(joined, f.held...)
		joined = append(joined, chunk...)
		buf = joined
		f.held = nil
	}

	out := make([]byte, 0, len(buf))
	i := 0
	for i < len(buf) {
		if buf[i] != escByte {
			out = append(out, buf[i])
			i++
			continue
		}
		if i+1 >= len(buf) {
			break // hold the lone ESC
		}
		next := buf[i+1]
		if next == '[' {
			j := i + 2
			for j < len(buf) && !(buf[j] >= 0x40 && buf[j] <= 0x7e) {
				j++
			}
			if j >= len(buf) {
				break // incomplete CSI: hold it
			}
			seq := buf[i : j+1]
			if !isTerminalReply(seq) {
				out = append(out, seq...)
			}
			i = j + 1
			continue
		}
		if isStringIntro(next) {
			end := findStringEnd(buf, i+2)
			if end < 0 {
				break // incomplete string escape: hold it
			}
			out = append(out, buf[i:end]...)
			i = end
			continue
		}
		out = append(out, buf[i], next) // two-byte escape, not a query reply
		i += 2
	}

	tail := buf[i:]
	if len(tail) > queryFilterMaxPending {
		out = append(out, tail...)
		f.held = nil
	} else {
		f.held = append([]byte(nil), tail...)
	}
	return out
}

// flush releases whatever push held back, e.g. on detach or when this client takes the lease and
// becomes the responder.
func (f *queryFilter) flush() []byte {
	held := f.held
	f.held = nil
	return held
}

// pending reports how many bytes are held back; zero in steady state.
func (f *queryFilter) pending() int { return len(f.held) }

// isTerminalReply reports whether a complete CSI sequence (ESC [ … final) is a reply a terminal
// sends to a query, which this client must not forward while it is not the responder.
func isTerminalReply(seq []byte) bool {
	final := seq[len(seq)-1]
	body := seq[2 : len(seq)-1]
	switch final {
	case 'c':
		// DA1 / DA2 / DA3: primary, secondary and tertiary device attributes.
		return true
	case 'y':
		// DECRQM mode report (DECRPM): CSI ? Ps ; Pm $ y.
		return true
	case 'p':
		// The DECRQM request form, CSI ? Ps $ p. A request never reaches stdin, so this only
		// covers a terminal that mirrors one back.
		return true
	case 'R':
		// Cursor position report (the reply to DSR, CSI 6 n).
		return true
	case 'I', 'O':
		// Focus in / focus out (mode 1004), which carry no parameters.
		return len(body) == 0
	case 't':
		// Window reports and the in-band resize report (xterm mode 2048): CSI 48 ; rows ; cols t.
		n, ok := firstNumber(body)
		return ok && (n == 4 || n == 48)
	default:
		return false
	}
}

// firstNumber reads the first parameter of a CSI body, skipping a private-parameter prefix. It
// reports whether any digit was seen.
func firstNumber(body []byte) (int, bool) {
	i := 0
	for i < len(body) && (body[i] == '?' || body[i] == '>' || body[i] == '=') {
		i++
	}
	n, seen := 0, false
	for i < len(body) && body[i] >= '0' && body[i] <= '9' {
		n = n*10 + int(body[i]-'0')
		seen = true
		i++
	}
	return n, seen
}

// isStringIntro reports whether a byte after ESC starts a string-like escape: OSC, DCS, SOS, PM, APC.
func isStringIntro(b byte) bool {
	switch b {
	case ']', 'P', 'X', '^', '_':
		return true
	}
	return false
}

// findStringEnd returns the index just past the terminator (BEL or ST) of a string-like escape, or
// -1 when the escape is not complete yet.
func findStringEnd(buf []byte, from int) int {
	for i := from; i < len(buf); i++ {
		if buf[i] == belByte {
			return i + 1
		}
		if buf[i] == escByte && i+1 < len(buf) && buf[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}
