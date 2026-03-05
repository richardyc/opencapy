package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
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
	Type    string `json:"type"`              // "ping", "approve", "deny", "send_keys"
	Session string `json:"session"`
	Keys    string `json:"keys,omitempty"`
}

// Client represents a connected iOS device.
type Client struct {
	ID   string
	conn *websocket.Conn
	send chan []byte
}

// Server is the WebSocket server that bridges watcher events to iOS.
type Server struct {
	port    int
	clients map[string]*Client
	events  <-chan watcher.Event
	mu      sync.RWMutex
}

// New creates a new WebSocket server.
func New(port int, events <-chan watcher.Event) *Server {
	return &Server{
		port:    port,
		clients: make(map[string]*Client),
		events:  events,
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
			for _, client := range s.clients {
				select {
				case client.send <- data:
				default:
					// Client too slow, skip
				}
			}
			s.mu.RUnlock()
		}
	}
}

// ClientCount returns the number of connected clients.
func (s *Server) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}
