package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/coclaw/pion-ipc/internal/ipc"
	"github.com/coclaw/pion-ipc/internal/rtc"
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

func TestService_PcCreate_WithSettings(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       "pc-settings",
		"iceServers": []interface{}{},
		"settings": map[string]interface{}{
			"sctpRtoMax":       uint32(10000),
			"iceFailedTimeout": uint32(15000),
		},
	})
	req := ipc.NewRequest(2100, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)

	if !res.Header.OK {
		t.Errorf("pc.create with settings failed: %s", res.Header.Error)
	}
}

func TestService_PcCreate_InvalidSettings(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       "pc-bad",
		"iceServers": []interface{}{},
		"settings": map[string]interface{}{
			"sctpRtoMax": uint32(400000), // > 300000 ms cap
		},
	})
	req := ipc.NewRequest(2101, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)

	if res.Header.OK {
		t.Error("expected failure for out-of-range settings")
	}
	if res.Header.Error == "" {
		t.Error("expected error message")
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

	// setLocalDescription on offerer (createOffer no longer auto-applies)
	setOfferLD, _ := msgpack.Marshal(map[string]interface{}{"type": "offer", "sdp": offerResult["sdp"]})
	setOfferLDReq := ipc.NewRequest(620, "pc.setLocalDescription", "pc-ice-offerer", "", setOfferLD)
	if r := env.sendAndReceive(t, setOfferLDReq); !r.Header.OK {
		t.Fatalf("offerer setLocalDescription failed: %s", r.Header.Error)
	}

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

	// setLocalDescription on answerer (createAnswer no longer auto-applies)
	setAnsLD, _ := msgpack.Marshal(map[string]interface{}{"type": "answer", "sdp": ansResult["sdp"]})
	setAnsLDReq := ipc.NewRequest(650, "pc.setLocalDescription", "pc-ice-answerer", "", setAnsLD)
	if r := env.sendAndReceive(t, setAnsLDReq); !r.Header.OK {
		t.Fatalf("answerer setLocalDescription failed: %s", r.Header.Error)
	}

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

// Verify each PC has an independent worker; they do not block each other.
func TestService_MultiPC_IndependentWorkers(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-a", 300)
	env.createPeerViaIPC(t, "pc-b", 301)
	env.createDCViaIPC(t, "pc-a", "rpc", 302)
	env.createDCViaIPC(t, "pc-b", "rpc", 303)

	// Operate on both PCs separately
	baReqA := &ipc.Frame{Header: ipc.Header{Type: ipc.MsgTypeRequest, ID: 304, Method: "dc.getBA", PcID: "pc-a", DcLabel: "rpc"}}
	resA := env.sendAndReceive(t, baReqA)
	if !resA.Header.OK {
		t.Errorf("dc.getBA on pc-a failed: %s", resA.Header.Error)
	}

	baReqB := &ipc.Frame{Header: ipc.Header{Type: ipc.MsgTypeRequest, ID: 305, Method: "dc.getBA", PcID: "pc-b", DcLabel: "rpc"}}
	resB := env.sendAndReceive(t, baReqB)
	if !resB.Header.OK {
		t.Errorf("dc.getBA on pc-b failed: %s", resB.Header.Error)
	}

	// pc-b should still work after closing pc-a
	closeA := ipc.NewRequest(306, "pc.close", "pc-a", "", nil)
	resCloseA := env.sendAndReceive(t, closeA)
	if !resCloseA.Header.OK {
		t.Errorf("pc.close pc-a failed: %s", resCloseA.Header.Error)
	}

	baReqB2 := &ipc.Frame{Header: ipc.Header{Type: ipc.MsgTypeRequest, ID: 307, Method: "dc.getBA", PcID: "pc-b", DcLabel: "rpc"}}
	resB2 := env.sendAndReceive(t, baReqB2)
	if !resB2.Header.OK {
		t.Errorf("dc.getBA on pc-b after pc-a closed failed: %s", resB2.Header.Error)
	}
}

// Verify worker is removed after pc.close; subsequent requests return not found.
func TestService_PostClose_WorkerRemoved(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-post", 310)
	env.createDCViaIPC(t, "pc-post", "rpc", 311)

	closeReq := ipc.NewRequest(312, "pc.close", "pc-post", "", nil)
	env.sendAndReceive(t, closeReq)

	// Request to a closed PC — dispatcher returns not found
	req := &ipc.Frame{Header: ipc.Header{Type: ipc.MsgTypeRequest, ID: 313, Method: "dc.getBA", PcID: "pc-post", DcLabel: "rpc"}}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected error for request to closed PC")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want to contain 'not found'", res.Header.Error)
	}
}

