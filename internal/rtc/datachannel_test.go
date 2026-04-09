package rtc

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
)

// safeBuffer is a thread-safe bytes.Buffer for capturing IPC events.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := make([]byte, b.buf.Len())
	copy(cp, b.buf.Bytes())
	return cp
}

// dcTestPair creates two connected PeerConnections with a DataChannel pair.
// The offerer creates a DC with the given label; the answerer receives it.
// Returns the wrapped offerer DC, the raw answerer DC, and a cleanup function.
func dcTestPair(t *testing.T, label string) (
	offererDC *DataChannel, answererRawDC *webrtc.DataChannel, eventBuf *safeBuffer, cleanup func(),
) {
	t.Helper()

	pc1, pc2 := newTestPeerPair(t)

	eventBuf = &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(eventBuf)

	// pc2 receives DC
	answererDCCh := make(chan *webrtc.DataChannel, 1)
	pc2.OnDataChannel(func(dc *webrtc.DataChannel) {
		answererDCCh <- dc
	})

	// Create DC on offerer side and wrap it
	rawDC, err := pc1.CreateDataChannel(label, nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}
	offererDC = WrapDataChannel(rawDC, "pc-1", logger, writer)

	// Do signaling
	doSignaling(t, pc1, pc2)

	// Wait for answerer to receive DC
	select {
	case answererRawDC = <-answererDCCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for answerer DC")
	}

	cleanup = func() {
		pc1.Close()
		pc2.Close()
	}
	return
}

// readAllEvents reads all IPC event frames from a snapshot of the buffer.
func readAllEvents(buf *safeBuffer) []*ipc.Frame {
	data := buf.Snapshot()
	reader := ipc.NewReader(bytes.NewReader(data))
	var frames []*ipc.Frame
	for {
		f, err := reader.ReadFrame()
		if err != nil {
			break
		}
		frames = append(frames, f)
	}
	return frames
}

