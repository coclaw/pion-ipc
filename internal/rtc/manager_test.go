package rtc

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nicosmd/pion-ipc/internal/ipc"
)

func newTestManager() *Manager {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	writer := ipc.NewWriter(&bytes.Buffer{})
	return NewManager(logger, writer)
}

func TestManager_CreatePeer(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	if err := m.CreatePeer("pc-1", nil); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
}

func TestManager_CreatePeer_Duplicate(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	if err := m.CreatePeer("pc-1", nil); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
	err := m.CreatePeer("pc-1", nil)
	if err == nil {
		t.Fatal("expected error for duplicate peer")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q", err)
	}
}

func TestManager_GetPeer_Success(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	if err := m.CreatePeer("pc-1", nil); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
	peer, err := m.GetPeer("pc-1")
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if peer == nil {
		t.Fatal("peer is nil")
	}
}

func TestManager_GetPeer_NotFound(t *testing.T) {
	m := newTestManager()
	_, err := m.GetPeer("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent peer")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q", err)
	}
}

func TestManager_ClosePeer_Success(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	if err := m.CreatePeer("pc-1", nil); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
	if err := m.ClosePeer("pc-1"); err != nil {
		t.Fatalf("ClosePeer: %v", err)
	}
	// Should not be found after close
	_, err := m.GetPeer("pc-1")
	if err == nil {
		t.Fatal("expected error after ClosePeer")
	}
}

func TestManager_ClosePeer_NotFound(t *testing.T) {
	m := newTestManager()
	err := m.ClosePeer("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent peer")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q", err)
	}
}

func TestManager_CloseAll(t *testing.T) {
	m := newTestManager()

	for i := 0; i < 3; i++ {
		if err := m.CreatePeer(strings.Repeat("x", i+1), nil); err != nil {
			t.Fatalf("CreatePeer: %v", err)
		}
	}

	m.CloseAll()

	// All peers should be gone
	for i := 0; i < 3; i++ {
		_, err := m.GetPeer(strings.Repeat("x", i+1))
		if err == nil {
			t.Errorf("peer %d still exists after CloseAll", i)
		}
	}
}

func TestManager_ConcurrentCreateClose(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	const n = 20
	var wg sync.WaitGroup

	// Concurrent creates
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			pcID := fmt.Sprintf("pc-%d", id)
			if err := m.CreatePeer(pcID, nil); err != nil {
				t.Errorf("CreatePeer(%s): %v", pcID, err)
			}
		}(i)
	}
	wg.Wait()

	// Verify all created
	for i := 0; i < n; i++ {
		pcID := fmt.Sprintf("pc-%d", i)
		if _, err := m.GetPeer(pcID); err != nil {
			t.Errorf("GetPeer(%s) after create: %v", pcID, err)
		}
	}

	// Concurrent close + get
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func(id int) {
			defer wg.Done()
			pcID := fmt.Sprintf("pc-%d", id)
			_ = m.ClosePeer(pcID) // may race, errors are OK
		}(i)
		go func(id int) {
			defer wg.Done()
			pcID := fmt.Sprintf("pc-%d", id)
			_, _ = m.GetPeer(pcID) // may or may not find it
		}(i)
	}
	wg.Wait()
}

func TestManager_CreatePeer_WithICEServers(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()

	servers := []ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	if err := m.CreatePeer("pc-ice", servers); err != nil {
		t.Fatalf("CreatePeer with ICE servers: %v", err)
	}
}
