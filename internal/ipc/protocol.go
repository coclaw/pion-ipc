// Package ipc implements the binary framing protocol for stdin/stdout IPC.
//
// Frame format:
//
//	[4 bytes: total length, uint32 LE] [2 bytes: header length, uint16 LE] [msgpack header] [raw payload]
//
// "total length" covers everything after the 4-byte prefix (i.e. headerLen field + header + payload).
// The header is a msgpack map containing message metadata (type, id, method, etc.).
// The raw payload follows immediately after the header within the frame.
package ipc

import (
	"encoding/binary"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	// LengthPrefixSize is the size of the frame length prefix in bytes.
	LengthPrefixSize = 4

	// HeaderLenSize is the size of the header length field in bytes.
	HeaderLenSize = 2

	// MaxFrameSize is the maximum allowed frame payload size (16 MiB).
	MaxFrameSize = 16 * 1024 * 1024
)

// Header contains the metadata fields of an IPC message.
type Header struct {
	Type     string `msgpack:"type"`
	ID       uint32 `msgpack:"id,omitempty"`
	Method   string `msgpack:"method,omitempty"`
	PcID     string `msgpack:"pcId,omitempty"`
	DcLabel  string `msgpack:"dcLabel,omitempty"`
	OK       bool   `msgpack:"ok,omitempty"`
	Error    string `msgpack:"error,omitempty"`
	Event    string `msgpack:"event,omitempty"`
	IsBinary bool   `msgpack:"isBinary,omitempty"`
}

// Frame represents a decoded IPC frame with header and optional raw payload.
type Frame struct {
	Header  Header
	Payload []byte
}

// EncodeFrame serializes a Frame into wire format.
func EncodeFrame(f *Frame) ([]byte, error) {
	headerBytes, err := msgpack.Marshal(&f.Header)
	if err != nil {
		return nil, fmt.Errorf("marshal header: %w", err)
	}

	if len(headerBytes) > 0xFFFF {
		return nil, fmt.Errorf("header too large: %d bytes", len(headerBytes))
	}

	totalLen := HeaderLenSize + len(headerBytes) + len(f.Payload)
	if totalLen > MaxFrameSize {
		return nil, fmt.Errorf("frame too large: %d > %d", totalLen, MaxFrameSize)
	}

	buf := make([]byte, LengthPrefixSize+totalLen)
	binary.LittleEndian.PutUint32(buf[:LengthPrefixSize], uint32(totalLen))
	binary.LittleEndian.PutUint16(buf[LengthPrefixSize:LengthPrefixSize+HeaderLenSize], uint16(len(headerBytes)))
	copy(buf[LengthPrefixSize+HeaderLenSize:], headerBytes)
	copy(buf[LengthPrefixSize+HeaderLenSize+len(headerBytes):], f.Payload)

	return buf, nil
}

// DecodeFrame deserializes a raw frame body (without the 4-byte length prefix) into a Frame.
func DecodeFrame(data []byte) (*Frame, error) {
	if len(data) < HeaderLenSize {
		return nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}

	headerLen := int(binary.LittleEndian.Uint16(data[:HeaderLenSize]))
	if HeaderLenSize+headerLen > len(data) {
		return nil, fmt.Errorf("header length %d exceeds frame size %d", headerLen, len(data))
	}

	var header Header
	if err := msgpack.Unmarshal(data[HeaderLenSize:HeaderLenSize+headerLen], &header); err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}

	payload := data[HeaderLenSize+headerLen:]

	return &Frame{
		Header:  header,
		Payload: payload,
	}, nil
}
