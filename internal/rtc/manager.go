package rtc

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/coclaw/pion-ipc/internal/ipc"
)

// Manager manages multiple PeerConnections.
type Manager struct {
	logger *slog.Logger
	writer *ipc.Writer
	peers  map[string]*Peer
	mu     sync.RWMutex
}

// NewManager creates a new PeerConnection manager.
func NewManager(logger *slog.Logger, writer *ipc.Writer) *Manager {
	return &Manager{
		logger: logger,
		writer: writer,
		peers:  make(map[string]*Peer),
	}
}

// CreatePeer creates a new PeerConnection with the given ID and ICE configuration.
func (m *Manager) CreatePeer(pcID string, iceServers []ICEServer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.peers[pcID]; exists {
		return fmt.Errorf("peer %q already exists", pcID)
	}

	peer, err := NewPeer(pcID, iceServers, m.logger, m.writer)
	if err != nil {
		return fmt.Errorf("create peer %q: %w", pcID, err)
	}

	m.peers[pcID] = peer
	m.logger.Info("peer created", "pcId", pcID)
	return nil
}

// GetPeer returns the Peer with the given ID.
func (m *Manager) GetPeer(pcID string) (*Peer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	peer, ok := m.peers[pcID]
	if !ok {
		return nil, fmt.Errorf("peer %q not found", pcID)
	}
	return peer, nil
}

// ClosePeer closes and removes the PeerConnection with the given ID.
// Deletes from map before releasing the lock so the slow peer.Close() (200-400ms) doesn't block other PCs.
func (m *Manager) ClosePeer(pcID string) error {
	m.mu.Lock()
	peer, ok := m.peers[pcID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("peer %q not found", pcID)
	}
	delete(m.peers, pcID)
	m.mu.Unlock()

	if err := peer.Close(); err != nil {
		m.logger.Warn("error closing peer", "pcId", pcID, "error", err)
	}
	m.logger.Info("peer closed", "pcId", pcID)
	return nil
}

// CloseAll closes all PeerConnections.
// Collects and clears the map under lock, then closes outside the lock to avoid blocking.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	peers := m.peers
	m.peers = make(map[string]*Peer)
	m.mu.Unlock()

	for id, peer := range peers {
		if err := peer.Close(); err != nil {
			m.logger.Warn("error closing peer during shutdown", "pcId", id, "error", err)
		}
	}
	m.logger.Info("all peers closed")
}

// ICEServer represents an ICE server configuration.
type ICEServer struct {
	URLs       []string `msgpack:"urls"`
	Username   string   `msgpack:"username,omitempty"`
	Credential string   `msgpack:"credential,omitempty"`
}
