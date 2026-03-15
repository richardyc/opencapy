package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/creack/pty"
	"github.com/richardyc/opencapy/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newShimCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "shim [args...]",
		Short:  "Run claude via the opencapy daemon (invoked by the claude shell function)",
		Hidden: true,
		// Pass all flags/args through to claude unchanged.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			claudePath, err := findRealClaude()
			if err != nil {
				return err
			}

			cfg, _ := config.Load()
			port := 7242
			if cfg != nil {
				port = cfg.Port
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Try to connect to daemon. If unavailable, run claude directly.
			wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				return fallbackExec(claudePath, args)
			}
			defer conn.CloseNow()
			// Snapshots with many sessions can exceed the default 32KB limit.
			conn.SetReadLimit(1 << 20) // 1MB

			// Get terminal size.
			cols, rows := 80, 24
			isTerminal := term.IsTerminal(int(os.Stdin.Fd()))
			if isTerminal {
				if c, r, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
					cols, rows = c, r
				}
			}

			cwd, _ := os.Getwd()

			// Register session with daemon — no process spawning on daemon side.
			if err := wsjson.Write(ctx, conn, map[string]interface{}{
				"type":         "register_session",
				"project_path": cwd,
				"branch":       os.Getenv("OPENCAPY_GIT_BRANCH"),
				"cols":         cols,
				"rows":         rows,
			}); err != nil {
				return fallbackExec(claudePath, args)
			}

			sessionName, err := waitSessionAssigned(ctx, conn)
			if err != nil {
				return fallbackExec(claudePath, args)
			}

			// Spawn claude in the user's context — natural env, no TCC prompts.
			claudeCmd := exec.Command(claudePath, args...)
			claudeCmd.Env = append(filteredEnv(), "OPENCAPY_SESSION="+sessionName)
			claudeCmd.Dir = cwd
			ptmx, err := pty.StartWithSize(claudeCmd, &pty.Winsize{
				Cols: uint16(cols),
				Rows: uint16(rows),
			})
			if err != nil {
				return fallbackExec(claudePath, args)
			}
			defer ptmx.Close()

			setTerminalTitle(fmt.Sprintf("claude · %s · running", sessionName))

			// Put terminal in raw mode if connected to a real TTY.
			if isTerminal {
				if oldState, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
					defer term.Restore(int(os.Stdin.Fd()), oldState)
				}
			}

			// stdin → ptmx (user keyboard to claude)
			go func() {
				buf := make([]byte, 256)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						ptmx.Write(buf[:n]) //nolint:errcheck
					}
					if err != nil {
						cancel()
						return
					}
				}
			}()

			// activeConn is the current live WS connection for sending pty_data.
			// The reconnect goroutine swaps it when a new connection is established.
			var connMu sync.Mutex
			activeConn := conn

			// shimBuf is a local ring buffer of recent PTY output (capped at 256 KB).
			// Replayed in reregister_session so the daemon can restore ds.buf after
			// a restart, giving iOS history when reconnecting to old sessions.
			const shimBufMax = 256 * 1024
			var shimBuf []byte

			sendToConn := func(msg interface{}) {
				connMu.Lock()
				c := activeConn
				connMu.Unlock()
				if c != nil {
					_ = wsjson.Write(ctx, c, msg)
				}
			}

			// ptmx output → stdout (display) + pty_data to daemon (streaming)
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := ptmx.Read(buf)
					if n > 0 {
						chunk := make([]byte, n)
						copy(chunk, buf[:n])
						// Keep a local ring buffer so we can replay history on reconnect.
						connMu.Lock()
						shimBuf = append(shimBuf, chunk...)
						if len(shimBuf) > shimBufMax {
							shimBuf = shimBuf[len(shimBuf)-shimBufMax:]
						}
						connMu.Unlock()
						// Strip claude's OSC title sequences so our session-name
						// title persists and is never overridden.
						os.Stdout.Write(oscTitleRe.ReplaceAll(chunk, nil)) //nolint:errcheck
						sendToConn(map[string]interface{}{
							"type":    "pty_data",
							"session": sessionName,
							"data":    base64.StdEncoding.EncodeToString(chunk),
						})
					}
					if err != nil {
						// io.EOF (macOS) and EIO (Linux) both mean the child exited normally.
						break
					}
				}
				// Notify daemon the session has ended with exit code for crash detection.
				exitCode := 0
				if err := claudeCmd.Wait(); err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						exitCode = ee.ExitCode()
					}
				}
				sendToConn(map[string]interface{}{
					"type":      "session_end",
					"session":   sessionName,
					"exit_code": exitCode,
				})
				cancel()
			}()

			// SIGWINCH → resize PTY + notify daemon
			winchCh := make(chan os.Signal, 1)
			signal.Notify(winchCh, syscall.SIGWINCH)
			go func() {
				for range winchCh {
					if !term.IsTerminal(int(os.Stdin.Fd())) {
						continue
					}
					c, r, err := term.GetSize(int(os.Stdin.Fd()))
					if err != nil {
						continue
					}
					pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)}) //nolint:errcheck
					_ = wsjson.Write(ctx, conn, map[string]interface{}{
						"type":    "pty_resize",
						"session": sessionName,
						"cols":    c,
						"rows":    r,
					})
				}
			}()
			defer signal.Stop(winchCh)

			// WS → route pty_input and pty_resize from iOS to the PTY
			for {
				var raw map[string]json.RawMessage
				if err := wsjson.Read(ctx, conn, &raw); err != nil {
					break
				}
				switch unquote(raw["type"]) {
				case "pty_input":
					var payload struct {
						Data string `json:"data"`
					}
					if err := json.Unmarshal(raw["payload"], &payload); err == nil {
						if decoded, err := base64.StdEncoding.DecodeString(payload.Data); err == nil {
							ptmx.Write(decoded) //nolint:errcheck
						}
					}
				case "pty_resize":
					var payload struct {
						Cols int `json:"cols"`
						Rows int `json:"rows"`
					}
					if err := json.Unmarshal(raw["payload"], &payload); err == nil && payload.Cols > 0 && payload.Rows > 0 {
						pty.Setsize(ptmx, &pty.Winsize{ //nolint:errcheck
							Cols: uint16(payload.Cols),
							Rows: uint16(payload.Rows),
						})
					}
				case "event":
					updateTitleFromEvent(sessionName, raw["payload"])
				}
			}

			setTerminalTitle(fmt.Sprintf("claude · %s · running", sessionName))
			// WS disconnected — daemon may have restarted. Reconnect, re-register,
			// and swap activeConn so PTY data resumes flowing to the new daemon.
			// Claude keeps running throughout; we just restore the monitoring link.
			go func() {
				backoff := 2 * time.Second
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					newConn, _, err := websocket.Dial(ctx, wsURL, nil)
					if err != nil {
						if backoff < 30*time.Second {
							backoff *= 2
						}
						continue
					}
					newConn.SetReadLimit(1 << 20)
					connMu.Lock()
					bufSnapshot := make([]byte, len(shimBuf))
					copy(bufSnapshot, shimBuf)
					connMu.Unlock()
					rereg := map[string]interface{}{
						"type":         "reregister_session",
						"session":      sessionName,
						"project_path": cwd,
						"branch":       os.Getenv("OPENCAPY_GIT_BRANCH"),
						"buf":          base64.StdEncoding.EncodeToString(bufSnapshot),
					}
					if wsjson.Write(ctx, newConn, rereg) != nil {
						newConn.CloseNow()
						continue
					}
					// Swap activeConn so the PTY output goroutine writes to the new connection.
					connMu.Lock()
					old := activeConn
					activeConn = newConn
					connMu.Unlock()
					if old != nil {
						old.CloseNow()
					}
					// Handle pty_input, pty_resize, and events from the new connection.
					for {
						var raw map[string]json.RawMessage
						if wsjson.Read(ctx, newConn, &raw) != nil {
							break
						}
						switch unquote(raw["type"]) {
						case "pty_input":
							var payload struct {
								Data string `json:"data"`
							}
							if err := json.Unmarshal(raw["payload"], &payload); err == nil {
								if decoded, err := base64.StdEncoding.DecodeString(payload.Data); err == nil {
									ptmx.Write(decoded) //nolint:errcheck
								}
							}
						case "pty_resize":
							var payload struct {
								Cols int `json:"cols"`
								Rows int `json:"rows"`
							}
							if err := json.Unmarshal(raw["payload"], &payload); err == nil && payload.Cols > 0 && payload.Rows > 0 {
								pty.Setsize(ptmx, &pty.Winsize{ //nolint:errcheck
									Cols: uint16(payload.Cols),
									Rows: uint16(payload.Rows),
								})
							}
						case "event":
							updateTitleFromEvent(sessionName, raw["payload"])
						}
					}
					// Connection dropped again — loop to reconnect.
					backoff = 2 * time.Second
				}
			}()
			<-ctx.Done()
			return nil
		},
	}
}

