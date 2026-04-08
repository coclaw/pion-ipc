package rtc

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/webrtc-dc-ipc/internal/ipc"
)

// Peer wraps a single pion PeerConnection.
type Peer struct {
	id       string
	pc       *webrtc.PeerConnection
	logger   *slog.Logger
	writer   *ipc.Writer
	dcs      map[string]*DataChannel
	mu       sync.RWMutex
	iceState string // 最近一次 ICE connection state
}

// NewPeer creates a new Peer with a pion PeerConnection.
func NewPeer(id string, iceServers []ICEServer, logger *slog.Logger, writer *ipc.Writer) (*Peer, error) {
	cfg := webrtc.Configuration{}
	for _, s := range iceServers {
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}

	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		return nil, fmt.Errorf("new peer connection: %w", err)
	}

	p := &Peer{
		id:     id,
		pc:     pc,
		logger: logger.With("pcId", id),
		writer: writer,
		dcs:    make(map[string]*DataChannel),
	}

	p.setupCallbacks()
	return p, nil
}

// setupCallbacks registers PeerConnection event handlers that emit IPC events.
func (p *Peer) setupCallbacks() {
	p.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		p.logger.Debug("ice candidate", "candidate", c.ToJSON().Candidate)
		init := c.ToJSON()
		payload, err := msgpack.Marshal(map[string]interface{}{
			"candidate":     init.Candidate,
			"sdpMid":        init.SDPMid,
			"sdpMLineIndex": init.SDPMLineIndex,
		})
		if err != nil {
			p.logger.Warn("failed to encode ice candidate", "error", err)
			return
		}
		_ = p.writer.SendEvent("pc.icecandidate", p.id, "", payload, false)
	})

	p.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		p.logger.Info("connection state changed", "state", state.String())
		payload, err := msgpack.Marshal(map[string]string{
			"connState": state.String(),
			"iceState":  p.iceState,
		})
		if err != nil {
			p.logger.Warn("failed to encode connection state", "error", err)
			return
		}
		_ = p.writer.SendEvent("pc.statechange", p.id, "", payload, false)
	})

	p.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		p.logger.Info("ice connection state changed", "state", state.String())
		p.iceState = state.String()
	})

	p.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		p.logger.Info("remote data channel opened", "label", dc.Label())
		wrapped := WrapDataChannel(dc, p.id, p.logger, p.writer)
		p.mu.Lock()
		p.dcs[dc.Label()] = wrapped
		p.mu.Unlock()
		payload, err := msgpack.Marshal(map[string]bool{
			"ordered": dc.Ordered(),
		})
		if err != nil {
			p.logger.Warn("failed to encode datachannel info", "error", err)
			return
		}
		_ = p.writer.SendEvent("pc.datachannel", p.id, dc.Label(), payload, false)
	})
}

// CreateOffer generates an SDP offer.
func (p *Peer) CreateOffer() (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("create offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}
	return offer.SDP, nil
}

// CreateAnswer generates an SDP answer.
func (p *Peer) CreateAnswer() (string, error) {
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}
	return answer.SDP, nil
}

// SetRemoteDescription sets the remote SDP.
func (p *Peer) SetRemoteDescription(sdpType, sdp string) error {
	var t webrtc.SDPType
	switch sdpType {
	case "offer":
		t = webrtc.SDPTypeOffer
	case "answer":
		t = webrtc.SDPTypeAnswer
	default:
		return fmt.Errorf("unknown sdp type: %s", sdpType)
	}
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: t, SDP: sdp})
}

// AddICECandidate adds a remote ICE candidate.
func (p *Peer) AddICECandidate(candidate string, sdpMid string, sdpMLineIndex uint16) error {
	return p.pc.AddICECandidate(webrtc.ICECandidateInit{
		Candidate:     candidate,
		SDPMid:        &sdpMid,
		SDPMLineIndex: &sdpMLineIndex,
	})
}

// CreateDataChannel creates a new DataChannel with the given label.
func (p *Peer) CreateDataChannel(label string, ordered bool) (*DataChannel, error) {
	dc, err := p.pc.CreateDataChannel(label, &webrtc.DataChannelInit{
		Ordered: &ordered,
	})
	if err != nil {
		return nil, fmt.Errorf("create data channel %q: %w", label, err)
	}

	wrapped := WrapDataChannel(dc, p.id, p.logger, p.writer)
	p.mu.Lock()
	p.dcs[label] = wrapped
	p.mu.Unlock()
	return wrapped, nil
}

// GetDataChannel returns the DataChannel with the given label.
func (p *Peer) GetDataChannel(label string) (*DataChannel, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	dc, ok := p.dcs[label]
	if !ok {
		return nil, fmt.Errorf("data channel %q not found", label)
	}
	return dc, nil
}

// SetLocalDescription sets the local SDP.
func (p *Peer) SetLocalDescription(sdpType, sdp string) error {
	var t webrtc.SDPType
	switch sdpType {
	case "offer":
		t = webrtc.SDPTypeOffer
	case "answer":
		t = webrtc.SDPTypeAnswer
	default:
		return fmt.Errorf("unknown sdp type: %s", sdpType)
	}
	return p.pc.SetLocalDescription(webrtc.SessionDescription{Type: t, SDP: sdp})
}

// RestartICE triggers an ICE restart by creating a new offer with ICE restart flag.
func (p *Peer) RestartICE() (string, error) {
	offer, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return "", fmt.Errorf("create restart offer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}
	return offer.SDP, nil
}

// Close closes the PeerConnection and all associated DataChannels.
func (p *Peer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for label, dc := range p.dcs {
		dc.Close()
		delete(p.dcs, label)
	}
	return p.pc.Close()
}
