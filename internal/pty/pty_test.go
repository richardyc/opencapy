package pty

import (
	"os/exec"
	"testing"
	"time"
)

// TestPTYManager_OpenClose opens a PTY session, writes a command, closes it.
// Skips if tmux is not installed.
func TestPTYManager_OpenClose(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH — skipping PTY test")
	}

	mgr := NewManager()

	// Create a throw-away tmux session so attach-session can find it.
	sessionName := "opencapy-pty-test"
	createCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName)
	if err := createCmd.Run(); err != nil {
		t.Skipf("could not create tmux session: %v", err)
	}
	defer exec.Command("tmux", "kill-session", "-t", sessionName).Run() //nolint:errcheck

	if err := mgr.Open(sessionName, "test-client-1", 80, 24); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Write a harmless command
	if err := mgr.Write(sessionName, []byte("echo hello\n")); err != nil {
		t.Logf("Write warning (non-fatal): %v", err)
	}

	// Give the goroutine a moment
	time.Sleep(50 * time.Millisecond)

	// Resize should not panic
	if err := mgr.Resize(sessionName, 120, 40); err != nil {
		t.Logf("Resize warning (non-fatal): %v", err)
	}

	// Close should not panic
	mgr.Close(sessionName)

	// Closing again should be a no-op
	mgr.Close(sessionName)
}
