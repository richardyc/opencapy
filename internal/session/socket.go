package session

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
)


// Frame types for the binary attach protocol.
const (
	FrameOutput byte = 1 // daemon → client
	FrameInput  byte = 2 // client → daemon
	FrameResize byte = 3 // client → daemon (4 bytes: rows u16 BE + cols u16 BE)
	FrameDetach byte = 4 // client → daemon (0 bytes)

	maxFramePayload = 65535 // max payload per frame (uint16)
)

// JSON protocol request/response.
type request struct {
	Op   string   `json:"op"`
	Name string   `json:"name,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`
	Cmd  string   `json:"cmd,omitempty"`
	Args []string `json:"args,omitempty"`
	Rows int      `json:"rows,omitempty"`
	Cols int      `json:"cols,omitempty"`
	Data string   `json:"data,omitempty"`
}

type response struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Name     string        `json:"name,omitempty"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// ListenSocket starts a Unix socket listener for CLI communication.
// Blocks until ctx is cancelled.
func (m *Manager) ListenSocket(ctx context.Context, sockPath string) error {
	// Remove stale socket file.
	os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sockPath, err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("[socket] accept: %v", err)
				continue
			}
		}
		go m.handleConn(conn)
	}
}

func (m *Manager) handleConn(conn net.Conn) {
	defer conn.Close()

	dec := json.NewDecoder(conn)
	var req request
	if err := dec.Decode(&req); err != nil {
		writeJSON(conn, response{Error: "invalid request"})
		return
	}

	switch req.Op {
	case "list":
		writeJSON(conn, response{OK: true, Sessions: m.List()})

	case "new":
		if req.Name == "" {
			writeJSON(conn, response{Error: "name required"})
			return
		}
		cols, rows := uint16(req.Cols), uint16(req.Rows)
		if cols == 0 {
			cols = 80
		}
		if rows == 0 {
			rows = 24
		}
		command := req.Cmd
		if command == "" {
			command = os.Getenv("SHELL")
			if command == "" {
				command = "/bin/sh"
			}
		}
		_, err := m.Create(req.Name, req.Cwd, command, req.Args, cols, rows)
		if err != nil {
			writeJSON(conn, response{Error: err.Error()})
			return
		}
		writeJSON(conn, response{OK: true, Name: req.Name})

	case "kill":
		if err := m.Kill(req.Name); err != nil {
			writeJSON(conn, response{Error: err.Error()})
			return
		}
		writeJSON(conn, response{OK: true})

	case "input":
		s := m.Get(req.Name)
		if s == nil {
			writeJSON(conn, response{Error: "session not found"})
			return
		}
		if err := s.Write([]byte(req.Data)); err != nil {
			writeJSON(conn, response{Error: err.Error()})
			return
		}
		writeJSON(conn, response{OK: true})

	case "attach":
		s := m.Get(req.Name)
		if s == nil {
			writeJSON(conn, response{Error: "session not found"})
			return
		}
		// Send ack, then switch to binary framing.
		writeJSON(conn, response{OK: true})
		m.handleAttach(conn, s)

	default:
		writeJSON(conn, response{Error: "unknown op: " + req.Op})
	}
}

var attachSeq uint64

func (m *Manager) handleAttach(conn net.Conn, s *Session) {
	attachSeq++
	clientID := fmt.Sprintf("sock-%d-%d", os.Getpid(), attachSeq)

	output, replay := s.Subscribe(clientID)
	defer s.Unsubscribe(clientID)

	log.Printf("[attach] session=%q ring buffer=%d bytes", s.Name, len(replay))

	// Send ring buffer replay as output frame.
	// Sanitize before replaying: strip alternate-screen switches and full-screen
	// erases. Claude Code uses ESC[?1049h to enter its alternate-screen TUI.
	// Re-executing that sequence hides all earlier primary-screen content from
	// the Mac terminal's scrollback. We strip it so the full history is visible.
	if len(replay) > 0 {
		if err := writeFrame(conn, FrameOutput, sanitizeForReplay(replay)); err != nil {
			return
		}
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Goroutine: session output → client.
	// When the session dies (channel closed), close the connection so the
	// main readFrame loop below unblocks and exits.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case data, ok := <-output:
				if !ok {
					conn.Close() // session died — unblock readFrame below
					return
				}
				if err := writeFrame(conn, FrameOutput, data); err != nil {
					return
				}
			}
		}
	}()

	// Read client frames until detach or error.
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			break
		}
		switch typ {
		case FrameInput:
			_ = s.Write(payload)
		case FrameResize:
			if len(payload) == 4 {
				rows := binary.BigEndian.Uint16(payload[0:2])
				cols := binary.BigEndian.Uint16(payload[2:4])
				s.Resize(cols, rows)
			}
		case FrameDetach:
			close(done)
			wg.Wait()
			return
		}
	}

	close(done)
	wg.Wait()
}

// sanitizeForReplay strips terminal sequences from ring buffer content that
// would corrupt the Mac terminal's scrollback when replayed. Specifically:
//   ESC [ ? 47 h/l    — alternate screen (old)
//   ESC [ ? 1047 h/l  — alternate screen
//   ESC [ ? 1049 h/l  — alternate screen + save/restore cursor (Claude Code uses this)
//   ESC [ 2 J         — erase entire display
//   ESC [ 3 J         — erase scrollback
//
// These are kept in live output (Claude's TUI needs them to draw correctly),
// but replaying them hides earlier history from the terminal's scrollback.
func sanitizeForReplay(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if end := skipAltScreenOrErase(data, i); end > i {
			i = end
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

func skipAltScreenOrErase(data []byte, i int) int {
	if i >= len(data) || data[i] != 0x1b || i+1 >= len(data) || data[i+1] != '[' {
		return i
	}
	j := i + 2

	// ESC [ 2 J  or  ESC [ 3 J — erase display / scrollback
	if j+1 < len(data) && (data[j] == '2' || data[j] == '3') && data[j+1] == 'J' {
		return j + 2
	}

	// ESC [ ? <number> h/l — DEC private mode (alternate screen switches)
	if j < len(data) && data[j] == '?' {
		j++
		numStart := j
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j < len(data) && (data[j] == 'h' || data[j] == 'l') && j > numStart {
			switch string(data[numStart:j]) {
			case "47", "1047", "1049":
				return j + 1
			}
		}
	}
	return i
}

func writeJSON(conn net.Conn, resp response) {
	_ = json.NewEncoder(conn).Encode(resp)
}

func readFrame(r io.Reader) (typ byte, payload []byte, err error) {
	header := make([]byte, 3)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	typ = header[0]
	length := binary.BigEndian.Uint16(header[1:3])
	if length == 0 {
		return typ, nil, nil
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return typ, payload, nil
}

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	// For large payloads, split into multiple frames (max 65535 bytes each).
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > maxFramePayload {
			chunk = chunk[:maxFramePayload]
		}
		header := make([]byte, 3)
		header[0] = typ
		binary.BigEndian.PutUint16(header[1:3], uint16(len(chunk)))
		if _, err := w.Write(header); err != nil {
			return err
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		payload = payload[len(chunk):]
	}
	return nil
}
