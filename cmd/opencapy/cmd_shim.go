package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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
			// Write diagnostics to a log file so failures are visible without
			// polluting the terminal (raw mode makes stderr invisible).
			logf, _ := os.OpenFile("/tmp/opencapy-shim.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			dbg := func(format string, v ...interface{}) {
				if logf != nil {
					fmt.Fprintf(logf, format+"\n", v...)
				}
			}
			defer func() {
				if logf != nil {
					logf.Close()
				}
			}()
			dbg("shim started args=%v", args)

			cfg, _ := config.Load()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			port := 7242
			if cfg != nil {
				port = cfg.Port
			}

			wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				dbg("ws dial failed: %v — falling back", err)
				return fallbackExec(args)
			}
			defer conn.CloseNow()
			dbg("ws connected")

			cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				cols, rows = 80, 24
			}
			dbg("terminal size %dx%d", cols, rows)

			cwd, _ := os.Getwd()
			dbg("cwd=%q", cwd)

			claudePath, err := findRealClaude()
			if err != nil {
				dbg("findRealClaude failed: %v", err)
				return err
			}
			dbg("claudePath=%q", claudePath)

			spawnMsg := map[string]interface{}{
				"type":         "spawn_pty",
				"args":         append([]string{claudePath}, args...),
				"project_path": cwd,
				"branch":       os.Getenv("OPENCAPY_GIT_BRANCH"),
				"cols":         cols,
				"rows":         rows,
				"env":          filteredEnv(),
			}
			if err := wsjson.Write(ctx, conn, spawnMsg); err != nil {
				dbg("wsjson.Write spawn_pty failed: %v — falling back", err)
				return fallbackExec(args)
			}
			dbg("spawn_pty sent")

			sessionName, err := waitSessionAssigned(ctx, conn)
			if err != nil {
				dbg("waitSessionAssigned failed: %v — falling back", err)
				return fallbackExec(args)
			}
			dbg("session assigned: %q", sessionName)

			setTerminalTitle(fmt.Sprintf("claude · %s · running", sessionName))

			oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				return fallbackExec(args)
			}
			defer term.Restore(int(os.Stdin.Fd()), oldState)

			// stdin → pty_input
			go func() {
				buf := make([]byte, 256)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						_ = wsjson.Write(ctx, conn, map[string]interface{}{
							"type":    "pty_input",
							"session": sessionName,
							"data":    base64.StdEncoding.EncodeToString(buf[:n]),
						})
					}
					if err != nil {
						cancel()
						return
					}
				}
			}()

			// SIGWINCH → pty_resize
			winchCh := make(chan os.Signal, 1)
			signal.Notify(winchCh, syscall.SIGWINCH)
			go func() {
				for range winchCh {
					c, r, err := term.GetSize(int(os.Stdin.Fd()))
					if err != nil {
						continue
					}
					_ = wsjson.Write(ctx, conn, map[string]interface{}{
						"type":    "pty_resize",
						"session": sessionName,
						"cols":    c,
						"rows":    r,
					})
				}
			}()
			defer signal.Stop(winchCh)

			// WS → stdout
			for {
				var raw map[string]json.RawMessage
				if err := wsjson.Read(ctx, conn, &raw); err != nil {
					break
				}
				switch unquote(raw["type"]) {
				case "pty_output":
					var payload struct {
						Data string `json:"data"`
					}
					if err := json.Unmarshal(raw["payload"], &payload); err == nil {
						if decoded, err := base64.StdEncoding.DecodeString(payload.Data); err == nil {
							os.Stdout.Write(decoded)
						}
					}
				case "event":
					updateTitleFromEvent(sessionName, raw["payload"])
				}
			}

			setTerminalTitle("")
			return nil
		},
	}
}

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

func updateTitleFromEvent(sessionName string, payloadRaw json.RawMessage) {
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return
	}
	switch payload.Type {
	case "approval":
		setTerminalTitle(fmt.Sprintf("🔴 claude · %s · needs OK", sessionName))
	case "done":
		setTerminalTitle(fmt.Sprintf("✓ claude · %s · done", sessionName))
	case "crash":
		setTerminalTitle(fmt.Sprintf("✗ claude · %s · crashed", sessionName))
	}
}

func setTerminalTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
}

// findRealClaude locates the real claude binary, skipping any path inside
// ~/.opencapy to prevent recursion if an old PATH shim is still present.
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
func fallbackExec(args []string) error {
	claudePath, err := findRealClaude()
	if err != nil {
		return err
	}
	return syscall.Exec(claudePath, append([]string{claudePath}, args...), os.Environ())
}

func unquote(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
