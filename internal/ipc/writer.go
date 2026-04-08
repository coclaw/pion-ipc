package ipc

import (
	"fmt"
	"io"
	"sync"
)

// Writer writes IPC frames to an io.Writer (typically stdout) in a thread-safe manner.
type Writer struct {
	w  io.Writer
	mu sync.Mutex
}

// NewWriter creates a new thread-safe frame writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteFrame encodes and writes a frame atomically.
func (w *Writer) WriteFrame(f *Frame) error {
	data, err := EncodeFrame(f)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.w.Write(data); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// SendResponse writes a response frame.
func (w *Writer) SendResponse(id uint32, ok bool, payload []byte, errMsg string) error {
	f := &Frame{
		Header: Header{
			Type:  MsgTypeResponse,
			ID:    id,
			OK:    ok,
			Error: errMsg,
		},
		Payload: payload,
	}
	return w.WriteFrame(f)
}

// SendEvent writes an event frame.
func (w *Writer) SendEvent(event string, pcID, dcLabel string, payload []byte, isBinary bool) error {
	f := &Frame{
		Header: Header{
			Type:     MsgTypeEvent,
			Event:    event,
			PcID:     pcID,
			DcLabel:  dcLabel,
			IsBinary: isBinary,
		},
		Payload: payload,
	}
	return w.WriteFrame(f)
}
