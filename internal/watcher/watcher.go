package watcher

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/richardyc/opencapy/internal/tmux"
)

// EventType classifies session events.
type EventType string

const (
	EventApproval EventType = "approval" // Claude needs permission (from hooks)
	EventCrash    EventType = "crash"    // session crashed (from shim exit code)
	EventDone     EventType = "done"     // task complete (from Stop hook or JSONL)
	EventRunning  EventType = "running"  // tool executing (from PreToolUse hook)
	EventOutput   EventType = "output"   // raw pane lines for live display
	EventIdle     EventType = "idle"     // waiting for input
)

// Event represents a session event.
type Event struct {
	Type      EventType `json:"type"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Watcher polls tmux sessions for raw terminal output (live display).
// All semantic events (approval, done, crash, running) come from Claude Code's
// hook system and the JSONL transcript watcher — no string pattern matching.
type Watcher struct {
	mu        sync.RWMutex
	sessions  map[string]string
	events    chan Event
	interval  time.Duration
	lastHash  map[string]uint64
	lastEvent map[string]map[EventType]time.Time
}

func New(interval time.Duration) *Watcher {
	return &Watcher{
		sessions:  make(map[string]string),
		events:    make(chan Event, 100),
		interval:  interval,
		lastHash:  make(map[string]uint64),
		lastEvent: make(map[string]map[EventType]time.Time),
	}
}

func (w *Watcher) HasSession(name string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.sessions[name]
	return ok
}

func (w *Watcher) AddSession(name, projectPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions[name] = projectPath
}

func (w *Watcher) RemoveSession(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.sessions, name)
	delete(w.lastHash, name)
	delete(w.lastEvent, name)
}

func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Start begins the poll loop. Blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *Watcher) poll() {
	w.mu.RLock()
	names := make([]string, 0, len(w.sessions))
	for name := range w.sessions {
		names = append(names, name)
	}
	w.mu.RUnlock()

	for _, name := range names {
		output, err := tmux.CapturePaneOutput(name, 20, true)
		if err != nil {
			continue
		}

		h := fnv.New64a()
		h.Write([]byte(output))
		hash := h.Sum64()
		w.mu.Lock()
		if w.lastHash[name] == hash {
			w.mu.Unlock()
			continue
		}
		w.lastHash[name] = hash
		w.mu.Unlock()

		lines := strings.Split(output, "\n")
		if len(lines) > 15 {
			lines = lines[len(lines)-15:]
		}
		w.tryEmit(name, Event{
			Type:      EventOutput,
			Session:   name,
			Content:   strings.Join(lines, "\n"),
			Timestamp: time.Now(),
		}, 1*time.Second)
	}
}

// Emit sends an event directly, bypassing cooldown.
// Used by hooks and the JSONL watcher for precise semantic events.
func (w *Watcher) Emit(ev Event) {
	select {
	case w.events <- ev:
	default:
	}
}

// FeedOutput emits raw terminal output for a direct session.
func (w *Watcher) FeedOutput(sessionName, output string) {
	if output == "" {
		return
	}
	lines := strings.Split(output, "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	w.tryEmit(sessionName, Event{
		Type:      EventOutput,
		Session:   sessionName,
		Content:   strings.Join(lines, "\n"),
		Timestamp: time.Now(),
	}, 1*time.Second)
}

func (w *Watcher) tryEmit(session string, ev Event, cooldown time.Duration) {
	w.mu.Lock()
	if w.lastEvent[session] == nil {
		w.lastEvent[session] = make(map[EventType]time.Time)
	}
	if last, ok := w.lastEvent[session][ev.Type]; ok && time.Since(last) < cooldown {
		w.mu.Unlock()
		return
	}
	w.lastEvent[session][ev.Type] = time.Now()
	w.mu.Unlock()
	select {
	case w.events <- ev:
	default:
	}
}
