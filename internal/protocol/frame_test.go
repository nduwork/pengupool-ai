package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// fixturesDir holds the cross-language golden frames: <name>.bin is the exact wire bytes and
// <name>.json is what a decoder must produce from them. The TypeScript and Swift clients read the
// same files, so a framing change breaks all of them at once.
var fixturesDir = filepath.Join("testdata", "v1")

type fixture struct {
	Name        string `json:"name"`
	Type        int    `json:"type"`
	Channel     uint32 `json:"channel"`
	Seq         uint64 `json:"seq"`
	Length      uint32 `json:"length"`
	PayloadUTF8 string `json:"payload_utf8,omitempty"`
	PayloadHex  string `json:"payload_hex,omitempty"`
	ExpectError string `json:"expect_error,omitempty"`
	TruncateTo  int    `json:"truncate_to,omitempty"`
}

func allFixtures() []struct {
	f    fixture
	wire []byte
} {
	hello := &Message{ID: "0199a5f1-0000-7000-8000-000000000001", Type: TypeHello, V: Version,
		Client: &ClientInfo{Kind: KindCLI, Name: "pengupool attach", Version: "0.1.0-prototype"}}
	helloJSON, err := json.Marshal(hello)
	if err != nil {
		panic(err)
	}
	controlFrame := Frame{Header: Header{Type: FrameControl, Length: uint32(len(helloJSON))}, Payload: helloJSON}
	controlWire := append(controlFrame.Header.MarshalBinary(), helloJSON...)

	output := []byte("hello from the agent\r\n")
	outputWire := append(Header{Type: FrameOutput, Channel: 1, Seq: 4096, Length: uint32(len(output))}.MarshalBinary(), output...)

	ackWire := Header{Type: FrameAck, Channel: 1, Seq: 8192}.MarshalBinary()

	chunk := []byte("row-3 content")
	chunkWire := append(Header{Type: FrameSnapshot, Channel: 1, Seq: 3, Length: uint32(len(chunk))}.MarshalBinary(), chunk...)

	truncated := Header{Type: FrameOutput, Channel: 1, Seq: 0, Length: 64}.MarshalBinary()[:12]

	oversize := Header{Type: FrameOutput, Channel: 1, Seq: 0, Length: MaxDataPayload + 1}.MarshalBinary()

	return []struct {
		f    fixture
		wire []byte
	}{
		{fixture{Name: "control_hello", Type: 1, Channel: 0, Seq: 0, Length: uint32(len(helloJSON)), PayloadUTF8: string(helloJSON)}, controlWire},
		{fixture{Name: "output_seq4096", Type: 2, Channel: 1, Seq: 4096, Length: uint32(len(output)), PayloadHex: hex.EncodeToString(output)}, outputWire},
		{fixture{Name: "ack_seq8192", Type: 4, Channel: 1, Seq: 8192, Length: 0}, ackWire},
		{fixture{Name: "snapshot_part3", Type: 5, Channel: 1, Seq: 3, Length: uint32(len(chunk)), PayloadHex: hex.EncodeToString(chunk)}, chunkWire},
		{fixture{Name: "truncated_header", ExpectError: CodeTruncatedFrame, TruncateTo: 12}, truncated},
		{fixture{Name: "oversize_length", ExpectError: CodeFrameTooLarge, Length: MaxDataPayload + 1}, oversize},
	}
}

