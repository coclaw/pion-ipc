package ipc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestReader_SingleFrame(t *testing.T) {
	f := &Frame{
		Header:  Header{Type: MsgTypeRequest, ID: 1, Method: "ping"},
		Payload: []byte("data"),
	}
	encoded, err := EncodeFrame(f)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}

	reader := NewReader(bytes.NewReader(encoded))
	got, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Method != "ping" {
		t.Errorf("method = %q, want %q", got.Header.Method, "ping")
	}
	if !bytes.Equal(got.Payload, []byte("data")) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestReader_MultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := uint32(1); i <= 3; i++ {
		f := &Frame{
			Header:  Header{Type: MsgTypeRequest, ID: i, Method: "test"},
			Payload: nil,
		}
		encoded, err := EncodeFrame(f)
		if err != nil {
			t.Fatalf("EncodeFrame #%d: %v", i, err)
		}
		buf.Write(encoded)
	}

	reader := NewReader(&buf)
	for i := uint32(1); i <= 3; i++ {
		got, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame #%d: %v", i, err)
		}
		if got.Header.ID != i {
			t.Errorf("frame #%d: id = %d", i, got.Header.ID)
		}
	}

	// Next read should return EOF
	_, err := reader.ReadFrame()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReader_EOF(t *testing.T) {
	reader := NewReader(bytes.NewReader(nil))
	_, err := reader.ReadFrame()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReader_TruncatedLengthPrefix(t *testing.T) {
	// Only 2 bytes of a 4-byte length prefix
	reader := NewReader(bytes.NewReader([]byte{0x01, 0x02}))
	_, err := reader.ReadFrame()
	if err == nil {
		t.Fatal("expected error for truncated length prefix")
	}
}

func TestReader_TruncatedPayload(t *testing.T) {
	// Length prefix says 100 bytes but only 4 bytes follow
	var buf bytes.Buffer
	length := make([]byte, LengthPrefixSize)
	binary.LittleEndian.PutUint32(length, 100)
	buf.Write(length)
	buf.Write([]byte{0x01, 0x02, 0x03, 0x04})

	reader := NewReader(&buf)
	_, err := reader.ReadFrame()
	if err == nil {
		t.Fatal("expected error for truncated payload")
	}
	if !strings.Contains(err.Error(), "read frame payload") {
		t.Errorf("error = %q", err)
	}
}

func TestReader_ReadError(t *testing.T) {
	// An io.Reader that returns a non-EOF error
	errReader := &errOnReadReader{err: fmt.Errorf("disk failure")}
	reader := NewReader(errReader)
	_, err := reader.ReadFrame()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "disk failure") {
		t.Errorf("error = %q, want containing 'disk failure'", err)
	}
}

func TestReader_ZeroLengthFrame(t *testing.T) {
	// totalLen=0 means 0 bytes of body → DecodeFrame gets empty slice → too short
	var buf bytes.Buffer
	length := make([]byte, LengthPrefixSize)
	binary.LittleEndian.PutUint32(length, 0)
	buf.Write(length)

	reader := NewReader(&buf)
	_, err := reader.ReadFrame()
	if err == nil {
		t.Fatal("expected error for zero-length frame")
	}
	// DecodeFrame with empty data returns "frame too short"
	if !strings.Contains(err.Error(), "frame too short") {
		t.Errorf("error = %q", err)
	}
}

// errOnReadReader returns a fixed error on every Read call.
type errOnReadReader struct {
	err error
}

func (r *errOnReadReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func TestReader_OversizedFrame(t *testing.T) {
	var buf bytes.Buffer
	length := make([]byte, LengthPrefixSize)
	binary.LittleEndian.PutUint32(length, MaxFrameSize+1)
	buf.Write(length)

	reader := NewReader(&buf)
	_, err := reader.ReadFrame()
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
	if !strings.Contains(err.Error(), "frame payload too large") {
		t.Errorf("error = %q", err)
	}
}
