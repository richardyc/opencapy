package session

import (
	"fmt"
	"sync"
)

// Manager tracks all daemon-owned PTY sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// Create spawns a new PTY session and registers it.
func (m *Manager) Create(name, cwd, command string, args []string, cols, rows uint16) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[name]; exists {
		return nil, fmt.Errorf("session %q already exists", name)
	}
	s, err := NewSession(name, cwd, command, args, cols, rows)
	if err != nil {
		return nil, err
	}
	m.sessions[name] = s
	return s, nil
}

// Get returns a session by name, or nil if not found.
func (m *Manager) Get(name string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[name]
}

// Kill terminates a session and removes it from the manager.
func (m *Manager) Kill(name string) error {
	m.mu.Lock()
	s, ok := m.sessions[name]
	if ok {
		delete(m.sessions, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	return s.Kill()
}

// List returns info for all sessions.
func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
	}
	return infos
}

// Exists returns true if a session with the given name exists.
func (m *Manager) Exists(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.sessions[name]
	return ok
}

// KillAll terminates all sessions.
func (m *Manager) KillAll() {
	m.mu.Lock()
	sessions := make(map[string]*Session, len(m.sessions))
	for k, v := range m.sessions {
		sessions[k] = v
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.Kill()
	}
}
