package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
	"github.com/nicosmd/pion-ipc/internal/rtc"
)

// Service is the main IPC message router.
// reader goroutine 将请求派发到 per-PC worker goroutine；同 PC 的请求串行执行（FIFO），
// 不同 PC 的请求完全并行。
type Service struct {
	logger   *slog.Logger
	reader   *ipc.Reader
	writer   *ipc.Writer
	manager  *rtc.Manager
	workersMu sync.Mutex
	workers   map[string]*pcWorker
	stopped   bool // 受 workersMu 保护，阻止 shutdown 后创建新 worker
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

// dispatchRequest 将请求派发到对应 PC 的 worker goroutine。
// 主要由 reader goroutine 调用；workersMu 保护 workers map 以协调 shutdown 路径。
func (s *Service) dispatchRequest(f *ipc.Frame) {
	method := f.Header.Method

	// 无 pcID 的方法直接在 reader goroutine 处理（μs 级）
	if method == "ping" {
		s.handleRequest(f)
		return
	}

	// pc.create: pcID 在 payload 中，需要轻量解码后创建 worker
	if method == "pc.create" {
		s.dispatchPcCreate(f)
		return
	}

	// 其他方法：根据 header.pcId 路由到对应 worker
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

// dispatchPcCreate 处理 pc.create 的派发：从 payload 提取 pcID，创建 worker。
func (s *Service) dispatchPcCreate(f *ipc.Frame) {
	var params struct {
		PcID string `msgpack:"pcId"`
	}
	if err := msgpack.Unmarshal(f.Payload, &params); err != nil || params.PcID == "" {
		// 解码失败或 pcID 为空——inline 处理，handleRequest 会返回合适的错误
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

// closeAllWorkers 标记 stopped 并关闭所有 worker 的 queue channel，使其 goroutine 退出。
// stopped 标记防止 reader goroutine 在 shutdown 后创建新 worker（避免 WaitGroup 竞态）。
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
	// 顺手把 send 之后的 BufferedAmount 带回给调用方，让 Node 侧不用再独立 IPC 查询。
	// pion-node 的 R 方案依赖这个字段——即每次 send ack 都同步刷新它维护的 _goBufferedBytes。
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
