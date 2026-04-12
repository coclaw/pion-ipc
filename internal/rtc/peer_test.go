package rtc

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/coclaw/pion-ipc/internal/ipc"
)

// newTestPeerPair creates two raw Pion PeerConnections for local testing.
func newTestPeerPair(t *testing.T) (*webrtc.PeerConnection, *webrtc.PeerConnection) {
	t.Helper()
	s := webrtc.SettingEngine{}
	// Use a basic API with no network (UDP mux not needed for loopback)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(s))

	pc1, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection(1): %v", err)
	}
	pc2, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection(2): %v", err)
	}

	// Exchange ICE candidates
	pc1.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			if err := pc2.AddICECandidate(c.ToJSON()); err != nil {
				t.Logf("pc2.AddICECandidate: %v", err)
			}
		}
	})
	pc2.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			if err := pc1.AddICECandidate(c.ToJSON()); err != nil {
				t.Logf("pc1.AddICECandidate: %v", err)
			}
		}
	})

	return pc1, pc2
}

// doSignaling performs a full offer/answer SDP exchange.
func doSignaling(t *testing.T, offerer, answerer *webrtc.PeerConnection) {
	t.Helper()
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if err := offerer.SetLocalDescription(offer); err != nil {
		t.Fatalf("offerer.SetLocalDescription: %v", err)
	}
	if err := answerer.SetRemoteDescription(offer); err != nil {
		t.Fatalf("answerer.SetRemoteDescription: %v", err)
	}
	answer, err := answerer.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	if err := answerer.SetLocalDescription(answer); err != nil {
		t.Fatalf("answerer.SetLocalDescription: %v", err)
	}
	if err := offerer.SetRemoteDescription(answer); err != nil {
		t.Fatalf("offerer.SetRemoteDescription: %v", err)
	}
}

func waitForConnectionState(t *testing.T, pc *webrtc.PeerConnection, target webrtc.PeerConnectionState, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == target {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	// Check current state first
	if pc.ConnectionState() == target {
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for connection state %s (current: %s)", target, pc.ConnectionState())
	}
}

func TestPeerConnection_SDPExchange(t *testing.T) {
	pc1, pc2 := newTestPeerPair(t)
	defer pc1.Close()
	defer pc2.Close()

	// Create a DC to force SDP media section
	_, err := pc1.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, pc1, pc2)

	waitForConnectionState(t, pc1, webrtc.PeerConnectionStateConnected, 10*time.Second)
	waitForConnectionState(t, pc2, webrtc.PeerConnectionStateConnected, 10*time.Second)
}

