package watcher

import (
	"testing"
)

func TestDetectEvents_Approval(t *testing.T) {
	events := DetectEvents("test", "Do you want to proceed? [Y/n]")
	if len(events) == 0 {
		t.Fatal("expected approval event")
	}
	if events[0].Type != EventApproval {
		t.Errorf("expected approval, got %s", events[0].Type)
	}
}

func TestDetectEvents_Crash(t *testing.T) {
	events := DetectEvents("test", "Traceback (most recent call last):\n  File train.py")
	found := false
	for _, e := range events {
		if e.Type == EventCrash {
			found = true
		}
	}
	if !found {
		t.Fatal("expected crash event")
	}
}

func TestDetectEvents_NoMatch(t *testing.T) {
	events := DetectEvents("test", "Step 100 of 1000. Loss: 0.24")
	if len(events) != 0 {
		t.Errorf("expected no events for normal output, got %d", len(events))
	}
}

func TestDetectEvents_FileEdit(t *testing.T) {
	events := DetectEvents("test", "Edited: src/train.py")
	found := false
	for _, e := range events {
		if e.Type == EventFileEdit {
			found = true
		}
	}
	if !found {
		t.Fatal("expected file_edit event")
	}
}
