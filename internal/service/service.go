package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
	"github.com/nicosmd/pion-ipc/internal/rtc"
)

// Service is the main IPC message router.
type Service struct {
	logger  *slog.Logger
	reader  *ipc.Reader
	writer  *ipc.Writer
	manager *rtc.Manager
}

// New creates a new Service.
func New(logger *slog.Logger, reader *ipc.Reader, writer *ipc.Writer) *Service {
	return &Service{
		logger:  logger,
		reader:  reader,
		writer:  writer,
		manager: rtc.NewManager(logger, writer),
	}
}

// Run starts the IPC message loop. It blocks until ctx is cancelled or stdin is closed.
func (s *Service) Run(ctx context.Context) error {
	defer s.manager.CloseAll()

	errCh := make(chan error, 1)
	go func() {
		for {
			frame, err := s.reader.ReadFrame()
			if err != nil {
				errCh <- err
				return
			}
			s.handleFrame(frame)
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err == io.EOF {
			s.logger.Info("stdin closed")
			return io.EOF
		}
		return fmt.Errorf("read loop: %w", err)
	}
}

// handleFrame dispatches a received frame based on its type.
func (s *Service) handleFrame(f *ipc.Frame) {
	switch f.Header.Type {
	case ipc.MsgTypeRequest:
		s.handleRequest(f)
	default:
		s.logger.Warn("ignoring unknown message type", "type", f.Header.Type)
	}
}

// handleRequest routes a request to the appropriate handler based on method name.
func (s *Service) handleRequest(f *ipc.Frame) {
	id := f.Header.ID
	method := f.Header.Method
	logger := s.logger.With("reqId", id, "method", method)

	logger.Debug("handling request")

	var err error
	switch method {
	case "pc.create":
		err = s.handlePcCreate(f)
	case "pc.close":
		err = s.handlePcClose(f)
	case "pc.createOffer":
		err = s.handlePcCreateOffer(f)
	case "pc.createAnswer":
		err = s.handlePcCreateAnswer(f)
	case "pc.setRemoteDescription":
		err = s.handlePcSetRemoteDescription(f)
	case "pc.addIceCandidate":
		err = s.handlePcAddIceCandidate(f)
	case "dc.create":
		err = s.handleDcCreate(f)
	case "dc.send":
		err = s.handleDcSend(f)
	case "dc.close":
		err = s.handleDcClose(f)
	case "dc.setBALT":
		err = s.handleDcSetBALT(f)
	case "dc.getBA":
		err = s.handleDcGetBA(f)
	case "pc.restartIce":
		err = s.handlePcRestartIce(f)
	case "pc.setLocalDescription":
		err = s.handlePcSetLocalDescription(f)
	case "ping":
		err = s.writer.SendResponse(f.Header.ID, true, nil, "")
	default:
		err = fmt.Errorf("unknown method: %s", method)
	}

	if err != nil {
		logger.Warn("request failed", "error", err)
		_ = s.writer.SendResponse(id, false, nil, err.Error())
	}
}

// --- PeerConnection handlers ---

type pcCreateParams struct {
	PcID       string          `msgpack:"pcId"`
	IceServers []rtc.ICEServer `msgpack:"iceServers"`
}

func (s *Service) handlePcCreate(f *ipc.Frame) error {
	var params pcCreateParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if params.PcID == "" {
		return fmt.Errorf("pcId is required")
	}
	if err := s.manager.CreatePeer(params.PcID, params.IceServers); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

func (s *Service) handlePcClose(f *ipc.Frame) error {
	pcID := f.Header.PcID
	if pcID == "" {
		return fmt.Errorf("pcId is required")
	}
	if err := s.manager.ClosePeer(pcID); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

func (s *Service) handlePcCreateOffer(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	sdp, err := peer.CreateOffer()
	if err != nil {
		return err
	}
	payload, err := msgpack.Marshal(map[string]string{"sdp": sdp})
	if err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, payload, "")
}

func (s *Service) handlePcCreateAnswer(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	sdp, err := peer.CreateAnswer()
	if err != nil {
		return err
	}
	payload, err := msgpack.Marshal(map[string]string{"sdp": sdp})
	if err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, payload, "")
}

type setRemoteDescParams struct {
	Type string `msgpack:"type"`
	SDP  string `msgpack:"sdp"`
}

func (s *Service) handlePcSetRemoteDescription(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	var params setRemoteDescParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if err := peer.SetRemoteDescription(params.Type, params.SDP); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

type addIceCandidateParams struct {
	Candidate     string `msgpack:"candidate"`
	SDPMid        string `msgpack:"sdpMid"`
	SDPMLineIndex uint16 `msgpack:"sdpMLineIndex"`
}

func (s *Service) handlePcAddIceCandidate(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	var params addIceCandidateParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if err := peer.AddICECandidate(params.Candidate, params.SDPMid, params.SDPMLineIndex); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

// --- DataChannel handlers ---

type dcCreateParams struct {
	Label   string `msgpack:"label"`
	Ordered bool   `msgpack:"ordered"`
}

func (s *Service) handleDcCreate(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	var params dcCreateParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if params.Label == "" {
		return fmt.Errorf("label is required")
	}
	if _, err := peer.CreateDataChannel(params.Label, params.Ordered); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

func (s *Service) handleDcSend(f *ipc.Frame) error {
	pcID := f.Header.PcID
	dcLabel := f.Header.DcLabel
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	dc, err := peer.GetDataChannel(dcLabel)
	if err != nil {
		return err
	}
	if f.Header.IsBinary {
		if err := dc.Send(f.Payload); err != nil {
			return err
		}
	} else {
		if err := dc.SendText(string(f.Payload)); err != nil {
			return err
		}
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

func (s *Service) handleDcClose(f *ipc.Frame) error {
	pcID := f.Header.PcID
	dcLabel := f.Header.DcLabel
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	dc, err := peer.GetDataChannel(dcLabel)
	if err != nil {
		return err
	}
	dc.Close()
	peer.RemoveDataChannel(dcLabel)
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

type dcSetBALTParams struct {
	Threshold uint64 `msgpack:"threshold"`
}

func (s *Service) handleDcSetBALT(f *ipc.Frame) error {
	pcID := f.Header.PcID
	dcLabel := f.Header.DcLabel
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	dc, err := peer.GetDataChannel(dcLabel)
	if err != nil {
		return err
	}
	var params dcSetBALTParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	dc.SetBufferedAmountLowThreshold(params.Threshold)
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}

func (s *Service) handleDcGetBA(f *ipc.Frame) error {
	pcID := f.Header.PcID
	dcLabel := f.Header.DcLabel
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	dc, err := peer.GetDataChannel(dcLabel)
	if err != nil {
		return err
	}
	payload, err := msgpack.Marshal(map[string]uint64{"bufferedAmount": dc.BufferedAmount()})
	if err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, payload, "")
}

func (s *Service) handlePcRestartIce(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	sdp, err := peer.RestartICE()
	if err != nil {
		return err
	}
	payload, err := msgpack.Marshal(map[string]string{"sdp": sdp})
	if err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, payload, "")
}

func (s *Service) handlePcSetLocalDescription(f *ipc.Frame) error {
	pcID := f.Header.PcID
	peer, err := s.manager.GetPeer(pcID)
	if err != nil {
		return err
	}
	var params setRemoteDescParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if err := peer.SetLocalDescription(params.Type, params.SDP); err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, nil, "")
}