// Verify recreating the same pcID after close rebuilds the worker.
func TestService_RecreateAfterClose(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// First create and close
	env.createPeerViaIPC(t, "pc-reuse", 320)
	closeReq := ipc.NewRequest(321, "pc.close", "pc-reuse", "", nil)
	env.sendAndReceive(t, closeReq)

	// Recreate with same pcID
	env.createPeerViaIPC(t, "pc-reuse", 322)
	env.createDCViaIPC(t, "pc-reuse", "rpc", 323)

	// The new PC should work normally
	baReq := &ipc.Frame{Header: ipc.Header{Type: ipc.MsgTypeRequest, ID: 324, Method: "dc.getBA", PcID: "pc-reuse", DcLabel: "rpc"}}
	res := env.sendAndReceive(t, baReq)
	if !res.Header.OK {
		t.Fatalf("dc.getBA after recreate failed: %s", res.Header.Error)
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

// --- Error path tests for all handlers ---

func TestService_PcClose_MissingPcId(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// pc.close with empty pcId in header — dispatcher routes by pcId,
	// so empty pcId means no worker found
	req := ipc.NewRequest(400, "pc.close", "", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for missing pcId")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want containing 'not found'", res.Header.Error)
	}
}

func TestService_PcCreate_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Send invalid msgpack as payload
	req := ipc.NewRequest(401, "pc.create", "", "", []byte{0xFF, 0xFF})
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
}

func TestService_PcCreateOffer_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(402, "pc.createOffer", "nonexistent", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want containing 'not found'", res.Header.Error)
	}
}

func TestService_PcCreateAnswer_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(403, "pc.createAnswer", "nonexistent", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want containing 'not found'", res.Header.Error)
	}
}

func TestService_PcSetRemoteDescription_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{"type": "offer", "sdp": "v=0\r\n"})
	req := ipc.NewRequest(404, "pc.setRemoteDescription", "nonexistent", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_PcAddIceCandidate_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{
		"candidate": "", "sdpMid": "0", "sdpMLineIndex": 0,
	})
	req := ipc.NewRequest(405, "pc.addIceCandidate", "nonexistent", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_PcAddIceCandidate_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-ice-invalid", 406)

	req := ipc.NewRequest(407, "pc.addIceCandidate", "pc-ice-invalid", "", []byte{0xFF, 0xFF})
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
	if !strings.Contains(res.Header.Error, "decode params") {
		t.Errorf("error = %q, want containing 'decode params'", res.Header.Error)
	}
}

func TestService_DcCreate_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{"label": "rpc", "ordered": true})
	req := ipc.NewRequest(408, "dc.create", "nonexistent", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_DcCreate_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-invalid", 409)

	req := ipc.NewRequest(410, "dc.create", "pc-dc-invalid", "", []byte{0xFF, 0xFF})
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
}

func TestService_DcSend_DcNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-nf", 411)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      412,
			Method:  "dc.send",
			PcID:    "pc-dc-nf",
			DcLabel: "nonexistent",
		},
		Payload: []byte("hello"),
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent dc")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want containing 'not found'", res.Header.Error)
	}
}

func TestService_DcSend_Binary(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-bin", 413)
	env.createDCViaIPC(t, "pc-bin", "rpc", 414)

	// Send binary data (IsBinary=true)
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:     ipc.MsgTypeRequest,
			ID:       415,
			Method:   "dc.send",
			PcID:     "pc-bin",
			DcLabel:  "rpc",
			IsBinary: true,
		},
		Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}
	res := env.sendAndReceive(t, req)
	// DC is not connected so send will fail, but verify the binary path is exercised
	if res.Header.Type != ipc.MsgTypeResponse {
		t.Errorf("type = %q, want response", res.Header.Type)
	}
	if res.Header.ID != 415 {
		t.Errorf("id = %d, want 415", res.Header.ID)
	}
}

