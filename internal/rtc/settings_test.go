package rtc

import (
	"reflect"
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
