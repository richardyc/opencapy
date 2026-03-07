package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// PTYOutput is a chunk of data read from a PTY session.
type PTYOutput struct {
	Session string
	Data    []byte
}

// PTYSession holds a single active PTY session.
type PTYSession struct {
	sessionName string
	ptmx        *os.File
	cmd         *exec.Cmd
}

// Manager manages active PTY sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*PTYSession
	events   chan PTYOutput
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*PTYSession),
		events:   make(chan PTYOutput, 256),
	}
}

// Events returns the read-only channel of PTY output events.
func (m *Manager) Events() <-chan PTYOutput {
	return m.events
}

// Open spawns `tmux attach-session -t <sessionName>` inside a PTY.
func (m *Manager) Open(sessionName string, cols, rows int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionName]; exists {
		return fmt.Errorf("pty session %q already open", sessionName)
	}

	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return fmt.Errorf("start pty for session %q: %w", sessionName, err)
	}

	sess := &PTYSession{
		sessionName: sessionName,
		ptmx:        ptmx,
		cmd:         cmd,
	}
	m.sessions[sessionName] = sess

	// Background goroutine: read pty output → events channel
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case m.events <- PTYOutput{Session: sessionName, Data: chunk}:
				default:
					// drop if channel full
				}
			}
			if err != nil {
				if err != io.EOF {
					// session ended or closed
				}
				break
			}
		}
		// Clean up from map when the process exits
		m.mu.Lock()
		delete(m.sessions, sessionName)
		m.mu.Unlock()
	}()

	return nil
}

// Write sends bytes to the PTY stdin of the named session.
func (m *Manager) Write(sessionName string, data []byte) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("pty session %q not found", sessionName)
	}
	_, err := sess.ptmx.Write(data)
	return err
}

// Resize updates the terminal window size for the named session.
func (m *Manager) Resize(sessionName string, cols, rows int) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("pty session %q not found", sessionName)
	}
	return pty.Setsize(sess.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// Close kills the PTY session and removes it from the manager.
func (m *Manager) Close(sessionName string) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionName]
	if ok {
		delete(m.sessions, sessionName)
	}
	m.mu.Unlock()

	if !ok {
		return
	}

	sess.ptmx.Close()
	if sess.cmd.Process != nil {
		sess.cmd.Process.Kill()
	}
	sess.cmd.Wait() //nolint:errcheck
}
