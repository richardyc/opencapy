package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"bufio"
	"net"
	"net/http"
	"net/url"
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
	// register_session / reregister_session (shim → daemon)
	Branch   string `json:"branch,omitempty"`    // git branch at launch
	ExitCode int    `json:"exit_code,omitempty"` // process exit code, sent in session_end
	Buf        string `json:"buf,omitempty"`          // base64 PTY ring buffer snapshot for reregister
	InsideTmux bool   `json:"inside_tmux,omitempty"` // true when shim is running inside a tmux session
}

// SessionSnapshot holds a point-in-time snapshot of a session's state.
type SessionSnapshot struct {
	Name            string          `json:"name"`
	ProjectPath     string          `json:"project_path"`
	LastOutput      string          `json:"last_output"` // last 20 lines of pane
	Created         time.Time       `json:"created"`
	LastActive      time.Time       `json:"last_active"`
	RecentEvents    []watcher.Event `json:"recent_events,omitempty"`
	SessionType     string          `json:"session_type"`                // "tmux" or "direct"
	LastUserMessage string          `json:"last_user_message,omitempty"` // last user input from JSONL transcript
	Branch          string          `json:"branch,omitempty"`            // git branch at session launch
	ModelName       string          `json:"model_name,omitempty"`        // e.g. "claude-opus-4-6"
	ContextTokens   int             `json:"context_tokens,omitempty"`    // cumulative input tokens from last assistant msg
	MaxContext      int             `json:"max_context,omitempty"`       // model context window size
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
	recentEvents      map[string][]watcher.Event // last 50 non-output events per session
	sessionLastActive map[string]time.Time       // last meaningful activity per session (from hooks)
	recentMu          sync.RWMutex
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
	// Pending approval channels: session → channel awaiting iOS approve/deny decision.
	// The PermissionRequest hook handler blocks on this channel until iOS responds.
	pendingApprovals   map[string]chan bool
	pendingApprovalsMu sync.Mutex
}

type liveActivityEntry struct {
	token       string
	machineName string
	lastPush    time.Time // throttle output-driven Live Activity updates
}

// directSessionState tracks a session spawned by the claude shim (no tmux involved).
type directSessionState struct {
	shimClientID    string    // WS client ID of the shim that owns this session
	cwd             string
	branch          string    // git branch at launch
	claudeSessionID string    // Claude Code's own session_id from the SessionStart hook
	jsonlPath       string    // path to Claude Code's JSONL transcript for this session
	createdAt       time.Time
	buf             []byte    // ring buffer of raw PTY output (last ~32 KB)
	subscribers     []string  // iOS client IDs subscribed via open_pty
	parentTmux      string    // if set, this is a child of a tmux session (hidden from session list)
	// Cached JSONL metadata — only re-parsed when file size changes.
	cachedModel     string
	cachedTokens    int
	cachedJSONLSize int64
}

// ChatTurn is one user→assistant exchange, parsed from the Claude Code JSONL transcript.
type ChatTurn struct {
	UserText   string   `json:"user_text"`
	ClaudeText string   `json:"claude_text"`
	ToolCount  int      `json:"tool_count"`
	ToolNames  []string `json:"tool_names"`   // e.g. ["Read server.go", "Edit auth.py"]
	Timestamp  string   `json:"timestamp"`    // ISO 8601
	StopReason string   `json:"stop_reason"`  // "end_turn", "tool_use", "max_tokens", etc.
	Model      string   `json:"model"`        // e.g. "claude-opus-4-6"
	CostUSD    float64  `json:"cost_usd"`     // from "result" record (last turn only)
	DurationMs int      `json:"duration_ms"`  // from "result" record (last turn only)
	NumTurns   int      `json:"num_turns"`    // from "result" record (last turn only)
}

// chatHistoryMeta holds session-level metadata parsed from the JSONL transcript.
type chatHistoryMeta struct {
	SessionDone bool    // true when a "result" record was found
	IsError     bool    // true when the result indicates an error
	CostUSD     float64 // total cost from the result record
	DurationMs  int     // total duration from the result record
	NumTurns    int     // number of agentic turns from the result record
	ResultText  string  // final result text (for "result" type records)
}

const directBufMax = 256 * 1024 // 256 KB ring buffer per direct session

// daemonProtocolVersion is sent in the hello message on every new connection.
// Increment when making breaking changes to the WebSocket protocol so the iOS
// app can detect version skew and prompt the user to update.
const daemonProtocolVersion = 1

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
		sessionLastActive:  make(map[string]time.Time),
		liveActivityTokens: make(map[string]liveActivityEntry),
		clientDeviceNames:  make(map[string]string),
		directSessions:     make(map[string]*directSessionState),
		pendingApprovals: make(map[string]chan bool),
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
	mux.HandleFunc("/approve", s.handleHTTPApprove)
	mux.HandleFunc("/unlink", s.handleUnlink)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	// Broadcast events to all clients
	go s.broadcastLoop(ctx)

	// Watch JSONL transcripts for completion — fallback when Stop hook doesn't fire
	// (e.g. user presses Escape/Ctrl+C to interrupt Claude).
	go s.jsonlWatchLoop(ctx)

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

