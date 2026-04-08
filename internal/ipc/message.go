package ipc

// Message type constants.
const (
	MsgTypeRequest  = "req"
	MsgTypeResponse = "res"
	MsgTypeEvent    = "evt"
)

// NewRequest creates a request Frame.
func NewRequest(id uint32, method, pcID, dcLabel string, payload []byte) *Frame {
	return &Frame{
		Header: Header{
			Type:    MsgTypeRequest,
			ID:      id,
			Method:  method,
			PcID:    pcID,
			DcLabel: dcLabel,
		},
		Payload: payload,
	}
}

// NewResponse creates a response Frame.
func NewResponse(id uint32, ok bool, payload []byte, errMsg string) *Frame {
	return &Frame{
		Header: Header{
			Type:  MsgTypeResponse,
			ID:    id,
			OK:    ok,
			Error: errMsg,
		},
		Payload: payload,
	}
}

// NewEvent creates an event Frame.
func NewEvent(event, pcID, dcLabel string, payload []byte, isBinary bool) *Frame {
	return &Frame{
		Header: Header{
			Type:     MsgTypeEvent,
			Event:    event,
			PcID:     pcID,
			DcLabel:  dcLabel,
			IsBinary: isBinary,
		},
		Payload: payload,
	}
}
