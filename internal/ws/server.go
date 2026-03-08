package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/richardyc/opencapy/internal/fsevent"
	fs "github.com/richardyc/opencapy/internal/fs"
	"github.com/richardyc/opencapy/internal/platform"
	ptymanager "github.com/richardyc/opencapy/internal/pty"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/push"
	"github.com/richardyc/opencapy/internal/tmux"
	"github.com/richardyc/opencapy/internal/watcher"
)

// OutboundMessage is sent to iOS clients.
type OutboundMessage struct {
	Type    string      `json:"type"`    // "event", "snapshot", "pong"
	Payload interface{} `json:"payload"`
}

// InboundMessage is received from iOS clients.
type InboundMessage struct {
	Type    string `json:"type"`              // "ping", "approve", "deny", "send_keys", "register_push", ...
	Session string `json:"session"`
	Keys    string `json:"keys,omitempty"`
	Token   string `json:"token,omitempty"`
	// PTY fields
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Data string `json:"data,omitempty"` // base64 for pty_input; raw path for file_write
	// File fields
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"` // base64 for file_write
	// Session creation fields
	Mode        string `json:"mode,omitempty"`         // "chat" or "terminal"
	ProjectPath string `json:"project_path,omitempty"` // working directory for new session
}

// SessionSnapshot holds a point-in-time snapshot of a session's state.
type SessionSnapshot struct {
	Name        string    `json:"name"`
	ProjectPath string    `json:"project_path"`
	LastOutput  string    `json:"last_output"` // last 20 lines of pane
	Timestamp   time.Time `json:"timestamp"`
}

// Client represents a connected iOS device.
type Client struct {
	ID   string
	conn *websocket.Conn
	send chan []byte
}

// Server is the WebSocket server that bridges watcher events to iOS.
type Server struct {
	port     int
	clients  map[string]*Client
	events   <-chan watcher.Event
	registry *project.Registry
	push     *push.Registry
	ptyMgr   *ptymanager.Manager
	mu       sync.RWMutex
}

// New creates a new WebSocket server.
func New(port int, events <-chan watcher.Event, reg *project.Registry, pushReg *push.Registry, ptyMgr *ptymanager.Manager) *Server {
	return &Server{
		port:     port,
		clients:  make(map[string]*Client),
		events:   events,
		registry: reg,
		push:     pushReg,
		ptyMgr:   ptyMgr,
	}
}

// Start begins the WebSocket server. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.RLock()
		clientCount := len(s.clients)
		s.mu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"clients": clientCount,
		})
	})
	mux.HandleFunc("/qr", s.handleQR)
	mux.HandleFunc("/pair", s.handlePair)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	// Broadcast events to all clients
	go s.broadcastLoop(ctx)

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("WebSocket server listening on :%d", s.port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handleQR generates and serves a QR code PNG for pairing.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := platform.Hostname()
	tailscaleHost, _ := platform.TailscaleHostname()

	qrContent := fmt.Sprintf(
		"opencapy://pair?name=%s&host=%s&port=%d&type=tailscale",
		hostname, tailscaleHost, s.port,
	)

	png, err := qrcode.Encode(qrContent, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "qr encode error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	w.Write(png) //nolint:errcheck
}

// handlePair returns JSON pairing information.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := platform.Hostname()
	tailscaleHost, _ := platform.TailscaleHostname()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"name": hostname,
		"host": tailscaleHost,
		"port": s.port,
		"type": "tailscale",
	})
}

