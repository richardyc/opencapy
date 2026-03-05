package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Session represents a tmux session.
type Session struct {
	Name    string
	Cwd     string
	Windows int
	Created time.Time
}

// NewSession creates a new detached tmux session.
func NewSession(name, cwd string) error {
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", cwd)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ListSessions returns all current tmux sessions.
func ListSessions() ([]Session, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F",
		"#{session_name}\t#{session_path}\t#{session_windows}\t#{session_created}")
	out, err := cmd.Output()
	if err != nil {
		// tmux returns error if no server is running
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
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}

		windows, _ := strconv.Atoi(parts[2])
		created, _ := strconv.ParseInt(parts[3], 10, 64)

		sessions = append(sessions, Session{
			Name:    parts[0],
			Cwd:     parts[1],
			Windows: windows,
			Created: time.Unix(created, 0),
		})
	}
	return sessions, nil
}

// KillSession kills a tmux session by name.
func KillSession(name string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Attach attaches to an existing tmux session.
// This replaces the current process with tmux attach.
func Attach(name string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	return syscall.Exec(tmuxPath, []string{"tmux", "attach-session", "-t", name}, os.Environ())
}

// CapturePaneOutput captures the last N lines from the active pane.
func CapturePaneOutput(sessionName string, lines int) (string, error) {
	start := fmt.Sprintf("-%d", lines)
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", start)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SendKeys sends keystrokes to a session pane.
func SendKeys(sessionName, keys string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", sessionName, keys, "Enter")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ActivePane returns the index of the most recently active pane.
func ActivePane(sessionName string) (string, error) {
	cmd := exec.Command("tmux", "display-message", "-t", sessionName, "-p", "#{pane_index}")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SessionExists checks if a session with given name exists.
func SessionExists(name string) (bool, error) {
	cmd := exec.Command("tmux", "has-session", "-t", name)
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
