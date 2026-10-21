package stream

import (
	"encoding/binary"
	"errors"
)

// Frame kinds.
const (
	KindOpen  byte = 1
	KindData  byte = 2
	KindClose byte = 3
	KindAck   byte = 4
	KindJSON  byte = 5
)

// HeaderLen is the envelope header size: u8 kind + u32 BE channel id.
const HeaderLen = 5

// Window is the per-channel credit window in bytes.
const Window = 256 * 1024

var ErrShortFrame = errors.New("stream: short frame")

// Frame is one decoded envelope.
type Frame struct {
	Kind    byte
	Chan    uint32
	Payload []byte
}

// Encode serialises f as [u8 kind][u32 BE chanId][payload].
func (f Frame) Encode() []byte {
	b := make([]byte, HeaderLen+len(f.Payload))
	b[0] = f.Kind
	binary.BigEndian.PutUint32(b[1:5], f.Chan)
	copy(b[HeaderLen:], f.Payload)
	return b
}

// Decode parses a frame. Payload aliases b.
func Decode(b []byte) (Frame, error) {
	if len(b) < HeaderLen {
		return Frame{}, ErrShortFrame
	}
	return Frame{Kind: b[0], Chan: binary.BigEndian.Uint32(b[1:5]), Payload: b[HeaderLen:]}, nil
}

// AckPayload encodes a consumed-byte count for KindAck frames (u32 BE).
func AckPayload(n uint32) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, n)
	return p
}

// ParseAck decodes a KindAck payload.
func ParseAck(p []byte) (uint32, error) {
	if len(p) < 4 {
		return 0, ErrShortFrame
	}
	return binary.BigEndian.Uint32(p), nil
}