// isPathAllowed checks whether path falls within one of the registered project paths.
// This prevents path traversal attacks on file_read, file_write, and list_dir.
func isPathAllowed(path string, reg *project.Registry) bool {
	if reg == nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, projectPath := range reg.AllProjects() {
		if abs == projectPath || strings.HasPrefix(abs, projectPath+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("websocket accept: %v", err)
		return
	}

	clientID := fmt.Sprintf("client-%d", time.Now().UnixNano())
	client := &Client{
		ID:   clientID,
		conn: conn,
		send: make(chan []byte, 64),
	}

	s.mu.Lock()
	s.clients[clientID] = client
	s.mu.Unlock()

	log.Printf("Client connected: %s", clientID)

	ctx := r.Context()

	// Send snapshot of current sessions to newly connected client.
	// Recover from "send on closed channel" if the client disconnects before
	// the (potentially slow) snapshotSessions call finishes.
	go func() {
		defer func() { recover() }() //nolint:errcheck
		snapshots := s.snapshotSessions()
		msg := OutboundMessage{Type: "snapshot", Payload: snapshots}
		data, err := json.Marshal(msg)
		if err == nil {
			select {
			case client.send <- data:
			default:
			}
		}
	}()

	// Writer goroutine
	go func() {
		defer conn.CloseNow()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-client.send:
				if !ok {
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
					return
				}
			}
		}
	}()

	// Heartbeat goroutine: ping every 30s, close if no response within 5s
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pingCtx)
				cancel()
				if err != nil {
					// client dead — trigger cleanup
					conn.CloseNow()
					return
				}
			}
		}
	}()

	// Reader goroutine (handles inbound messages)
	defer func() {
		s.mu.Lock()
		delete(s.clients, clientID)
		s.mu.Unlock()

		// Clean up all PTY sessions owned by this client
		if s.ptyMgr != nil {
			s.ptyMgr.CloseByClient(clientID)
		}

		close(client.send)
		conn.CloseNow()
		log.Printf("Client disconnected: %s", clientID)
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var msg InboundMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("invalid message from %s: %v", clientID, err)
			continue
		}

		s.handleInbound(ctx, client, msg)
	}
}

