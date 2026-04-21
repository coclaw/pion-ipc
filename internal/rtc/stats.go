package rtc

import "github.com/pion/webrtc/v4"

// SctpStats is a snapshot of the underlying SCTP association's state.
// Fields mirror pion-webrtc's SCTPTransportStats for the values actually
// populated in pion-webrtc v4.0.12 (see sctptransport.go:402-422).
//
// Unit conventions:
//   - byte counters: bytes
//   - window / MTU: bytes
//   - SrttMs: milliseconds (converted from pion's seconds representation at GetSctpStats time)
type SctpStats struct {
	BytesSent        uint64  `msgpack:"bytesSent"`
	BytesReceived    uint64  `msgpack:"bytesReceived"`
	SrttMs           float64 `msgpack:"srttMs"`
	CongestionWindow uint32  `msgpack:"congestionWindow"`
	ReceiverWindow   uint32  `msgpack:"receiverWindow"`
	MTU              uint32  `msgpack:"mtu"`
}

// GetSctpStats returns the current SCTP association stats, or nil when the
// PeerConnection has not yet established an SCTP association (e.g. before
// DTLS handshake completes, or after close).
//
// Implementation note: pion-webrtc's SCTPTransport.collectStats always emits
// an SCTPTransportStats entry even when the underlying sctp.Association is nil
// (sctptransport.go:402-422) — in that case every association-derived field is
// the zero value. We use MTU == 0 as the "no association" signal because
// pion-sctp sets mtu=initialMTU(1228) at association creation (association.go:365),
// so a zero MTU reliably means "no association yet".
func (p *Peer) GetSctpStats() *SctpStats {
	report := p.pc.GetStats()
	for _, s := range report {
		ts, ok := s.(webrtc.SCTPTransportStats)
		if !ok {
			continue
		}
		if ts.MTU == 0 {
			return nil
		}
		// pion-sctp stores SRTT in milliseconds internally; SCTPTransport.collectStats
		// multiplies by 0.001 to produce seconds for its public stats struct. We
		// multiply back by 1000 so callers see a straightforward ms value.
		return &SctpStats{
			BytesSent:        ts.BytesSent,
			BytesReceived:    ts.BytesReceived,
			SrttMs:           ts.SmoothedRoundTripTime * 1000,
			CongestionWindow: ts.CongestionWindow,
			ReceiverWindow:   ts.ReceiverWindow,
			MTU:              ts.MTU,
		}
	}
	return nil
}
