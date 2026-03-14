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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/richardyc/opencapy/internal/fsevent"
	fs "github.com/richardyc/opencapy/internal/fs"
	"github.com/richardyc/opencapy/internal/mdns"
	"github.com/richardyc/opencapy/internal/platform"
	ptymanager "github.com/richardyc/opencapy/internal/pty"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/push"
	"github.com/richardyc/opencapy/internal/relay"
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
	Name    string `json:"name,omitempty"` // device name for register_device
	// PTY fields
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Data string `json:"data,omitempty"` // base64 for pty_input; raw path for file_write
	// File fields
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"` // base64 for file_write
	// Git fields
	Message string `json:"message,omitempty"`
	Staged  bool   `json:"staged,omitempty"`
	// Session creation fields
	Mode        string `json:"mode,omitempty"`         // "chat" or "terminal"
	ProjectPath string `json:"project_path,omitempty"` // working directory for new session
	// Pane capture
	Lines int `json:"lines,omitempty"` // lines of scrollback to capture
	// Live Activity
	Machine string `json:"machine,omitempty"` // machine name from iOS for live activity token
	// register_session (shim → daemon): shim owns the PTY, daemon just routes
	Branch   string `json:"branch,omitempty"`    // git branch at launch
	ExitCode int    `json:"exit_code,omitempty"` // process exit code, sent in session_end
}

// SessionSnapshot holds a point-in-time snapshot of a session's state.
type SessionSnapshot struct {
	Name         string          `json:"name"`
	ProjectPath  string          `json:"project_path"`
	LastOutput   string          `json:"last_output"` // last 20 lines of pane
	Created      time.Time       `json:"created"`
	LastActive   time.Time       `json:"last_active"`
	RecentEvents []watcher.Event `json:"recent_events,omitempty"`
	SessionType  string          `json:"session_type"` // "tmux" or "direct"
}

// Client represents a connected iOS device.
type Client struct {
	ID   string
	conn *websocket.Conn
	send chan []byte
}

// Server is the WebSocket server that bridges watcher events to iOS.
type Server struct {
	port         int
	relayToken   string // persistent token for relay pairing (loaded once at startup)
	clients      map[string]*Client
	events       <-chan watcher.Event
	sessionWatch *watcher.Watcher
	registry     *project.Registry
	push         *push.Registry
	ptyMgr       *ptymanager.Manager
	mu           sync.RWMutex
	recentEvents map[string][]watcher.Event // last 50 non-output events per session
	recentMu     sync.RWMutex
	// Live Activity push tokens: session name → per-activity APNs token from iOS
	liveActivityTokens   map[string]liveActivityEntry
	liveActivityTokensMu sync.RWMutex
	// Device names: clientID → human-readable device name (e.g. "Richard's iPhone")
	// Populated when iOS registers a Live Activity (which includes the machine name).
	clientDeviceNames   map[string]string
	clientDeviceNamesMu sync.RWMutex
	// Direct sessions: spawned by the claude shim, not tmux.
	directSessions   map[string]*directSessionState
	directSessionsMu sync.RWMutex
}

type liveActivityEntry struct {
	token       string
	machineName string
}

// directSessionState tracks a session spawned by the claude shim (no tmux involved).
type directSessionState struct {
	shimClientID string    // WS client ID of the shim that owns this session
	cwd          string
	createdAt    time.Time
	buf          []byte    // ring buffer of raw PTY output (last ~32 KB)
	subscribers  []string  // iOS client IDs subscribed via open_pty
}

const directBufMax = 32 * 1024 // 32 KB ring buffer per direct session

// New creates a new WebSocket server.
func New(port int, w *watcher.Watcher, reg *project.Registry, pushReg *push.Registry, ptyMgr *ptymanager.Manager) *Server {
	// Load (or generate on first run) the persistent relay pairing token.
	relayToken, err := relay.LoadOrCreate()
	if err != nil {
		log.Printf("[relay] failed to load token: %v — relay pairing unavailable", err)
	}
	return &Server{
		port:               port,
		relayToken:         relayToken,
		clients:            make(map[string]*Client),
		events:             w.Events(),
		sessionWatch:       w,
		registry:           reg,
		push:               pushReg,
		ptyMgr:             ptyMgr,
		recentEvents:       make(map[string][]watcher.Event),
		liveActivityTokens: make(map[string]liveActivityEntry),
		clientDeviceNames:  make(map[string]string),
		directSessions:     make(map[string]*directSessionState),
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
	mux.HandleFunc("/hooks/claude", s.handleClaudeHook)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	// Broadcast events to all clients
	go s.broadcastLoop(ctx)

	// Connect outbound to relay as "mac" so iOS clients can reach us.
	if s.relayToken != "" {
		go s.runRelayClient(ctx)
	}

	// Advertise on the local network via mDNS/Bonjour so iOS can connect
	// directly when on the same Wi-Fi — skipping the relay entirely.
	if s.relayToken != "" {
		hostname := platform.Hostname()
		if pub, err := mdns.Publish(hostname, s.relayToken, s.port); err != nil {
			log.Printf("[mDNS] failed to advertise: %v", err)
		} else {
			go func() {
				<-ctx.Done()
				pub.Stop()
			}()
		}
	}

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
// Relay is the default pairing method; Tailscale is the fallback.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := platform.Hostname()
	var qrContent string
	if s.relayToken != "" {
		// Relay pairing — no Tailscale or SSH required.
		qrContent = relay.PairURL(s.relayToken, hostname, relay.DefaultRelayURL)
	} else {
		// Fallback to Tailscale if relay token unavailable.
		tailscaleHost, _ := platform.TailscaleHostname()
		qrContent = fmt.Sprintf(
			"opencapy://pair?name=%s&host=%s&port=%d&type=tailscale",
			hostname, tailscaleHost, s.port,
		)
	}

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
	w.Header().Set("Content-Type", "application/json")

	if s.relayToken != "" {
		// Relay is the default pairing method.
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":      hostname,
			"type":      "relay",
			"relay_url": relay.DefaultRelayURL,
			"token":     s.relayToken,
			"pair_url":  relay.PairURL(s.relayToken, hostname, relay.DefaultRelayURL),
		})
	} else {
		// Fallback: Tailscale direct.
		tailscaleHost, _ := platform.TailscaleHostname()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name": hostname,
			"host": tailscaleHost,
			"port": s.port,
			"type": "tailscale",
		})
	}
}

