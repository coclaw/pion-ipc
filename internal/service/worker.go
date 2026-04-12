package service

import (
	"log/slog"

	"github.com/nicosmd/pion-ipc/internal/ipc"
)

const workerQueueSize = 256

// pcWorker 处理单个 PeerConnection 的所有 RPC 请求。
// 同一 PC 的请求在 worker goroutine 内串行执行，保证 FIFO 语义；
// 不同 PC 的 worker 完全并行。
type pcWorker struct {
	pcID   string
	queue  chan *ipc.Frame
	svc    *Service
	logger *slog.Logger
}

func newPcWorker(pcID string, svc *Service) *pcWorker {
	return &pcWorker{
		pcID:   pcID,
		queue:  make(chan *ipc.Frame, workerQueueSize),
		svc:    svc,
		logger: svc.logger.With("worker", pcID),
	}
}

// run 是 worker goroutine 的主循环。queue 关闭后自动退出。
func (w *pcWorker) run() {
	w.logger.Info("worker started")
	defer w.logger.Info("worker exited")

	for f := range w.queue {
		w.processFrame(f)
	}
}

// processFrame 处理单个请求帧，带 panic 恢复。
func (w *pcWorker) processFrame(f *ipc.Frame) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("panic in worker", "panic", r, "method", f.Header.Method, "reqId", f.Header.ID)
			_ = w.svc.writer.SendResponse(f.Header.ID, false, nil, "internal error: panic in worker")
		}
	}()
	w.svc.handleRequest(f)
}
