package tmux

import (
	"testing"
)

func TestSessionExists_Nonexistent(t *testing.T) {
	exists, err := SessionExists("opencapy-nonexistent-test-xyz-12345")
	if err != nil {
		t.Skipf("tmux not available or error: %v", err)
	}
	if exists {
		t.Error("expected nonexistent session to return false")
	}
}