// handleUnlink broadcasts machine_unlinked to all iOS clients, then wipes the relay
// token and push device registry so a fresh QR scan is required to re-pair.
func (s *Server) handleUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Notify all connected iOS clients before invalidating credentials.
	data, _ := json.Marshal(OutboundMessage{Type: "machine_unlinked"})
	s.mu.RLock()
	for _, c := range s.clients {
		select {
		case c.send <- data:
		default:
		}
	}
	s.mu.RUnlock()

	// Delete relay token (regenerated on next daemon start) and push registry.
	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".opencapy", "relay_token.json"))
	os.Remove(filepath.Join(home, ".opencapy", "devices.json"))

	log.Println("[unlink] relay token and device registry cleared")
	w.WriteHeader(http.StatusOK)
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

	// Send hello immediately so the client knows the daemon protocol version.
	// This is the first message on every connection; iOS can reject mismatches.
	if helloMsg, err := json.Marshal(OutboundMessage{
		Type:    "hello",
		Payload: map[string]int{"version": daemonProtocolVersion},
	}); err == nil {
		select {
		case client.send <- helloMsg:
		default:
		}
	}

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
		// If PermissionRequest hook is still waiting, resolve it directly — this
		// is more reliable than sending keystrokes since newer Claude Code may not
		// show a terminal prompt when a hook handles the permission request.
		s.pendingApprovalsMu.Lock()
		ch, hasPending := s.pendingApprovals[msg.Session]
		s.pendingApprovalsMu.Unlock()
		if hasPending {
			select {
			case ch <- true:
			default:
			}
		} else {
			// Fallback: no pending hook channel — the hook timed out and Claude Code
			// is showing an interactive terminal prompt. Send Escape (dismiss any
			// autocomplete dropdown) then Enter to select the default (Allow).
			if s.IsDirectSession(msg.Session) {
				s.forwardToShim(msg.Session, "pty_input", map[string]string{
					"session": msg.Session,
					"data":    base64.StdEncoding.EncodeToString([]byte("\x1b")),
				})
				go func() {
					time.Sleep(100 * time.Millisecond)
					s.forwardToShim(msg.Session, "pty_input", map[string]string{
						"session": msg.Session,
						"data":    base64.StdEncoding.EncodeToString([]byte("\r")),
					})
				}()
			} else {
				_ = tmux.SendRawKeys(msg.Session, []string{"Escape"})
				go func() {
					time.Sleep(100 * time.Millisecond)
					_ = tmux.SendRawKeys(msg.Session, []string{"Enter"})
				}()
			}
		}

	case "deny":
		if msg.Session == "" {
			break
		}
		s.pendingApprovalsMu.Lock()
		ch, hasPending := s.pendingApprovals[msg.Session]
		s.pendingApprovalsMu.Unlock()
		if hasPending {
			select {
			case ch <- false:
			default:
			}
		} else {
			if s.IsDirectSession(msg.Session) {
				deny := "\x1b[B\r"
				s.forwardToShim(msg.Session, "pty_input", map[string]string{
					"session": msg.Session,
					"data":    base64.StdEncoding.EncodeToString([]byte(deny)),
				})
			} else {
				_ = tmux.SendRawKeys(msg.Session, []string{"Down", "Enter"})
			}
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

	case "request_chat_history":
		if msg.Session != "" {
			go s.sendChatHistory(msg.Session)
		}

	case "kill_session":
		if msg.Session != "" {
			// Direct sessions are owned by the Mac terminal — skip.
			if s.IsDirectSession(msg.Session) {
				log.Printf("kill_session %q: ignored (direct session)", msg.Session)
				break
			}
			log.Printf("kill_session: killing %q", msg.Session)
			if s.ptyMgr != nil {
				s.ptyMgr.Close(msg.Session)
			}
			if err := tmux.KillSession(msg.Session); err != nil {
				log.Printf("kill_session %q: tmux error: %v", msg.Session, err)
			} else {
				log.Printf("kill_session %q: done", msg.Session)
			}
			if s.sessionWatch != nil {
				s.sessionWatch.RemoveSession(msg.Session)
			}
			if s.registry != nil {
				_ = s.registry.Unregister(msg.Session)
				_ = s.registry.Save()
			}
			go s.BroadcastSnapshot()
		}

	case "register_push":
		if msg.Token != "" && s.push != nil {
			_ = s.push.Register(msg.Token, client.ID)
			log.Printf("Device registered for push: %s", client.ID)
		}

	case "unregister_device":
		if msg.Token != "" && s.push != nil {
			s.push.Unregister(msg.Token)
			log.Printf("Device unregistered: %s", client.ID)
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
		// Shim reconnected after a daemon restart — restore session so hook events
		// and PTY streaming work again.
		if msg.Session == "" || msg.ProjectPath == "" {
			break
		}
		var restoredBuf []byte
		if msg.Buf != "" {
			restoredBuf, _ = base64.StdEncoding.DecodeString(msg.Buf)
		}
		// Recover JSONL path from the persisted registry.
		var jsonlPath, claudeID string
		if s.registry != nil {
			if id, ok := s.registry.GetClaudeSessionID(msg.Session); ok {
				claudeID = id
				jsonlPath = claudeJSONLPath(msg.ProjectPath, id)
			}
		}
		// If the shim is running inside a tmux session, attach claude metadata
		// to the parent instead of creating a separate direct session.
		var tmuxName string
		if msg.InsideTmux {
			tmuxName = s.findTmuxSessionByCwd(msg.ProjectPath)
		}
		if tmuxName != "" {
			s.directSessionsMu.Lock()
			s.directSessions[msg.Session] = &directSessionState{
				shimClientID: client.ID,
				cwd:          msg.ProjectPath,
				buf:          restoredBuf,
				parentTmux:   tmuxName,
			}
			s.directSessionsMu.Unlock()
			// Persist claude session ID under the tmux name so sendChatHistory works.
			if s.registry != nil && claudeID != "" {
				_ = s.registry.SetClaudeSessionID(tmuxName, claudeID)
			}
			log.Printf("[hook] session %q attached to tmux %q", msg.Session, tmuxName)
			go s.BroadcastSnapshot()
			go s.sendChatHistory(tmuxName)
			break
		}
		s.directSessionsMu.Lock()
		if ds, ok := s.directSessions[msg.Session]; ok {
			ds.shimClientID = client.ID
			ds.cwd = msg.ProjectPath
			ds.buf = restoredBuf
			if msg.Branch != "" {
				ds.branch = msg.Branch
			}
			if ds.jsonlPath == "" && jsonlPath != "" {
				ds.claudeSessionID = claudeID
				ds.jsonlPath = jsonlPath
			}
		} else {
			created := time.Now()
			if s.registry != nil {
				if t, ok := s.registry.GetCreatedAt(msg.Session); ok {
					created = t
				}
			}
			s.directSessions[msg.Session] = &directSessionState{
				shimClientID:    client.ID,
				cwd:             msg.ProjectPath,
				branch:          msg.Branch,
				claudeSessionID: claudeID,
				jsonlPath:       jsonlPath,
				buf:             restoredBuf,
				createdAt:       created,
			}
		}
		s.directSessionsMu.Unlock()
		log.Printf("[hook] session %q re-registered (buf=%d bytes)", msg.Session, len(restoredBuf))
		go s.BroadcastSnapshot()
		go s.sendChatHistory(msg.Session)

	case "register_session":
		// Shim owns the PTY — daemon just assigns a name and tracks the session.
		if msg.ProjectPath == "" {
			break
		}
		sessionName := directSessionName(msg.ProjectPath, msg.Branch)
		// If the shim is inside tmux, attach as child instead of creating a separate session.
		var tmuxName string
		if msg.InsideTmux {
			tmuxName = s.findTmuxSessionByCwd(msg.ProjectPath)
		}
		if tmuxName != "" {
			s.directSessionsMu.Lock()
			s.directSessions[sessionName] = &directSessionState{
				shimClientID: client.ID,
				cwd:          msg.ProjectPath,
				branch:       msg.Branch,
				parentTmux:   tmuxName,
				createdAt:    time.Now(),
			}
			s.directSessionsMu.Unlock()
			log.Printf("[hook] session %q attached to tmux %q", sessionName, tmuxName)
			// Still send ack so the shim knows its assigned name.
			ack, _ := json.Marshal(OutboundMessage{
				Type:    "session_assigned",
				Payload: map[string]string{"name": sessionName},
			})
			select {
			case client.send <- ack:
			default:
			}
			go s.BroadcastSnapshot()
			break
		}
		s.directSessionsMu.Lock()
		s.directSessions[sessionName] = &directSessionState{
			shimClientID: client.ID,
			cwd:          msg.ProjectPath,
			branch:       msg.Branch,
			createdAt:    time.Now(),
		}
		s.directSessionsMu.Unlock()
		if s.registry != nil {
			_ = s.registry.Register(sessionName, msg.ProjectPath)
			s.registry.SetCreatedAt(sessionName, time.Now())
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
			// Passively track CWD from OSC-7 sequences emitted by the shell.
			if cwd := parseOSC7CWD(decoded); cwd != "" {
				ds.cwd = cwd
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
			// Send parsed chat history from Claude Code's JSONL transcript.
			go s.sendChatHistory(msg.Session)
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
				s.sessionLastActive[ev.Session] = ev.Timestamp
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
			for _, client := range s.clients {
				select {
				case client.send <- data:
				default:
					// Client too slow, skip
				}
			}
			s.mu.RUnlock()

			// Push notifications — always sent via APNs so they arrive on the lock
			// screen regardless of WS connection state. The iOS app suppresses the
			// banner in-app when the user is actively watching the session.
			if s.push != nil {
				switch ev.Type {
				case watcher.EventApproval:
					s.push.Send(push.ApprovalPayload(ev.Session))
				case watcher.EventCrash:
					s.push.Send(push.CrashPayload(ev.Session, ev.Content))
				case watcher.EventDone:
					s.push.Send(push.DonePayload(ev.Session))
				}
			}

			// Live Activity push — always sent via APNs push token so the lock
			// screen widget updates even when the app is backgrounded.
			if s.push != nil {
				s.liveActivityTokensMu.RLock()
				entry, hasToken := s.liveActivityTokens[ev.Session]
				s.liveActivityTokensMu.RUnlock()

				if hasToken {
					var state *push.LiveActivityContentState
					switch ev.Type {
					case watcher.EventRunning:
						// Include latest pane output so Live Activity shows streaming content.
						content := ev.Content
						s.directSessionsMu.RLock()
						if ds, ok := s.directSessions[ev.Session]; ok && ds.buf != nil {
							if last := lastNLines(string(ds.buf), 3); last != "" {
								content = last
							}
						}
						s.directSessionsMu.RUnlock()
						state = &push.LiveActivityContentState{
							SessionName: ev.Session,
							MachineName: entry.machineName,
							Status:      "running",
							LastOutput:  content,
						}
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
						s.liveActivityTokensMu.Lock()
						entry.lastPush = time.Now()
						s.liveActivityTokens[ev.Session] = entry
						s.liveActivityTokensMu.Unlock()
						go s.push.SendLiveActivity(entry.token, *state)
					} else if ev.Type == watcher.EventOutput {
						// Throttle: during extended thinking no hooks fire, so send a
						// low-frequency output update if >15s has passed since last push.
						s.liveActivityTokensMu.Lock()
						due := time.Since(entry.lastPush) > 15*time.Second
						if due {
							entry.lastPush = time.Now()
							s.liveActivityTokens[ev.Session] = entry
						}
						s.liveActivityTokensMu.Unlock()
						if due {
							go s.push.SendLiveActivity(entry.token, push.LiveActivityContentState{
								SessionName: ev.Session,
								MachineName: entry.machineName,
								Status:      "running",
								LastOutput:  ev.Content,
							})
						}
					}
				}
			}
		}
	}
}

// jsonlWatchLoop periodically checks each session's JSONL transcript for changes.
// When the file stops growing and the latest turn has a Claude response, emits
// EventDone as a fallback for when the Stop hook doesn't fire (Ctrl+C interrupt).
// Only fires when the JSONL has been stable (no growth) for one full cycle,
// preventing false Done events while Claude is still actively working.
func (s *Server) jsonlWatchLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	lastSize := make(map[string]int64)
	// prevSize stores the size from TWO cycles ago — if lastSize == prevSize,
	// the file has been stable for one full cycle.
	prevSize := make(map[string]int64)
	emitted := make(map[string]int64) // file size at which we last emitted Done

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.directSessionsMu.RLock()
			sessions := make(map[string]string)
			for name, ds := range s.directSessions {
				if ds.jsonlPath != "" {
					sessions[name] = ds.jsonlPath
				}
			}
			s.directSessionsMu.RUnlock()

			for name, path := range sessions {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				size := info.Size()
				prev := lastSize[name]
				twoCyclesAgo := prevSize[name]
				prevSize[name] = prev
				lastSize[name] = size

				// Only act when the file has been stable for one full cycle
				// (size unchanged between last two checks) and we haven't
				// already emitted for this size.
				if size == 0 || size != prev || prev != twoCyclesAgo || size == emitted[name] {
					continue
				}

				// File is stable — check if the session is actually done.
				// Use the "result" record (definitive) or fall back to
				// stop_reason == "end_turn" on the last assistant message.
				turns, meta := parseChatHistory(path)
				if len(turns) > 0 {
					last := turns[len(turns)-1]
					isDone := meta.SessionDone || (last.ClaudeText != "" && last.StopReason == "end_turn")
					if isDone {
						emitted[name] = size
						s.sessionWatch.Emit(watcher.Event{
							Type:      watcher.EventDone,
							Session:   name,
							Content:   last.ClaudeText,
							Timestamp: time.Now(),
						})
						go s.sendChatHistory(name)
					}
				}
			}

			for name := range lastSize {
				if _, ok := sessions[name]; !ok {
					delete(lastSize, name)
					delete(prevSize, name)
					delete(emitted, name)
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
		lastActive := s.sessionLastActive[sess.Name]
		s.recentMu.RUnlock()

		// Use tracked last-active from meaningful events (hooks); fall back to
		// tmux's session_activity for cold-boot ordering before any hooks have fired.
		if lastActive.IsZero() {
			lastActive = sess.LastActive
		}
		snap := SessionSnapshot{
			Name:         sess.Name,
			ProjectPath:  projectPath,
			LastOutput:   strings.Join(lines, "\n"),
			Created:      sess.Created,
			LastActive:   lastActive,
			RecentEvents: recent,
			SessionType:  "tmux",
		}
		// Attach claude metadata if claude is running inside this tmux session.
		if s.registry != nil {
			if cid, ok := s.registry.GetClaudeSessionID(sess.Name); ok {
				jp := claudeJSONLPath(projectPath, cid)
				snap.LastUserMessage = lastUserMessageFromJSONL(jp)
				model, tokens := sessionMetaFromJSONL(jp)
				snap.ModelName = model
				snap.ContextTokens = tokens
				if model != "" {
					snap.MaxContext = maxContextForModel(model)
				}
			}
		}
		snapshots = append(snapshots, snap)
	}

	// Append direct (non-tmux) sessions that are NOT children of a tmux session.
	s.directSessionsMu.RLock()
	for name, ds := range s.directSessions {
		if ds.parentTmux != "" {
			continue // hidden — metadata is on the parent tmux session
		}
		s.recentMu.RLock()
		recent := append([]watcher.Event{}, s.recentEvents[name]...)
		directLastActive := s.sessionLastActive[name]
		s.recentMu.RUnlock()
		if directLastActive.IsZero() {
			// Use JSONL file mod time as a proxy for last meaningful activity.
			if ds.jsonlPath != "" {
				if info, err := os.Stat(ds.jsonlPath); err == nil {
					directLastActive = info.ModTime()
				}
			}
			if directLastActive.IsZero() {
				directLastActive = ds.createdAt
			}
		}
		snap := SessionSnapshot{
			Name:         name,
			ProjectPath:  ds.cwd,
			LastOutput:   string(ds.buf),
			Created:      ds.createdAt,
			LastActive:   directLastActive,
			RecentEvents: recent,
			SessionType:  "direct",
			Branch:       ds.branch,
		}
		if ds.jsonlPath != "" {
			snap.LastUserMessage = lastUserMessageFromJSONL(ds.jsonlPath)
			// Only re-parse JSONL metadata when the file has grown.
			var fileSize int64
			if info, err := os.Stat(ds.jsonlPath); err == nil {
				fileSize = info.Size()
			}
			if fileSize != ds.cachedJSONLSize {
				model, tokens := sessionMetaFromJSONL(ds.jsonlPath)
				ds.cachedModel = model
				ds.cachedTokens = tokens
				ds.cachedJSONLSize = fileSize
			}
			snap.ModelName = ds.cachedModel
			snap.ContextTokens = ds.cachedTokens
			if ds.cachedModel != "" {
				snap.MaxContext = maxContextForModel(ds.cachedModel)
			}
		}
		snapshots = append(snapshots, snap)
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



// handleClaudeHook receives structured events from Claude Code's hook system
// and emits them to iOS clients. This replaces the fragile PTY string-matching
// approach for approval, done, and crash detection on direct sessions.
//
// Hooks are configured by opencapy install in ~/.claude/settings.json.
// Always returns 200 so claude is never blocked if the daemon is slow.
func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	// Do NOT write the status header here — PermissionRequest needs to block and
	// write a JSON body before committing the response.
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return
	}
	hookName, _ := payload["hook_event_name"].(string)
	cwd, _ := payload["cwd"].(string)
	claudeSessionID, _ := payload["session_id"].(string)

	// Session name comes from the OPENCAPY_SESSION env var, passed as ?session= query param.
	// If missing (claude run directly without the shim, e.g. inside a tmux session whose
	// shell hasn't sourced the opencapy init yet), fall back to matching by CWD.
	sessionName := r.URL.Query().Get("session")
	if sessionName == "" && cwd != "" {
		sessionName = s.findTmuxSessionByCwd(cwd)
	}
	if sessionName == "" {
		log.Printf("[hook] %s: no session for cwd=%q", hookName, cwd)
		w.WriteHeader(http.StatusOK)
		return
	}

	// SessionStart: bind Claude Code's session_id and JSONL path.
	if hookName == "SessionStart" {
		var parentTmux string
		s.directSessionsMu.Lock()
		if ds, ok := s.directSessions[sessionName]; ok {
			ds.cwd = cwd
			parentTmux = ds.parentTmux
			if claudeSessionID != "" {
				ds.claudeSessionID = claudeSessionID
				ds.jsonlPath = claudeJSONLPath(cwd, claudeSessionID)
			}
		}
		s.directSessionsMu.Unlock()
		// Persist claude session ID — under parent tmux name if applicable.
		persistName := sessionName
		if parentTmux != "" {
			persistName = parentTmux
		}
		if s.registry != nil && claudeSessionID != "" {
			_ = s.registry.SetClaudeSessionID(persistName, claudeSessionID)
		}
		log.Printf("[hook] SessionStart: %q → %q sessionID=%q", sessionName, persistName, claudeSessionID)
		if parentTmux != "" {
			go s.sendChatHistory(parentTmux)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Remap to parent tmux session so events land on the right session in iOS.
	s.directSessionsMu.RLock()
	if ds, ok := s.directSessions[sessionName]; ok && ds.parentTmux != "" {
		sessionName = ds.parentTmux
	}
	s.directSessionsMu.RUnlock()

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
		// Block until iOS approves/denies or 5-minute timeout.
		// Return a JSON decision body so Claude Code handles it directly
		// rather than falling back to an interactive terminal prompt.
		ch := make(chan bool, 1)
		s.pendingApprovalsMu.Lock()
		s.pendingApprovals[sessionName] = ch
		s.pendingApprovalsMu.Unlock()
		defer func() {
			s.pendingApprovalsMu.Lock()
			delete(s.pendingApprovals, sessionName)
			s.pendingApprovalsMu.Unlock()
		}()
		w.Header().Set("Content-Type", "application/json")
		select {
		case approved := <-ch:
			if approved {
				w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`))
			} else {
				w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Denied from iOS"}}}`))
			}
		case <-time.After(5 * time.Minute):
			// Timeout — return empty body, Claude falls back to its default.
			w.WriteHeader(http.StatusOK)
		}
	case "Stop":
		if active, _ := payload["stop_hook_active"].(bool); active {
			w.WriteHeader(http.StatusOK)
			return // already in a stop hook loop, skip
		}
		// stop_reason=="tool_use" means Claude responded with tool calls and is
		// about to execute them — not actually done. Only emit EventDone when the
		// turn truly ends (end_turn, max_tokens, etc.) so the title doesn't flash
		// "idle" between tool calls during extended-thinking / agentic runs.
		stopReason, _ := payload["stop_reason"].(string)
		if stopReason != "tool_use" {
			lastMsg, _ := payload["last_assistant_message"].(string)
			s.sessionWatch.Emit(watcher.Event{
				Type:      watcher.EventDone,
				Session:   sessionName,
				Content:   lastMsg,
				Timestamp: time.Now(),
			})
		}
		// Re-read transcript now that the turn is complete and push to iOS.
		go s.sendChatHistory(sessionName)
		w.WriteHeader(http.StatusOK)
	case "UserPromptSubmit":
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventRunning,
			Session:   sessionName,
			Content:   "thinking…",
			Timestamp: time.Now(),
		})
		w.WriteHeader(http.StatusOK)
	case "PreToolUse":
		toolName, _ := payload["tool_name"].(string)
		detail := toolSummary(toolName, payload["tool_input"])
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventRunning,
			Session:   sessionName,
			Content:   detail,
			Timestamp: time.Now(),
		})
		w.WriteHeader(http.StatusOK)
	case "PostToolUse":
		// Keep session status as "running" while Claude processes the tool result,
		// but send empty content — the tool name was already emitted by PreToolUse.
		// Empty content tells iOS to count this as a keep-alive, not a new tool step.
		s.sessionWatch.Emit(watcher.Event{
			Type:      watcher.EventRunning,
			Session:   sessionName,
			Content:   "",
			Timestamp: time.Now(),
		})
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
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

