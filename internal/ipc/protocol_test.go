package ipc

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundtrip_EmptyPayload(t *testing.T) {
	f := &Frame{
		Header:  Header{Type: MsgTypeRequest, ID: 1, Method: "ping"},
		Payload: nil,
	}
	encoded, err := EncodeFrame(f)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	// Skip the 4-byte length prefix for DecodeFrame
	decoded, err := DecodeFrame(encoded[LengthPrefixSize:])
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.Header.Type != MsgTypeRequest {
		t.Errorf("type = %q, want %q", decoded.Header.Type, MsgTypeRequest)
	}
	if decoded.Header.ID != 1 {
		t.Errorf("id = %d, want 1", decoded.Header.ID)
	}
	if decoded.Header.Method != "ping" {
		t.Errorf("method = %q, want %q", decoded.Header.Method, "ping")
	}
	if len(decoded.Payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(decoded.Payload))
	}
}

func TestEncodeDecodeRoundtrip_WithPayload(t *testing.T) {
	payload := []byte("hello world")
	f := &Frame{
		Header:  Header{Type: MsgTypeResponse, ID: 42, OK: true},
		Payload: payload,
	}
	encoded, err := EncodeFrame(f)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	decoded, err := DecodeFrame(encoded[LengthPrefixSize:])
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.Header.Type != MsgTypeResponse {
		t.Errorf("type = %q, want %q", decoded.Header.Type, MsgTypeResponse)
	}
	if decoded.Header.ID != 42 {
		t.Errorf("id = %d, want 42", decoded.Header.ID)
	}
	if !decoded.Header.OK {
		t.Error("ok = false, want true")
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Errorf("payload = %q, want %q", decoded.Payload, payload)
	}
}

func TestEncodeDecodeRoundtrip_AllHeaderFields(t *testing.T) {
	f := &Frame{
		Header: Header{
			Type:     MsgTypeEvent,
			ID:       99,
			Method:   "dc.send",
			PcID:     "pc-1",
			DcLabel:  "rpc",
			OK:       true,
			Error:    "some error",
			Event:    "dc.message",
			IsBinary: true,
		},
		Payload: []byte{0x00, 0xFF, 0x80},
	}
	encoded, err := EncodeFrame(f)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	decoded, err := DecodeFrame(encoded[LengthPrefixSize:])
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	h := decoded.Header
	if h.Type != MsgTypeEvent {
		t.Errorf("type = %q", h.Type)
	}
	if h.ID != 99 {
		t.Errorf("id = %d", h.ID)
	}
	if h.Method != "dc.send" {
		t.Errorf("method = %q", h.Method)
	}
	if h.PcID != "pc-1" {
		t.Errorf("pcId = %q", h.PcID)
	}
	if h.DcLabel != "rpc" {
		t.Errorf("dcLabel = %q", h.DcLabel)
	}
	if !h.OK {
		t.Error("ok = false")
	}
	if h.Error != "some error" {
		t.Errorf("error = %q", h.Error)
	}
	if h.Event != "dc.message" {
		t.Errorf("event = %q", h.Event)
	}
	if !h.IsBinary {
		t.Error("isBinary = false")
	}
	if !bytes.Equal(decoded.Payload, []byte{0x00, 0xFF, 0x80}) {
		t.Errorf("payload mismatch")
	}
}

func TestEncodeFrame_TooLarge(t *testing.T) {
	f := &Frame{
		Header:  Header{Type: MsgTypeRequest},
		Payload: make([]byte, MaxFrameSize+1),
	}
	_, err := EncodeFrame(f)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
	if !strings.Contains(err.Error(), "frame too large") {
		t.Errorf("error = %q, want 'frame too large'", err)
	}
}

func TestDecodeFrame_TooShort(t *testing.T) {
	_, err := DecodeFrame([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
	if !strings.Contains(err.Error(), "frame too short") {
		t.Errorf("error = %q", err)
	}
}

func TestDecodeFrame_HeaderLenExceedsFrame(t *testing.T) {
	// 2 bytes header len claiming 255, but only 2 bytes total
	data := []byte{0xFF, 0x00}
	_, err := DecodeFrame(data)
	if err == nil {
		t.Fatal("expected error for header length exceeding frame")
	}
	if !strings.Contains(err.Error(), "header length") {
		t.Errorf("error = %q", err)
	}
}

func TestDecodeFrame_EmptyHeader(t *testing.T) {
	// Header len = 0, rest is payload. msgpack Unmarshal of empty bytes should succeed
	// with zero-value Header, but actually msgpack.Unmarshal of empty input returns error.
	// So header len = 0 means 0 bytes of msgpack → decode error.
	data := []byte{0x00, 0x00, 0xAA, 0xBB} // headerLen=0, payload=0xAABB
	_, err := DecodeFrame(data)
	if err == nil {
		t.Fatal("expected error for empty header bytes")
	}
}

func TestDecodeFrame_InvalidMsgpack(t *testing.T) {
	// headerLen=2, followed by invalid msgpack bytes
	data := []byte{0x02, 0x00, 0xFF, 0xFF}
	_, err := DecodeFrame(data)
	if err == nil {
		t.Fatal("expected error for invalid msgpack")
	}
	if !strings.Contains(err.Error(), "decode header") {
		t.Errorf("error = %q", err)
	}
}
