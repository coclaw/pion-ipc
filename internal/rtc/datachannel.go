package rtc

import (
	"log/slog"

	"github.com/pion/webrtc/v4"

	"github.com/nicosmd/webrtc-dc-ipc/internal/ipc"
)

// DataChannel wraps a pion DataChannel with IPC event emission.
type DataChannel struct {
	dc     *webrtc.DataChannel
	pcID   string
	logger *slog.Logger
	writer *ipc.Writer
}

// WrapDataChannel wraps a pion DataChannel and sets up IPC event callbacks.
func WrapDataChannel(dc *webrtc.DataChannel, pcID string, logger *slog.Logger, writer *ipc.Writer) *DataChannel {
	d := &DataChannel{
		dc:     dc,
		pcID:   pcID,
		logger: logger.With("dcLabel", dc.Label()),
		writer: writer,
	}
	d.setupCallbacks()
	return d
}

// setupCallbacks registers DataChannel event handlers that emit IPC events.
func (d *DataChannel) setupCallbacks() {
	d.dc.OnOpen(func() {
		d.logger.Info("data channel opened")
		// TODO: emit dc.open event
	})

	d.dc.OnClose(func() {
		d.logger.Info("data channel closed")
		// TODO: emit dc.close event
	})

	d.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		_ = d.writer.SendEvent("dc.message", d.pcID, d.dc.Label(), msg.Data, msg.IsString == false)
	})

	d.dc.OnError(func(err error) {
		d.logger.Error("data channel error", "error", err)
		// TODO: emit dc.error event
	})
}

// Send sends data through the DataChannel.
func (d *DataChannel) Send(data []byte) error {
	return d.dc.Send(data)
}

// SendText sends a text message through the DataChannel.
func (d *DataChannel) SendText(text string) error {
	return d.dc.SendText(text)
}

// Label returns the DataChannel label.
func (d *DataChannel) Label() string {
	return d.dc.Label()
}

// Close closes the DataChannel.
func (d *DataChannel) Close() {
	if err := d.dc.Close(); err != nil {
		d.logger.Warn("error closing data channel", "error", err)
	}
}