// handleHTTPApprove provides a simple HTTP endpoint for the iOS widget extension
// to approve permissions directly, bypassing the WebSocket + Darwin notification IPC.
// GET /approve?session=<name>&action=allow|deny
func (s *Server) handleHTTPApprove(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	action := r.URL.Query().Get("action")
	if session == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}
	if action == "" {
		action = "allow"
	}

	approved := action == "allow"
	s.pendingApprovalsMu.Lock()
	ch, hasPending := s.pendingApprovals[session]
	s.pendingApprovalsMu.Unlock()

	if hasPending {
		select {
		case ch <- approved:
		default:
		}
		w.Write([]byte(`{"ok":true,"method":"hook"}`))
	} else {
		// Fallback: send keystrokes to terminal.
		if approved {
			if s.IsDirectSession(session) {
				s.forwardToShim(session, "pty_input", map[string]string{
					"session": session,
					"data":    base64.StdEncoding.EncodeToString([]byte("\x1b")),
				})
				go func() {
					time.Sleep(100 * time.Millisecond)
					s.forwardToShim(session, "pty_input", map[string]string{
						"session": session,
						"data":    base64.StdEncoding.EncodeToString([]byte("\r")),
					})
				}()
			} else {
				_ = tmux.SendRawKeys(session, []string{"Escape"})
				go func() {
					time.Sleep(100 * time.Millisecond)
					_ = tmux.SendRawKeys(session, []string{"Enter"})
				}()
			}
		}
		w.Write([]byte(`{"ok":true,"method":"fallback"}`))
	}
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

