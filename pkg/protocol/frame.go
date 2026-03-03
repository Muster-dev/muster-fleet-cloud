package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HeaderSize is the fixed frame header size in bytes.
const HeaderSize = 89

// IDSize is the size of source/dest identity fields.
const IDSize = 32

// Frame represents a single MTP wire frame.
type Frame struct {
	Version   uint8
	MsgType   uint8
	RequestID [16]byte
	SourceID  [IDSize]byte
	DestID    [IDSize]byte
	Flags     uint8
	Payload   []byte
}

// Encode serializes a Frame into bytes for transmission.
func Encode(f *Frame) []byte {
	payloadLen := uint32(len(f.Payload))
	buf := make([]byte, HeaderSize+int(payloadLen))

	buf[0] = f.Version
	buf[1] = f.MsgType
	binary.BigEndian.PutUint32(buf[2:6], payloadLen)
	copy(buf[6:22], f.RequestID[:])
	copy(buf[22:54], f.SourceID[:])
	copy(buf[54:86], f.DestID[:])
	buf[86] = f.Flags
	buf[87] = 0 // reserved
	buf[88] = 0 // reserved
	copy(buf[HeaderSize:], f.Payload)

	return buf
}

// Decode parses bytes into a Frame.
func Decode(data []byte) (*Frame, error) {
	if len(data) < HeaderSize {
		return nil, fmt.Errorf("frame too short: %d bytes (need %d)", len(data), HeaderSize)
	}

	version := data[0]
	if version != Version {
		return nil, fmt.Errorf("unsupported protocol version: %d", version)
	}

	payloadLen := binary.BigEndian.Uint32(data[2:6])
	totalLen := HeaderSize + int(payloadLen)
	if len(data) < totalLen {
		return nil, fmt.Errorf("frame truncated: got %d bytes, expected %d", len(data), totalLen)
	}

	f := &Frame{
		Version: version,
		MsgType: data[1],
		Flags:   data[86],
	}
	copy(f.RequestID[:], data[6:22])
	copy(f.SourceID[:], data[22:54])
	copy(f.DestID[:], data[54:86])

	if payloadLen > 0 {
		f.Payload = make([]byte, payloadLen)
		copy(f.Payload, data[HeaderSize:totalLen])
	}

	return f, nil
}

// NewFrame creates a frame with version set.
func NewFrame(msgType uint8, requestID [16]byte, sourceID, destID string, flags uint8, payload []byte) *Frame {
	f := &Frame{
		Version:   Version,
		MsgType:   msgType,
		RequestID: requestID,
		Flags:     flags,
		Payload:   payload,
	}
	f.SourceID = PadID(sourceID)
	f.DestID = PadID(destID)
	return f
}

// PadID pads an identity string to IDSize bytes with zero padding.
func PadID(id string) [IDSize]byte {
	var buf [IDSize]byte
	copy(buf[:], id)
	return buf
}

// ParseID extracts a trimmed identity string from a padded ID field.
func ParseID(id [IDSize]byte) string {
	for i, b := range id {
		if b == 0 {
			return string(id[:i])
		}
	}
	return string(id[:])
}

// IsEncrypted checks if the ENCRYPTED flag is set.
func (f *Frame) IsEncrypted() bool {
	return f.Flags&FlagEncrypted != 0
}

// Validate performs basic frame validation.
func (f *Frame) Validate() error {
	if f.Version != Version {
		return fmt.Errorf("unsupported version: %d", f.Version)
	}
	if f.MsgType == 0 {
		return errors.New("message type cannot be zero")
	}
	return nil
}
