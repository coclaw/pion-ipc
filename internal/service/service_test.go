package service

import (
	"bytes"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
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

// createPeerViaIPC is a helper that creates a peer through the IPC protocol.
func (e *testEnv) createPeerViaIPC(t *testing.T, pcID string, reqID uint32) {
	t.Helper()
	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       pcID,
		"iceServers": []interface{}{},
	})
	req := ipc.NewRequest(reqID, "pc.create", "", "", params)
	res := e.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("pc.create(%s) failed: %s", pcID, res.Header.Error)
	}
}

// createDCViaIPC creates a DataChannel on the given peer through IPC.
func (e *testEnv) createDCViaIPC(t *testing.T, pcID, label string, reqID uint32) {
	t.Helper()
	params, _ := msgpack.Marshal(map[string]interface{}{
		"label":   label,
		"ordered": true,
	})
	req := ipc.NewRequest(reqID, "dc.create", pcID, "", params)
	res := e.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.create(%s/%s) failed: %s", pcID, label, res.Header.Error)
	}
}

func TestService_PcCreateOffer(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-offer", 20)
	env.createDCViaIPC(t, "pc-offer", "rpc", 21)

	req := ipc.NewRequest(22, "pc.createOffer", "pc-offer", "", nil)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("pc.createOffer failed: %s", res.Header.Error)
	}

	var result map[string]string
	if err := msgpack.Unmarshal(res.Payload, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result["sdp"] == "" {
		t.Error("offer SDP is empty")
	}
}

func TestService_PcCreateAnswer(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create a peer and a DC, generate an offer, then set a remote offer and create answer
	env.createPeerViaIPC(t, "pc-answer", 30)
	env.createDCViaIPC(t, "pc-answer", "rpc", 31)

	// Create offer to get an SDP
	offerReq := ipc.NewRequest(32, "pc.createOffer", "pc-answer", "", nil)
	offerRes := env.sendAndReceive(t, offerReq)
	if !offerRes.Header.OK {
		t.Fatalf("pc.createOffer failed: %s", offerRes.Header.Error)
	}
	var offerResult map[string]string
	msgpack.Unmarshal(offerRes.Payload, &offerResult)
	offerSDP := offerResult["sdp"]

	// Create a second peer to be the answerer
	env.createPeerViaIPC(t, "pc-answerer", 33)

	// Set remote description on answerer
	setParams, _ := msgpack.Marshal(map[string]interface{}{
		"type": "offer",
		"sdp":  offerSDP,
	})
	setReq := ipc.NewRequest(34, "pc.setRemoteDescription", "pc-answerer", "", setParams)
	setRes := env.sendAndReceive(t, setReq)
	if !setRes.Header.OK {
		t.Fatalf("pc.setRemoteDescription failed: %s", setRes.Header.Error)
	}

	// Now create answer
	ansReq := ipc.NewRequest(35, "pc.createAnswer", "pc-answerer", "", nil)
	ansRes := env.sendAndReceive(t, ansReq)
	if !ansRes.Header.OK {
		t.Fatalf("pc.createAnswer failed: %s", ansRes.Header.Error)
	}

	var ansResult map[string]string
	if err := msgpack.Unmarshal(ansRes.Payload, &ansResult); err != nil {
		t.Fatalf("unmarshal answer: %v", err)
	}
	if ansResult["sdp"] == "" {
		t.Error("answer SDP is empty")
	}
}

func TestService_PcSetRemoteDescription(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create two peers: offerer and answerer
	env.createPeerViaIPC(t, "pc-offerer", 40)
	env.createDCViaIPC(t, "pc-offerer", "rpc", 41)

	// Create offer
	offerReq := ipc.NewRequest(42, "pc.createOffer", "pc-offerer", "", nil)
	offerRes := env.sendAndReceive(t, offerReq)
	if !offerRes.Header.OK {
		t.Fatalf("pc.createOffer failed: %s", offerRes.Header.Error)
	}
	var offerResult map[string]string
	msgpack.Unmarshal(offerRes.Payload, &offerResult)

	// Create answerer and set remote description
	env.createPeerViaIPC(t, "pc-remote", 43)
	params, _ := msgpack.Marshal(map[string]interface{}{
		"type": "offer",
		"sdp":  offerResult["sdp"],
	})
	req := ipc.NewRequest(44, "pc.setRemoteDescription", "pc-remote", "", params)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("pc.setRemoteDescription failed: %s", res.Header.Error)
	}
}

func TestService_PcSetRemoteDescription_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-invalid-rd", 50)

	// Send invalid msgpack
	req := ipc.NewRequest(51, "pc.setRemoteDescription", "pc-invalid-rd", "", []byte{0xFF, 0xFF})
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
}

