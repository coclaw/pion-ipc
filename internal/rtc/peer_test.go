package rtc

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/nicosmd/webrtc-dc-ipc/internal/ipc"
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

	// Track messages from both directions
	var mu sync.Mutex
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
	_ = mu // suppress unused warning
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
