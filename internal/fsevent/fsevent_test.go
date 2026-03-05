package fsevent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileWatcher_BasicChange(t *testing.T) {
	tmp := t.TempDir()

	// Create a CLAUDE.md to watch
	claudePath := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("initial content"), 0644); err != nil {
		t.Fatal(err)
	}

	fw, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if err := fw.AddProject(tmp); err != nil {
		t.Fatalf("AddProject() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go fw.Start(ctx)

	// Give watcher time to set up
	time.Sleep(100 * time.Millisecond)

	// Modify the file
	if err := os.WriteFile(claudePath, []byte("updated content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for event
	select {
	case ev := <-fw.Events():
		if ev.RelPath != "CLAUDE.md" {
			t.Errorf("expected CLAUDE.md event, got %s", ev.RelPath)
		}
		if ev.Content != "updated content" {
			t.Errorf("expected updated content, got %q", ev.Content)
		}
		if ev.ProjectPath != tmp {
			t.Errorf("expected project path %s, got %s", tmp, ev.ProjectPath)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for file event")
	}
}

func TestFileWatcher_NonexistentProject(t *testing.T) {
	fw, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	// Should not error on nonexistent project (graceful skip)
	err = fw.AddProject("/nonexistent/path/xyz")
	if err != nil {
		t.Errorf("expected no error for nonexistent project, got: %v", err)
	}
}
