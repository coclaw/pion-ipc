package ipc

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Reader reads IPC frames from an io.Reader (typically stdin).
type Reader struct {
	r io.Reader
}

// NewReader creates a new frame reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// ReadFrame reads and decodes the next frame from the underlying reader.
// Returns io.EOF when the stream is closed.
func (r *Reader) ReadFrame() (*Frame, error) {
	// Read 4-byte length prefix.
	lenBuf := make([]byte, LengthPrefixSize)
	if _, err := io.ReadFull(r.r, lenBuf); err != nil {
		return nil, err // io.EOF or io.ErrUnexpectedEOF
	}

	payloadLen := binary.LittleEndian.Uint32(lenBuf)
	if payloadLen > MaxFrameSize {
		return nil, fmt.Errorf("frame payload too large: %d > %d", payloadLen, MaxFrameSize)
	}

	// Read the full payload (header + raw payload).
	data := make([]byte, payloadLen)
	if _, err := io.ReadFull(r.r, data); err != nil {
		return nil, fmt.Errorf("read frame payload: %w", err)
	}

	return DecodeFrame(data)
}
