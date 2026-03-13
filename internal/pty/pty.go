package pty

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// tmuxBin returns the absolute path to tmux, checking common Homebrew locations
// when the binary is not in the process PATH (e.g. when running as a LaunchAgent).
func tmuxBin() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "tmux"
}

// PTYOutput is a chunk of data read from a PTY session.
type PTYOutput struct {
	Session  string
	ClientID string // which client opened this PTY
	Data     []byte
}

// PTYSession holds a single active PTY session.
type PTYSession struct {
	sessionName string
	groupName   string // grouped tmux session name; empty for direct sessions
	ptmx        *os.File
	cmd         *exec.Cmd
	clientID    string // who opened this PTY
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

// Open spawns a grouped tmux session mirroring sessionName inside a PTY.
// startDir sets the working directory of the grouped session (should match the
// target session's project path so session.projectPath is correct on iOS).
//
// Two-step approach: first create the grouped session synchronously (detached),
// apply styling, then open a PTY via attach-session. This avoids the race
// condition where set-option ran before new-session had registered with the
// tmux server, causing the brown status bar to never appear.
//
// Mouse is explicitly disabled on the grouped session so tmux does not emit
// mouse-tracking escape sequences (\x1b[?1003h etc.) into the PTY. Those codes
// cause SwiftTerm's terminal emulator to activate mouse-tracking mode internally,
// intercepting UIScrollView pan gestures even when allowMouseReporting=false.
//
// clientID identifies which WebSocket client owns this PTY session.
func (m *Manager) Open(sessionName, clientID string, cols, rows int, startDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionName]; exists {
		return fmt.Errorf("pty session %q already open", sessionName)
	}

	// Derive a unique grouped-session name for this connection.
	groupName := "ocpy_" + sessionName
	// Kill any stale grouped session from a previous connection (idempotent).
	_ = exec.Command(tmuxBin(), "kill-session", "-t", groupName).Run()

	// Step 1: Create the grouped session synchronously (detached, no PTY yet).
	// new-session -d -s <group> -t <target>: creates a detached session that
	// shares the window group of <target>.  The group session has its own
	// terminal size and client list, so attaching iOS here never resizes the
	// Mac client.
	createArgs := []string{"new-session", "-d", "-s", groupName, "-t", sessionName}
	if startDir != "" {
		createArgs = append(createArgs, "-c", startDir)
	}
	if err := exec.Command(tmuxBin(), createArgs...).Run(); err != nil {
		return fmt.Errorf("create grouped session for %q: %w", sessionName, err)
	}

	// Step 2: Apply opencapy branding now that the session definitely exists.
	// Per-session set-option so this only affects the ocpy_ mirror session.
	_ = exec.Command(tmuxBin(), "set-option", "-t", groupName,
		"status-style", "bg=#7B5B3A,fg=#F5E6D3").Run()
	// Disable mouse so tmux does not send mouse-tracking enable sequences that
	// would intercept iOS UIScrollView touch events inside SwiftTerm.
	_ = exec.Command(tmuxBin(), "set-option", "-t", groupName, "mouse", "off").Run()

	// Step 3: Open a PTY by attaching to the already-created grouped session.
	cmd := exec.Command(tmuxBin(), "attach-session", "-t", groupName)
	// Ensure UTF-8 locale so tmux treats this PTY client as a Unicode terminal.
	// Without LANG/LC_CTYPE the LaunchAgent environment has no locale set, which
	// makes tmux fall back to ASCII mode and substitute _ for multi-byte characters.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		_ = exec.Command(tmuxBin(), "kill-session", "-t", groupName).Run()
		return fmt.Errorf("start pty for session %q: %w", sessionName, err)
	}

	sess := &PTYSession{
		sessionName: sessionName,
		groupName:   groupName,
		ptmx:        ptmx,
		cmd:         cmd,
		clientID:    clientID,
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
				case m.events <- PTYOutput{Session: sessionName, ClientID: clientID, Data: chunk}:
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
		// Clean up on natural exit — but only if this goroutine's session is
		// still the active one.  If Close() was already called and a new Open()
		// replaced it, skip cleanup to avoid killing the new grouped session.
		_ = sess.cmd.Wait()
		_ = sess.ptmx.Close()
		m.mu.Lock()
		isCurrent := m.sessions[sessionName] == sess
		if isCurrent {
			delete(m.sessions, sessionName)
		}
		m.mu.Unlock()
		if isCurrent {
			_ = exec.Command(tmuxBin(), "kill-session", "-t", sess.groupName).Run()
		}
	}()

	return nil
}

// OpenDirect spawns an arbitrary command inside a PTY without any tmux involvement.
// Used by the claude shim so the daemon can stream output to iOS without tmux.
func (m *Manager) OpenDirect(sessionName, clientID string, cols, rows int, cwd string, args []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionName]; exists {
		return fmt.Errorf("pty session %q already open", sessionName)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = cwd
	// Build environment from the daemon's env, stripping CLAUDECODE so that
	// claude does not refuse to start with "nested session" error when the
	// daemon process itself was launched inside a Claude Code session.
	env := make([]string, 0, len(os.Environ())+4)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env,
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return fmt.Errorf("start direct pty for %q: %w", sessionName, err)
	}

	sess := &PTYSession{
		sessionName: sessionName,
		groupName:   "", // empty = direct session; no tmux cleanup on close
		ptmx:        ptmx,
		cmd:         cmd,
		clientID:    clientID,
	}
	m.sessions[sessionName] = sess

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case m.events <- PTYOutput{Session: sessionName, ClientID: clientID, Data: chunk}:
				default:
				}
			}
			if err != nil {
				break
			}
		}
		_ = sess.cmd.Wait()
		_ = sess.ptmx.Close()
		m.mu.Lock()
		if m.sessions[sessionName] == sess {
			delete(m.sessions, sessionName)
		}
		m.mu.Unlock()
		// Signal that the process exited by sending a zero-length PTYOutput.
		// The server uses this to deregister the direct session.
		select {
		case m.events <- PTYOutput{Session: sessionName, ClientID: clientID, Data: nil}:
		default:
		}
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

	_ = sess.ptmx.Close()
	if sess.cmd.Process != nil {
		sess.cmd.Process.Kill()
	}
	if sess.groupName != "" {
		_ = exec.Command(tmuxBin(), "kill-session", "-t", sess.groupName).Run()
	}
}

// CloseByClient closes and removes all PTY sessions owned by the given clientID.
// Called when the owning WebSocket client disconnects.
func (m *Manager) CloseByClient(clientID string) {
	m.mu.Lock()
	var toClose []*PTYSession
	var toDelete []string
	for name, sess := range m.sessions {
		if sess.clientID == clientID {
			toClose = append(toClose, sess)
			toDelete = append(toDelete, name)
		}
	}
	for _, name := range toDelete {
		delete(m.sessions, name)
	}
	m.mu.Unlock()

	for _, sess := range toClose {
		_ = sess.ptmx.Close()
		if sess.cmd.Process != nil {
			sess.cmd.Process.Kill()
		}
		if sess.groupName != "" {
			_ = exec.Command(tmuxBin(), "kill-session", "-t", sess.groupName).Run()
		}
	}
}
