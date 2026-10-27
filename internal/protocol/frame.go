// Package protocol implements the versioned PenguPool wire protocol, v1.
//
// It is spoken on every hop: CLI/browser/iOS <-> daemon <-> worker. The frame layout, limits and
// the meaning of every field are specified in docs/protocol/v1.md, which this package implements.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// Version is the protocol major version carried in the hello exchange.
const Version = 1

// HeaderSize is the fixed frame header size: type:u8 | channel:u32 | seq:u64 | length:u32.
const HeaderSize = 17

// Payload limits. They are checked before any allocation.
const (
	MaxControlPayload  = 1 << 20 // 1 MiB
	MaxDataPayload     = 64 << 10
	MaxInputPayload    = 64 << 10
	MaxSnapshotPayload = 64 << 10
)

// FrameType identifies a frame's payload kind.
type FrameType uint8

// Frame types. Channel 0 is reserved for control JSON; terminal data uses nonzero channels.
const (
	FrameControl  FrameType = 0x01
	FrameOutput   FrameType = 0x02
	FrameInput    FrameType = 0x03
	FrameAck      FrameType = 0x04
	FrameSnapshot FrameType = 0x05
)

func (t FrameType) String() string {
	switch t {
	case FrameControl:
		return "control"
	case FrameOutput:
		return "output"
	case FrameInput:
		return "input"
	case FrameAck:
		return "ack"
	case FrameSnapshot:
		return "snapshot"
	default:
		return fmt.Sprintf("frame(%#x)", uint8(t))
	}
}

// MaxPayload reports the largest allowed payload for this frame type. Unknown types are refused.
func (t FrameType) MaxPayload() (uint32, bool) {
	switch t {
	case FrameControl:
		return MaxControlPayload, true
	case FrameOutput:
		return MaxDataPayload, true
	case FrameInput:
		return MaxInputPayload, true
	case FrameAck:
		return 0, true
	case FrameSnapshot:
		return MaxSnapshotPayload, true
	default:
		return 0, false
	}
}

// Header is a decoded 17-byte frame header.
type Header struct {
	Type    FrameType
	Channel uint32
	Seq     uint64
	Length  uint32
}

// MarshalBinary encodes the header into its 17-byte big-endian wire form.
func (h Header) MarshalBinary() []byte {
	b := make([]byte, HeaderSize)
	b[0] = byte(h.Type)
	binary.BigEndian.PutUint32(b[1:5], h.Channel)
	binary.BigEndian.PutUint64(b[5:13], h.Seq)
	binary.BigEndian.PutUint32(b[13:17], h.Length)
	return b
}

// ParseHeader decodes and validates a 17-byte header.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, Errorf(CodeTruncatedFrame, "header needs %d bytes, got %d", HeaderSize, len(b))
	}
	h := Header{
		Type:    FrameType(b[0]),
		Channel: binary.BigEndian.Uint32(b[1:5]),
		Seq:     binary.BigEndian.Uint64(b[5:13]),
		Length:  binary.BigEndian.Uint32(b[13:17]),
	}
	max, ok := h.Type.MaxPayload()
	if !ok {
		return Header{}, Errorf(CodeUnknownFrameType, "unknown frame type %#x", uint8(h.Type))
	}
	if h.Length > max {
		return Header{}, Errorf(CodeFrameTooLarge, "%s frame length %d exceeds limit %d", h.Type, h.Length, max)
	}
	if h.Type == FrameControl && h.Channel != 0 {
		return Header{}, Errorf(CodeInvalidChannel, "control frame must use channel 0, got %d", h.Channel)
	}
	if h.Type != FrameControl && h.Channel == 0 {
		return Header{}, Errorf(CodeInvalidChannel, "%s frame must use a nonzero channel", h.Type)
	}
	return h, nil
}

// Frame is a header plus its payload.
type Frame struct {
	Header  Header
	Payload []byte
}

// ReadFrame reads exactly one frame, refusing over-limit and truncated input before allocating.
func ReadFrame(r io.Reader) (Frame, error) {
	var hb [HeaderSize]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		if err == io.EOF {
			return Frame{}, io.EOF
		}
		return Frame{}, Errorf(CodeTruncatedFrame, "read header: %v", err)
	}
	h, err := ParseHeader(hb[:])
	if err != nil {
		return Frame{}, err
	}
	f := Frame{Header: h}
	if h.Length == 0 {
		return f, nil
	}
	f.Payload = make([]byte, h.Length)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return Frame{}, Errorf(CodeTruncatedFrame, "read payload: %v", err)
	}
	if h.Type == FrameControl && !utf8.Valid(f.Payload) {
		return Frame{}, Errorf(CodeInvalidUTF8, "control payload is not valid UTF-8")
	}
	return f, nil
}

// WriteFrame writes a frame, enforcing the per-type payload limit.
func WriteFrame(w io.Writer, f Frame) error {
	max, ok := f.Header.Type.MaxPayload()
	if !ok {
		return Errorf(CodeUnknownFrameType, "unknown frame type %#x", uint8(f.Header.Type))
	}
	if uint32(len(f.Payload)) > max {
		return Errorf(CodeFrameTooLarge, "%s frame length %d exceeds limit %d", f.Header.Type, len(f.Payload), max)
	}
	if len(f.Payload) != int(f.Header.Length) {
		f.Header.Length = uint32(len(f.Payload))
	}
	if _, err := w.Write(f.Header.MarshalBinary()); err != nil {
		return err
	}
	if len(f.Payload) == 0 {
		return nil
	}
	_, err := w.Write(f.Payload)
	return err
}

// WriteControl marshals a control message and writes it on channel 0.
func WriteControl(w io.Writer, msg *Message) error {
	if msg.ID == "" {
		msg.ID = NewID()
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(b) > MaxControlPayload {
		return Errorf(CodeFrameTooLarge, "control message %q is %d bytes", msg.Type, len(b))
	}
	return WriteFrame(w, Frame{Header: Header{Type: FrameControl, Length: uint32(len(b))}, Payload: b})
}

// DecodeControl decodes a control frame payload into a message.
func DecodeControl(f Frame) (*Message, error) {
	if f.Header.Type != FrameControl {
		return nil, Errorf(CodeInvalidArgument, "expected a control frame, got %s", f.Header.Type)
	}
	var m Message
	if err := json.Unmarshal(f.Payload, &m); err != nil {
		return nil, Errorf(CodeInvalidArgument, "control payload: %v", err)
	}
	if m.ID == "" {
		return nil, Errorf(CodeInvalidArgument, "control message has no id")
	}
	if m.Type == "" {
		return nil, Errorf(CodeInvalidArgument, "control message has no type")
	}
	return &m, nil
}