// claudeJSONLPath returns the path to Claude Code's JSONL transcript.
// Claude encodes the project path by replacing every '/' with '-'.
// discoverJSONLPath finds the most recently modified JSONL transcript for a
// project by scanning ~/.claude/projects/<encoded-cwd>/. Used as a fallback
// when the registry doesn't have the Claude session ID.
func discoverJSONLPath(cwd string) string {
	home, _ := os.UserHomeDir()
	encoded := strings.ReplaceAll(cwd, "/", "-")
	dir := filepath.Join(home, ".claude", "projects", encoded)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var bestPath string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestPath = filepath.Join(dir, e.Name())
		}
	}
	return bestPath
}

func claudeJSONLPath(cwd, sessionID string) string {
	home, _ := os.UserHomeDir()
	encoded := strings.ReplaceAll(cwd, "/", "-")
	return filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
}


// lastUserMessageFromJSONL reads the JSONL transcript and returns the last
// user message text, truncated to 80 chars. Scans the full file but only
// keeps the most recent "user" record — lightweight enough for snapshot builds.
func lastUserMessageFromJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	type contentItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type jsonlMsg struct {
		Content json.RawMessage `json:"content"`
	}
	type rec struct {
		Type    string   `json:"type"`
		Message jsonlMsg `json:"message"`
	}

	extractText := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
		var items []contentItem
		if json.Unmarshal(raw, &items) == nil {
			var parts []string
			for _, item := range items {
				if item.Type == "text" {
					if t := strings.TrimSpace(item.Text); t != "" {
						parts = append(parts, t)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
		return ""
	}

	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var r rec
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		if r.Type == "user" {
			if t := extractText(r.Message.Content); t != "" {
				last = t
			}
		}
	}
	// Truncate to first line, max 80 chars — used as a session title/preview.
	if idx := strings.IndexByte(last, '\n'); idx >= 0 {
		last = last[:idx]
	}
	if len(last) > 80 {
		last = last[:77] + "…"
	}
	return last
}

// sessionMetaFromJSONL reads the JSONL transcript and extracts the model name
// and cumulative input token count from the most recent assistant message.
func sessionMetaFromJSONL(path string) (modelName string, inputTokens int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()

	type usage struct {
		InputTokens            int `json:"input_tokens"`
		CacheCreationTokens    int `json:"cache_creation_input_tokens"`
		CacheReadTokens        int `json:"cache_read_input_tokens"`
	}
	type msg struct {
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	}
	type rec struct {
		Type    string `json:"type"`
		Message msg    `json:"message"`
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var r rec
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		if r.Type == "assistant" && r.Message.Model != "" {
			modelName = r.Message.Model
			u := r.Message.Usage
			inputTokens = u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
		}
	}
	return modelName, inputTokens
}

// findTmuxSessionByCwd returns the name of a tmux session whose cwd matches,
// or "" if none found. Used to attach claude metadata to the parent tmux session.
func (s *Server) findTmuxSessionByCwd(cwd string) string {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return ""
	}
	for _, sess := range sessions {
		if sess.Cwd == cwd {
			return sess.Name
		}
		// Also check registry in case tmux cwd drifted.
		if s.registry != nil {
			if p, ok := s.registry.GetProject(sess.Name); ok && p == cwd {
				return sess.Name
			}
		}
	}
	return ""
}

