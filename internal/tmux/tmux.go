package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// tmuxPath returns the absolute path to the tmux binary.
// LaunchAgent / systemd environments often have a minimal PATH that omits
// Homebrew (/opt/homebrew/bin) or /usr/local/bin, so we fall back to known
// locations when exec.LookPath fails.
var (
	tmuxBin     string
	tmuxBinOnce sync.Once
)

func tmuxPath() string {
	tmuxBinOnce.Do(func() {
		if p, err := exec.LookPath("tmux"); err == nil {
			tmuxBin = p
			return
		}
		for _, p := range []string{
			"/opt/homebrew/bin/tmux", // Apple Silicon Homebrew
			"/usr/local/bin/tmux",    // Intel Homebrew / Linux
			"/usr/bin/tmux",
		} {
			if _, err := os.Stat(p); err == nil {
				tmuxBin = p
				return
			}
		}
		tmuxBin = "tmux" // last resort
	})
	return tmuxBin
}

// Session represents a tmux session.
type Session struct {
	Name       string
	Cwd        string
	Windows    int
	Created    time.Time
	LastActive time.Time
}

// CapybaraColor is the opencapy brand status-bar color — warm capybara brown.
const CapybaraColor = "#7B5B3A"

// scrollConf contains tmux key bindings that reduce Magic Trackpad scroll sensitivity.
// Default tmux scrolls 5 lines per wheel event; this reduces it to 1.
// The mouse_any_flag check ensures app-level mouse events (e.g. Claude Code TUI) are
// still forwarded correctly instead of being intercepted by copy mode.
const scrollConf = `bind -T copy-mode WheelUpPane   send-keys -X -N 1 scroll-up
bind -T copy-mode WheelDownPane send-keys -X -N 1 scroll-down
bind -n WheelUpPane   if -Ft= '#{mouse_any_flag}' 'send-keys -M' 'if -Ft= "#{pane_in_mode}" "send-keys -X -N 1 scroll-up" "copy-mode -et="'
bind -n WheelDownPane if -Ft= '#{mouse_any_flag}' 'send-keys -M' 'if -Ft= "#{pane_in_mode}" "send-keys -X -N 1 scroll-down" "send-keys -M"'
`

// ApplyScrollConfig writes the scroll sensitivity config to a temp file and sources it
// into the running tmux server. Safe to call multiple times (idempotent bindings).
func ApplyScrollConfig() {
	path := os.TempDir() + "/.opencapy-scroll.conf"
	if err := os.WriteFile(path, []byte(scrollConf), 0o644); err != nil {
		return
	}
	_ = exec.Command(tmuxPath(),"source-file", path).Run()
}

// NewSession creates a new detached tmux session with the opencapy capybara status bar.
func NewSession(name, cwd string) error {
	cmd := exec.Command(tmuxPath(),"new-session", "-d", "-s", name, "-c", cwd)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	// Set capybara brown status bar on this session only.
	_ = exec.Command(tmuxPath(),"set-option", "-t", name,
		"status-style", "bg="+CapybaraColor+",fg=#F5E6D3").Run()
	// Enable mouse so trackpad scroll works without entering copy mode.
	_ = exec.Command(tmuxPath(),"set-option", "-t", name, "mouse", "on").Run()
	return nil
}

// ListSessions returns all current tmux sessions.
func ListSessions() ([]Session, error) {
	const sep = "|"
	cmd := exec.Command(tmuxPath(), "list-sessions", "-F",
		"#{session_name}"+sep+"#{session_path}"+sep+"#{session_windows}"+sep+"#{session_created}"+sep+"#{session_activity}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// tmux exits 1 when no server is running — treat as empty list, not error.
		if strings.Contains(err.Error(), "exit status") {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, sep, 5)
		if len(parts) < 5 {
			continue
		}

		windows, _ := strconv.Atoi(parts[2])
		created, _ := strconv.ParseInt(parts[3], 10, 64)
		activity, _ := strconv.ParseInt(parts[4], 10, 64)

		sessions = append(sessions, Session{
			Name:       parts[0],
			Cwd:        parts[1],
			Windows:    windows,
			Created:    time.Unix(created, 0),
			LastActive: time.Unix(activity, 0),
		})
	}
	return sessions, nil
}

// KillSession kills a tmux session by name.
func KillSession(name string) error {
	cmd := exec.Command(tmuxPath(),"kill-session", "-t", name)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Attach attaches to an existing tmux session.
// If already inside tmux ($TMUX is set), uses switch-client to avoid nesting.
// This replaces the current process with tmux.
func Attach(name string) error {
	bin := tmuxPath()
	if os.Getenv("TMUX") != "" {
		return syscall.Exec(bin, []string{bin, "switch-client", "-t", name}, os.Environ())
	}
	return syscall.Exec(bin, []string{bin, "attach-session", "-t", name}, os.Environ())
}

// CapturePaneOutput captures the last N lines from the active pane.
func CapturePaneOutput(sessionName string, lines int) (string, error) {
	start := fmt.Sprintf("-%d", lines)
	// -e preserves ANSI escape sequences so iOS can render colours universally.
	cmd := exec.Command(tmuxPath(), "capture-pane", "-t", sessionName, "-p", "-e", "-S", start)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SendKeys sends keystrokes to a session pane.
func SendKeys(sessionName, keys string) error {
	cmd := exec.Command(tmuxPath(),"send-keys", "-t", sessionName, keys, "Enter")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ActivePane returns the index of the most recently active pane.
func ActivePane(sessionName string) (string, error) {
	cmd := exec.Command(tmuxPath(),"display-message", "-t", sessionName, "-p", "#{pane_index}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SessionExists checks if a session with given name exists.
func SessionExists(name string) (bool, error) {
	cmd := exec.Command(tmuxPath(),"has-session", "-t", name)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	// Exit code 1 means session doesn't exist
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}
	return false, err
}