// waitSessionAssigned reads WS messages until session_assigned arrives.
func waitSessionAssigned(ctx context.Context, conn *websocket.Conn) (string, error) {
	for {
		var raw map[string]json.RawMessage
		if err := wsjson.Read(ctx, conn, &raw); err != nil {
			return "", err
		}
		if unquote(raw["type"]) == "session_assigned" {
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw["payload"], &payload); err == nil && payload.Name != "" {
				return payload.Name, nil
			}
		}
	}
}

// updateTitleFromEvent updates the terminal title on approval/done/crash events,
// but only if the event belongs to this shim's session.
func updateTitleFromEvent(sessionName string, payloadRaw json.RawMessage) {
	var payload struct {
		Type    string `json:"type"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return
	}
	if payload.Session != sessionName {
		return
	}
	switch payload.Type {
	case "running":
		setTerminalTitle(fmt.Sprintf("claude · %s · running", sessionName))
	case "approval":
		setTerminalTitle(fmt.Sprintf("🔴 claude · %s · needs OK", sessionName))
	case "done":
		setTerminalTitle(fmt.Sprintf("✓ claude · %s · done", sessionName))
	case "crash":
		setTerminalTitle(fmt.Sprintf("✗ claude · %s · crashed", sessionName))
	}
}

// setTerminalTitle updates the terminal window/tab title via OSC 0 escape.
func setTerminalTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
}

// oscTitleRe matches OSC 0/1/2 title-setting sequences that programs like
// claude emit to set the terminal window/tab title. We strip these so the
// shim's own title (showing the session name) is never overridden.
var oscTitleRe = regexp.MustCompile(`\x1b][012];[^\x07]*\x07`)

// findRealClaude locates the real claude binary, skipping ~/.opencapy paths
// to prevent recursion if any old shim files are still present on disk.
func findRealClaude() (string, error) {
	home, _ := os.UserHomeDir()
	skipPrefix := filepath.Join(home, ".opencapy")
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.HasPrefix(dir, skipPrefix) {
			continue
		}
		candidate := filepath.Join(dir, "claude")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("claude not found in PATH — install it from https://docs.anthropic.com/en/docs/claude-code/getting-started")
}

// fallbackExec replaces the current process with the real claude binary.
// Used when the daemon is unavailable — completely transparent to the user.
func fallbackExec(claudePath string, args []string) error {
	return syscall.Exec(claudePath, append([]string{claudePath}, args...), os.Environ())
}

// unquote strips surrounding JSON quotes from a raw JSON string value.
func unquote(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
