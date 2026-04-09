package ipc

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestRoundtrip_Request(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	f := NewRequest(1, "pc.create", "pc-1", "", []byte(`{"test":true}`))

	go func() {
		if err := w.WriteFrame(f); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		pw.Close()
	}()

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Type != MsgTypeRequest {
		t.Errorf("type = %q", got.Header.Type)
	}
	if got.Header.ID != 1 {
		t.Errorf("id = %d", got.Header.ID)
	}
	if got.Header.Method != "pc.create" {
		t.Errorf("method = %q", got.Header.Method)
	}
	if got.Header.PcID != "pc-1" {
		t.Errorf("pcId = %q", got.Header.PcID)
	}
	if !bytes.Equal(got.Payload, []byte(`{"test":true}`)) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestRoundtrip_Response(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	f := NewResponse(42, true, []byte("result"), "")

	go func() {
		if err := w.WriteFrame(f); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		pw.Close()
	}()

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Type != MsgTypeResponse {
		t.Errorf("type = %q", got.Header.Type)
	}
	if !got.Header.OK {
		t.Error("ok = false")
	}
	if !bytes.Equal(got.Payload, []byte("result")) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestRoundtrip_Event(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	f := NewEvent("dc.message", "pc-1", "rpc", []byte("event data"), false)

	go func() {
		if err := w.WriteFrame(f); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		pw.Close()
	}()

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Header.Type != MsgTypeEvent {
		t.Errorf("type = %q", got.Header.Type)
	}
	if got.Header.Event != "dc.message" {
		t.Errorf("event = %q", got.Header.Event)
	}
	if !bytes.Equal(got.Payload, []byte("event data")) {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestRoundtrip_BinaryPayload(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	// Create a binary payload with all byte values
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	f := NewEvent("dc.message", "pc-1", "data", payload, true)

	go func() {
		if err := w.WriteFrame(f); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		pw.Close()
	}()

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !got.Header.IsBinary {
		t.Error("isBinary = false")
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("binary payload mismatch: got %d bytes, want 256", len(got.Payload))
	}
}

func TestRoundtrip_MultipleMessages(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	frames := []*Frame{
		NewRequest(1, "ping", "", "", nil),
		NewResponse(1, true, []byte("pong"), ""),
		NewEvent("pc.statechange", "pc-1", "", []byte("connected"), false),
		NewRequest(2, "dc.send", "pc-1", "rpc", []byte("hello")),
	}

	go func() {
		for _, f := range frames {
			if err := w.WriteFrame(f); err != nil {
				t.Errorf("WriteFrame: %v", err)
			}
		}
		pw.Close()
	}()

	for i, want := range frames {
		got, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame #%d: %v", i, err)
		}
		if got.Header.Type != want.Header.Type {
			t.Errorf("frame #%d: type = %q, want %q", i, got.Header.Type, want.Header.Type)
		}
		if got.Header.ID != want.Header.ID {
			t.Errorf("frame #%d: id = %d, want %d", i, got.Header.ID, want.Header.ID)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("frame #%d: payload mismatch", i)
		}
	}
}

func TestRoundtrip_AllByteValues(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	// Build a payload containing all 256 byte values
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i)
	}

	f := NewRequest(1, "test", "", "", payload)

	go func() {
		if err := w.WriteFrame(f); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		pw.Close()
	}()

	got, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("payload mismatch: got %d bytes", len(got.Payload))
		for i := range payload {
			if i >= len(got.Payload) || got.Payload[i] != payload[i] {
				t.Errorf("first diff at index %d", i)
				break
			}
		}
	}
}

func TestRoundtrip_ConcurrentSendReceive(t *testing.T) {
	pr, pw := io.Pipe()
	w := NewWriter(pw)
	r := NewReader(pr)

	const n = 50
	var wg sync.WaitGroup

	// Writer goroutines
	wg.Add(1)
	go func() {
		defer wg.Done()
		var writeWg sync.WaitGroup
		writeWg.Add(n)
		for i := 0; i < n; i++ {
			go func(id int) {
				defer writeWg.Done()
				f := NewRequest(uint32(id), "test", "", "", []byte("payload"))
				if err := w.WriteFrame(f); err != nil {
					t.Errorf("WriteFrame(%d): %v", id, err)
				}
			}(i)
		}
		writeWg.Wait()
		pw.Close()
	}()

	// Reader
	received := 0
	for {
		_, err := r.ReadFrame()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("ReadFrame: %v", err)
		}
		received++
	}

	wg.Wait()
	if received != n {
		t.Errorf("received %d frames, want %d", received, n)
	}
}
