package session

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"

	"golang.org/x/term"
)

// SocketPath returns the default daemon socket path.
func SocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".opencapy", "daemon.sock")
}

func dial() (net.Conn, error) {
	return net.Dial("unix", SocketPath())
}

func rpc(req request) (response, error) {
	conn, err := dial()
	if err != nil {
		return response{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

// ListSessions returns all daemon sessions.
func ListSessions() ([]SessionInfo, error) {
	resp, err := rpc(request{Op: "list"})
	if err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

// CreateSession creates a new daemon session.
func CreateSession(name, cwd, command string, args []string, rows, cols int) (string, error) {
	resp, err := rpc(request{
		Op:   "new",
		Name: name,
		Cwd:  cwd,
		Cmd:  command,
		Args: args,
		Rows: rows,
		Cols: cols,
	})
	if err != nil {
		return "", err
	}
	return resp.Name, nil
}

// KillSession kills a daemon session.
func KillSession(name string) error {
	_, err := rpc(request{Op: "kill", Name: name})
	return err
}

// SendInput sends data to a session without attaching.
func SendInput(name, data string) error {
	_, err := rpc(request{Op: "input", Name: name, Data: data})
	return err
}

// SessionExists checks if a session exists.
func SessionExists(name string) bool {
	sessions, err := ListSessions()
	if err != nil {
		return false
	}
	for _, s := range sessions {
		if s.Name == name {
			return true
		}
	}
	return false
}

// ── Terminal filtering ────────────────────────────────────────────────────────
//
// We are NOT a full terminal emulator — we forward PTY bytes transparently.
// Terminal QUERY sequences from the inner session (cursor position, color
// queries) forwarded to the outer terminal would create a feedback loop: outer
// terminal responds on stdin → we forward response to daemon → shell echoes it
// → garbage text. We prevent this by filtering queries from PTY output and
// filtering terminal responses from stdin input.
//
// These same filters are also used server-side for iOS pty_input (server.go).

// FilterTerminalQueries removes terminal query sequences from PTY output.
// Programs emit these to ask the terminal about cursor position, colors, etc.
// Forwarding them to the outer terminal causes a stdin feedback loop.
//
// Filtered: ESC[6n (DECCPR), ESC[c/ESC[>c (DA1/DA2),
//           ESC]10/11/12;?BEL (color queries)
func FilterTerminalQueries(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if end := skipQueryAt(data, i); end > i {
			i = end
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

func skipQueryAt(data []byte, i int) int {
	if i >= len(data) || data[i] != 0x1b || i+1 >= len(data) {
		return i
	}
	switch data[i+1] {
	case '[':
		j := i + 2
		if j < len(data) && data[j] == '>' {
			j++ // DA2 prefix
		}
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j >= len(data) {
			return i
		}
		switch data[j] {
		case 'n': // DECCPR: ESC[6n
			return j + 1
		case 'c': // DA1/DA2
			return j + 1
		}
	case ']':
		// OSC color queries: ESC]10;?BEL, ESC]11;?BEL, ESC]12;?BEL
		j := i + 2
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j < len(data) && data[j] == ';' {
			j++
			if j < len(data) && data[j] == '?' {
				j++
				if j < len(data) && data[j] == 0x07 {
					return j + 1
				}
				if j+1 < len(data) && data[j] == 0x1b && data[j+1] == '\\' {
					return j + 2
				}
			}
		}
	}
	return i
}

// FilterTerminalResponses strips terminal response sequences from input.
// These are responses the outer terminal sends for queries it received —
// we don't want them reaching the inner shell as unexpected input.
//
// Filtered: ESC[row;colR (CPR), ESC]11;rgb:...BEL/ST (OSC color responses)
func FilterTerminalResponses(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if end := skipResponseAt(data, i); end > i {
			i = end
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

func skipResponseAt(data []byte, i int) int {
	if i >= len(data) || data[i] != 0x1b || i+1 >= len(data) {
		return i
	}
	switch data[i+1] {
	case '[': // CSI — filter ESC [ <digits> ; <digits> R (CPR)
		j := i + 2
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j < len(data) && data[j] == ';' {
			j++
			for j < len(data) && data[j] >= '0' && data[j] <= '9' {
				j++
			}
			if j < len(data) && data[j] == 'R' {
				return j + 1
			}
		}
	case ']': // OSC — filter color responses terminated by BEL or ST
		j := i + 2
		for j < len(data) {
			if data[j] == 0x07 {
				return j + 1
			}
			if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
				return j + 2
			}
			j++
		}
	}
	return i
}

// ── Title ─────────────────────────────────────────────────────────────────────

// SetTerminalTitle sets the outer terminal's tab/window title via OSC 0.
func SetTerminalTitle(w *os.File, title string) {
	fmt.Fprintf(w, "\033]0;%s\007", title)
}

// prefixSessionTitle rewrites OSC 0/1/2 title sequences in PTY output,
// prepending the daemon session name so the tab always shows which session
// is active. Skips the prefix if the title already starts with sessionName
// to avoid double-prefixing (e.g. when reattaching after a title was already set).
func prefixSessionTitle(sessionName string, data []byte) []byte {
	prefix := sessionName + " · "
	prefixB := []byte(prefix)
	out := make([]byte, 0, len(data)+len(prefixB))
	for i := 0; i < len(data); {
		// Match: ESC ] (0|1|2) ;
		if i+3 < len(data) && data[i] == 0x1b && data[i+1] == ']' {
			cmd := data[i+2]
			if (cmd == '0' || cmd == '1' || cmd == '2') && data[i+3] == ';' {
				// Check if title already starts with our prefix (avoid double-prefix
				// when reattaching to a session whose ring buffer has prior titles).
				rest := data[i+4:]
				if len(rest) >= len(prefixB) && string(rest[:len(prefixB)]) == prefix {
					// Already prefixed — pass through unchanged.
					out = append(out, data[i:i+4]...)
					i += 4
					continue
				}
				out = append(out, data[i:i+4]...) // ESC ] N ;
				out = append(out, prefixB...)
				i += 4
				continue
			}
		}
		out = append(out, data[i])
		i++
	}
	return out
}

// Attach connects to a daemon session in interactive raw terminal mode.
// Supports ~. detach (after newline) and SIGWINCH for resize.
//
// Title behaviour mirrors direct sessions: for sessions running claude via the
// shim, the shim writes OSC title sequences to its stderr (which goes into the
// PTY output stream). Those sequences flow transparently to the outer terminal,
// producing the same "claude · name · running/idle" tab titles as direct
// sessions — no special handling needed here.
func Attach(name string, rows, cols int) error {
	// Refuse to attach to the session we're already running inside — would
	// create an echo loop where the session's output feeds back as its input.
	if os.Getenv("OPENCAPY_SESSION") == name {
		return fmt.Errorf("already inside session %q — detach first with Enter then ~.", name)
	}

	conn, err := dial()
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(request{
		Op:   "attach",
		Name: name,
		Rows: rows,
		Cols: cols,
	}); err != nil {
		return err
	}

	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("attach: %s", resp.Error)
	}

	// Set initial tab title. The inner session (shell or shim) will update it
	// naturally via OSC sequences in the PTY output stream.
	SetTerminalTitle(os.Stdout, name)

	// Enter raw mode.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	var (
		wg      sync.WaitGroup
		once    sync.Once
		done    = make(chan struct{})
		stdinCh = make(chan []byte, 8)
		curCols atomic.Int32
	)
	curCols.Store(int32(cols))
	closeDone := func() { once.Do(func() { close(done) }) }

	// Goroutine: session output → stdout.
	// Filter cursor/color queries to prevent outer-terminal feedback loops.
	// OSC title sequences from the inner shim flow through untouched.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer closeDone()
		for {
			typ, payload, err := readFrame(conn)
			if err != nil {
				return
			}
			if typ == FrameOutput {
				processed := FilterTerminalQueries(payload)
				processed = prefixSessionTitle(name, processed)
				os.Stdout.Write(processed)
			}
		}
	}()

	// Goroutine: stdin → filter terminal responses → FrameInput.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := FilterTerminalResponses(buf[:n])
				if len(chunk) > 0 {
					select {
					case stdinCh <- chunk:
					case <-done:
						return
					}
				}
			}
			if err != nil {
				closeDone()
				return
			}
		}
	}()

	// Goroutine: SIGWINCH → FrameResize.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-sigCh:
				w, h, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				curCols.Store(int32(w))
				payload := make([]byte, 4)
				binary.BigEndian.PutUint16(payload[0:2], uint16(h))
				binary.BigEndian.PutUint16(payload[2:4], uint16(w))
				writeFrame(conn, FrameResize, payload)
			}
		}
	}()

	// Main: select on stdin + done.
	afterNewline := true
	lastByte := byte('\n')
	for {
		select {
		case <-done:
			wg.Wait()
			fmt.Fprintf(os.Stdout, "\r\nSession %q ended.\r\n", name)
			return nil

		case data := <-stdinCh:
			for i, b := range data {
				if afterNewline && lastByte == '~' && b == '.' {
					writeFrame(conn, FrameDetach, nil)
					conn.Close()
					closeDone()
					wg.Wait()
					fmt.Fprintf(os.Stdout, "\r\nDetached from %q.\r\n", name)
					return nil
				}
				afterNewline = (b == '\r' || b == '\n')
				if i == len(data)-1 {
					lastByte = b
				}
			}
			if err := writeFrame(conn, FrameInput, data); err != nil {
				closeDone()
				wg.Wait()
				return nil
			}
		}
	}
}

// GetTerminalSize returns the current terminal dimensions.
func GetTerminalSize() (cols, rows int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return w, h
}
