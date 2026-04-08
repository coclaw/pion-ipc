package service

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/webrtc-dc-ipc/internal/ipc"
)

// testEnv sets up a Service connected to in-memory pipes for testing.
type testEnv struct {
	// Write requests here (simulated stdin)
	reqWriter *ipc.Writer
	stdinPW   *io.PipeWriter

	// Read responses here (simulated stdout)
	resReader *ipc.Reader
	stdoutPR  *io.PipeReader

	svc *Service
}

func newTestEnv() *testEnv {
	stdinPR, stdinPW := io.Pipe()
	stdoutPR, stdoutPW := io.Pipe()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	reader := ipc.NewReader(stdinPR)
	writer := ipc.NewWriter(stdoutPW)

	svc := New(logger, reader, writer)

	return &testEnv{
		reqWriter: ipc.NewWriter(stdinPW),
		stdinPW:   stdinPW,
		resReader: ipc.NewReader(stdoutPR),
		stdoutPR:  stdoutPR,
		svc:       svc,
	}
}

func (e *testEnv) close() {
	e.stdinPW.Close()
	e.stdoutPR.Close()
}

// sendAndReceive sends a request frame and reads the response, skipping any event frames.
func (e *testEnv) sendAndReceive(t *testing.T, f *ipc.Frame) *ipc.Frame {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.reqWriter.WriteFrame(f)
	}()

	resCh := make(chan *ipc.Frame, 1)
	go func() {
		for {
			frame, err := e.resReader.ReadFrame()
			if err != nil {
				t.Errorf("ReadFrame: %v", err)
				return
			}
			// Skip event frames (e.g. pc.statechange emitted by callbacks)
			if frame.Header.Type == ipc.MsgTypeEvent {
				continue
			}
			resCh <- frame
			return
		}
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout writing request")
	}

	select {
	case frame := <-resCh:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading response")
		return nil
	}
}

func TestService_Ping(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(1, "ping", "", "", nil)
	res := env.sendAndReceive(t, req)

	if res.Header.Type != ipc.MsgTypeResponse {
		t.Errorf("type = %q, want %q", res.Header.Type, ipc.MsgTypeResponse)
	}
	if res.Header.ID != 1 {
		t.Errorf("id = %d, want 1", res.Header.ID)
	}
	if !res.Header.OK {
		t.Errorf("ok = false, error = %q", res.Header.Error)
	}
}

func TestService_PcCreate(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       "pc-1",
		"iceServers": []interface{}{},
	})
	req := ipc.NewRequest(2, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)

	if !res.Header.OK {
		t.Errorf("pc.create failed: %s", res.Header.Error)
	}
}

func TestService_PcCreate_MissingPcId(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId": "",
	})
	req := ipc.NewRequest(3, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)

	if res.Header.OK {
		t.Error("expected failure for missing pcId")
	}
}

func TestService_UnknownMethod(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(4, "nonexistent.method", "", "", nil)
	res := env.sendAndReceive(t, req)

	if res.Header.OK {
		t.Error("expected failure for unknown method")
	}
	if res.Header.Error == "" {
		t.Error("expected error message")
	}
}

func TestService_StdinClose(t *testing.T) {
	env := newTestEnv()

	errCh := make(chan error, 1)
	go func() {
		errCh <- env.svc.Run(t.Context())
	}()

	// Close stdin to signal shutdown
	env.stdinPW.Close()

	select {
	case err := <-errCh:
		if err != io.EOF {
			t.Errorf("Run returned %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for Run to return")
	}

	env.stdoutPR.Close()
}

func TestService_PcCreateAndClose(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create
	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       "pc-close-test",
		"iceServers": []interface{}{},
	})
	req := ipc.NewRequest(10, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("pc.create failed: %s", res.Header.Error)
	}

	// Close
	closeReq := ipc.NewRequest(11, "pc.close", "pc-close-test", "", nil)
	closeRes := env.sendAndReceive(t, closeReq)
	if !closeRes.Header.OK {
		t.Errorf("pc.close failed: %s", closeRes.Header.Error)
	}
}

func TestService_PcClose_NotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(12, "pc.close", "nonexistent", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for closing nonexistent peer")
	}
}