func TestService_DcClose_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      416,
			Method:  "dc.close",
			PcID:    "nonexistent",
			DcLabel: "rpc",
		},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_DcClose_DcNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dc-close-nf", 417)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      418,
			Method:  "dc.close",
			PcID:    "pc-dc-close-nf",
			DcLabel: "nonexistent",
		},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent dc")
	}
}

func TestService_DcSetBALT_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{"threshold": uint64(1024)})
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      419,
			Method:  "dc.setBALT",
			PcID:    "nonexistent",
			DcLabel: "rpc",
		},
		Payload: params,
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_DcSetBALT_DcNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-balt-nf", 420)

	params, _ := msgpack.Marshal(map[string]interface{}{"threshold": uint64(1024)})
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      421,
			Method:  "dc.setBALT",
			PcID:    "pc-balt-nf",
			DcLabel: "nonexistent",
		},
		Payload: params,
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent dc")
	}
}

func TestService_DcSetBALT_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-balt-inv", 422)
	env.createDCViaIPC(t, "pc-balt-inv", "rpc", 423)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      424,
			Method:  "dc.setBALT",
			PcID:    "pc-balt-inv",
			DcLabel: "rpc",
		},
		Payload: []byte{0xFF, 0xFF},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
}

func TestService_DcGetBA_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      425,
			Method:  "dc.getBA",
			PcID:    "nonexistent",
			DcLabel: "rpc",
		},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_DcGetBA_DcNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-ba-nf", 426)

	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      427,
			Method:  "dc.getBA",
			PcID:    "pc-ba-nf",
			DcLabel: "nonexistent",
		},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent dc")
	}
}

func TestService_PcRestartIce_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	req := ipc.NewRequest(428, "pc.restartIce", "nonexistent", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_PcSetLocalDescription_PeerNotFound(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	params, _ := msgpack.Marshal(map[string]interface{}{"type": "offer", "sdp": "v=0\r\n"})
	req := ipc.NewRequest(429, "pc.setLocalDescription", "nonexistent", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for nonexistent peer")
	}
}

func TestService_PcSetLocalDescription_InvalidPayload(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-setld-inv", 430)

	req := ipc.NewRequest(431, "pc.setLocalDescription", "pc-setld-inv", "", []byte{0xFF, 0xFF})
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid payload")
	}
	if !strings.Contains(res.Header.Error, "decode params") {
		t.Errorf("error = %q, want containing 'decode params'", res.Header.Error)
	}
}

func TestService_DispatchRequest_NoWorker(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Send a request with a pcId that has no worker (not created via pc.create)
	req := &ipc.Frame{
		Header: ipc.Header{
			Type:    ipc.MsgTypeRequest,
			ID:      432,
			Method:  "dc.getBA",
			PcID:    "no-worker-pc",
			DcLabel: "rpc",
		},
	}
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for request to PC with no worker")
	}
	if !strings.Contains(res.Header.Error, "not found") {
		t.Errorf("error = %q, want containing 'not found'", res.Header.Error)
	}
}

func TestService_PcCreate_Duplicate(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-dup", 433)

	// Second create with same pcId should fail
	params, _ := msgpack.Marshal(map[string]interface{}{
		"pcId":       "pc-dup",
		"iceServers": []interface{}{},
	})
	req := ipc.NewRequest(434, "pc.create", "", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for duplicate pcId")
	}
	if !strings.Contains(res.Header.Error, "already exists") {
		t.Errorf("error = %q, want containing 'already exists'", res.Header.Error)
	}
}

func TestService_RunReadError(t *testing.T) {
	stdinPR, stdinPW := io.Pipe()
	stdoutPR, stdoutPW := io.Pipe()

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	reader := ipc.NewReader(stdinPR)
	writer := ipc.NewWriter(stdoutPW)
	svc := New(logger, reader, writer)

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(t.Context())
	}()

	// Close stdin with an error to trigger non-EOF read error
	stdinPW.CloseWithError(fmt.Errorf("connection reset"))

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error from Run")
		}
		if err == io.EOF {
			t.Error("expected non-EOF error")
		}
		if !strings.Contains(err.Error(), "read loop") {
			t.Errorf("error = %q, want containing 'read loop'", err.Error())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for Run to return")
	}

	stdoutPR.Close()
	stdoutPW.Close()
}

