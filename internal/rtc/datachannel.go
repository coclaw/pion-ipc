package rtc

import (
	"log/slog"

	"github.com/pion/webrtc/v4"

	"github.com/coclaw/pion-ipc/internal/ipc"
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
		if err := d.writer.SendEvent("dc.open", d.pcID, d.dc.Label(), nil, false); err != nil {
			d.logger.Warn("failed to send event", "event", "dc.open", "error", err)
		}
	})

	d.dc.OnClose(func() {
		d.logger.Info("data channel closed")
		if err := d.writer.SendEvent("dc.close", d.pcID, d.dc.Label(), nil, false); err != nil {
			d.logger.Warn("failed to send event", "event", "dc.close", "error", err)
		}
	})

	d.dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if err := d.writer.SendEvent("dc.message", d.pcID, d.dc.Label(), msg.Data, !msg.IsString); err != nil {
			d.logger.Warn("failed to send event", "event", "dc.message", "error", err)
		}
	})

	d.dc.OnError(func(err error) {
		d.logger.Error("data channel error", "error", err)
		if err := d.writer.SendEvent("dc.error", d.pcID, d.dc.Label(), []byte(err.Error()), false); err != nil {
			d.logger.Warn("failed to send event", "event", "dc.error", "error", err)
		}
	})

	d.dc.OnBufferedAmountLow(func() {
		if err := d.writer.SendEvent("dc.bufferedamountlow", d.pcID, d.dc.Label(), nil, false); err != nil {
			d.logger.Warn("failed to send event", "event", "dc.bufferedamountlow", "error", err)
		}
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

// BufferedAmount returns the current buffered amount.
func (d *DataChannel) BufferedAmount() uint64 {
	return d.dc.BufferedAmount()
}

// SetBufferedAmountLowThreshold sets the threshold for the bufferedamountlow event.
func (d *DataChannel) SetBufferedAmountLowThreshold(threshold uint64) {
	d.dc.SetBufferedAmountLowThreshold(threshold)
}

// Close closes the DataChannel.
func (d *DataChannel) Close() {
	if err := d.dc.Close(); err != nil {
		d.logger.Warn("error closing data channel", "error", err)
	}
}
