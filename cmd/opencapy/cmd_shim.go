package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/richardyc/opencapy/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newShimCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shim [args...]",
		Short: "Run claude via the opencapy daemon (used by the claude shim script)",
		// Hide from help — users invoke `claude`, not `opencapy shim`.
		Hidden: true,
		// Pass all flags/args through to claude unchanged.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil || cfg.RealClaudePath == "" {
				return fallbackExec(cfg, args)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws", cfg.Port)
			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				// Daemon not running — exec the real claude directly.
				return fallbackExec(cfg, args)
			}
			defer conn.CloseNow()

			// Get terminal dimensions.
			cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
			if err != nil {
				cols, rows = 80, 24
			}

			cwd, _ := os.Getwd()
			claudeArgs := append([]string{cfg.RealClaudePath}, args...)

			// Ask daemon to spawn claude and assign a session name.
			spawnMsg := map[string]interface{}{
				"type":         "spawn_pty",
				"args":         claudeArgs,
				"project_path": cwd,
				"cols":         cols,
				"rows":         rows,
			}
			if err := wsjson.Write(ctx, conn, spawnMsg); err != nil {
				return fallbackExec(cfg, args)
			}

			// Wait for session_assigned acknowledgement.
			sessionName, err := waitSessionAssigned(ctx, conn)
			if err != nil {
				return fallbackExec(cfg, args)
			}

			// Update terminal title with session name.
			setTerminalTitle(fmt.Sprintf("claude · %s · running", sessionName))

			// Switch stdin to raw mode so keystrokes go straight through.
			oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				return fallbackExec(cfg, args)
			}
			defer term.Restore(int(os.Stdin.Fd()), oldState)

			// Forward stdin → daemon as pty_input messages.
			go func() {
				buf := make([]byte, 256)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						msg := map[string]interface{}{
							"type":    "pty_input",
							"session": sessionName,
							"data":    base64.StdEncoding.EncodeToString(buf[:n]),
						}
						_ = wsjson.Write(ctx, conn, msg)
					}
					if err != nil {
						cancel()
						return
					}
				}
			}()

			// Forward SIGWINCH → pty_resize messages.
			winchCh := make(chan os.Signal, 1)
			signal.Notify(winchCh, syscall.SIGWINCH)
			go func() {
				for range winchCh {
					c, r, err := term.GetSize(int(os.Stdin.Fd()))
					if err != nil {
						continue
					}
					msg := map[string]interface{}{
						"type":    "pty_resize",
						"session": sessionName,
						"cols":    c,
						"rows":    r,
					}
					_ = wsjson.Write(ctx, conn, msg)
				}
			}()
			defer signal.Stop(winchCh)

			// Main loop: read daemon → write to stdout.
			for {
				var raw map[string]json.RawMessage
				if err := wsjson.Read(ctx, conn, &raw); err != nil {
					break
				}
				msgType := unquote(raw["type"])
				switch msgType {
				case "pty_output":
					var payload struct {
						Data string `json:"data"`
					}
					if err := json.Unmarshal(raw["payload"], &payload); err == nil {
						decoded, err := base64.StdEncoding.DecodeString(payload.Data)
						if err == nil {
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

// waitSessionAssigned reads messages until session_assigned arrives.
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

// updateTitleFromEvent updates the terminal title based on approval/done/crash events.
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

// setTerminalTitle sets the terminal window/tab title via OSC 0 escape sequence.
func setTerminalTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
}

// fallbackExec replaces the current process with the real claude binary.
// Used when the daemon is unavailable — completely transparent to the user.
func fallbackExec(cfg *config.Config, args []string) error {
	claudePath := ""
	if cfg != nil {
		claudePath = cfg.RealClaudePath
	}
	if claudePath == "" {
		// Last resort: search PATH (excluding ourselves).
		claudePath, _ = exec.LookPath("claude")
	}
	if claudePath == "" {
		return fmt.Errorf("claude not found — set real_claude_path in ~/.opencapy/config.json")
	}
	claudeArgs := append([]string{claudePath}, args...)
	return syscall.Exec(claudePath, claudeArgs, os.Environ())
}

// unquote removes surrounding JSON quotes from a raw JSON string value.
func unquote(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
