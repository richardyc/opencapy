package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/richardyc/opencapy/internal/fsevent"
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
	Type    string `json:"type"`              // "ping", "approve", "deny", "send_keys", "register_push"
	Session string `json:"session"`
	Keys    string `json:"keys,omitempty"`
	Token   string `json:"token,omitempty"`
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
	mu       sync.RWMutex
}

// New creates a new WebSocket server.
func New(port int, events <-chan watcher.Event, reg *project.Registry, pushReg *push.Registry) *Server {
	return &Server{
		port:     port,
		clients:  make(map[string]*Client),
		events:   events,
		registry: reg,
		push:     pushReg,
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

	// Send snapshot of current sessions to newly connected client
	go func() {
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