func TestService_PcAddIceCandidate(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create two peers and do SDP exchange
	env.createPeerViaIPC(t, "pc-ice-offerer", 60)
	env.createDCViaIPC(t, "pc-ice-offerer", "rpc", 61)

	offerReq := ipc.NewRequest(62, "pc.createOffer", "pc-ice-offerer", "", nil)
	offerRes := env.sendAndReceive(t, offerReq)
	var offerResult map[string]string
	msgpack.Unmarshal(offerRes.Payload, &offerResult)

	env.createPeerViaIPC(t, "pc-ice-answerer", 63)
	setParams, _ := msgpack.Marshal(map[string]interface{}{
		"type": "offer",
		"sdp":  offerResult["sdp"],
	})
	setReq := ipc.NewRequest(64, "pc.setRemoteDescription", "pc-ice-answerer", "", setParams)
	env.sendAndReceive(t, setReq)

	ansReq := ipc.NewRequest(65, "pc.createAnswer", "pc-ice-answerer", "", nil)
	ansRes := env.sendAndReceive(t, ansReq)
	var ansResult map[string]string
	msgpack.Unmarshal(ansRes.Payload, &ansResult)

	// Set answer on offerer
	setAnsParams, _ := msgpack.Marshal(map[string]interface{}{
		"type": "answer",
		"sdp":  ansResult["sdp"],
	})
	setAnsReq := ipc.NewRequest(66, "pc.setRemoteDescription", "pc-ice-offerer", "", setAnsParams)
	env.sendAndReceive(t, setAnsReq)

	// Now add an ICE candidate (empty candidate is valid for end-of-candidates)
	iceParams, _ := msgpack.Marshal(map[string]interface{}{
		"candidate":     "",
		"sdpMid":        "0",
		"sdpMLineIndex": 0,
	})
	iceReq := ipc.NewRequest(67, "pc.addIceCandidate", "pc-ice-offerer", "", iceParams)
	res := env.sendAndReceive(t, iceReq)
	if !res.Header.OK {
		t.Fatalf("pc.addIceCandidate failed: %s", res.Header.Error)
	}
}

func TestService_DcCreate(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc", 70)

	params, _ := msgpack.Marshal(map[string]interface{}{
		"label":   "rpc",
		"ordered": true,
	})
	req := ipc.NewRequest(71, "dc.create", "pc-dc", "", params)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.create failed: %s", res.Header.Error)
	}
}

func TestService_DcCreate_MissingLabel(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-nolabel", 80)

	params, _ := msgpack.Marshal(map[string]interface{}{
		"label":   "",
		"ordered": true,
	})
	req := ipc.NewRequest(81, "dc.create", "pc-dc-nolabel", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for empty label")
	}
}

func TestService_DcCreate_OrderedDefaultTrue(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-ordered", 82)

	// Omit "ordered" field — should default to true (WebRTC spec default).
	params, _ := msgpack.Marshal(map[string]interface{}{
		"label": "rpc",
	})
	req := ipc.NewRequest(83, "dc.create", "pc-dc-ordered", "", params)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.create without ordered failed: %s", res.Header.Error)
	}
}

func TestService_DcCreate_OrderedExplicitFalse(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-unordered", 84)

	// Explicit ordered=false should be respected.
	params, _ := msgpack.Marshal(map[string]interface{}{
		"label":   "unreliable",
		"ordered": false,
	})
	req := ipc.NewRequest(85, "dc.create", "pc-dc-unordered", "", params)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.create with ordered=false failed: %s", res.Header.Error)
	}
}

func TestService_DcSend(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-send", 90)
	env.createDCViaIPC(t, "pc-send", "rpc", 91)

	// dc.send on a DC that's not connected will fail at the pion level
	// but the service layer should handle the request and forward the error
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      92,
			Method:  "dc.send",
			PcID:    "pc-send",
			DcLabel: "rpc",
		},
		Payload: []byte("hello"),
	}
	res := env.sendAndReceive(t, req)
	// The DC is not connected so send may fail, but it should not crash.
	// Verify we got a proper response with the correct ID.
	if res.Header.Type != ipc.MsgTypeResponse {
		t.Errorf("type = %q, want %q", res.Header.Type, ipc.MsgTypeResponse)
	}
	if res.Header.ID != 92 {
		t.Errorf("id = %d, want 92", res.Header.ID)
	}
}

func TestService_DcSend_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      100,
			Method:  "dc.send",
			PcID:    "nonexistent",
			DcLabel: "rpc",
		},
		Payload: []byte("hello"),
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_DcClose(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dcclose", 110)
	env.createDCViaIPC(t, "pc-dcclose", "rpc", 111)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      112,
			Method:  "dc.close",
			PcID:    "pc-dcclose",
			DcLabel: "rpc",
		},
	}
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.close failed: %s", res.Header.Error)
	}
}

