package session

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const ringSize = 2 * 1024 * 1024 // 2 MB ring buffer — Claude's TUI re-renders frequently

// Session is a daemon-owned PTY session with ring buffer and client fan-out.
type Session struct {
	Name      string
	Cwd       string
	CreatedAt time.Time

	ptmx *os.File
	cmd  *exec.Cmd

	mu      sync.Mutex
	ring    []byte
	clients map[string]chan []byte // clientID → bounded output chan
	primary string                // first attacher owns resize
	cols    uint16
	rows    uint16
	alive   bool

	// OnOutput is called for every PTY read (for watcher integration).
	OnOutput func(name string, data []byte)
}

// SessionInfo is a read-only snapshot of session state.
type SessionInfo struct {
	Name      string    `json:"name"`
	Cwd       string    `json:"cwd"`
	CreatedAt time.Time `json:"created_at"`
	Alive     bool      `json:"alive"`
	Clients   int       `json:"clients"`
}

// NewSession spawns a process in a new PTY and starts the read loop.
func NewSession(name, cwd, command string, args []string, cols, rows uint16) (*Session, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = cwd
	cmd.Env = append(safeEnv(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
		// Advertise session name so child processes (e.g. the claude shim) know
		// they're running inside a daemon session and can register as children.
		"OPENCAPY_SESSION="+name,
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		return nil, err
	}

	s := &Session{
		Name:      name,
		Cwd:       cwd,
		CreatedAt: time.Now(),
		ptmx:      ptmx,
		cmd:       cmd,
		ring:      make([]byte, 0, ringSize),
		clients:   make(map[string]chan []byte),
		cols:      cols,
		rows:      rows,
		alive:     true,
	}

	go s.readLoop()
	return s, nil
}

func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			s.mu.Lock()
			// Append to ring buffer, trim if too large.
			s.ring = append(s.ring, chunk...)
			if len(s.ring) > ringSize {
				s.ring = s.ring[len(s.ring)-ringSize:]
			}
			// Fan out to all subscribed clients.
			// Drop the chunk if the channel is full — don't disconnect. The ring
			// buffer already has the data; the client's terminal will self-correct
			// on the next received chunk. Disconnecting caused "middle of session"
			// truncation when claude resume blasted a large history all at once.
			for _, ch := range s.clients {
				select {
				case ch <- chunk:
				default:
				}
			}
			s.mu.Unlock()

			if s.OnOutput != nil {
				s.OnOutput(s.Name, chunk)
			}
		}
		if err != nil {
			break
		}
	}

	// Process exited.
	_ = s.cmd.Wait()
	_ = s.ptmx.Close()

	s.mu.Lock()
	s.alive = false
	for id, ch := range s.clients {
		close(ch)
		delete(s.clients, id)
	}
	s.mu.Unlock()
}

// Subscribe registers a client and returns a bounded output channel plus the
// current ring buffer snapshot (prefixed with terminal reset).
func (s *Session) Subscribe(clientID string) (output <-chan []byte, replay []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan []byte, 4096)
	s.clients[clientID] = ch
	if s.primary == "" {
		s.primary = clientID
	}

	// Return raw ring buffer — no terminal reset prefix. A reset causes terminal
	// emulators to send initialization queries back to the PTY, injecting garbage.
	snap := make([]byte, len(s.ring))
	copy(snap, s.ring)

	return ch, snap
}

// Unsubscribe removes a client from the fan-out.
func (s *Session) Unsubscribe(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.clients[clientID]; ok {
		close(ch)
		delete(s.clients, clientID)
	}
	if s.primary == clientID {
		s.primary = ""
		// Promote next client if any.
		for id := range s.clients {
			s.primary = id
			break
		}
	}
}

// Write sends data to the PTY.
func (s *Session) Write(data []byte) error {
	_, err := s.ptmx.Write(data)
	return err
}

// Resize updates the PTY window size. Any client can resize; last write wins.
func (s *Session) Resize(cols, rows uint16) {
	s.mu.Lock()
	s.cols = cols
	s.rows = rows
	s.mu.Unlock()
	_ = pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Kill sends SIGHUP and closes the PTY.
func (s *Session) Kill() error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(os.Kill)
	}
	return s.ptmx.Close()
}

// RingSnapshot returns a copy of the ring buffer.
func (s *Session) RingSnapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := make([]byte, len(s.ring))
	copy(snap, s.ring)
	return snap
}

// Info returns a read-only snapshot of session state.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		Name:      s.Name,
		Cwd:       s.Cwd,
		CreatedAt: s.CreatedAt,
		Alive:     s.alive,
		Clients:   len(s.clients),
	}
}

// Alive returns whether the session process is still running.
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

// safeEnv builds a PTY environment from an allowlist of the process env vars.
func safeEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"SSH_AUTH_SOCK": true, "SSH_AGENT_PID": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"EDITOR": true, "VISUAL": true, "PAGER": true,
		"COLORTERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
		"SSL_CERT_FILE": true,
	}
	allowedPrefixes := []string{
		"LC_", "XDG_", "GIT_",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
		"NVM_", "PYENV_", "RBENV_", "GOPATH", "GOROOT", "GOENV", "GOBIN",
		"CARGO_HOME", "RUSTUP_HOME",
		"JAVA_HOME", "ANDROID_HOME", "ANDROID_SDK_ROOT",
	}
	var env []string
	for _, kv := range os.Environ() {
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if allowed[key] {
			env = append(env, kv)
			continue
		}
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(key, p) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
