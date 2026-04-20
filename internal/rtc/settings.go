package rtc

import (
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
)

// pion v4.0.12 default ICE timeouts (ms). Source: ice/v4/agent_config.go
// (defaultDisconnectedTimeout / defaultFailedTimeout / defaultKeepaliveInterval).
// Kept in sync manually; verify on pion upgrade.
const (
	defaultIceDisconnectedMs uint32 = 5000
	defaultIceFailedMs       uint32 = 25000
	defaultIceKeepAliveMs    uint32 = 2000
)

// Max duration value accepted by per-field validation (5 minutes).
// Protects against garbage / accidental overflow; pion itself has no hard cap.
const maxDurationMs uint32 = 300000

// PeerSettings mirrors a curated subset of pion webrtc.SettingEngine knobs.
// Each field is optional (pointer) — nil means "do not call the corresponding pion setter".
// Durations are milliseconds on the wire.
type PeerSettings struct {
	SctpRtoMax               *uint32 `msgpack:"sctpRtoMax,omitempty"`
	SctpMaxReceiveBufferSize *uint32 `msgpack:"sctpMaxReceiveBufferSize,omitempty"`
	IceDisconnectedTimeout   *uint32 `msgpack:"iceDisconnectedTimeout,omitempty"`
	IceFailedTimeout         *uint32 `msgpack:"iceFailedTimeout,omitempty"`
	IceKeepAliveInterval     *uint32 `msgpack:"iceKeepAliveInterval,omitempty"`
	StunGatherTimeout        *uint32 `msgpack:"stunGatherTimeout,omitempty"`
}

// BuildSettingEngine translates PeerSettings into a configured webrtc.SettingEngine.
// A nil input yields a zero-value SettingEngine — equivalent to calling webrtc.NewPeerConnection
// without any API customization.
func BuildSettingEngine(s *PeerSettings) (webrtc.SettingEngine, error) {
	var se webrtc.SettingEngine
	if s == nil {
		return se, nil
	}

	if err := validateDurationMs("sctpRtoMax", s.SctpRtoMax); err != nil {
		return se, err
	}
	if err := validateDurationMs("iceDisconnectedTimeout", s.IceDisconnectedTimeout); err != nil {
		return se, err
	}
	if err := validateDurationMs("iceFailedTimeout", s.IceFailedTimeout); err != nil {
		return se, err
	}
	if err := validateDurationMs("iceKeepAliveInterval", s.IceKeepAliveInterval); err != nil {
		return se, err
	}
	if err := validateDurationMs("stunGatherTimeout", s.StunGatherTimeout); err != nil {
		return se, err
	}

	if s.SctpRtoMax != nil {
		se.SetSCTPRTOMax(time.Duration(*s.SctpRtoMax) * time.Millisecond)
	}
	if s.SctpMaxReceiveBufferSize != nil {
		se.SetSCTPMaxReceiveBufferSize(*s.SctpMaxReceiveBufferSize)
	}

	// pion SetICETimeouts takes all three params together. If the caller set any of
	// the three ICE timeouts, merge with pion's current defaults for the missing ones.
	if s.IceDisconnectedTimeout != nil || s.IceFailedTimeout != nil || s.IceKeepAliveInterval != nil {
		disc := pickDurationMs(s.IceDisconnectedTimeout, defaultIceDisconnectedMs)
		failed := pickDurationMs(s.IceFailedTimeout, defaultIceFailedMs)
		keep := pickDurationMs(s.IceKeepAliveInterval, defaultIceKeepAliveMs)
		se.SetICETimeouts(disc, failed, keep)
	}

	if s.StunGatherTimeout != nil {
		se.SetSTUNGatherTimeout(time.Duration(*s.StunGatherTimeout) * time.Millisecond)
	}

	return se, nil
}

func validateDurationMs(name string, v *uint32) error {
	if v == nil {
		return nil
	}
	if *v > maxDurationMs {
		return fmt.Errorf("settings.%s=%d exceeds max %d ms", name, *v, maxDurationMs)
	}
	return nil
}

func pickDurationMs(v *uint32, defaultMs uint32) time.Duration {
	ms := defaultMs
	if v != nil {
		ms = *v
	}
	return time.Duration(ms) * time.Millisecond
}
