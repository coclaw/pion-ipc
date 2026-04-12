package service

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/nicosmd/pion-ipc/internal/ipc"
	"github.com/nicosmd/pion-ipc/internal/rtc"
)

// TestWorker_PanicRecovery 验证 worker 的 panic 恢复：handler panic 后发送错误响应，
// 后续请求仍可正常处理。
func TestWorker_PanicRecovery(t *testing.T) {
	outBuf := &bytes.Buffer{}
	writer := ipc.NewWriter(outBuf)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	svc := &Service{
		logger:  logger,
		writer:  writer,
		manager: nil, // nil manager → GetPeer 时 nil deref → panic
		workers: make(map[string]*pcWorker),
	}

	w := newPcWorker("panic-pc", svc)

	// 触发 handlePcCreateOffer → manager.GetPeer → nil deref → panic
	f := ipc.NewRequest(1, "pc.createOffer", "panic-pc", "", nil)
	w.processFrame(f)

	// 应发送错误响应而非崩溃
	reader := ipc.NewReader(outBuf)
	res, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if res.Header.OK {
		t.Error("expected error response after panic")
	}
	if res.Header.ID != 1 {
		t.Errorf("id = %d, want 1", res.Header.ID)
	}
	if !strings.Contains(res.Header.Error, "panic") {
		t.Errorf("error = %q, want to contain 'panic'", res.Header.Error)
	}

	// 后续请求仍可处理（再次 panic，但同样被 recover）
	f2 := ipc.NewRequest(2, "pc.createOffer", "panic-pc", "", nil)
	w.processFrame(f2)

	res2, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(2): %v", err)
	}
	if res2.Header.ID != 2 {
		t.Errorf("id = %d, want 2", res2.Header.ID)
	}
}

// TestWorker_QueueDraining 验证 worker goroutine 能正确消费队列中所有帧后退出。
func TestWorker_QueueDraining(t *testing.T) {
	outBuf := &bytes.Buffer{}
	writer := ipc.NewWriter(outBuf)
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	svc := &Service{
		logger:  logger,
		writer:  writer,
		manager: rtc.NewManager(logger, writer),
		workers: make(map[string]*pcWorker),
	}

	w := newPcWorker("drain-pc", svc)

	// 先入队再启动 worker
	params, _ := msgpack.Marshal(map[string]interface{}{"pcId": "drain-pc", "iceServers": []interface{}{}})
	w.queue <- ipc.NewRequest(1, "pc.create", "", "", params)

	dcParams, _ := msgpack.Marshal(map[string]interface{}{"label": "test", "ordered": true})
	w.queue <- ipc.NewRequest(2, "dc.create", "drain-pc", "", dcParams)

	close(w.queue)

	// worker 应消费所有帧后退出
	done := make(chan struct{})
	go func() {
		w.run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after queue closed")
	}

	// 验证两个请求都产生了响应
	reader := ipc.NewReader(outBuf)
	for i := 0; i < 2; i++ {
		res, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame(%d): %v", i, err)
		}
		if res.Header.Type != ipc.MsgTypeResponse {
			t.Errorf("frame %d: type = %q, want response", i, res.Header.Type)
		}
	}
}
