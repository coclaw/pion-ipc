package rtc

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coclaw/pion-ipc/internal/ipc"
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

// 验证 ClosePeer 不阻塞其他 PC 的 GetPeer。
// 旧实现在 m.mu.Lock 下调 peer.Close()（200~400ms），会阻塞其他 goroutine 的 RLock。
func TestManager_ClosePeer_DoesNotBlockGetPeer(t *testing.T) {
	m := newTestManager()

	if err := m.CreatePeer("pc-1", nil); err != nil {
		t.Fatalf("CreatePeer(pc-1): %v", err)
	}
	if err := m.CreatePeer("pc-2", nil); err != nil {
		t.Fatalf("CreatePeer(pc-2): %v", err)
	}

	// 在后台关闭 pc-1（pc.Close 需要 200~400ms）
	closeDone := make(chan struct{})
	go func() {
		_ = m.ClosePeer("pc-1")
		close(closeDone)
	}()

	// GetPeer(pc-2) 应在 pc-1 close 完成之前就能返回
	getPeerDone := make(chan struct{})
	go func() {
		// 给 ClosePeer goroutine 一点启动时间
		<-time.After(10 * time.Millisecond)
		_, err := m.GetPeer("pc-2")
		if err != nil {
			t.Errorf("GetPeer(pc-2) failed: %v", err)
		}
		close(getPeerDone)
	}()

	select {
	case <-getPeerDone:
		// 正常——GetPeer 没被阻塞
	case <-time.After(3 * time.Second):
		t.Fatal("GetPeer(pc-2) blocked by ClosePeer(pc-1) — lock held during slow close")
	}

	<-closeDone
	m.CloseAll()
}

// 验证 CloseAll 完成后 map 已清空，且不持锁调慢操作。
func TestManager_CloseAll_DoesNotBlockGetPeer(t *testing.T) {
	m := newTestManager()

	for i := 0; i < 3; i++ {
		if err := m.CreatePeer(fmt.Sprintf("pc-%d", i), nil); err != nil {
			t.Fatalf("CreatePeer: %v", err)
		}
	}

	// CloseAll 后台执行
	closeAllDone := make(chan struct{})
	go func() {
		m.CloseAll()
		close(closeAllDone)
	}()

	// 新的 CreatePeer 应该不被长时间阻塞
	createDone := make(chan struct{})
	go func() {
		<-time.After(10 * time.Millisecond)
		// CloseAll 释放锁后 CreatePeer 就能拿到锁
		_ = m.CreatePeer("pc-new", nil)
		close(createDone)
	}()

	select {
	case <-createDone:
		// 正常
	case <-time.After(3 * time.Second):
		t.Fatal("CreatePeer blocked by CloseAll — lock held during slow close")
	}

	<-closeAllDone
	m.CloseAll()
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