// toolSummary builds a short description from a PreToolUse hook payload.
// It extracts the first available detail key (file_path, command, pattern, query, url)
// from tool_input, basenames file paths, and truncates to 50 chars.
func toolSummary(toolName string, toolInput interface{}) string {
	if toolName == "" {
		return ""
	}
	m, ok := toolInput.(map[string]interface{})
	if !ok {
		return toolName
	}
	// Try common detail keys in priority order.
	for _, key := range []string{"file_path", "command", "pattern", "query", "url"} {
		v, ok := m[key].(string)
		if !ok || v == "" {
			continue
		}
		if key == "file_path" {
			if i := strings.LastIndex(v, "/"); i >= 0 {
				v = v[i+1:]
			}
		}
		if len(v) > 50 {
			v = v[:47] + "..."
		}
		return toolName + " " + v
	}
	return toolName
}

// maxContextForModel returns the context window size for a given Claude model ID.
func maxContextForModel(model string) int {
	switch {
	case strings.Contains(model, "opus-4-6"),
		strings.Contains(model, "sonnet-4-6"):
		return 1_000_000
	case strings.Contains(model, "haiku"):
		return 200_000
	default:
		return 200_000
	}
}

// parseChatHistory reads Claude Code's JSONL transcript and returns completed turns
// plus session-level metadata (cost, duration, whether the session is done).
func parseChatHistory(path string) ([]ChatTurn, chatHistoryMeta) {
	f, err := os.Open(path)
	if err != nil {
		return nil, chatHistoryMeta{}
	}
	defer f.Close()

	type contentItem struct {
		Type  string                 `json:"type"`
		Text  string                 `json:"text"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	}
	type jsonlMsg struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
		Model      string          `json:"model"`
	}
	type jsonlRecord struct {
		Type       string   `json:"type"`
		Subtype    string   `json:"subtype"`    // for "result" records: "success", "error_*"
		Message    jsonlMsg `json:"message"`
		Timestamp  string   `json:"timestamp"`
		// Fields from "result" records:
		IsError    bool     `json:"is_error"`
		Result     string   `json:"result"`       // final text for "result" records
		CostUSD    float64  `json:"total_cost_usd"`
		DurationMs int      `json:"duration_ms"`
		NumTurns   int      `json:"num_turns"`
	}

	extractText := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
		var items []contentItem
		if json.Unmarshal(raw, &items) == nil {
			var parts []string
			for _, item := range items {
				if item.Type == "text" {
					if t := strings.TrimSpace(item.Text); t != "" {
						parts = append(parts, t)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
		return ""
	}

	countTools := func(raw json.RawMessage) (int, []string) {
		var items []contentItem
		if json.Unmarshal(raw, &items) != nil {
			return 0, nil
		}
		var n int
		var names []string
		for _, item := range items {
			if item.Type == "tool_use" {
				n++
				names = append(names, toolSummary(item.Name, item.Input))
			}
		}
		return n, names
	}

	var turns []ChatTurn
	var cur *ChatTurn
	var meta chatHistoryMeta

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB per line for large messages
	for scanner.Scan() {
		var rec jsonlRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		switch rec.Type {
		case "user":
			userText := extractText(rec.Message.Content)
			if userText == "" {
				// Tool-result messages have no text — they're part of the
				// current turn's tool flow, not a new user message.
				continue
			}
			if cur != nil {
				turns = append(turns, *cur)
			}
			cur = &ChatTurn{
				UserText:  userText,
				Timestamp: rec.Timestamp,
			}
		case "assistant":
			if cur == nil {
				// Assistant message before any real user message — create
				// a synthetic turn so tool counts and text aren't lost.
				cur = &ChatTurn{Timestamp: rec.Timestamp}
			}
			if t := extractText(rec.Message.Content); t != "" {
				if cur.ClaudeText != "" {
					cur.ClaudeText += "\n"
				}
				cur.ClaudeText += t
			}
			tc, tn := countTools(rec.Message.Content)
		cur.ToolCount += tc
		cur.ToolNames = append(cur.ToolNames, tn...)
			if rec.Message.StopReason != "" {
				cur.StopReason = rec.Message.StopReason
			}
			if rec.Message.Model != "" {
				cur.Model = rec.Message.Model
			}
		case "result":
			// The definitive "session done" record — emitted once when
			// Claude Code finishes (success or error).
			meta.SessionDone = true
			meta.IsError = rec.IsError
			meta.CostUSD = rec.CostUSD
			meta.DurationMs = rec.DurationMs
			meta.NumTurns = rec.NumTurns
			meta.ResultText = rec.Result
			// Stamp cost/duration onto the last turn for display.
			if cur != nil {
				cur.CostUSD = rec.CostUSD
				cur.DurationMs = rec.DurationMs
				cur.NumTurns = rec.NumTurns
			}
		}
	}
	// Always include the last turn — even if Claude hasn't responded yet —
	// so the chat view shows the user's in-progress message.
	if cur != nil {
		turns = append(turns, *cur)
	}
	return turns, meta
}

// sendChatHistory reads the JSONL transcript for a session and broadcasts
// the parsed turns to all connected clients.
func (s *Server) sendChatHistory(session string) {
	var jsonlPath string

	// Check direct sessions first.
	s.directSessionsMu.RLock()
	if ds, ok := s.directSessions[session]; ok {
		jsonlPath = ds.jsonlPath
	}
	s.directSessionsMu.RUnlock()

	// Fall back to registry (covers tmux sessions with claude running inside).
	if jsonlPath == "" && s.registry != nil {
		if cid, ok := s.registry.GetClaudeSessionID(session); ok {
			if proj, ok := s.registry.GetProject(session); ok {
				jsonlPath = claudeJSONLPath(proj, cid)
			}
		}
	}

	// Last resort: scan the project directory for the most recently modified JSONL.
	// Handles sessions from before the current daemon start where the registry has no entry.
	if jsonlPath == "" {
		if proj, ok := s.registry.GetProject(session); ok {
			jsonlPath = discoverJSONLPath(proj)
		}
	}
	if jsonlPath == "" {
		return
	}

	turns, meta := parseChatHistory(jsonlPath)
	if turns == nil {
		turns = []ChatTurn{}
	}
	msg, err := json.Marshal(OutboundMessage{
		Type: "chat_history",
		Payload: map[string]interface{}{
			"session":      session,
			"session_done": meta.SessionDone,
			"is_error":     meta.IsError,
			"cost_usd":     meta.CostUSD,
			"duration_ms":  meta.DurationMs,
			"num_turns":    meta.NumTurns,
			"turns":        turns,
		},
	})
	if err != nil {
		return
	}

	s.mu.RLock()
	for _, c := range s.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
	s.mu.RUnlock()
}

// parseOSC7CWD extracts the last OSC-7 working-directory path from a PTY data
// chunk. Returns "" if no OSC-7 sequence is present.
//
// Format: ESC ] 7 ; file://hostname/path BEL   (or ST = ESC \)
// Emitted by modern shells (zsh, bash with vte.sh, fish) on every prompt,
// giving us a live CWD without polling.
// lastNLines returns the last n non-empty lines from s.
func lastNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	var result []string
	for i := len(lines) - 1; i >= 0 && len(result) < n; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			result = append([]string{lines[i]}, result...)
		}
	}
	return strings.Join(result, "\n")
}

func parseOSC7CWD(data []byte) string {
	const osc7Prefix = "\x1b]7;"
	s := string(data)
	idx := strings.LastIndex(s, osc7Prefix)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(osc7Prefix):]
	end := strings.IndexAny(rest, "\x07\x1b")
	if end < 0 {
		return ""
	}
	uri := rest[:end]
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}
	// Strip scheme and host: file://hostname/path → /path
	hostPath := uri[len("file://"):]
	slash := strings.IndexByte(hostPath, '/')
	if slash < 0 {
		return ""
	}
	path, err := url.PathUnescape(hostPath[slash:])
	if err != nil {
		return hostPath[slash:]
	}
	return path
}
