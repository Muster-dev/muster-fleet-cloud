package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	reqID := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	payload := []byte(`{"cmd":"deploy","service":"api"}`)

	original := NewFrame(MsgCommand, reqID, "org1/agent-a", "org1/cli-b", FlagEncrypted, payload)
	encoded := Encode(original)
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Version != original.Version {
		t.Errorf("Version mismatch: got %d, want %d", decoded.Version, original.Version)
	}
	if decoded.MsgType != original.MsgType {
		t.Errorf("MsgType mismatch: got %d, want %d", decoded.MsgType, original.MsgType)
	}
	if decoded.RequestID != original.RequestID {
		t.Errorf("RequestID mismatch: got %v, want %v", decoded.RequestID, original.RequestID)
	}
	if decoded.SourceID != original.SourceID {
		t.Errorf("SourceID mismatch: got %v, want %v", decoded.SourceID, original.SourceID)
	}
	if decoded.DestID != original.DestID {
		t.Errorf("DestID mismatch: got %v, want %v", decoded.DestID, original.DestID)
	}
	if decoded.Flags != original.Flags {
		t.Errorf("Flags mismatch: got %d, want %d", decoded.Flags, original.Flags)
	}
	if !bytes.Equal(decoded.Payload, original.Payload) {
		t.Errorf("Payload mismatch: got %q, want %q", decoded.Payload, original.Payload)
	}
}

func TestNewFrameSetsVersion(t *testing.T) {
	f := NewFrame(MsgHeartbeat, [16]byte{}, "src", "dst", 0, nil)
	if f.Version != Version {
		t.Errorf("NewFrame version: got %d, want %d", f.Version, Version)
	}
}

func TestPadIDParseIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"short string", "org1/agent"},
		{"max-length string", strings.Repeat("x", IDSize)},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			padded := PadID(tt.input)
			parsed := ParseID(padded)

			// For max-length, the full 32 bytes are used with no null terminator
			want := tt.input
			if len(want) > IDSize {
				want = want[:IDSize]
			}
			if parsed != want {
				t.Errorf("PadID/ParseID round-trip: got %q, want %q", parsed, want)
			}
		})
	}
}

func TestDecodeRejectsTruncatedData(t *testing.T) {
	data := make([]byte, HeaderSize-1)
	_, err := Decode(data)
	if err == nil {
		t.Fatal("expected error for truncated data, got nil")
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	f := NewFrame(MsgCommand, [16]byte{}, "src", "dst", 0, []byte("hello"))
	encoded := Encode(f)
	// Corrupt version byte
	encoded[0] = 0xFF

	_, err := Decode(encoded)
	if err == nil {
		t.Fatal("expected error for wrong version, got nil")
	}
}

func TestIsEncrypted(t *testing.T) {
	encrypted := NewFrame(MsgCommand, [16]byte{}, "s", "d", FlagEncrypted, nil)
	if !encrypted.IsEncrypted() {
		t.Error("expected IsEncrypted() to be true when FlagEncrypted is set")
	}

	notEncrypted := NewFrame(MsgCommand, [16]byte{}, "s", "d", 0, nil)
	if notEncrypted.IsEncrypted() {
		t.Error("expected IsEncrypted() to be false when FlagEncrypted is not set")
	}

	// FlagEncrypted combined with other flags
	combined := NewFrame(MsgCommand, [16]byte{}, "s", "d", FlagEncrypted|FlagCompressed, nil)
	if !combined.IsEncrypted() {
		t.Error("expected IsEncrypted() to be true when FlagEncrypted is set among other flags")
	}
}

func TestValidateRejectsZeroMsgType(t *testing.T) {
	f := &Frame{
		Version: Version,
		MsgType: 0,
	}
	if err := f.Validate(); err == nil {
		t.Fatal("expected error for zero MsgType, got nil")
	}
}

func TestValidateAcceptsValidFrame(t *testing.T) {
	f := NewFrame(MsgCommand, [16]byte{}, "s", "d", 0, nil)
	if err := f.Validate(); err != nil {
		t.Fatalf("unexpected error for valid frame: %v", err)
	}
}

func TestLargePayloadEncodeDecode(t *testing.T) {
	payload := make([]byte, 1024*1024) // 1 MiB
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	original := NewFrame(MsgStreamData, [16]byte{0xAB}, "src", "dst", FlagStreamContinued, payload)
	encoded := Encode(original)
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode large payload failed: %v", err)
	}

	if !bytes.Equal(decoded.Payload, payload) {
		t.Error("large payload mismatch after encode/decode round-trip")
	}
}