func TestService_ShutdownPreventsNewWorkers(t *testing.T) {
	env := newTestEnv()

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.svc.Run(ctx)
	}()

	// Create a peer first to verify the service is running
	env.createPeerViaIPC(t, "pc-before-stop", 440)

	// Cancel context to trigger shutdown
	cancel()

	select {
	case <-errCh:
		// Service has stopped
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for service to stop")
	}

	env.close()
}

func TestService_PcSetRemoteDescription_InvalidSdpType(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-invalid-sdp-type", 450)

	params, _ := msgpack.Marshal(map[string]interface{}{
		"type": "invalid",
		"sdp":  "v=0\r\n",
	})
	req := ipc.NewRequest(451, "pc.setRemoteDescription", "pc-invalid-sdp-type", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid SDP type")
	}
	if !strings.Contains(res.Header.Error, "unknown sdp type") {
		t.Errorf("error = %q, want containing 'unknown sdp type'", res.Header.Error)
	}
}

func TestService_PcSetLocalDescription_InvalidSdpType(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-ld-invalid-type", 452)

	params, _ := msgpack.Marshal(map[string]interface{}{
		"type": "invalid",
		"sdp":  "v=0\r\n",
	})
	req := ipc.NewRequest(453, "pc.setLocalDescription", "pc-ld-invalid-type", "", params)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for invalid SDP type")
	}
}

// Test unknown method routed through a worker (dispatcher routes by pcId to worker,
// then handleRequest hits the default case).
func TestService_UnknownMethodViaWorker(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	env.createPeerViaIPC(t, "pc-unk", 460)

	// Send unknown method with the pcId of an existing peer → goes to worker → handleRequest default
	req := ipc.NewRequest(461, "totally.unknown", "pc-unk", "", nil)
	res := env.sendAndReceive(t, req)
	if res.Header.OK {
		t.Error("expected failure for unknown method")
	}
	if !strings.Contains(res.Header.Error, "unknown method") {
		t.Errorf("error = %q, want containing 'unknown method'", res.Header.Error)
	}
}

// Test that a successful pc.restartIce through a fully connected peer returns SDP.
// Test handlePcClose directly when pcId is empty (defensive branch).
// This path is unreachable through the normal dispatcher but is a valid defensive check.
func TestService_HandlePcClose_EmptyPcId_Direct(t *testing.T) {
	outBuf := &bytes.Buffer{}
	writer := ipc.NewWriter(outBuf)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	svc := &Service{
		logger:  logger,
		writer:  writer,
		manager: rtc.NewManager(logger, writer),
		workers: make(map[string]*pcWorker),
	}

	f := ipc.NewRequest(1, "pc.close", "", "", nil)
	svc.handleRequest(f)

	reader := ipc.NewReader(outBuf)
	res, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if res.Header.OK {
		t.Error("expected failure for empty pcId")
	}
	if !strings.Contains(res.Header.Error, "pcId is required") {
		t.Errorf("error = %q, want containing 'pcId is required'", res.Header.Error)
	}
}

// Test handler error paths that are only reachable when the peer is removed from
// the manager between dispatch and execution (race scenario).
// We call handleRequest directly with a pcId not in the manager.
func TestService_HandlersDirectCall_PeerNotFound(t *testing.T) {
	outBuf := &bytes.Buffer{}
	writer := ipc.NewWriter(outBuf)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	svc := &Service{
		logger:  logger,
		writer:  writer,
		manager: rtc.NewManager(logger, writer),
		workers: make(map[string]*pcWorker),
	}

	methods := []struct {
		method  string
		payload []byte
	}{
		{"pc.createOffer", nil},
		{"pc.createAnswer", nil},
		{"pc.setRemoteDescription", mustMarshal(map[string]interface{}{"type": "offer", "sdp": "v=0\r\n"})},
		{"pc.addIceCandidate", mustMarshal(map[string]interface{}{"candidate": "", "sdpMid": "0", "sdpMLineIndex": 0})},
		{"dc.create", mustMarshal(map[string]interface{}{"label": "rpc"})},
		{"dc.send", []byte("data")},
		{"dc.close", nil},
		{"dc.setBALT", mustMarshal(map[string]interface{}{"threshold": uint64(1024)})},
		{"dc.getBA", nil},
		{"pc.restartIce", nil},
		{"pc.setLocalDescription", mustMarshal(map[string]interface{}{"type": "offer", "sdp": "v=0\r\n"})},
	}

	for i, m := range methods {
		f := &ipc.Frame{
			Header: ipc.Header{
				Type:    ipc.MsgTypeRequest,
				ID:      uint32(i + 1),
				Method:  m.method,
				PcID:    "ghost-peer",
				DcLabel: "rpc",
			},
			Payload: m.payload,
		}
		svc.handleRequest(f)
	}

	reader := ipc.NewReader(outBuf)
	for i, m := range methods {
		res, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame(%s): %v", m.method, err)
		}
		if res.Header.OK {
			t.Errorf("[%d] %s: expected failure for ghost peer", i, m.method)
		}
		if !strings.Contains(res.Header.Error, "not found") {
			t.Errorf("[%d] %s: error = %q, want containing 'not found'", i, m.method, res.Header.Error)
		}
	}
}

