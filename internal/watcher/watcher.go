package watcher

import (
	"context"
	"strings"
	"sync"
	"time"
)

// EventType classifies session events.
type EventType string

const (
	EventApproval EventType = "approval" // Claude needs permission (from hooks)
	EventCrash    EventType = "crash"    // session crashed (from shim exit code)
	EventDone     EventType = "done"     // task complete (from Stop hook or JSONL)
	EventInput    EventType = "input"    // user message sent via chat (from chat_send)
	EventQuestion EventType = "question" // AskUserQuestion from Claude (structured Q&A)
	EventRunning  EventType = "running"  // tool executing (from PreToolUse/PostToolUse hooks)
	EventOutput   EventType = "output"   // raw pane lines for live display
)

// Event represents a session event.
type Event struct {
	Type      EventType `json:"type"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Watcher manages session events. All events are pushed via callbacks
// (FeedOutput for PTY data, Emit for hook events).
type Watcher struct {
	mu        sync.RWMutex
	sessions  map[string]string
	events    chan Event
	lastEvent map[string]map[EventType]time.Time
}

func New() *Watcher {
	return &Watcher{
		sessions:  make(map[string]string),
		events:    make(chan Event, 100),
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
	delete(w.lastEvent, name)
}

func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Start blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	<-ctx.Done()
}

// Emit sends an event directly, bypassing cooldown.
// Used by hooks and the JSONL watcher for precise semantic events.
func (w *Watcher) Emit(ev Event) {
	select {
	case w.events <- ev:
	default:
	}
}

// FeedOutput emits raw terminal output for a session.
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
