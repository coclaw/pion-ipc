package rtc

import (
	"fmt"
	"net"
	"strings"
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
	SctpRtoMax               *uint32              `msgpack:"sctpRtoMax,omitempty"`
	SctpMaxReceiveBufferSize *uint32              `msgpack:"sctpMaxReceiveBufferSize,omitempty"`
	IceDisconnectedTimeout   *uint32              `msgpack:"iceDisconnectedTimeout,omitempty"`
	IceFailedTimeout         *uint32              `msgpack:"iceFailedTimeout,omitempty"`
	IceKeepAliveInterval     *uint32              `msgpack:"iceKeepAliveInterval,omitempty"`
	StunGatherTimeout        *uint32              `msgpack:"stunGatherTimeout,omitempty"`
	InterfaceFilter          *InterfaceFilterRule `msgpack:"interfaceFilter,omitempty"`
	IPFilter                 *IPFilterRule        `msgpack:"ipFilter,omitempty"`
}

// InterfaceFilterRule decides which network interface names participate in ICE gathering.
// Empty rule (both lists nil/empty) is equivalent to nil — pion's default (no filtering) is kept.
// Matching is case-sensitive strings.HasPrefix on the interface name.
//
// Semantics when at least one list is non-empty:
//   - If AllowPrefixes is non-empty, the interface must match one of them; otherwise it is dropped.
//   - Then DenyPrefixes is checked; any match drops the interface (deny wins over allow on overlap).
type InterfaceFilterRule struct {
	DenyPrefixes  []string `msgpack:"denyPrefixes,omitempty"`
	AllowPrefixes []string `msgpack:"allowPrefixes,omitempty"`
}

// IPFilterRule decides which IPs participate in ICE gathering.
// Empty rule (both lists nil/empty) is equivalent to nil — pion's default is kept.
// CIDRs are parsed via net.ParseCIDR; an invalid CIDR causes BuildSettingEngine to fail.
//
// Semantics mirror InterfaceFilterRule: allow narrows the set, deny removes (deny wins on overlap).
type IPFilterRule struct {
	DenyCIDRs  []string `msgpack:"denyCIDRs,omitempty"`
	AllowCIDRs []string `msgpack:"allowCIDRs,omitempty"`
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

	if f := CompileInterfaceFilter(s.InterfaceFilter); f != nil {
		se.SetInterfaceFilter(f)
	}
	f, err := CompileIPFilter(s.IPFilter)
	if err != nil {
		return se, err
	}
	if f != nil {
		se.SetIPFilter(f)
	}

	return se, nil
}

// CompileInterfaceFilter turns an InterfaceFilterRule into a keep-or-drop closure that
// pion's SettingEngine.SetInterfaceFilter accepts. Returns nil when no filtering applies
// (nil rule, or both lists empty); the caller should skip the setter in that case so pion's
// default (no filter) is preserved.
func CompileInterfaceFilter(rule *InterfaceFilterRule) func(string) bool {
	if rule == nil || (len(rule.DenyPrefixes) == 0 && len(rule.AllowPrefixes) == 0) {
		return nil
	}
	deny := append([]string(nil), rule.DenyPrefixes...)
	allow := append([]string(nil), rule.AllowPrefixes...)
	return func(name string) bool {
		if len(allow) > 0 && !hasAnyPrefix(name, allow) {
			return false
		}
		if hasAnyPrefix(name, deny) {
			return false
		}
		return true
	}
}

// CompileIPFilter turns an IPFilterRule into a keep-or-drop closure that pion's
// SettingEngine.SetIPFilter accepts. CIDR strings are parsed once up front;
// any invalid entry causes an error and no filter is installed (no partial state).
// Returns (nil, nil) when no filtering applies — caller skips the setter.
func CompileIPFilter(rule *IPFilterRule) (func(net.IP) bool, error) {
	if rule == nil || (len(rule.DenyCIDRs) == 0 && len(rule.AllowCIDRs) == 0) {
		return nil, nil
	}
	deny, err := parseCIDRs("ipFilter.denyCIDRs", rule.DenyCIDRs)
	if err != nil {
		return nil, err
	}
	allow, err := parseCIDRs("ipFilter.allowCIDRs", rule.AllowCIDRs)
	if err != nil {
		return nil, err
	}
	return func(ip net.IP) bool {
		if len(allow) > 0 && !ipInAny(ip, allow) {
			return false
		}
		if ipInAny(ip, deny) {
			return false
		}
		return true
	}, nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func ipInAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDRs(label string, items []string) ([]*net.IPNet, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]*net.IPNet, 0, len(items))
	for _, s := range items {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid CIDR %q: %w", label, s, err)
		}
		out = append(out, n)
	}
	return out, nil
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