func (s *Server) handleInbound(ctx context.Context, client *Client, msg InboundMessage) {
	switch msg.Type {
	case "ping":
		resp := OutboundMessage{Type: "pong", Payload: nil}
		data, _ := json.Marshal(resp)
		select {
		case client.send <- data:
		default:
		}

	case "approve":
		if msg.Session != "" {
			_ = tmux.SendKeys(msg.Session, "y")
		}

	case "deny":
		if msg.Session != "" {
			_ = tmux.SendKeys(msg.Session, "n")
		}

	case "send_keys":
		if msg.Session != "" && msg.Keys != "" {
			_ = tmux.SendKeys(msg.Session, msg.Keys)
		}

	case "register_push":
		if msg.Token != "" && s.push != nil {
			_ = s.push.Register(msg.Token, client.ID)
			log.Printf("Device registered for push: %s", client.ID)
		}

	// ── PTY messages ──────────────────────────────────────────────────────────

	case "close_pty":
		if s.ptyMgr != nil && msg.Session != "" {
			s.ptyMgr.Close(msg.Session)
		}

	case "open_pty":
		if s.ptyMgr == nil || msg.Session == "" {
			break
		}
		// Close any stale PTY for this session before reopening (handles reconnect/reopen)
		s.ptyMgr.Close(msg.Session)
		cols, rows := msg.Cols, msg.Rows
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		if err := s.ptyMgr.Open(msg.Session, client.ID, cols, rows); err != nil {
			log.Printf("open_pty %q: %v", msg.Session, err)
		}

	case "pty_input":
		if s.ptyMgr == nil || msg.Session == "" || msg.Data == "" {
			break
		}
		decoded, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			log.Printf("pty_input decode: %v", err)
			break
		}
		if err := s.ptyMgr.Write(msg.Session, decoded); err != nil {
			log.Printf("pty_input write %q: %v", msg.Session, err)
		}

	case "pty_resize":
		if s.ptyMgr == nil || msg.Session == "" {
			break
		}
		cols, rows := msg.Cols, msg.Rows
		if cols <= 0 || rows <= 0 {
			break
		}
		if err := s.ptyMgr.Resize(msg.Session, cols, rows); err != nil {
			log.Printf("pty_resize %q: %v", msg.Session, err)
		}

	// ── File messages ─────────────────────────────────────────────────────────

	case "list_dir":
		if msg.Path == "" {
			break
		}
		if !isPathAllowed(msg.Path, s.registry) {
			data, _ := json.Marshal(OutboundMessage{
				Type:    "error",
				Payload: map[string]string{"message": "path not allowed"},
			})
			select {
			case client.send <- data:
			default:
			}
			break
		}
		tree, err := fs.BuildTree(msg.Path, 3)
		if err != nil {
			log.Printf("list_dir %q: %v", msg.Path, err)
			break
		}
		resp := OutboundMessage{Type: "file_tree", Payload: tree}
		data, _ := json.Marshal(resp)
		select {
		case client.send <- data:
		default:
		}

	case "file_read":
		if msg.Path == "" {
			break
		}
		if !isPathAllowed(msg.Path, s.registry) {
			data, _ := json.Marshal(OutboundMessage{
				Type:    "error",
				Payload: map[string]string{"message": "path not allowed"},
			})
			select {
			case client.send <- data:
			default:
			}
			break
		}
		// Guard: reject files larger than 1 MB
		info, err := os.Stat(msg.Path)
		if err != nil || info.Size() > 1<<20 {
			data, _ := json.Marshal(OutboundMessage{
				Type:    "error",
				Payload: map[string]string{"message": "file too large or not found"},
			})
			select {
			case client.send <- data:
			default:
			}
			break
		}
		content, err := os.ReadFile(msg.Path)
		if err != nil {
			log.Printf("file_read %q: %v", msg.Path, err)
			break
		}
		resp := OutboundMessage{
			Type: "file_content",
			Payload: map[string]interface{}{
				"path":    msg.Path,
				"content": base64.StdEncoding.EncodeToString(content),
				"size":    len(content),
			},
		}
		data, _ := json.Marshal(resp)
		select {
		case client.send <- data:
		default:
		}

	case "new_session":
		s.handleNewSession(client, msg)

	case "file_write":
		if msg.Path == "" || msg.Content == "" {
			break
		}
		if !isPathAllowed(msg.Path, s.registry) {
			data, _ := json.Marshal(OutboundMessage{
				Type:    "error",
				Payload: map[string]string{"message": "path not allowed"},
			})
			select {
			case client.send <- data:
			default:
			}
			break
		}
		decoded, err := base64.StdEncoding.DecodeString(msg.Content)
		if err != nil {
			log.Printf("file_write decode %q: %v", msg.Path, err)
			break
		}
		writeErr := os.WriteFile(msg.Path, decoded, 0o644)
		ok := writeErr == nil
		if writeErr != nil {
			log.Printf("file_write %q: %v", msg.Path, writeErr)
		}
		resp := OutboundMessage{
			Type: "file_write_ack",
			Payload: map[string]interface{}{
				"path": msg.Path,
				"ok":   ok,
			},
		}
		data, _ := json.Marshal(resp)
		select {
		case client.send <- data:
		default:
		}
	}
}

func (s *Server) handleNewSession(client *Client, msg InboundMessage) {
	// Resolve working directory: use provided path, first registered project, or home dir.
	cwd := msg.ProjectPath
	if cwd == "" && s.registry != nil {
		if projects := s.registry.AllProjects(); len(projects) > 0 {
			cwd = projects[0]
		}
	}
	if cwd == "" {
		home, _ := os.UserHomeDir()
		cwd = home
	}

	// Generate a unique session name.
	prefix := "term"
	if msg.Mode == "chat" {
		prefix = "chat"
	}
	name := fmt.Sprintf("%s-%s", prefix, time.Now().Format("0102-150405"))

	if err := tmux.NewSession(name, cwd); err != nil {
		log.Printf("new_session create %q: %v", name, err)
		data, _ := json.Marshal(OutboundMessage{
			Type:    "error",
			Payload: map[string]string{"message": "failed to create session: " + err.Error()},
		})
		select {
		case client.send <- data:
		default:
		}
		return
	}

	// For chat mode, launch the AI assistant after a brief shell init delay.
	if msg.Mode == "chat" {
		time.Sleep(300 * time.Millisecond)
		_ = tmux.SendKeys(name, "claude")
	}

	// Broadcast a fresh snapshot so all clients see the new session appear.
	go func() {
		time.Sleep(600 * time.Millisecond)
		s.BroadcastSnapshot()
	}()

	// Ack with the new session name.
	data, _ := json.Marshal(OutboundMessage{
		Type:    "session_created",
		Payload: map[string]string{"name": name, "mode": msg.Mode},
	})
	select {
	case client.send <- data:
	default:
	}
}