// TestGenerateFixtures rewrites the golden frames on disk. Run it explicitly with
// `go test ./internal/protocol/ -run TestGenerateFixtures` after a deliberate protocol change.
func TestGenerateFixtures(t *testing.T) {
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range allFixtures() {
		if err := os.WriteFile(filepath.Join(fixturesDir, c.f.Name+".bin"), c.wire, 0o644); err != nil {
			t.Fatal(err)
		}
		meta, err := json.MarshalIndent(c.f, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixturesDir, c.f.Name+".json"), append(meta, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGoldenFixtures decodes the checked-in bytes and compares them with the checked-in metadata.
func TestGoldenFixtures(t *testing.T) {
	for _, c := range allFixtures() {
		wire, err := os.ReadFile(filepath.Join(fixturesDir, c.f.Name+".bin"))
		if err != nil {
			t.Fatalf("%s: %v (run TestGenerateFixtures)", c.f.Name, err)
		}
		var want fixture
		raw, err := os.ReadFile(filepath.Join(fixturesDir, c.f.Name+".json"))
		if err != nil {
			t.Fatalf("%s: %v", c.f.Name, err)
		}
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("%s: %v", c.f.Name, err)
		}
		if !bytes.Equal(wire, c.wire) {
			t.Fatalf("%s: checked-in bytes differ from the generator; regenerate only for a deliberate protocol change", c.f.Name)
		}

		f, err := ReadFrame(bytes.NewReader(wire))
		if want.ExpectError != "" {
			if err == nil {
				t.Fatalf("%s: expected %s, decoded %+v", c.f.Name, want.ExpectError, f.Header)
			}
			if got := CodeOf(err); got != want.ExpectError {
				t.Fatalf("%s: expected code %s, got %s (%v)", c.f.Name, want.ExpectError, got, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.f.Name, err)
		}
		h := f.Header
		if int(h.Type) != want.Type || h.Channel != want.Channel || h.Seq != want.Seq || h.Length != want.Length {
			t.Fatalf("%s: got {type:%d channel:%d seq:%d length:%d}, want %+v", c.f.Name, h.Type, h.Channel, h.Seq, h.Length, want)
		}
		if want.PayloadUTF8 != "" && string(f.Payload) != want.PayloadUTF8 {
			t.Fatalf("%s: payload %q, want %q", c.f.Name, f.Payload, want.PayloadUTF8)
		}
		if want.PayloadHex != "" && hex.EncodeToString(f.Payload) != want.PayloadHex {
			t.Fatalf("%s: payload %x, want %s", c.f.Name, f.Payload, want.PayloadHex)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Type: FrameSnapshot, Channel: 0xdeadbeef, Seq: 1 << 40, Length: 12345}
	got, err := ParseHeader(h.MarshalBinary())
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Fatalf("got %+v, want %+v", got, h)
	}
	if size := len(h.MarshalBinary()); size != HeaderSize {
		t.Fatalf("header is %d bytes, want %d", size, HeaderSize)
	}
}

func TestParseHeaderRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		code string
	}{
		{"short header", make([]byte, HeaderSize-1), CodeTruncatedFrame},
		{"unknown type", append([]byte{0x7f}, make([]byte, HeaderSize-1)...), CodeUnknownFrameType},
		{"control on nonzero channel", Header{Type: FrameControl, Channel: 3, Length: 2}.MarshalBinary(), CodeInvalidChannel},
		{"output on channel zero", Header{Type: FrameOutput, Channel: 0, Length: 2}.MarshalBinary(), CodeInvalidChannel},
		{"control over limit", Header{Type: FrameControl, Channel: 0, Length: MaxControlPayload + 1}.MarshalBinary(), CodeFrameTooLarge},
		{"snapshot over limit", Header{Type: FrameSnapshot, Channel: 1, Length: MaxSnapshotPayload + 1}.MarshalBinary(), CodeFrameTooLarge},
		{"ack with payload", Header{Type: FrameAck, Channel: 1, Length: 1}.MarshalBinary(), CodeFrameTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseHeader(c.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := CodeOf(err); got != c.code {
				t.Fatalf("got code %s, want %s", got, c.code)
			}
		})
	}
}

func TestReadFrameRejectsTruncatedPayload(t *testing.T) {
	wire := append(Header{Type: FrameOutput, Channel: 1, Length: 16}.MarshalBinary(), []byte("short")...)
	_, err := ReadFrame(bytes.NewReader(wire))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := CodeOf(err); got != CodeTruncatedFrame {
		t.Fatalf("got %s, want %s", got, CodeTruncatedFrame)
	}
}

func TestReadFrameRejectsInvalidUTF8Control(t *testing.T) {
	payload := []byte{'{', '"', 'i', 'd', '"', ':', 0xff, 0xfe, '}'}
	wire := append(Header{Type: FrameControl, Length: uint32(len(payload))}.MarshalBinary(), payload...)
	_, err := ReadFrame(bytes.NewReader(wire))
	if got := CodeOf(err); got != CodeInvalidUTF8 {
		t.Fatalf("got %v, want %s", err, CodeInvalidUTF8)
	}
}

func TestReadFrameEOF(t *testing.T) {
	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestWriteControlEchoesIDAndLimits(t *testing.T) {
	var buf bytes.Buffer
	msg := &Message{Type: TypePing}
	if err := WriteControl(&buf, msg); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeControl(f)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || msg.ID != got.ID {
		t.Fatalf("id was not assigned and echoed: %+v", got)
	}
	if got.Type != TypePing {
		t.Fatalf("got %q", got.Type)
	}

	big := &Message{Type: TypeError, Message: string(bytes.Repeat([]byte("x"), MaxControlPayload))}
	if err := WriteControl(io.Discard, big); CodeOf(err) != CodeFrameTooLarge {
		t.Fatalf("got %v, want %s", err, CodeFrameTooLarge)
	}
}

func TestDecodeControlRejectsIncomplete(t *testing.T) {
	for name, payload := range map[string]string{
		"no id":   `{"type":"ping"}`,
		"no type": `{"id":"0199a5f1-0000-7000-8000-000000000001"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := Frame{Header: Header{Type: FrameControl, Length: uint32(len(payload))}, Payload: []byte(payload)}
			if _, err := DecodeControl(f); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := DecodeControl(Frame{Header: Header{Type: FrameOutput, Channel: 1}}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestErrorMessageCarriesCode(t *testing.T) {
	m := ErrorMessage("req-1", Errorf(CodeNotFound, "session %s is gone", "abc"))
	if m.ID != "req-1" || m.Type != TypeError || m.Code != CodeNotFound {
		t.Fatalf("unexpected message %+v", m)
	}
	if CodeOf(errors.New("plain")) != CodeInvalidArgument {
		t.Fatal("unclassified errors must map to invalid_argument")
	}
}