// isPathAllowed checks whether path falls within one of the registered project paths,
// or within /tmp (always allowed for temporary file uploads).
// This prevents path traversal attacks on file_read, file_write, and list_dir.
func isPathAllowed(path string, reg *project.Registry) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// /tmp is always safe for temporary uploads (images, etc.)
	if abs == "/tmp" || strings.HasPrefix(abs, "/tmp/") {
		return true
	}
	if reg == nil {
		return false
	}
	for _, projectPath := range reg.AllProjects() {
		if abs == projectPath || strings.HasPrefix(abs, projectPath+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isListDirAllowed is like isPathAllowed but also permits ancestor directories of
// registered projects and the user's home directory subtree, so the iOS folder
// browser can navigate freely to discover new project directories. Listing
// directory names is low-risk compared to reading file contents, so this broader
// check is safe for list_dir only.
func isListDirAllowed(path string, reg *project.Registry) bool {
	if isPathAllowed(path, reg) {
		return true
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// Always allow browsing within the user's home directory so the folder
	// browser works even before any sessions/projects are registered.
	if home, err := os.UserHomeDir(); err == nil {
		sep := string(filepath.Separator)
		if abs == home || strings.HasPrefix(abs, home+sep) {
			return true
		}
	}
	if reg == nil {
		return false
	}
	sep := string(filepath.Separator)
	for _, projectPath := range reg.AllProjects() {
		// Allow any directory that is a proper ancestor of a registered project.
		if strings.HasPrefix(projectPath, abs+sep) {
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
	// Allow large inbound messages (e.g. base64-encoded images for paste_image).
	// Default is 32KB which is too small for image payloads.
	conn.SetReadLimit(20 * 1024 * 1024) // 20 MB

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
	go s.refreshTmuxStatus()

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

		s.clientDeviceNamesMu.Lock()
		delete(s.clientDeviceNames, clientID)
		s.clientDeviceNamesMu.Unlock()

		// Clean up all PTY sessions owned by this client
		if s.ptyMgr != nil {
			s.ptyMgr.CloseByClient(clientID)
		}

		// Deregister any direct sessions owned by this shim client.
		s.directSessionsMu.Lock()
		for name, ds := range s.directSessions {
			if ds.shimClientID == clientID {
				delete(s.directSessions, name)
				if s.registry != nil {
					_ = s.registry.Unregister(name)
					_ = s.registry.Save()
				}
			}
		}
		s.directSessionsMu.Unlock()

		// Remove this client from any direct session subscriber lists.
		s.directSessionsMu.Lock()
		for _, ds := range s.directSessions {
			filtered := ds.subscribers[:0]
			for _, id := range ds.subscribers {
				if id != clientID {
					filtered = append(filtered, id)
				}
			}
			ds.subscribers = filtered
		}
		s.directSessionsMu.Unlock()

		close(client.send)
		conn.CloseNow()
		log.Printf("Client disconnected: %s", clientID)
		go s.refreshTmuxStatus()
		go s.BroadcastSnapshot()
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

	case "refresh_sessions":
		// Client is requesting a fresh snapshot (e.g. returning to session list).
		go func() {
			snapshots := s.snapshotSessions()
			msg := OutboundMessage{Type: "snapshot", Payload: snapshots}
			data, err := json.Marshal(msg)
			if err != nil {
				return
			}
			select {
			case client.send <- data:
			default:
			}
		}()

	case "approve":
		if msg.Session == "" {
			break
		}
		if s.IsDirectSession(msg.Session) {
			// Send "1" + Enter: navigates to option 1 and confirms.
			s.forwardToShim(msg.Session, "pty_input", map[string]string{
				"session": msg.Session,
				"data":    base64.StdEncoding.EncodeToString([]byte("1\r")),
			})
		} else {
			_ = tmux.SendKeyNoEnter(msg.Session, "1")
			_ = tmux.SendRawKeys(msg.Session, []string{"Enter"})
		}

	case "deny":
		if msg.Session == "" {
			break
		}
		if s.IsDirectSession(msg.Session) {
			// Down×3 moves past all options to the last one, Enter confirms.
			// \x1b[B is the Down arrow escape sequence in raw terminal mode.
			deny := "\x1b[B\x1b[B\x1b[B\r"
			s.forwardToShim(msg.Session, "pty_input", map[string]string{
				"session": msg.Session,
				"data":    base64.StdEncoding.EncodeToString([]byte(deny)),
			})
		} else {
			_ = tmux.SendRawKeys(msg.Session, []string{"Down", "Down", "Down", "Enter"})
		}

	case "send_keys":
		if msg.Session == "" || msg.Keys == "" {
			break
		}
		// Default: send via PTY (direct sessions). Fall back to tmux only for tmux sessions.
		if s.IsDirectSession(msg.Session) {
			s.forwardToShim(msg.Session, "pty_input", map[string]string{
				"session": msg.Session,
				"data":    base64.StdEncoding.EncodeToString([]byte(msg.Keys + "\n")),
			})
		} else {
			_ = tmux.SendKeys(msg.Session, msg.Keys)
		}

	case "kill_session":
		if msg.Session != "" {
			log.Printf("kill_session: killing %q", msg.Session)
			// Close any open PTY for this session before killing it.
			if s.ptyMgr != nil {
				s.ptyMgr.Close(msg.Session)
			}
			if err := tmux.KillSession(msg.Session); err != nil {
				log.Printf("kill_session %q: tmux error: %v", msg.Session, err)
			} else {
				log.Printf("kill_session %q: done", msg.Session)
			}
			// Stop the watcher from polling a dead session.
			if s.sessionWatch != nil {
				s.sessionWatch.RemoveSession(msg.Session)
			}
			// Unregister from the project registry so it doesn't reappear.
			if s.registry != nil {
				_ = s.registry.Unregister(msg.Session)
				_ = s.registry.Save()
			}
			// Broadcast updated session list immediately.
			go s.BroadcastSnapshot()
		}

	case "register_push":
		if msg.Token != "" && s.push != nil {
			_ = s.push.Register(msg.Token, client.ID)
			log.Printf("Device registered for push: %s", client.ID)
		}

	case "register_device":
		// Sent immediately on connect so status bar shows device name right away.
		if msg.Name != "" {
			s.clientDeviceNamesMu.Lock()
			s.clientDeviceNames[client.ID] = msg.Name
			s.clientDeviceNamesMu.Unlock()
			log.Printf("Device name registered: %s (%s)", msg.Name, client.ID)
			go s.refreshTmuxStatus()
		}

	case "register_live_activity":
		if msg.Session != "" && msg.Token != "" {
			s.liveActivityTokensMu.Lock()
			s.liveActivityTokens[msg.Session] = liveActivityEntry{
				token:       msg.Token,
				machineName: msg.Machine,
			}
			s.liveActivityTokensMu.Unlock()
			log.Printf("[live activity] registered token for session %s (machine=%s)", msg.Session, msg.Machine)
			// Store the device name so we can show it in the tmux status bar.
			if msg.Machine != "" {
				s.clientDeviceNamesMu.Lock()
				s.clientDeviceNames[client.ID] = msg.Machine
				s.clientDeviceNamesMu.Unlock()
				go s.refreshTmuxStatus()
			}
		}

	// paste_image: decode base64 PNG, write to /tmp, set Mac clipboard via
	// osascript, then ack so iOS can send Ctrl+V into the terminal.
	case "paste_image":
		sendImageAck := func(errMsg string) {
			payload := map[string]string{"session": msg.Session}
			if errMsg != "" {
				payload["error"] = errMsg
			}
			resp := OutboundMessage{
				Type:    "image_pasted",
				Payload: payload,
			}
			data, _ := json.Marshal(resp)
			select {
			case client.send <- data:
			default:
			}
		}
		if msg.Data == "" {
			break
		}
		decoded, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			log.Printf("paste_image decode: %v", err)
			sendImageAck("Failed to decode image")
			break
		}
		// iOS sends PNG (lossless, no black-image artefacts from HEIC sources).
		ts := time.Now().UnixNano()
		pngPath := fmt.Sprintf("/tmp/opencapy_clip_%d.png", ts)
		if err := os.WriteFile(pngPath, decoded, 0o644); err != nil {
			log.Printf("paste_image write: %v", err)
			sendImageAck("Failed to write image")
			break
		}
		// Electron (Claude Code) reads public.tiff from NSPasteboard via
		// clipboard.readImage(). Setting only «class PNGf» leaves no TIFF
		// representation, so Electron gets a blank/black image.
		// Convert to TIFF first using sips (macOS built-in), then set
		// the clipboard as "TIFF picture" so the TIFF UTI is present.
		tiffPath := fmt.Sprintf("/tmp/opencapy_clip_%d.tiff", ts)
		if err := exec.Command("sips", "-s", "format", "tiff", pngPath, "--out", tiffPath).Run(); err != nil {
			log.Printf("paste_image sips: %v — falling back to PNG", err)
			tiffPath = pngPath
		}
		clipPath := tiffPath
		script := fmt.Sprintf(
			`set the clipboard to (read (POSIX file "%s") as TIFF picture)`,
			clipPath,
		)
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			log.Printf("paste_image osascript: %v", err)
			sendImageAck("Failed to set clipboard")
			break
		}
		// Brief pause so the NSPasteboard change fully propagates before
		// Claude Code's clipboard read is triggered.
		time.Sleep(300 * time.Millisecond)
		// Send Ctrl+V (0x16) to the session so Claude Code reads the clipboard.
		if msg.Session != "" {
			if s.IsDirectSession(msg.Session) {
				// Direct session: forward Ctrl+V through the shim's PTY.
				s.forwardToShim(msg.Session, "pty_input", map[string]string{
					"session": msg.Session,
					"data":    base64.StdEncoding.EncodeToString([]byte{0x16}),
				})
			} else {
				// tmux session: send via tmux send-keys.
				_ = tmux.SendKeyNoEnter(msg.Session, "\x16")
			}
		}
		sendImageAck("")

	// ── PTY messages ──────────────────────────────────────────────────────────

	case "reregister_session":
		// Shim reconnected after a daemon restart — restore session so hook events match.
		if msg.Session == "" || msg.ProjectPath == "" {
			break
		}
		s.directSessionsMu.Lock()
		s.directSessions[msg.Session] = &directSessionState{
			shimClientID: client.ID,
			cwd:          msg.ProjectPath,
			createdAt:    time.Now(),
		}
		s.directSessionsMu.Unlock()
		log.Printf("[hook] session %q re-registered after daemon restart", msg.Session)

	case "register_session":
		// Shim owns the PTY — daemon just assigns a name and tracks the session.
		if msg.ProjectPath == "" {
			break
		}
		sessionName := directSessionName(msg.ProjectPath, msg.Branch)
		s.directSessionsMu.Lock()
		s.directSessions[sessionName] = &directSessionState{
			shimClientID: client.ID,
			cwd:          msg.ProjectPath,
			createdAt:    time.Now(),
		}
		s.directSessionsMu.Unlock()
		if s.registry != nil {
			_ = s.registry.Register(sessionName, msg.ProjectPath)
			_ = s.registry.Save()
		}
		ack, _ := json.Marshal(OutboundMessage{
			Type:    "session_assigned",
			Payload: map[string]string{"name": sessionName},
		})
		select {
		case client.send <- ack:
		default:
		}
		go s.BroadcastSnapshot()

	case "pty_data":
		// Shim streams PTY output — buffer it and fan out to iOS subscribers.
		if msg.Session == "" || msg.Data == "" {
			break
		}
		decoded, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			break
		}
		var subscribers []string
		s.directSessionsMu.Lock()
		if ds, ok := s.directSessions[msg.Session]; ok {
			ds.buf = append(ds.buf, decoded...)
			if len(ds.buf) > directBufMax {
				ds.buf = ds.buf[len(ds.buf)-directBufMax:]
			}
			subscribers = append([]string(nil), ds.subscribers...)
		}
		s.directSessionsMu.Unlock()
		// Emit EventOutput for live pane display. Approval/done/crash detection
		// is handled by Claude Code hooks via the /hooks/claude endpoint.
		s.sessionWatch.FeedOutput(msg.Session, string(decoded))
		// Forward to iOS subscribers as pty_output.
		if len(subscribers) > 0 {
			outMsg, _ := json.Marshal(OutboundMessage{
				Type: "pty_output",
				Payload: map[string]interface{}{
					"session": msg.Session,
					"data":    msg.Data, // already base64
				},
			})
			s.mu.RLock()
			for _, id := range subscribers {
				if c, ok := s.clients[id]; ok {
					select {
					case c.send <- outMsg:
					default:
					}
				}
			}
			s.mu.RUnlock()
		}

	case "session_end":
		// Shim signals claude has exited.
		if msg.Session == "" {
			break
		}
		if msg.ExitCode != 0 {
			s.sessionWatch.Emit(watcher.Event{
				Type:      watcher.EventCrash,
				Session:   msg.Session,
				Content:   fmt.Sprintf("Process exited with code %d", msg.ExitCode),
				Timestamp: time.Now(),
			})
		}
		s.directSessionsMu.Lock()
		delete(s.directSessions, msg.Session)
		s.directSessionsMu.Unlock()
		if s.registry != nil {
			_ = s.registry.Unregister(msg.Session)
			_ = s.registry.Save()
		}
		go s.BroadcastSnapshot()

	case "close_pty":
		if msg.Session == "" {
			break
		}
		// For direct sessions iOS sends close_pty to unsubscribe, not to kill the process.
		if s.IsDirectSession(msg.Session) {
			s.directSessionsMu.Lock()
			if ds, ok := s.directSessions[msg.Session]; ok {
				filtered := ds.subscribers[:0]
				for _, id := range ds.subscribers {
					if id != client.ID {
						filtered = append(filtered, id)
					}
				}
				ds.subscribers = filtered
			}
			s.directSessionsMu.Unlock()
			break
		}
		if s.ptyMgr != nil {
			s.ptyMgr.Close(msg.Session)
		}

	case "open_pty":
		if msg.Session == "" {
			break
		}
		cols, rows := msg.Cols, msg.Rows
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		// Direct session: subscribe iOS client to the existing PTY stream + replay history.
		if s.IsDirectSession(msg.Session) {
			s.directSessionsMu.Lock()
			ds, ok := s.directSessions[msg.Session]
			var history []byte
			if ok {
				ds.subscribers = append(ds.subscribers, client.ID)
				history = make([]byte, len(ds.buf))
				copy(history, ds.buf)
			}
			s.directSessionsMu.Unlock()
			if !ok {
				break
			}
			// Send ring buffer as pane_content so SwiftTerm populates scrollback.
			paneMsg, _ := json.Marshal(OutboundMessage{
				Type:    "pane_content",
				Payload: map[string]string{"session": msg.Session, "content": string(history)},
			})
			select {
			case client.send <- paneMsg:
			default:
			}
			break
		}
		if s.ptyMgr == nil {
			break
		}
		// Tmux session: close any stale PTY and open a new grouped session.
		s.ptyMgr.Close(msg.Session)
		startDir := ""
		if s.registry != nil {
			if p, ok := s.registry.GetProject(msg.Session); ok {
				startDir = p
			}
		}
		if err := s.ptyMgr.Open(msg.Session, client.ID, cols, rows, startDir); err != nil {
			log.Printf("open_pty %q: %v", msg.Session, err)
		}

	case "pty_input":
		if msg.Session == "" || msg.Data == "" {
			break
		}
		// Direct session: forward to the shim that owns the PTY.
		if s.IsDirectSession(msg.Session) {
			s.forwardToShim(msg.Session, "pty_input", map[string]string{
				"session": msg.Session,
				"data":    msg.Data,
			})
			break
		}
		// tmux session: write directly to the grouped PTY.
		if s.ptyMgr == nil {
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
		if msg.Session == "" {
			break
		}
		cols, rows := msg.Cols, msg.Rows
		if cols <= 0 || rows <= 0 {
			break
		}
		// Direct session: forward resize to shim.
		if s.IsDirectSession(msg.Session) {
			s.forwardToShim(msg.Session, "pty_resize", map[string]int{
				"cols": cols,
				"rows": rows,
			})
			break
		}
		// tmux session: resize the grouped PTY.
		if s.ptyMgr == nil {
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
		if !isListDirAllowed(msg.Path, s.registry) {
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

	case "capture_pane":
		if msg.Session == "" {
			break
		}
		var output string
		if s.IsDirectSession(msg.Session) {
			s.directSessionsMu.RLock()
			if ds, ok := s.directSessions[msg.Session]; ok {
				output = string(ds.buf)
			}
			s.directSessionsMu.RUnlock()
		} else {
			lines := msg.Lines
			if lines <= 0 {
				lines = 300
			}
			// Plain text (no -e) so SwiftTerm accumulates scrollback lines instead
			// of cursor-positioning escape sequences that prevent scrollback growth.
			var err error
			output, err = tmux.CapturePaneOutput(msg.Session, lines, false)
			if err != nil {
				log.Printf("capture_pane %q: %v", msg.Session, err)
				break
			}
		}
		data, _ := json.Marshal(OutboundMessage{
			Type:    "pane_content",
			Payload: map[string]string{"session": msg.Session, "content": output},
		})
		select {
		case client.send <- data:
		default:
		}

	case "new_session":
		s.handleNewSession(client, msg)

	// ── Git messages ──────────────────────────────────────────────────────────

	case "git_status":
		projectPath, ok := s.registry.GetProject(msg.Session)
		if !ok || projectPath == "" {
			break
		}
		result := parseGitStatus(projectPath)
		result.Session = msg.Session
		data, _ := json.Marshal(OutboundMessage{Type: "git_status_result", Payload: result})
		select {
		case client.send <- data:
		default:
		}

	case "git_stage":
		if msg.Path == "" {
			break
		}
		projectPath, ok := s.registry.GetProject(msg.Session)
		if !ok || projectPath == "" {
			break
		}
		runGit(projectPath, "add", "--", msg.Path) //nolint:errcheck
		result := parseGitStatus(projectPath)
		result.Session = msg.Session
		data, _ := json.Marshal(OutboundMessage{Type: "git_status_result", Payload: result})
		select {
		case client.send <- data:
		default:
		}

	case "git_unstage":
		if msg.Path == "" {
			break
		}
		projectPath, ok := s.registry.GetProject(msg.Session)
		if !ok || projectPath == "" {
			break
		}
		runGit(projectPath, "restore", "--staged", "--", msg.Path) //nolint:errcheck
		result := parseGitStatus(projectPath)
		result.Session = msg.Session
		data, _ := json.Marshal(OutboundMessage{Type: "git_status_result", Payload: result})
		select {
		case client.send <- data:
		default:
		}

	case "git_commit":
		if msg.Message == "" {
			break
		}
		projectPath, ok := s.registry.GetProject(msg.Session)
		if !ok || projectPath == "" {
			break
		}
		runGit(projectPath, "commit", "-m", msg.Message) //nolint:errcheck
		result := parseGitStatus(projectPath)
		result.Session = msg.Session
		data, _ := json.Marshal(OutboundMessage{Type: "git_status_result", Payload: result})
		select {
		case client.send <- data:
		default:
		}

	case "git_diff":
		if msg.Path == "" {
			break
		}
		projectPath, ok := s.registry.GetProject(msg.Session)
		if !ok || projectPath == "" {
			break
		}
		result := gitDiff(projectPath, msg.Path, msg.Staged)
		data, _ := json.Marshal(OutboundMessage{Type: "git_diff_result", Payload: result})
		select {
		case client.send <- data:
		default:
		}

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
	// Expand leading ~ because tmux's -c flag does not perform tilde expansion.
	if cwd == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			cwd = h
		}
	} else if strings.HasPrefix(cwd, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			cwd = filepath.Join(h, cwd[2:])
		}
	}
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

	// Register so isPathAllowed passes for file browsing.
	if s.registry != nil {
		_ = s.registry.Register(name, cwd)
		_ = s.registry.Save()
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

			// Store non-output events for replay when a new client connects.
			if ev.Type != watcher.EventOutput {
				s.recentMu.Lock()
				s.recentEvents[ev.Session] = append(s.recentEvents[ev.Session], ev)
				if len(s.recentEvents[ev.Session]) > 50 {
					s.recentEvents[ev.Session] = s.recentEvents[ev.Session][len(s.recentEvents[ev.Session])-50:]
				}
				s.recentMu.Unlock()
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

			// Live Activity push — sent for key events regardless of WS client count
			// so the lock screen Live Activity updates even when the app is backgrounded.
			if s.push != nil {
				s.liveActivityTokensMu.RLock()
				entry, hasToken := s.liveActivityTokens[ev.Session]
				s.liveActivityTokensMu.RUnlock()

				if hasToken {
					var state *push.LiveActivityContentState
					switch ev.Type {
					case watcher.EventApproval:
						content := ev.Content
						state = &push.LiveActivityContentState{
							SessionName:     ev.Session,
							MachineName:     entry.machineName,
							Status:          "approval",
							LastOutput:      content,
							NeedsApproval:   true,
							ApprovalContent: &content,
						}
					case watcher.EventDone:
						state = &push.LiveActivityContentState{
							SessionName: ev.Session,
							MachineName: entry.machineName,
							Status:      "done",
							LastOutput:  ev.Content,
						}
					case watcher.EventCrash:
						state = &push.LiveActivityContentState{
							SessionName: ev.Session,
							MachineName: entry.machineName,
							Status:      "crashed",
							LastOutput:  ev.Content,
						}
					}
					if state != nil {
						go s.push.SendLiveActivity(entry.token, *state)
					}
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

// SendPTYOutput sends pty_output to the iOS client that owns this tmux PTY session.
// Direct sessions stream via pty_data messages instead — this is tmux-only.
func (s *Server) SendPTYOutput(out ptymanager.PTYOutput) {
	if out.Data == nil {
		return
	}
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

// forwardToShim sends a message to the shim that owns the given direct session.
func (s *Server) forwardToShim(session, msgType string, payload interface{}) {
	s.directSessionsMu.RLock()
	ds, ok := s.directSessions[session]
	shimID := ""
	if ok {
		shimID = ds.shimClientID
	}
	s.directSessionsMu.RUnlock()
	if shimID == "" {
		return
	}
	data, err := json.Marshal(OutboundMessage{Type: msgType, Payload: payload})
	if err != nil {
		return
	}
	s.mu.RLock()
	c, ok := s.clients[shimID]
	s.mu.RUnlock()
	if ok {
		select {
		case c.send <- data:
		default:
		}
	}
}

// refreshTmuxStatus updates the right side of the tmux status bar to show
// how many iOS devices are connected (and the device name when only one is).
// Device names are populated once the iOS app registers a Live Activity.
func (s *Server) refreshTmuxStatus() {
	s.mu.RLock()
	count := len(s.clients)
	s.mu.RUnlock()

	if count == 0 {
		// No phone connected — show pairing guide so first-time CLI users
		// know what to do next.
		host := platform.Hostname()
		tmux.SetGlobalStatusRight(fmt.Sprintf("  \U0001F4F1 %s:%d  ", host, s.port))
		tmux.SetGlobalStatusLeft("  #[fg=red]●#[default] No phone · run `opencapy qr` to pair  ")
		return
	}

	s.clientDeviceNamesMu.RLock()
	seen := map[string]bool{}
	var uniqueNames []string
	for _, name := range s.clientDeviceNames {
		if name != "" && !seen[name] {
			seen[name] = true
			uniqueNames = append(uniqueNames, name)
		}
	}
	s.clientDeviceNamesMu.RUnlock()

	var text string
	switch {
	case len(uniqueNames) == 1:
		text = fmt.Sprintf("  #[fg=green]●#[default] %s  ", uniqueNames[0])
	case len(uniqueNames) > 1:
		text = fmt.Sprintf("  #[fg=green]●#[default] %d iPhones  ", len(uniqueNames))
	case count == 1:
		text = "  #[fg=green]●#[default] 1 phone  "
	default:
		text = fmt.Sprintf("  #[fg=green]●#[default] %d phones  ", count)
	}

	tmux.SetGlobalStatusRight(text)
	// Clear the pairing guide once a phone is connected.
	tmux.SetGlobalStatusLeft("")
}

// snapshotSessions builds a snapshot of all currently-registered sessions.
func (s *Server) snapshotSessions() []SessionSnapshot {
	var snapshots []SessionSnapshot

	sessions, err := tmux.ListSessions()
	if err != nil {
		return snapshots
	}

	for _, sess := range sessions {
		// Skip internal PTY mirror sessions created by opencapy itself.
		if strings.HasPrefix(sess.Name, "ocpy_") {
			continue
		}
		projectPath := sess.Cwd
		if s.registry != nil {
			if p, ok := s.registry.GetProject(sess.Name); ok {
				projectPath = p
			}
		}

		output, _ := tmux.CapturePaneOutput(sess.Name, 20, true)
		// Trim to last 20 lines just in case
		lines := strings.Split(output, "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}

		s.recentMu.RLock()
		recent := append([]watcher.Event{}, s.recentEvents[sess.Name]...)
		s.recentMu.RUnlock()

		snapshots = append(snapshots, SessionSnapshot{
			Name:         sess.Name,
			ProjectPath:  projectPath,
			LastOutput:   strings.Join(lines, "\n"),
			Created:      sess.Created,
			LastActive:   sess.LastActive,
			RecentEvents: recent,
			SessionType:  "tmux",
		})
	}

	// Append direct (non-tmux) sessions spawned by the claude shim.
	s.directSessionsMu.RLock()
	for name, ds := range s.directSessions {
		s.recentMu.RLock()
		recent := append([]watcher.Event{}, s.recentEvents[name]...)
		s.recentMu.RUnlock()
		snapshots = append(snapshots, SessionSnapshot{
			Name:         name,
			ProjectPath:  ds.cwd,
			LastOutput:   string(ds.buf),
			Created:      ds.createdAt,
			LastActive:   ds.createdAt,
			RecentEvents: recent,
			SessionType:  "direct",
		})
	}
	s.directSessionsMu.RUnlock()

	return snapshots
}

// directSessionName builds a human-readable session key from the project path,
// optional git branch, and current time. Slashes in branch names are replaced
// with hyphens so the name is safe to use as a map key.
// Examples:
//
//	myrepo-feat-login-fix-1432
//	myrepo-1432   (no branch)
func directSessionName(projectPath, branch string) string {
	base := filepath.Base(projectPath)
	if base == "" || base == "." {
		base = "session"
	}
	t := time.Now().Format("1504") // HHmm
	if branch == "" {
		return base + "-" + t
	}
	// Replace path separators and spaces with hyphens.
	safeBranch := strings.NewReplacer("/", "-", " ", "-").Replace(branch)
	return base + "-" + safeBranch + "-" + t
}

// IsDirectSession returns true if the named session was spawned by the claude shim.
func (s *Server) IsDirectSession(name string) bool {
	s.directSessionsMu.RLock()
	_, ok := s.directSessions[name]
	s.directSessionsMu.RUnlock()
	return ok
}

// findDirectSessionByCwd returns the most recently created direct session with
// an exact cwd match. After SessionStart updates the stored cwd this is always
// sufficient and unambiguous.
func (s *Server) findDirectSessionByCwd(cwd string) string {
	s.directSessionsMu.RLock()
	defer s.directSessionsMu.RUnlock()
	var best string
	var bestTime time.Time
	for name, ds := range s.directSessions {
		if ds.cwd == cwd && ds.createdAt.After(bestTime) {
			best = name
			bestTime = ds.createdAt
		}
	}
	return best
}

// bindSessionByCwd is used only during SessionStart to establish the session→cwd
// mapping. Tries exact match first, then falls back to the single active session
// (unambiguous when there is only one). After this call all subsequent hook
// events will match exactly via findDirectSessionByCwd.
func (s *Server) bindSessionByCwd(cwd string) string {
	if name := s.findDirectSessionByCwd(cwd); name != "" {
		return name
	}
	s.directSessionsMu.RLock()
	defer s.directSessionsMu.RUnlock()
	if len(s.directSessions) == 1 {
		for name := range s.directSessions {
			return name
		}
	}
	return ""
}

// handleClaudeHook receives structured events from Claude Code's hook system
// and emits them to iOS clients. This replaces the fragile PTY string-matching
// approach for approval, done, and crash detection on direct sessions.
//
// Hooks are configured by opencapy install in ~/.claude/settings.json.
// Always returns 200 so claude is never blocked if the daemon is slow.
func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodPost {
		return
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return
	}
	hookName, _ := payload["hook_event_name"].(string)
	cwd, _ := payload["cwd"].(string)

	// SessionStart: update the session's stored cwd to claude's actual project
	// directory so all subsequent hooks match exactly via findDirectSessionByCwd.
	if hookName == "SessionStart" {
		if name := s.bindSessionByCwd(cwd); name != "" {
			s.directSessionsMu.Lock()
			if ds, ok := s.directSessions[name]; ok {
				ds.cwd = cwd
			}
			s.directSessionsMu.Unlock()
			log.Printf("[hook] SessionStart: bound %q → cwd=%q", name, cwd)
		}
		return
	}

	sessionName := s.findDirectSessionByCwd(cwd)
	if sessionName == "" {
		log.Printf("[hook] %s: no session for cwd=%q", hookName, cwd)
		return
	}

	switch hookName {
	case "PermissionRequest":
		toolName, _ := payload["tool_name"].(string)
		toolInputBytes, _ := json.Marshal(payload["tool_input"])
		content := fmt.Sprintf("Permission needed: %s\n%s", toolName, string(toolInputBytes))
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventApproval,
			Session:   sessionName,
			Content:   content,
			Timestamp: time.Now(),
		})
	case "Stop":
		if active, _ := payload["stop_hook_active"].(bool); active {
			return // already in a stop hook loop, skip
		}
		lastMsg, _ := payload["last_assistant_message"].(string)
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventDone,
			Session:   sessionName,
			Content:   lastMsg,
			Timestamp: time.Now(),
		})
	case "PreToolUse":
		// Claude finished thinking and is about to execute a tool.
		// Emit running immediately so the Live Activity shows active status
		// during the thinking phase before PostToolUse fires.
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventRunning,
			Session:   sessionName,
			Timestamp: time.Now(),
		})
	case "PostToolUse":
		// Tool completed — keep Live Activity in running state for next tool/thinking.
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventRunning,
			Session:   sessionName,
			Timestamp: time.Now(),
		})
	}
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

// runRelayClient connects outbound to the relay as "mac" and routes messages
// to/from the existing handleInbound machinery. Reconnects automatically.
func (s *Server) runRelayClient(ctx context.Context) {
	wsURL := relay.WSURL(s.relayToken, relay.DefaultRelayURL, "mac")
	backoff := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := s.dialRelay(ctx, wsURL); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[relay] disconnected (%v) — reconnecting in %s", err, backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

func (s *Server) dialRelay(ctx context.Context, wsURL string) error {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		// Allow large messages (images, PTY output)
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return err
	}
	conn.SetReadLimit(20 * 1024 * 1024)
	defer conn.CloseNow()

	clientID := fmt.Sprintf("relay-mac-%d", time.Now().UnixNano())
	client := &Client{
		ID:   clientID,
		conn: conn,
		send: make(chan []byte, 256),
	}

	s.mu.Lock()
	s.clients[clientID] = client
	s.mu.Unlock()
	log.Printf("[relay] connected as mac (token …%s)", s.relayToken[len(s.relayToken)-8:])
	go s.refreshTmuxStatus()

	defer func() {
		s.mu.Lock()
		delete(s.clients, clientID)
		s.mu.Unlock()
		close(client.send)
		go s.refreshTmuxStatus()
	}()

	// Writer goroutine: drain client.send → relay WebSocket
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for msg := range client.send {
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = conn.Write(writeCtx, websocket.MessageText, msg)
			cancel()
		}
	}()

	// Send a snapshot immediately so iOS gets sessions as soon as it connects.
	go func() {
		snapshots := s.snapshotSessions()
		msg := OutboundMessage{Type: "snapshot", Payload: snapshots}
		data, _ := json.Marshal(msg)
		select {
		case client.send <- data:
		default:
		}
	}()

	// Reader: relay forwards iOS messages here
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		var msg InboundMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Skip relay control messages
		if msg.Type == "relay_connected" || msg.Type == "peer_connected" || msg.Type == "peer_disconnected" {
			if msg.Type == "peer_connected" {
				// iOS just connected — push a fresh snapshot
				go func() {
					snapshots := s.snapshotSessions()
					out := OutboundMessage{Type: "snapshot", Payload: snapshots}
					outData, _ := json.Marshal(out)
					select {
					case client.send <- outData:
					default:
					}
				}()
			}
			continue
		}

		s.handleInbound(ctx, client, msg)
	}
}