// waitForEvent polls the buffer until an event with the given name appears or timeout.
func waitForEvent(t *testing.T, buf *safeBuffer, eventName string, timeout time.Duration) *ipc.Frame {
	t.Helper()
	deadline := time.After(timeout)
	for {
		frames := readAllEvents(buf)
		for _, f := range frames {
			if f.Header.Event == eventName {
				return f
			}
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for event %q", eventName)
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestDataChannel_OnOpen_EmitsEvent(t *testing.T) {
	offererDC, _, eventBuf, cleanup := dcTestPair(t, "test-open")
	defer cleanup()
	_ = offererDC

	// WrapDataChannel already registered OnOpen which emits dc.open.
	// We must not call dc.OnOpen again (it would overwrite the callback).
	// Instead, just wait for the dc.open event in the buffer.
	f := waitForEvent(t, eventBuf, "dc.open", 10*time.Second)
	if f.Header.PcID != "pc-1" {
		t.Errorf("pcId = %q, want %q", f.Header.PcID, "pc-1")
	}
	if f.Header.DcLabel != "test-open" {
		t.Errorf("dcLabel = %q, want %q", f.Header.DcLabel, "test-open")
	}
}

func TestDataChannel_OnClose_EmitsEvent(t *testing.T) {
	offererDC, _, eventBuf, cleanup := dcTestPair(t, "test-close")
	defer cleanup()

	// Wait for DC open first (via event, don't overwrite callback)
	waitForEvent(t, eventBuf, "dc.open", 10*time.Second)

	// Close the DC
	offererDC.Close()

	// Wait for dc.close event
	f := waitForEvent(t, eventBuf, "dc.close", 5*time.Second)
	if f.Header.PcID != "pc-1" {
		t.Errorf("pcId = %q", f.Header.PcID)
	}
}

func TestDataChannel_OnMessage_TextAndBinary(t *testing.T) {
	offererDC, answererRawDC, eventBuf, cleanup := dcTestPair(t, "test-msg")
	defer cleanup()
	_ = offererDC

	// Wait for DC open via event (don't overwrite callbacks)
	waitForEvent(t, eventBuf, "dc.open", 10*time.Second)

	// Wait for answerer DC to open
	answererOpen := make(chan struct{})
	answererRawDC.OnOpen(func() {
		close(answererOpen)
	})
	select {
	case <-answererOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for answerer DC open")
	}

	// Send text from answerer to offerer (offerer has the wrapped DC with event emission)
	if err := answererRawDC.SendText("hello text"); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	// Wait for dc.message event (text)
	f := waitForEvent(t, eventBuf, "dc.message", 5*time.Second)
	if f.Header.IsBinary {
		t.Error("expected text message, got binary")
	}
	if string(f.Payload) != "hello text" {
		t.Errorf("payload = %q, want %q", f.Payload, "hello text")
	}

	// Send binary from answerer to offerer
	if err := answererRawDC.Send([]byte{0xDE, 0xAD}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for the binary dc.message event
	deadline := time.After(5 * time.Second)
	for {
		frames := readAllEvents(eventBuf)
		for _, fr := range frames {
			if fr.Header.Event == "dc.message" && fr.Header.IsBinary {
				if !bytes.Equal(fr.Payload, []byte{0xDE, 0xAD}) {
					t.Errorf("binary payload = %x, want dead", fr.Payload)
				}
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for binary dc.message event")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestDataChannel_BufferedAmount(t *testing.T) {
	offererDC, _, _, cleanup := dcTestPair(t, "test-ba")
	defer cleanup()

	// BufferedAmount should return a value (0 before open, or 0 since nothing sent)
	ba := offererDC.BufferedAmount()
	_ = ba // just ensure no panic; value is 0 before open
}

func TestDataChannel_SetBufferedAmountLowThreshold(t *testing.T) {
	offererDC, _, _, cleanup := dcTestPair(t, "test-balt")
	defer cleanup()

	// Should not panic
	offererDC.SetBufferedAmountLowThreshold(1024)
}

func TestDataChannel_SendAndSendText(t *testing.T) {
	offererDC, answererRawDC, eventBuf, cleanup := dcTestPair(t, "test-send")
	defer cleanup()

	// Wait for DC open via event
	waitForEvent(t, eventBuf, "dc.open", 10*time.Second)

	msgCh := make(chan webrtc.DataChannelMessage, 2)
	answererOpen := make(chan struct{})
	answererRawDC.OnOpen(func() {
		close(answererOpen)
	})
	answererRawDC.OnMessage(func(msg webrtc.DataChannelMessage) {
		msgCh <- msg
	})

	select {
	case <-answererOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for answerer DC open")
	}

	// Send text
	if err := offererDC.SendText("text msg"); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case msg := <-msgCh:
		if string(msg.Data) != "text msg" {
			t.Errorf("text msg = %q", msg.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for text message")
	}

	// Send binary
	if err := offererDC.Send([]byte{0x01, 0x02}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case msg := <-msgCh:
		if !bytes.Equal(msg.Data, []byte{0x01, 0x02}) {
			t.Errorf("binary msg = %x", msg.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for binary message")
	}
}

func TestDataChannel_Close(t *testing.T) {
	offererDC, _, _, cleanup := dcTestPair(t, "test-dc-close")
	defer cleanup()

	// Close should not panic
	offererDC.Close()
}

func TestDataChannel_Label(t *testing.T) {
	offererDC, _, _, cleanup := dcTestPair(t, "my-label")
	defer cleanup()

	if offererDC.Label() != "my-label" {
		t.Errorf("Label() = %q, want %q", offererDC.Label(), "my-label")
	}
}

// Verify that OnMessage event carries proper msgpack-decoded content when applicable.
func TestDataChannel_OnMessage_VerifyEventPayload(t *testing.T) {
	offererDC, answererRawDC, eventBuf, cleanup := dcTestPair(t, "test-verify")
	defer cleanup()
	_ = offererDC

	// Wait for DC open via event
	waitForEvent(t, eventBuf, "dc.open", 10*time.Second)

	answererOpen := make(chan struct{})
	answererRawDC.OnOpen(func() {
		close(answererOpen)
	})
	select {
	case <-answererOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout")
	}

	// Send a msgpack payload from answerer
	payload, _ := msgpack.Marshal(map[string]string{"key": "value"})
	if err := answererRawDC.Send(payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for dc.message event with binary flag
	deadline := time.After(5 * time.Second)
	for {
		frames := readAllEvents(eventBuf)
		for _, fr := range frames {
			if fr.Header.Event == "dc.message" && fr.Header.IsBinary {
				if !bytes.Equal(fr.Payload, payload) {
					t.Errorf("payload mismatch")
				}
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for binary event")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
