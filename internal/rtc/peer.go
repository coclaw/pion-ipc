package rtc

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
)

// Peer wraps a single pion PeerConnection.
type Peer struct {
	id       string
	pc       *webrtc.PeerConnection
	logger   *slog.Logger
	writer   *ipc.Writer
	dcs       map[string]*DataChannel
	mu        sync.RWMutex
	iceState  atomic.Value // 最近一次 ICE connection state (string)
	connState atomic.Value // 最近一次 connection state (string)
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
	p.iceState.Store("")
	p.connState.Store("")

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
		if err := p.writer.SendEvent("pc.icecandidate", p.id, "", payload, false); err != nil {
			p.logger.Warn("failed to send event", "event", "pc.icecandidate", "error", err)
		}
	})

	p.pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		p.logger.Info("connection state changed", "state", state.String())
		p.connState.Store(state.String())
		payload, err := msgpack.Marshal(map[string]string{
			"connState": state.String(),
			"iceState":  p.iceState.Load().(string),
		})
		if err != nil {
			p.logger.Warn("failed to encode connection state", "error", err)
			return
		}
		if err := p.writer.SendEvent("pc.statechange", p.id, "", payload, false); err != nil {
			p.logger.Warn("failed to send event", "event", "pc.statechange", "error", err)
		}
		if state == webrtc.PeerConnectionStateConnected {
			go p.emitSelectedCandidatePair()
		}
	})

	p.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		p.logger.Info("ice connection state changed", "state", state.String())
		p.iceState.Store(state.String())
		payload, err := msgpack.Marshal(map[string]string{
			"connState": p.connState.Load().(string),
			"iceState":  state.String(),
		})
		if err != nil {
			p.logger.Warn("failed to encode ice connection state", "error", err)
			return
		}
		if err := p.writer.SendEvent("pc.statechange", p.id, "", payload, false); err != nil {
			p.logger.Warn("failed to send event", "event", "pc.statechange", "error", err)
		}
	})

	p.pc.OnICEGatheringStateChange(func(state webrtc.ICEGatheringState) {
		stateStr := ""
		switch state {
		case webrtc.ICEGatheringStateNew:
			stateStr = "new"
		case webrtc.ICEGatheringStateGathering:
			stateStr = "gathering"
		case webrtc.ICEGatheringStateComplete:
			stateStr = "complete"
		default:
			stateStr = "unknown"
		}
		p.logger.Info("ice gathering state changed", "state", stateStr)
		payload, err := msgpack.Marshal(map[string]string{"state": stateStr})
		if err != nil {
			p.logger.Warn("failed to encode ice gathering state", "error", err)
			return
		}
		_ = p.writer.SendEvent("pc.icegatheringstatechange", p.id, "", payload, false)
	})

	p.pc.OnSignalingStateChange(func(state webrtc.SignalingState) {
		p.logger.Info("signaling state changed", "state", state.String())
		payload, err := msgpack.Marshal(map[string]string{"state": state.String()})
		if err != nil {
			p.logger.Warn("failed to encode signaling state", "error", err)
			return
		}
		_ = p.writer.SendEvent("pc.signalingstatechange", p.id, "", payload, false)
	})

	p.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		p.logger.Info("remote data channel opened", "label", dc.Label())
		wrapped := WrapDataChannel(dc, p.id, p.logger, p.writer)
		p.mu.Lock()
		if old, exists := p.dcs[dc.Label()]; exists {
			old.Close()
		}
		p.dcs[dc.Label()] = wrapped
		p.mu.Unlock()
		payload, err := msgpack.Marshal(map[string]bool{
			"ordered": dc.Ordered(),
		})
		if err != nil {
			p.logger.Warn("failed to encode datachannel info", "error", err)
			return
		}
		if err := p.writer.SendEvent("pc.datachannel", p.id, dc.Label(), payload, false); err != nil {
			p.logger.Warn("failed to send event", "event", "pc.datachannel", "error", err)
		}
	})
}

type candidateInfo struct {
	Type     string `msgpack:"type"`
	Address  string `msgpack:"address"`
	Port     uint16 `msgpack:"port"`
	Protocol string `msgpack:"protocol"`
}

type candidatePairPayload struct {
	Local  candidateInfo `msgpack:"local"`
	Remote candidateInfo `msgpack:"remote"`
}

// emitSelectedCandidatePair 获取选中的 ICE candidate pair 并发送事件。
func (p *Peer) emitSelectedCandidatePair() {
	sctp := p.pc.SCTP()
	if sctp == nil {
		p.logger.Warn("SCTP transport not available for candidate pair")
		return
	}
	dtls := sctp.Transport()
	if dtls == nil {
		p.logger.Warn("DTLS transport not available for candidate pair")
		return
	}
	ice := dtls.ICETransport()
	if ice == nil {
		p.logger.Warn("ICE transport not available for candidate pair")
		return
	}
	pair, err := ice.GetSelectedCandidatePair()
	if err != nil || pair == nil {
		p.logger.Warn("failed to get selected candidate pair", "error", err)
		return
	}

	cp := candidatePairPayload{
		Local: candidateInfo{
			Type:     pair.Local.Typ.String(),
			Address:  pair.Local.Address,
			Port:     pair.Local.Port,
			Protocol: strings.ToLower(pair.Local.Protocol.String()),
		},
		Remote: candidateInfo{
			Type:     pair.Remote.Typ.String(),
			Address:  pair.Remote.Address,
			Port:     pair.Remote.Port,
			Protocol: strings.ToLower(pair.Remote.Protocol.String()),
		},
	}
	payload, err := msgpack.Marshal(cp)
	if err != nil {
		p.logger.Warn("failed to encode candidate pair", "error", err)
		return
	}
	if err := p.writer.SendEvent("pc.selectedcandidatepairchange", p.id, "", payload, false); err != nil {
		p.logger.Warn("failed to send event", "event", "pc.selectedcandidatepairchange", "error", err)
	}
}

// CreateOffer generates an SDP offer without applying it.
// Caller must explicitly call SetLocalDescription to apply.
func (p *Peer) CreateOffer() (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("create offer: %w", err)
	}
	return offer.SDP, nil
}

// CreateAnswer generates an SDP answer without applying it.
// Caller must explicitly call SetLocalDescription to apply.
func (p *Peer) CreateAnswer() (string, error) {
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
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
	if old, exists := p.dcs[label]; exists {
		old.Close()
	}
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

// RemoveDataChannel removes a DataChannel from the internal map by label.
func (p *Peer) RemoveDataChannel(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.dcs, label)
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
// Does not apply the offer; caller must call SetLocalDescription explicitly.
func (p *Peer) RestartICE() (string, error) {
	offer, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return "", fmt.Errorf("create restart offer: %w", err)
	}
	return offer.SDP, nil
}

// Close closes the PeerConnection and all associated DataChannels.
// 先在锁内交换 dcs map 再释放锁，避免慢 pc.Close()（200~400ms）阻塞 GetDataChannel 等。
func (p *Peer) Close() error {
	p.mu.Lock()
	dcs := p.dcs
	p.dcs = make(map[string]*DataChannel)
	p.mu.Unlock()

	for _, dc := range dcs {
		dc.Close()
	}
	return p.pc.Close()
}