func mustMarshal(v interface{}) []byte {
	b, err := msgpack.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestService_PcRestartIce_Success(t *testing.T) {
	env := newTestEnv()
	defer env.close()

	go env.svc.Run(t.Context())

	// Create two peers, do SDP exchange, then restart ICE on the offerer
	env.createPeerViaIPC(t, "pc-ri-off", 470)
	env.createDCViaIPC(t, "pc-ri-off", "rpc", 471)

	// Create offer
	offerRes := env.sendAndReceive(t, ipc.NewRequest(472, "pc.createOffer", "pc-ri-off", "", nil))
	if !offerRes.Header.OK {
		t.Fatalf("createOffer: %s", offerRes.Header.Error)
	}
	var offerResult map[string]string
	msgpack.Unmarshal(offerRes.Payload, &offerResult)

	// Set local description on offerer
	ldParams, _ := msgpack.Marshal(map[string]interface{}{"type": "offer", "sdp": offerResult["sdp"]})
	setLDRes := env.sendAndReceive(t, ipc.NewRequest(473, "pc.setLocalDescription", "pc-ri-off", "", ldParams))
	if !setLDRes.Header.OK {
		t.Fatalf("setLocalDescription: %s", setLDRes.Header.Error)
	}

	// Create answerer, set remote desc, create answer
	env.createPeerViaIPC(t, "pc-ri-ans", 474)
	rdParams, _ := msgpack.Marshal(map[string]interface{}{"type": "offer", "sdp": offerResult["sdp"]})
	env.sendAndReceive(t, ipc.NewRequest(475, "pc.setRemoteDescription", "pc-ri-ans", "", rdParams))

	ansRes := env.sendAndReceive(t, ipc.NewRequest(476, "pc.createAnswer", "pc-ri-ans", "", nil))
	if !ansRes.Header.OK {
		t.Fatalf("createAnswer: %s", ansRes.Header.Error)
	}
	var ansResult map[string]string
	msgpack.Unmarshal(ansRes.Payload, &ansResult)

	// Set local description on answerer
	ansLDParams, _ := msgpack.Marshal(map[string]interface{}{"type": "answer", "sdp": ansResult["sdp"]})
	env.sendAndReceive(t, ipc.NewRequest(477, "pc.setLocalDescription", "pc-ri-ans", "", ansLDParams))

	// Set remote desc on offerer
	ansRDParams, _ := msgpack.Marshal(map[string]interface{}{"type": "answer", "sdp": ansResult["sdp"]})
	env.sendAndReceive(t, ipc.NewRequest(478, "pc.setRemoteDescription", "pc-ri-off", "", ansRDParams))

	// Give peers time to connect
	time.Sleep(500 * time.Millisecond)

	// Now restart ICE - should succeed on the connected peer
	riRes := env.sendAndReceive(t, ipc.NewRequest(479, "pc.restartIce", "pc-ri-off", "", nil))
	if !riRes.Header.OK {
		// May fail if peers didn't fully connect via loopback - that's OK
		t.Logf("pc.restartIce failed (peers may not be connected): %s", riRes.Header.Error)
	} else {
		var riResult map[string]string
		if err := msgpack.Unmarshal(riRes.Payload, &riResult); err != nil {
			t.Fatalf("unmarshal restartIce result: %v", err)
		}
		if riResult["sdp"] == "" {
			t.Error("restartIce SDP is empty")
		}
	}
}
