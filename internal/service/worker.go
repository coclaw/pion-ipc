package service

import (
	"log/slog"

	"github.com/coclaw/pion-ipc/internal/ipc"
)

const workerQueueSize = 256

// pcWorker handles all RPC requests for a single PeerConnection.
// Requests are executed serially (FIFO) within the worker goroutine;
// workers for different PCs run fully in parallel.
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

// run is the worker goroutine main loop. Exits when the queue channel is closed.
func (w *pcWorker) run() {
	w.logger.Info("worker started")
	defer w.logger.Info("worker exited")

	for f := range w.queue {
		w.processFrame(f)
	}
}

// processFrame handles a single request frame with panic recovery.
func (w *pcWorker) processFrame(f *ipc.Frame) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("panic in worker", "panic", r, "method", f.Header.Method, "reqId", f.Header.ID)
			_ = w.svc.writer.SendResponse(f.Header.ID, false, nil, "internal error: panic in worker")
		}
	}()
	w.svc.handleRequest(f)
}