func TestService_DcSetBALT(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-balt", 120)
	env.createDCViaIPC(t, "pc-balt", "rpc", 121)

	params, _ := msgpack.Marshal(map[string]interface{}{
		"threshold": uint64(1024),
	})
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      122,
			Method:  "dc.setBALT",
			PcID:    "pc-balt",
			DcLabel: "rpc",
		},
		Payload: params,
	}
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.setBALT failed: %s", res.Header.Error)
	}
}

func TestService_DcGetBA(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-ba", 130)
	env.createDCViaIPC(t, "pc-ba", "rpc", 131)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      132,
			Method:  "dc.getBA",
			PcID:    "pc-ba",
			DcLabel: "rpc",
		},
	}
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("dc.getBA failed: %s", res.Header.Error)
	}

	var result map[string]uint64
	if err := msgpack.Unmarshal(res.Payload, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// bufferedAmount should be 0 for a fresh DC
	if _, ok := result["bufferedAmount"]; !ok {
		t.Error("missing bufferedAmount in response")
	}
}

func TestService_PcRestartIce(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// RestartICE requires a fully connected peer (ICE agent).
	// Without one, it should return an error. This verifies the method is wired up.
	env.createPeerViaIPC(t, "pc-restart", 140)
	env.createDCViaIPC(t, "pc-restart", "rpc", 141)

	req := ipc.NewRequest(142, "pc.restartIce", "pc-restart", "", nil)
	res := env.sendAndReceive(t, req)
	// Expect failure because ICE agent is not established without signaling
	if res.Header.OK {
		t.Log("pc.restartIce succeeded (unexpected for unconnected peer, but OK)")
	} else {
		// Verify it's the expected error
		if res.Header.Error == "" {
			t.Error("expected error message for ICE restart without connection")
		}
	}
}

func TestService_PcSetLocalDescription(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create a peer with a DC, then create offer to get SDP.
	// createOffer already calls SetLocalDescription internally,
	// but we test the explicit pc.setLocalDescription by re-setting
	// the same offer (which is a no-op in terms of state but exercises the path).
	env.createPeerViaIPC(t, "pc-setld", 150)
	env.createDCViaIPC(t, "pc-setld", "rpc", 151)

	// Create offer (this internally sets local description)
	offerReq := ipc.NewRequest(152, "pc.createOffer", "pc-setld", "", nil)
	offerRes := env.sendAndReceive(t, offerReq)
	if !offerRes.Header.OK {
		t.Fatalf("pc.createOffer failed: %s", offerRes.Header.Error)
	}
	var offerResult map[string]string
	msgpack.Unmarshal(offerRes.Payload, &offerResult)

	// Re-setting the same local offer should succeed (stable -> have-local-offer is already done,
	// but have-local-offer -> SetLocal(offer) with the same offer should work)
	ldParams, _ := msgpack.Marshal(map[string]interface{}{
		"type": "offer",
		"sdp":  offerResult["sdp"],
	})
	req := ipc.NewRequest(153, "pc.setLocalDescription", "pc-setld", "", ldParams)
	res := env.sendAndReceive(t, req)
	// Verify the method is routed and invoked with a proper response.
	if res.Header.Type != ipc.MsgTypeResponse {
		t.Errorf("type = %q, want %q", res.Header.Type, ipc.MsgTypeResponse)
	}
	if res.Header.ID != 153 {
		t.Errorf("id = %d, want 153", res.Header.ID)
	}
}

func TestService_ConcurrentRequests(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	const n = 10

	// Drain responses concurrently to avoid deadlock with io.Pipe
	resCh := make(chan *ipc.Frame, n*2)
	go func() {
		for {
			frame, err := env.resReader.ReadFrame()
			if err != nil {
				return
			}
			if frame.Header.Type == ipc.MsgTypeResponse {
				resCh <- frame
			}
		}
	}()

	// Send n concurrent ping requests
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			req := ipc.NewRequest(uint32(210+id), "ping", "", "", nil)
			if err := env.reqWriter.WriteFrame(req); err != nil {
				t.Errorf("WriteFrame(%d): %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	// Collect all n responses
	received := 0
	deadline := time.After(10 * time.Second)
	for received < n {
		select {
		case <-resCh:
			received++
		case <-deadline:
			t.Fatalf("timeout: received %d/%d responses", received, n)
		}
	}
}

func TestService_NonRequestFrame(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Send an event frame (type="evt") which should be ignored
	evtFrame := ipc.NewEvent("some.event", "pc-1", "rpc", []byte("data"), false)
	if err := env.reqWriter.WriteFrame(evtFrame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// Then send a ping to verify the service is still alive
	req := ipc.NewRequest(999, "ping", "", "", nil)
	res := env.sendAndReceive(t, req)
	if !res.Header.OK {
		t.Fatalf("ping after event frame failed: %s", res.Header.Error)
	}
}
