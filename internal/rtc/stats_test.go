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

// TestGetSctpStats_NotConnected verifies that GetSctpStats returns nil on a freshly
// created Peer whose PeerConnection has not yet gone through DTLS handshake — pion
// does not populate SCTPTransportStats when the underlying association is nil.
func TestGetSctpStats_NotConnected(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})

	peer, err := NewPeer("test-peer", nil, nil, logger, writer)
	if err != nil {
		t.Fatalf("NewPeer: %v", err)
	}
	defer peer.Close()

	stats := peer.GetSctpStats()
	if stats != nil {
		t.Fatalf("expected nil stats for not-yet-connected peer, got %+v", stats)
	}
}

// TestSctpStats_MsgpackRoundtrip ensures field names in msgpack tags survive
// encode/decode and match JS-side expectations (lowerCamelCase).
func TestSctpStats_MsgpackRoundtrip(t *testing.T) {
	original := &SctpStats{
		BytesSent:        1000,
		BytesReceived:    2000,
		SrttMs:           42.5,
		CongestionWindow: 4096,
		ReceiverWindow:   131072,
		MTU:              1200,
	}
	encoded, err := msgpack.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded SctpStats
	if err := msgpack.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != *original {
		t.Fatalf("roundtrip mismatch\n got: %+v\nwant: %+v", decoded, *original)
	}

	// Verify wire field names by decoding into a generic map.
	var asMap map[string]interface{}
	if err := msgpack.Unmarshal(encoded, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	wantKeys := []string{"bytesSent", "bytesReceived", "srttMs", "congestionWindow", "receiverWindow", "mtu"}
	for _, k := range wantKeys {
		if _, ok := asMap[k]; !ok {
			t.Errorf("missing wire key %q; got keys %v", k, asMap)
		}
	}
}

// TestSctpStats_MsgpackNilPointer confirms msgpack encodes a nil *SctpStats as
// nil (rather than failing), so the JS side receives null for "no association".
func TestSctpStats_MsgpackNilPointer(t *testing.T) {
	var stats *SctpStats
	encoded, err := msgpack.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	var decoded *SctpStats
	if err := msgpack.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal nil: %v", err)
	}
	if decoded != nil {
		t.Fatalf("expected nil after roundtrip, got %+v", decoded)
	}
}

// TestGetSctpStats_Connected verifies GetSctpStats returns populated stats
// after a real DTLS/SCTP handshake completes between a peer pair.
func TestGetSctpStats_Connected(t *testing.T) {
	peer, _, pc2 := newWrappedPeerPair(t)
	defer peer.Close()
	defer pc2.Close()

	if _, err := peer.pc.CreateDataChannel("stats-test", nil); err != nil {
		t.Fatalf("CreateDataChannel: %v", err)
	}
	doSignaling(t, peer.pc, pc2)
	pollConnectionState(t, peer.pc, webrtc.PeerConnectionStateConnected, 10*time.Second)

	// Give the SCTP association a brief moment to finish its setup after the
	// PeerConnection reports connected; MTU is set at sctp.Client return.
	var stats *SctpStats
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats = peer.GetSctpStats()
		if stats != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats after connection, got nil")
	}
	if stats.MTU == 0 {
		t.Errorf("expected non-zero MTU after association creation, got 0")
	}
}