func TestPeerConnection_DataChannel_Messaging(t *testing.T) {
	pc1, pc2 := newTestPeerPair(t)
	defer pc1.Close()
	defer pc2.Close()

	msgReceived := make(chan string, 1)

	// pc2 receives DC
	pc2.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			msgReceived <- string(msg.Data)
		})
	})

	dc1, err := pc1.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, pc1, pc2)

	// Wait for DC open
	dcOpen := make(chan struct{})
	dc1.OnOpen(func() {
		close(dcOpen)
	})
	select {
	case <-dcOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for DC open")
	}

	// Send message
	if err := dc1.SendText("hello from pc1"); err != nil {
		t.Fatalf("SendText: %v", err)
	}

	select {
	case msg := <-msgReceived:
		if msg != "hello from pc1" {
			t.Errorf("received = %q, want %q", msg, "hello from pc1")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestPeerConnection_ICERestart_DataChannelSurvives(t *testing.T) {
	pc1, pc2 := newTestPeerPair(t)
	defer pc1.Close()
	defer pc2.Close()

	pc2Messages := make(chan string, 10)

	pc2.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			pc2Messages <- string(msg.Data)
		})
		dc.OnOpen(func() {
			// Send a message back after DC is open on answerer side
			_ = dc.SendText("ack")
		})
	})

	dc1, err := pc1.CreateDataChannel("survive", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	pc1Messages := make(chan string, 10)
	dc1.OnMessage(func(msg webrtc.DataChannelMessage) {
		pc1Messages <- string(msg.Data)
	})

	doSignaling(t, pc1, pc2)

	// Wait for DC open
	dcOpen := make(chan struct{})
	dc1.OnOpen(func() {
		close(dcOpen)
	})
	select {
	case <-dcOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for DC open")
	}

	// Phase 1: verify communication works before ICE restart
	if err := dc1.SendText("before-restart"); err != nil {
		t.Fatalf("SendText before restart: %v", err)
	}
	select {
	case msg := <-pc2Messages:
		if msg != "before-restart" {
			t.Errorf("before restart: received = %q", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for pre-restart message")
	}

	// Phase 2: ICE restart
	offer, err := pc1.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		t.Fatalf("CreateOffer(ICERestart): %v", err)
	}
	if err := pc1.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription(restart offer): %v", err)
	}
	if err := pc2.SetRemoteDescription(offer); err != nil {
		t.Fatalf("pc2.SetRemoteDescription(restart offer): %v", err)
	}
	answer, err := pc2.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer(restart): %v", err)
	}
	if err := pc2.SetLocalDescription(answer); err != nil {
		t.Fatalf("pc2.SetLocalDescription(restart answer): %v", err)
	}
	if err := pc1.SetRemoteDescription(answer); err != nil {
		t.Fatalf("pc1.SetRemoteDescription(restart answer): %v", err)
	}

	// Wait for reconnection
	waitForConnectionState(t, pc1, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Phase 3: verify DataChannel still works after ICE restart
	// Give a moment for ICE to fully settle
	time.Sleep(200 * time.Millisecond)

	if err := dc1.SendText("after-restart"); err != nil {
		t.Fatalf("SendText after restart: %v", err)
	}
	select {
	case msg := <-pc2Messages:
		if msg != "after-restart" {
			t.Errorf("after restart: received = %q", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for post-restart message")
	}
}

func TestPeer_Wrapper(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	// CreateOffer should work (creates an offer with no media → may be empty but shouldn't error)
	// Actually, Pion requires at least one media or data channel to create an offer.
	_, err = peer.CreateDataChannel("test", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	sdp, err := peer.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if sdp == "" {
		t.Error("offer SDP is empty")
	}
}

func TestPeer_GetDataChannel(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	_, err = peer.CreateDataChannel("rpc", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	dc, err := peer.GetDataChannel("rpc")
	if err != nil {
		t.Fatalf("GetDataChannel: %v", err)
	}
	if dc.Label() != "rpc" {
		t.Errorf("label = %q, want %q", dc.Label(), "rpc")
	}

	// Not found
	_, err = peer.GetDataChannel("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent DC")
	}
}

func TestPeer_SetRemoteDescription_InvalidType(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	err = peer.SetRemoteDescription("invalid", "v=0\r\n")
	if err == nil {
		t.Fatal("expected error for invalid SDP type")
	}
}

func TestPeer_SetLocalDescription_InvalidType(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	err = peer.SetLocalDescription("invalid", "v=0\r\n")
	if err == nil {
		t.Fatal("expected error for invalid SDP type")
	}
}

func TestPeer_Close(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}

	_, err = peer.CreateDataChannel("dc1", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPeer_RestartICE(t *testing.T) {
	// RestartICE requires an active ICE agent, which means we need two peers
	// that have completed signaling.
	pc1, pc2 := newTestPeerPair(t)
	defer pc2.Close()

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	// Wrap pc1 as a Peer manually (use its underlying PC)
	peer := &Peer{
		id:     "test-peer",
		pc:     pc1,
		logger: logger.With("pcId", "test-peer"),
		writer: writer,
		dcs:    make(map[string]*DataChannel),
	}
	peer.iceState.Store("")
	peer.connState.Store("")
	defer peer.Close()

	// Create a DC so the offer has media
	_, err := pc1.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	// Do signaling to establish ICE agent
	doSignaling(t, pc1, pc2)
	waitForConnectionState(t, pc1, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Now RestartICE should work
	sdp, err := peer.RestartICE()
	if err != nil {
		t.Fatalf("RestartICE: %v", err)
	}
	if sdp == "" {
		t.Error("RestartICE returned empty SDP")
	}
}

func TestPeer_RemoveDataChannel(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	_, err = peer.CreateDataChannel("rpc", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	// Verify it exists
	_, err = peer.GetDataChannel("rpc")
	if err != nil {
		t.Fatalf("GetDataChannel before remove: %v", err)
	}

	// Remove it
	peer.RemoveDataChannel("rpc")

	// Should not be found now
	_, err = peer.GetDataChannel("rpc")
	if err == nil {
		t.Fatal("expected error after RemoveDataChannel")
	}
}

func TestPeer_Close_ThenCallMethods(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}

	_, err = peer.CreateDataChannel("test", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, CreateOffer should return an error
	_, err = peer.CreateOffer()
	if err == nil {
		t.Error("expected error from CreateOffer after Close")
	}

	// CreateAnswer should return an error
	_, err = peer.CreateAnswer()
	if err == nil {
		t.Error("expected error from CreateAnswer after Close")
	}

	// CreateDataChannel should return an error
	_, err = peer.CreateDataChannel("new-dc", true)
	if err == nil {
		t.Error("expected error from CreateDataChannel after Close")
	}
}

func TestPeer_CreateDataChannel_SameLabel(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	_, err = peer.CreateDataChannel("dup", true)
	if err != nil {
		t.Fatalf("first CreateDataChannel: %v", err)
	}

	// Pion allows creating multiple DCs with the same label (they get different IDs).
	// Our wrapper stores by label, so the second overwrites the first in the map.
	_, err = peer.CreateDataChannel("dup", true)
	if err != nil {
		t.Fatalf("second CreateDataChannel: %v", err)
	}

	// GetDataChannel should still work (returns the latest one)
	dc, err := peer.GetDataChannel("dup")
	if err != nil {
		t.Fatalf("GetDataChannel: %v", err)
	}
	if dc.Label() != "dup" {
		t.Errorf("label = %q", dc.Label())
	}
}

// waitForEvents polls the safeBuffer until one or more matching events appear, returning all matches.
func waitForEvents(t *testing.T, buf *safeBuffer, eventName string, timeout time.Duration) []*ipc.Frame {
	t.Helper()
	deadline := time.After(timeout)
	for {
		frames := readAllEvents(buf)
		var matched []*ipc.Frame
		for _, f := range frames {
			if f.Header.Event == eventName {
				matched = append(matched, f)
			}
		}
		if len(matched) > 0 {
			return matched
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for event %q", eventName)
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// newWrappedPeerPair creates a Peer wrapper pair with event capture.
// Returns peer1 (offerer) with its safeBuffer, and the raw pc2 (answerer).
func newWrappedPeerPair(t *testing.T) (*Peer, *safeBuffer, *webrtc.PeerConnection) {
	t.Helper()
	pc1, pc2 := newTestPeerPair(t)

	sb := &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(sb)

	peer := &Peer{
		id:     "test-peer",
		pc:     pc1,
		logger: logger.With("pcId", "test-peer"),
		writer: writer,
		dcs:    make(map[string]*DataChannel),
	}
	peer.iceState.Store("")
	peer.connState.Store("")
	peer.setupCallbacks()

	return peer, sb, pc2
}

// pollConnectionState polls until the PeerConnection reaches the target state without overwriting callbacks.
func pollConnectionState(t *testing.T, pc *webrtc.PeerConnection, target webrtc.PeerConnectionState, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if pc.ConnectionState() == target {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for connection state %s (current: %s)", target, pc.ConnectionState())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestPeerSelectedCandidatePair(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	f := waitForEvent(t, sb, "pc.selectedcandidatepairchange", 10*time.Second)

	var payload candidatePairPayload
	if err := msgpack.Unmarshal(f.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Local.Address == "" {
		t.Error("local address is empty")
	}
	if payload.Remote.Address == "" {
		t.Error("remote address is empty")
	}
	if payload.Local.Type == "" {
		t.Error("local type is empty")
	}
	if payload.Local.Protocol == "" {
		t.Error("local protocol is empty")
	}
	if payload.Local.Port == 0 {
		t.Error("local port is 0")
	}
}

func TestPeerICEGatheringState(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	events := waitForEvents(t, sb, "pc.icegatheringstatechange", 10*time.Second)

	// Should receive at least gathering or complete
	states := make(map[string]bool)
	for _, e := range events {
		var payload map[string]string
		if err := msgpack.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		states[payload["state"]] = true
	}

	if !states["gathering"] && !states["complete"] {
		t.Errorf("expected gathering or complete state, got: %v", states)
	}
}

func TestPeerSignalingState(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)

	// Wait for the stable state (signaling state should be stable after doSignaling)
	deadline := time.After(5 * time.Second)
	for {
		frames := readAllEvents(sb)
		states := make(map[string]bool)
		for _, f := range frames {
			if f.Header.Event != "pc.signalingstatechange" {
				continue
			}
			var payload map[string]string
			if err := msgpack.Unmarshal(f.Payload, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			states[payload["state"]] = true
		}
		if states["have-local-offer"] && states["stable"] {
			return // test passed
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for signaling states; have-local-offer=%v, stable=%v",
				states["have-local-offer"], states["stable"])
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestPeer_FullSDPExchangeViaWrapper(t *testing.T) {
	// Test the full SDP exchange using Peer wrapper methods (CreateOffer, SetLocalDescription,
	// SetRemoteDescription, CreateAnswer) to exercise the valid "offer"/"answer" code paths.
	pc1, pc2 := newTestPeerPair(t)

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer1 := &Peer{
		id: "peer-1", pc: pc1, logger: logger.With("pcId", "peer-1"),
		writer: writer, dcs: make(map[string]*DataChannel),
	}
	peer1.iceState.Store("")
	peer1.connState.Store("")

	peer2 := &Peer{
		id: "peer-2", pc: pc2, logger: logger.With("pcId", "peer-2"),
		writer: writer, dcs: make(map[string]*DataChannel),
	}
	peer2.iceState.Store("")
	peer2.connState.Store("")

	defer peer1.Close()
	defer peer2.Close()

	// Create DC to force media section
	_, err := peer1.CreateDataChannel("test", true)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	// CreateOffer on peer1
	offerSDP, err := peer1.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if offerSDP == "" {
		t.Fatal("offer SDP is empty")
	}

	// SetLocalDescription on peer1 with offer
	if err := peer1.SetLocalDescription("offer", offerSDP); err != nil {
		t.Fatalf("peer1.SetLocalDescription(offer): %v", err)
	}

	// SetRemoteDescription on peer2 with offer
	if err := peer2.SetRemoteDescription("offer", offerSDP); err != nil {
		t.Fatalf("peer2.SetRemoteDescription(offer): %v", err)
	}

	// CreateAnswer on peer2
	answerSDP, err := peer2.CreateAnswer()
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	if answerSDP == "" {
		t.Fatal("answer SDP is empty")
	}

	// SetLocalDescription on peer2 with answer
	if err := peer2.SetLocalDescription("answer", answerSDP); err != nil {
		t.Fatalf("peer2.SetLocalDescription(answer): %v", err)
	}

	// SetRemoteDescription on peer1 with answer
	if err := peer1.SetRemoteDescription("answer", answerSDP); err != nil {
		t.Fatalf("peer1.SetRemoteDescription(answer): %v", err)
	}

	// Wait for connected state
	pollConnectionState(t, pc1, webrtc.PeerConnectionStateConnected, 10*time.Second)
	pollConnectionState(t, pc2, webrtc.PeerConnectionStateConnected, 10*time.Second)
}

func TestPeer_AddICECandidate(t *testing.T) {
	// Test AddICECandidate by doing a full SDP exchange first, then adding an
	// end-of-candidates signal (empty candidate string).
	pc1, pc2 := newTestPeerPair(t)

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer1 := &Peer{
		id: "peer-1", pc: pc1, logger: logger.With("pcId", "peer-1"),
		writer: writer, dcs: make(map[string]*DataChannel),
	}
	peer1.iceState.Store("")
	peer1.connState.Store("")

	defer peer1.Close()
	defer pc2.Close()

	_, err := pc1.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, pc1, pc2)
	pollConnectionState(t, pc1, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Add an empty ICE candidate (end-of-candidates signal) - should not error
	err = peer1.AddICECandidate("", "0", 0)
	if err != nil {
		t.Fatalf("AddICECandidate: %v", err)
	}
}

// Test that a remote DataChannel triggers the OnDataChannel callback on the wrapped Peer,
// emitting a pc.datachannel event and storing the DC.
func TestPeer_OnDataChannel_RemoteCreated(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	// Create DC on pc2 (the remote side) → should trigger OnDataChannel on the wrapped peer
	_, err := pc2.CreateDataChannel("remote-dc", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel on remote: %v", err)
	}

	// Do signaling with pc2 as the offerer (it created the DC)
	doSignaling(t, pc2, peer.pc)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Wait for pc.datachannel event
	f := waitForEvent(t, sb, "pc.datachannel", 10*time.Second)
	if f.Header.PcID != "test-peer" {
		t.Errorf("pcId = %q, want %q", f.Header.PcID, "test-peer")
	}
	if f.Header.DcLabel != "remote-dc" {
		t.Errorf("dcLabel = %q, want %q", f.Header.DcLabel, "remote-dc")
	}

	// Verify the DC was stored in the peer's map
	dc, err := peer.GetDataChannel("remote-dc")
	if err != nil {
		t.Fatalf("GetDataChannel after remote create: %v", err)
	}
	if dc.Label() != "remote-dc" {
		t.Errorf("stored DC label = %q", dc.Label())
	}
}

// Test ICE candidate event emission from the wrapped Peer.
func TestPeer_OnICECandidate_EmitsEvent(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)

	// Wait for at least one pc.icecandidate event
	f := waitForEvent(t, sb, "pc.icecandidate", 10*time.Second)
	if f.Header.PcID != "test-peer" {
		t.Errorf("pcId = %q, want %q", f.Header.PcID, "test-peer")
	}

	// Verify the payload has candidate info
	var payload map[string]interface{}
	if err := msgpack.Unmarshal(f.Payload, &payload); err != nil {
		t.Fatalf("unmarshal icecandidate payload: %v", err)
	}
	if _, ok := payload["candidate"]; !ok {
		t.Error("missing 'candidate' field in ice candidate event")
	}
}

// Test connection state change event emission.
func TestPeer_OnConnectionStateChange_EmitsEvent(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// We should see pc.statechange events with connState populated
	events := waitForEvents(t, sb, "pc.statechange", 10*time.Second)
	hasConnState := false
	for _, e := range events {
		var payload map[string]string
		if err := msgpack.Unmarshal(e.Payload, &payload); err != nil {
			continue
		}
		if payload["connState"] != "" {
			hasConnState = true
			break
		}
	}
	if !hasConnState {
		t.Error("no pc.statechange event with connState found")
	}
}

func TestPeerICEConnectionStateIndependent(t *testing.T) {
	peer, sb, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	_, err := peer.pc.CreateDataChannel("test", nil)
	if err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}

	doSignaling(t, peer.pc, pc2)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Collect all pc.statechange events
	events := waitForEvents(t, sb, "pc.statechange", 10*time.Second)

	// Should have events from ICE connection state changes (non-empty iceState)
	hasIceState := false
	for _, e := range events {
		var payload map[string]string
		if err := msgpack.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload["iceState"] != "" {
			hasIceState = true
			break
		}
	}
	if !hasIceState {
		t.Error("no pc.statechange event with non-empty iceState found (ICE connection state should emit statechange)")
	}
}