func (s *Server) broadcastLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.events:
			if !ok {
				return
			}

			msg := OutboundMessage{
				Type:    "event",
				Payload: ev,
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}

			s.mu.RLock()
			clientCount := len(s.clients)
			for _, client := range s.clients {
				select {
				case client.send <- data:
				default:
					// Client too slow, skip
				}
			}
			s.mu.RUnlock()

			// Push notifications when no iOS clients are connected.
			if s.push != nil && clientCount == 0 {
				switch ev.Type {
				case watcher.EventApproval:
					s.push.Send(push.Payload{
						Aps: push.ApsPayload{
							Alert: push.AlertPayload{
								Title: "Approval needed",
								Body:  fmt.Sprintf("[%s] Claude Code needs your input", ev.Session),
							},
							Sound:    "default",
							Category: "APPROVAL",
						},
						Session: ev.Session,
						Event:   string(ev.Type),
					})
				case watcher.EventCrash:
					s.push.Send(push.CrashPayload(ev.Session, ev.Content))
				case watcher.EventDone:
					s.push.Send(push.DonePayload(ev.Session))
				}
			}
		}
	}
}

// BroadcastSnapshot sends a fresh snapshot to all connected clients.
func (s *Server) BroadcastSnapshot() {
	snapshots := s.snapshotSessions()
	msg := OutboundMessage{Type: "snapshot", Payload: snapshots}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		select {
		case c.send <- data:
		default:
		}
	}
}

// SendPTYOutput sends pty_output only to the client that owns the PTY session.
func (s *Server) SendPTYOutput(out ptymanager.PTYOutput) {
	msg := OutboundMessage{
		Type: "pty_output",
		Payload: map[string]interface{}{
			"session": out.Session,
			"data":    base64.StdEncoding.EncodeToString(out.Data),
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.mu.RLock()
	c, ok := s.clients[out.ClientID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

// snapshotSessions builds a snapshot of all currently-registered sessions.
func (s *Server) snapshotSessions() []SessionSnapshot {
	var snapshots []SessionSnapshot
	now := time.Now()

	sessions, err := tmux.ListSessions()
	if err != nil {
		return snapshots
	}

	for _, sess := range sessions {
		projectPath := sess.Cwd
		if s.registry != nil {
			if p, ok := s.registry.GetProject(sess.Name); ok {
				projectPath = p
			}
		}

		output, _ := tmux.CapturePaneOutput(sess.Name, 20)
		// Trim to last 20 lines just in case
		lines := strings.Split(output, "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}

		snapshots = append(snapshots, SessionSnapshot{
			Name:        sess.Name,
			ProjectPath: projectPath,
			LastOutput:  strings.Join(lines, "\n"),
			Timestamp:   now,
		})
	}

	return snapshots
}

// BroadcastFileEvent marshals a FileEvent and broadcasts it to all connected clients.
func (s *Server) BroadcastFileEvent(ev fsevent.FileEvent) {
	msg := OutboundMessage{
		Type:    "file_event",
		Payload: ev,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("BroadcastFileEvent marshal: %v", err)
		return
	}

	s.mu.RLock()
	for _, client := range s.clients {
		select {
		case client.send <- data:
		default:
			// Client too slow, skip
		}
	}
	s.mu.RUnlock()
}

// ClientCount returns the number of connected clients.
func (s *Server) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}
