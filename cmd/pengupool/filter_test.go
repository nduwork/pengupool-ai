package main

import "testing"

// TestQueryFilter covers the terminal-query responder filter (docs/protocol/v1.md section 4): while
// this client is not the lease holder it must not forward its own terminal's replies, and it must
// leave every other byte — a keystroke, a plain escape, a cursor key — alone.
func TestQueryFilter(t *testing.T) {
	cases := []struct {
		name string
		in   []string // pushed as separate chunks
		want string
	}{
		{
			name: "plain bytes and cursor keys pass through",
			in:   []string{"hi there", "\x1b[A", "\x1b[1;5H", "\x1bOP", "x"},
			want: "hi there\x1b[A\x1b[1;5H\x1bOPx",
		},
		{name: "DA1 reply", in: []string{"\x1b[?1;2c"}, want: ""},
		{name: "DA1 reply with three params", in: []string{"\x1b[?62;c"}, want: ""},
		{name: "DA2 reply", in: []string{"\x1b[>0;95;0c"}, want: ""},
		{name: "DECRQM mode report", in: []string{"\x1b[?2004;1$y"}, want: ""},
		{name: "DECRQM mode report, ANSI form", in: []string{"\x1b[4;2$y"}, want: ""},
		{name: "cursor position report", in: []string{"\x1b[10;20R"}, want: ""},
		{name: "focus in", in: []string{"\x1b[I"}, want: ""},
		{name: "focus out", in: []string{"\x1b[O"}, want: ""},
		{name: "in-band resize report", in: []string{"\x1b[48;24;80t"}, want: ""},
		{name: "window report", in: []string{"\x1b[4;0;0t"}, want: ""},
		{
			name: "a reply split over two chunks is dropped whole",
			in:   []string{"\x1b[?", "1;2c"},
			want: "",
		},
		{
			name: "a reply split over two chunks, then input",
			in:   []string{"\x1b[?1", ";2cok"},
			want: "ok",
		},
		{
			name: "a cursor key split over two chunks is forwarded whole",
			in:   []string{"\x1b[", "A"},
			want: "\x1b[A",
		},
		{
			name: "a report request form is not a key",
			in:   []string{"\x1b[?25$p"},
			want: "",
		},
		{
			name: "an OSC sequence passes through whole",
			in:   []string{"\x1b]0;title\x07ok"},
			want: "\x1b]0;title\x07ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueryFilter()
			var got []byte
			for _, chunk := range tc.in {
				got = append(got, f.push([]byte(chunk))...)
			}
			if string(got) != tc.want {
				t.Fatalf("filter pushed %q, got %q, want %q", tc.in, got, tc.want)
			}
			if pending := f.pending(); pending != 0 {
				// Every case ends on a complete sequence, so nothing may stay held.
				t.Fatalf("filter still holds %d bytes", pending)
			}
		})
	}
}

// TestQueryFilterHoldsThenFlushes checks that a lone ESC is held (it may be the start of a reply)
// and released by flush, which is what happens when the client takes the lease or detaches.
func TestQueryFilterHoldsThenFlushes(t *testing.T) {
	f := newQueryFilter()
	if out := f.push([]byte("\x1b")); len(out) != 0 {
		t.Fatalf("a lone ESC must be held, got %q", out)
	}
	if f.pending() != 1 {
		t.Fatalf("pending is %d, want 1", f.pending())
	}
	if out := f.flush(); string(out) != "\x1b" {
		t.Fatalf("flush released %q, want the held ESC", out)
	}
	if f.pending() != 0 {
		t.Fatalf("pending is %d after flush, want 0", f.pending())
	}
	if out := f.push([]byte("x")); string(out) != "x" {
		t.Fatalf("after flush the filter returned %q, want %q", out, "x")
	}
}

// TestQueryFilterGivesUpOnRunawaySequence bounds the hold: an unterminated escape cannot stall the
// user's input forever.
func TestQueryFilterGivesUpOnRunawaySequence(t *testing.T) {
	f := newQueryFilter()
	junk := "\x1b[" + string(make([]byte, queryFilterMaxPending+16))
	out := f.push([]byte(junk))
	if len(out) == 0 {
		t.Fatal("the filter held a runaway sequence instead of giving up")
	}
	if f.pending() != 0 {
		t.Fatalf("pending is %d, want 0", f.pending())
	}
}
