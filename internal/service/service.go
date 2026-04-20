package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/coclaw/pion-ipc/internal/ipc"
	"github.com/coclaw/pion-ipc/internal/rtc"
)

// Service is the main IPC message router.
// The reader goroutine dispatches requests to per-PC worker goroutines;
// requests for the same PC are executed serially (FIFO), while different PCs run in parallel.
type Service struct {
	logger   *slog.Logger
	reader   *ipc.Reader
	writer   *ipc.Writer
	manager  *rtc.Manager
	workersMu sync.Mutex
	workers   map[string]*pcWorker
	stopped   bool // guarded by workersMu; prevents new worker creation after shutdown
	workerWg  sync.WaitGroup
}

// New creates a new Service.
func New(logger *slog.Logger, reader *ipc.Reader, writer *ipc.Writer) *Service {
	return &Service{
		logger:  logger,
		reader:  reader,
		writer:  writer,
		manager: rtc.NewManager(logger, writer),
		workers: make(map[string]*pcWorker),
	}
}

// Run starts the IPC message loop. It blocks until ctx is cancelled or stdin is closed.
func (s *Service) Run(ctx context.Context) error {
	defer func() {
		s.closeAllWorkers()
		s.workerWg.Wait()
		s.manager.CloseAll()
	}()

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
		s.dispatchRequest(f)
	default:
		s.logger.Warn("ignoring unknown message type", "type", f.Header.Type)
	}
}

// dispatchRequest routes requests to the per-PC worker goroutine.
// Called from the reader goroutine; workersMu guards the workers map for shutdown coordination.
func (s *Service) dispatchRequest(f *ipc.Frame) {
	method := f.Header.Method

	// Methods without pcID are handled inline in the reader goroutine (μs-level)
	if method == "ping" {
		s.handleRequest(f)
		return
	}

	// pc.create: pcID is in the payload; light decode then create/route to worker
	if method == "pc.create" {
		s.dispatchPcCreate(f)
		return
	}

	// Other methods: route to worker by header.pcId
	pcID := f.Header.PcID

	s.workersMu.Lock()
	w, ok := s.workers[pcID]
	if !ok {
		s.workersMu.Unlock()
		s.logger.Warn("no worker for pcId", "pcId", pcID, "method", method, "reqId", f.Header.ID)
		_ = s.writer.SendResponse(f.Header.ID, false, nil, fmt.Sprintf("peer %q not found", pcID))
		return
	}
	w.queue <- f
	if method == "pc.close" {
		close(w.queue)
		delete(s.workers, pcID)
	}
	s.workersMu.Unlock()
}

// dispatchPcCreate handles pc.create dispatch: extracts pcID from payload and creates a worker.
func (s *Service) dispatchPcCreate(f *ipc.Frame) {
	var params struct {
		PcID string `msgpack:"pcId"`
	}
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil || params.PcID == "" {
		// Decode failed or pcID empty — handle inline; handleRequest returns the appropriate error
		s.handleRequest(f)
		return
	}

	s.workersMu.Lock()
	if s.stopped {
		s.workersMu.Unlock()
		_ = s.writer.SendResponse(f.Header.ID, false, nil, "service is shutting down")
		return
	}
	w, exists := s.workers[params.PcID]
	if !exists {
		w = newPcWorker(params.PcID, s)
		s.workers[params.PcID] = w
		s.workerWg.Add(1)
		go func() {
			defer s.workerWg.Done()
			w.run()
		}()
	}
	w.queue <- f
	s.workersMu.Unlock()
}

// closeAllWorkers marks stopped and closes all worker queue channels so their goroutines exit.
// The stopped flag prevents the reader goroutine from creating new workers after shutdown (avoids WaitGroup race).
func (s *Service) closeAllWorkers() {
	s.workersMu.Lock()
	s.stopped = true
	for pcID, w := range s.workers {
		close(w.queue)
		delete(s.workers, pcID)
	}
	s.workersMu.Unlock()
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
	PcID       string            `msgpack:"pcId"`
	IceServers []rtc.ICEServer   `msgpack:"iceServers"`
	Settings   *rtc.PeerSettings `msgpack:"settings,omitempty"`
}

func (s *Service) handlePcCreate(f *ipc.Frame) error {
	var params pcCreateParams
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	if params.PcID == "" {
		return fmt.Errorf("pcId is required")
	}
	if err := s.manager.CreatePeer(params.PcID, params.IceServers, params.Settings); err != nil {
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
	Ordered *bool  `msgpack:"ordered,omitempty"`
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
	// Default to true (matching WebRTC spec) when omitted.
	ordered := params.Ordered == nil || *params.Ordered
	if _, err := peer.CreateDataChannel(params.Label, ordered); err != nil {
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
	// Return the post-send BufferedAmount in the ack payload so the caller can
	// track SCTP buffer depth without a separate dc.getBA round-trip.
	payload, err := msgpack.Marshal(map[string]uint64{"bufferedAmount": dc.BufferedAmount()})
	if err != nil {
		return err
	}
	return s.writer.SendResponse(f.Header.ID, true, payload, "")
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
