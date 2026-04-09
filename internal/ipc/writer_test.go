package ipc

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestWriter_SingleFrame(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	f := &Frame{
		Header:  Header{Type: MsgTypeRequest, ID: 1, Method: "ping"},
		Payload: []byte("hi"),
	}
	if err := w.WriteFrame(f); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// Verify by reading back
	reader := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Method != "ping" {
		t.Errorf("method = %q", got.Header.Method)
	}
	if !bytes.Equal(got.Payload, []byte("hi")) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestWriter_ConcurrentWrites(t *testing.T) {
	var buf lockedBuffer
	w := NewWriter(&buf)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			f := &Frame{
				Header:  Header{Type: MsgTypeRequest, ID: uint32(id), Method: "test"},
				Payload: []byte("data"),
			}
			if err := w.WriteFrame(f); err != nil {
				t.Errorf("WriteFrame(%d): %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	// Read all frames back and verify none are corrupt
	reader := NewReader(bytes.NewReader(buf.Bytes()))
	count := 0
	for {
		_, err := reader.ReadFrame()
		if err != nil {
			break
		}
		count++
	}
	if count != n {
		t.Errorf("read %d frames, want %d", count, n)
	}
}

func TestWriter_SendResponse(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendResponse(10, true, []byte("ok"), ""); err != nil {
		t.Fatalf("SendResponse: %v", err)
	}

	reader := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Type != MsgTypeResponse {
		t.Errorf("type = %q", got.Header.Type)
	}
	if got.Header.ID != 10 {
		t.Errorf("id = %d", got.Header.ID)
	}
	if !got.Header.OK {
		t.Error("ok = false")
	}
	if !bytes.Equal(got.Payload, []byte("ok")) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestWriter_SendResponse_Error(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendResponse(11, false, nil, "something failed"); err != nil {
		t.Fatalf("SendResponse: %v", err)
	}

	reader := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.OK {
		t.Error("ok = true, want false")
	}
	if got.Header.Error != "something failed" {
		t.Errorf("error = %q", got.Header.Error)
	}
}

func TestWriter_SendEvent(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.SendEvent("dc.message", "pc-1", "rpc", []byte{0xFF}, true); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}

	reader := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Type != MsgTypeEvent {
		t.Errorf("type = %q", got.Header.Type)
	}
	if got.Header.Event != "dc.message" {
		t.Errorf("event = %q", got.Header.Event)
	}
	if got.Header.PcID != "pc-1" {
		t.Errorf("pcId = %q", got.Header.PcID)
	}
	if got.Header.DcLabel != "rpc" {
		t.Errorf("dcLabel = %q", got.Header.DcLabel)
	}
	if !got.Header.IsBinary {
		t.Error("isBinary = false")
	}
	if !bytes.Equal(got.Payload, []byte{0xFF}) {
		t.Errorf("payload = %x", got.Payload)
	}
}

func TestWriter_WriteFrame_WriterError(t *testing.T) {
	ew := &errWriter{err: fmt.Errorf("broken pipe")}
	w := NewWriter(ew)

	f := &Frame{
		Header:  Header{Type: MsgTypeRequest, ID: 1, Method: "ping"},
		Payload: nil,
	}
	err := w.WriteFrame(f)
	if err == nil {
		t.Fatal("expected error from writer")
	}
	if !strings.Contains(err.Error(), "write frame") {
		t.Errorf("error = %q, want containing 'write frame'", err)
	}
	if !strings.Contains(err.Error(), "broken pipe") {
		t.Errorf("error = %q, want containing 'broken pipe'", err)
	}
}

// errWriter always returns an error on Write.
type errWriter struct {
	err error
}

func (w *errWriter) Write(p []byte) (int, error) {
	return 0, w.err
}

// lockedBuffer is a thread-safe bytes.Buffer for concurrent write tests.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}
