package rtc

import (
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func u32ptr(v uint32) *uint32 {
	return &v
}

// assertEqual asserts that the built SettingEngine matches an expected engine produced
// by calling pion setters directly. Using DeepEqual on the struct avoids depending on
// private field names — only the net effect of setter composition matters.
func assertEqual(t *testing.T, got, want webrtc.SettingEngine) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SettingEngine mismatch:\n got:  %+v\n want: %+v", got, want)
	}
}

func TestBuildSettingEngine_Nil(t *testing.T) {
	got, err := BuildSettingEngine(nil)
	if err != nil {
		t.Fatalf("nil settings: unexpected error %v", err)
	}
	assertEqual(t, got, webrtc.SettingEngine{})
}

func TestBuildSettingEngine_Empty(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{})
	if err != nil {
		t.Fatalf("empty settings: unexpected error %v", err)
	}
	assertEqual(t, got, webrtc.SettingEngine{})
}

func TestBuildSettingEngine_SctpRtoMax(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{SctpRtoMax: u32ptr(10000)})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetSCTPRTOMax(10 * time.Second)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_SctpMaxReceiveBufferSize(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{SctpMaxReceiveBufferSize: u32ptr(2 * 1024 * 1024)})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetSCTPMaxReceiveBufferSize(2 * 1024 * 1024)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_StunGatherTimeout(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{StunGatherTimeout: u32ptr(8000)})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetSTUNGatherTimeout(8 * time.Second)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_ICETimeouts_SinglePartial(t *testing.T) {
	// Only iceFailedTimeout specified — the other two fall back to pion defaults.
	got, err := BuildSettingEngine(&PeerSettings{IceFailedTimeout: u32ptr(15000)})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetICETimeouts(
		time.Duration(defaultIceDisconnectedMs)*time.Millisecond,
		15*time.Second,
		time.Duration(defaultIceKeepAliveMs)*time.Millisecond,
	)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_ICETimeouts_TwoFields(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{
		IceDisconnectedTimeout: u32ptr(3000),
		IceKeepAliveInterval:   u32ptr(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetICETimeouts(
		3*time.Second,
		time.Duration(defaultIceFailedMs)*time.Millisecond,
		time.Second,
	)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_ICETimeouts_AllThree(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{
		IceDisconnectedTimeout: u32ptr(3000),
		IceFailedTimeout:       u32ptr(12000),
		IceKeepAliveInterval:   u32ptr(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetICETimeouts(3*time.Second, 12*time.Second, time.Second)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_AllFields(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{
		SctpRtoMax:               u32ptr(10000),
		SctpMaxReceiveBufferSize: u32ptr(1 << 20),
		IceDisconnectedTimeout:   u32ptr(3000),
		IceFailedTimeout:         u32ptr(12000),
		IceKeepAliveInterval:     u32ptr(1000),
		StunGatherTimeout:        u32ptr(8000),
	})
	if err != nil {
		t.Fatal(err)
	}
	var want webrtc.SettingEngine
	want.SetSCTPRTOMax(10 * time.Second)
	want.SetSCTPMaxReceiveBufferSize(1 << 20)
	want.SetICETimeouts(3*time.Second, 12*time.Second, time.Second)
	want.SetSTUNGatherTimeout(8 * time.Second)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_OutOfRange(t *testing.T) {
	bad := uint32(maxDurationMs + 1)
	cases := []struct {
		name     string
		settings *PeerSettings
	}{
		{"sctpRtoMax", &PeerSettings{SctpRtoMax: &bad}},
		{"iceDisconnectedTimeout", &PeerSettings{IceDisconnectedTimeout: &bad}},
		{"iceFailedTimeout", &PeerSettings{IceFailedTimeout: &bad}},
		{"iceKeepAliveInterval", &PeerSettings{IceKeepAliveInterval: &bad}},
		{"stunGatherTimeout", &PeerSettings{StunGatherTimeout: &bad}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := BuildSettingEngine(c.settings); err == nil {
				t.Fatalf("%s=%d expected error", c.name, bad)
			}
		})
	}
}

func TestBuildSettingEngine_BoundaryMax(t *testing.T) {
	got, err := BuildSettingEngine(&PeerSettings{SctpRtoMax: u32ptr(maxDurationMs)})
	if err != nil {
		t.Fatalf("boundary: %v", err)
	}
	var want webrtc.SettingEngine
	want.SetSCTPRTOMax(time.Duration(maxDurationMs) * time.Millisecond)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_Zero(t *testing.T) {
	// Zero is accepted and distinct from "not set" thanks to the pointer field.
	got, err := BuildSettingEngine(&PeerSettings{IceFailedTimeout: u32ptr(0)})
	if err != nil {
		t.Fatalf("zero: %v", err)
	}
	var want webrtc.SettingEngine
	want.SetICETimeouts(
		time.Duration(defaultIceDisconnectedMs)*time.Millisecond,
		0,
		time.Duration(defaultIceKeepAliveMs)*time.Millisecond,
	)
	assertEqual(t, got, want)
}

func TestBuildSettingEngine_EmptyFilterRulesNoOp(t *testing.T) {
	// Empty (non-nil) rule structs with no entries must behave the same as nil filter:
	// no setter call, SettingEngine remains zero-valued.
	got, err := BuildSettingEngine(&PeerSettings{
		InterfaceFilter: &InterfaceFilterRule{},
		IPFilter:        &IPFilterRule{},
	})
	if err != nil {
		t.Fatalf("empty filter rules: %v", err)
	}
	assertEqual(t, got, webrtc.SettingEngine{})
}

func TestBuildSettingEngine_FilterInstalled(t *testing.T) {
	// Filter installation path: provide non-empty rules so setters are actually invoked.
	// We cannot DeepEqual against a "want" SettingEngine — closure equality fails.
	// Behavioral correctness of the filters themselves is covered by CompileInterfaceFilter /
	// CompileIPFilter tests; here we only assert BuildSettingEngine accepts the rules and
	// returns without error (i.e. exercises the setter call paths).
	_, err := BuildSettingEngine(&PeerSettings{
		InterfaceFilter: &InterfaceFilterRule{DenyPrefixes: []string{"docker"}},
		IPFilter:        &IPFilterRule{DenyCIDRs: []string{"172.16.0.0/12"}},
	})
	if err != nil {
		t.Fatalf("filter install: %v", err)
	}
}

func TestBuildSettingEngine_InvalidCIDRReturnsError(t *testing.T) {
	cases := []struct {
		name     string
		settings *PeerSettings
		wantSub  string
	}{
		{
			"denyCIDRs invalid",
			&PeerSettings{IPFilter: &IPFilterRule{DenyCIDRs: []string{"not-a-cidr"}}},
			"ipFilter.denyCIDRs",
		},
		{
			"allowCIDRs invalid",
			&PeerSettings{IPFilter: &IPFilterRule{AllowCIDRs: []string{"10.0.0.0/99"}}},
			"ipFilter.allowCIDRs",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildSettingEngine(c.settings)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("error missing %q substring: %v", c.wantSub, err)
			}
		})
	}
}

// --- CompileInterfaceFilter ---

func TestCompileInterfaceFilter_NilOrEmpty(t *testing.T) {
	cases := []struct {
		name string
		rule *InterfaceFilterRule
	}{
		{"nil rule", nil},
		{"both lists empty", &InterfaceFilterRule{}},
		{"empty allow, empty deny", &InterfaceFilterRule{AllowPrefixes: []string{}, DenyPrefixes: []string{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CompileInterfaceFilter(c.rule); got != nil {
				t.Fatalf("expected nil filter, got non-nil")
			}
		})
	}
}

func TestCompileInterfaceFilter_DenyOnly(t *testing.T) {
	f := CompileInterfaceFilter(&InterfaceFilterRule{DenyPrefixes: []string{"docker", "br-", "veth"}})
	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	cases := map[string]bool{
		"docker0":     false,
		"br-abcdef12": false,
		"veth1234":    false,
		"eth0":        true,
		"wlp2s0":      true,
		"tailscale0":  true,
		"lo":          true,
		"":            true,
	}
	for name, want := range cases {
		if got := f(name); got != want {
			t.Errorf("%q: got keep=%v want %v", name, got, want)
		}
	}
}

func TestCompileInterfaceFilter_AllowOnly(t *testing.T) {
	f := CompileInterfaceFilter(&InterfaceFilterRule{AllowPrefixes: []string{"eth", "wlp"}})
	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	cases := map[string]bool{
		"eth0":    true,
		"wlp2s0":  true,
		"docker0": false,
		"lo":      false,
		"":        false,
	}
	for name, want := range cases {
		if got := f(name); got != want {
			t.Errorf("%q: got keep=%v want %v", name, got, want)
		}
	}
}

func TestCompileInterfaceFilter_DenyWinsOnOverlap(t *testing.T) {
	// allow includes "eth", deny includes "eth1" — "eth1" must be dropped (deny wins).
	f := CompileInterfaceFilter(&InterfaceFilterRule{
		AllowPrefixes: []string{"eth"},
		DenyPrefixes:  []string{"eth1"},
	})
	cases := map[string]bool{
		"eth0": true,
		"eth1": false,
		"eth2": true,
		"wlp0": false, // not in allow
	}
	for name, want := range cases {
		if got := f(name); got != want {
			t.Errorf("%q: got keep=%v want %v", name, got, want)
		}
	}
}

func TestCompileInterfaceFilter_CaseSensitive(t *testing.T) {
	f := CompileInterfaceFilter(&InterfaceFilterRule{DenyPrefixes: []string{"docker"}})
	if !f("Docker0") {
		t.Error("uppercase Docker0 should pass (case-sensitive prefix match)")
	}
	if f("docker0") {
		t.Error("lowercase docker0 should be dropped")
	}
}

// --- CompileIPFilter ---

func TestCompileIPFilter_NilOrEmpty(t *testing.T) {
	cases := []struct {
		name string
		rule *IPFilterRule
	}{
		{"nil rule", nil},
		{"both lists empty", &IPFilterRule{}},
		{"empty allow, empty deny", &IPFilterRule{AllowCIDRs: []string{}, DenyCIDRs: []string{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := CompileIPFilter(c.rule)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f != nil {
				t.Fatalf("expected nil filter, got non-nil")
			}
		})
	}
}

func TestCompileIPFilter_DenyOnly_IPv4(t *testing.T) {
	f, err := CompileIPFilter(&IPFilterRule{DenyCIDRs: []string{"172.16.0.0/12", "169.254.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"172.17.0.1":     false, // docker bridge typical
		"172.31.255.255": false, // /12 boundary upper
		"172.32.0.0":     true,  // just outside /12
		"169.254.1.2":    false,
		"192.168.1.1":    true,
		"10.0.0.1":       true,
		"8.8.8.8":        true,
	}
	for s, want := range cases {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test ip %s", s)
		}
		if got := f(ip); got != want {
			t.Errorf("%s: got keep=%v want %v", s, got, want)
		}
	}
}

func TestCompileIPFilter_AllowOnly(t *testing.T) {
	f, err := CompileIPFilter(&IPFilterRule{AllowCIDRs: []string{"192.168.0.0/16", "10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"192.168.1.1": true,
		"10.0.0.1":    true,
		"172.16.0.1":  false,
		"8.8.8.8":     false,
	}
	for s, want := range cases {
		if got := f(net.ParseIP(s)); got != want {
			t.Errorf("%s: got keep=%v want %v", s, got, want)
		}
	}
}

func TestCompileIPFilter_DenyWinsOnOverlap(t *testing.T) {
	// allow 10/8 but deny 10.0.0.0/24 — addresses in /24 dropped, others in /8 kept.
	f, err := CompileIPFilter(&IPFilterRule{
		AllowCIDRs: []string{"10.0.0.0/8"},
		DenyCIDRs:  []string{"10.0.0.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"10.0.0.5":   false, // in deny
		"10.0.1.5":   true,  // in allow, not in deny
		"10.255.0.1": true,
		"192.168.1.1": false, // not in allow
	}
	for s, want := range cases {
		if got := f(net.ParseIP(s)); got != want {
			t.Errorf("%s: got keep=%v want %v", s, got, want)
		}
	}
}

func TestCompileIPFilter_IPv6(t *testing.T) {
	f, err := CompileIPFilter(&IPFilterRule{DenyCIDRs: []string{"fe80::/10", "fc00::/7"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"fe80::1":     false, // link-local
		"fd12:3456::1": false, // ULA fc00::/7
		"2001:db8::1": true,  // global
		"::1":         true,  // loopback (not in deny)
	}
	for s, want := range cases {
		if got := f(net.ParseIP(s)); got != want {
			t.Errorf("%s: got keep=%v want %v", s, got, want)
		}
	}
}

func TestCompileIPFilter_InvalidCIDR(t *testing.T) {
	cases := []struct {
		name string
		rule *IPFilterRule
	}{
		{"deny garbage", &IPFilterRule{DenyCIDRs: []string{"not-a-cidr"}}},
		{"deny bad mask", &IPFilterRule{DenyCIDRs: []string{"10.0.0.0/99"}}},
		{"allow missing mask", &IPFilterRule{AllowCIDRs: []string{"10.0.0.0"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := CompileIPFilter(c.rule)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if f != nil {
				t.Fatalf("expected nil filter on error, got non-nil")
			}
		})
	}
}
