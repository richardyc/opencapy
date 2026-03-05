package watcher

import (
	"context"
	"hash/fnv"
	"regexp"
	"sync"
	"time"

	"github.com/richardyc/opencapy/internal/tmux"
)

// EventType classifies detected CC events.
type EventType string

const (
	EventApproval EventType = "approval"
	EventThinking EventType = "thinking"
	EventFileEdit EventType = "file_edit"
	EventIdle     EventType = "idle"
	EventCrash    EventType = "crash"
	EventDone     EventType = "done"
)

// Event represents a detected CC event in a tmux pane.
type Event struct {
	Type      EventType `json:"type"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

var patterns = map[EventType]*regexp.Regexp{
	EventApproval: regexp.MustCompile(`(?i)(do you want to proceed|yes.*no|\[y/n\]|❯\s*1\.?\s*yes)`),
	EventThinking: regexp.MustCompile(`(?i)(architecting\.\.\.|thinking\.\.\.|analyzing\.\.\.|✶)`),
	EventFileEdit: regexp.MustCompile(`(?i)(edited?:\s*|created?:\s*|modified?:\s*)(.+)`),
	EventIdle:     regexp.MustCompile(`(?m)^❯\s*$`),
	EventCrash:    regexp.MustCompile(`(?i)(traceback|error:|panic:|fatal:|exception:)`),
	EventDone:     regexp.MustCompile(`(?i)(task complete|all done|finished)`),
}

// DetectEvents scans pane output and returns matched events.
func DetectEvents(sessionName, output string) []Event {
	var events []Event
	now := time.Now()

	for eventType, pat := range patterns {
		if match := pat.FindString(output); match != "" {
			events = append(events, Event{
				Type:      eventType,
				Session:   sessionName,
				Content:   match,
				Timestamp: now,
			})
		}
	}
	return events
}

// Watcher polls all registered sessions and emits events.
type Watcher struct {
	mu        sync.RWMutex
	sessions  map[string]string              // name -> project path
	events    chan Event
	interval  time.Duration
	lastHash  map[string]uint64              // session -> hash of last pane output
	lastEvent map[string]map[EventType]time.Time // session -> event type -> last emit time
}

// New creates a new Watcher with the given poll interval.
func New(interval time.Duration) *Watcher {
	return &Watcher{
		sessions:  make(map[string]string),
		events:    make(chan Event, 100),
		interval:  interval,
		lastHash:  make(map[string]uint64),
		lastEvent: make(map[string]map[EventType]time.Time),
	}
}

// AddSession registers a session for watching.
func (w *Watcher) AddSession(name, projectPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions[name] = projectPath
}

// RemoveSession unregisters a session.
func (w *Watcher) RemoveSession(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.sessions, name)
	delete(w.lastHash, name)
	delete(w.lastEvent, name)
}

// Events returns the read-only event channel.
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
		output, err := tmux.CapturePaneOutput(name, 20)
		if err != nil {
			continue
		}

		// Compute content hash — skip if unchanged
		h := fnv.New64a()
		h.Write([]byte(output))
		hash := h.Sum64()

		w.mu.Lock()
		if w.lastHash[name] == hash {
			w.mu.Unlock()
			continue // nothing changed
		}
		w.lastHash[name] = hash
		w.mu.Unlock()

		events := DetectEvents(name, output)
		for _, ev := range events {
			// 2-second cooldown per (session, event type)
			w.mu.Lock()
			if w.lastEvent[name] == nil {
				w.lastEvent[name] = make(map[EventType]time.Time)
			}
			if last, ok := w.lastEvent[name][ev.Type]; ok && time.Since(last) < 2*time.Second {
				w.mu.Unlock()
				continue
			}
			w.lastEvent[name][ev.Type] = time.Now()
			w.mu.Unlock()

			select {
			case w.events <- ev:
			default:
				// Channel full, drop oldest
			}
		}
	}
}
