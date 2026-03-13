package watcher

import (
	"context"
	"hash/fnv"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/richardyc/opencapy/internal/tmux"
)

// EventType classifies detected CC events.
type EventType string

const (
	EventApproval EventType = "approval"
	EventCrash    EventType = "crash"
	EventDone     EventType = "done"
	EventOutput   EventType = "output" // raw pane lines for live display
)

// Event represents a detected CC event in a tmux pane.
type Event struct {
	Type      EventType `json:"type"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// structuredPatterns are used only for push notifications and the approval popup.
// Display is handled by raw EventOutput — no more brittle pattern-matching for UI.
var structuredPatterns = map[EventType]*regexp.Regexp{
	EventApproval: regexp.MustCompile(`(?i)(do you want to proceed|\[y/n\]|❯\s*1\.?\s*yes)`),
	EventCrash:    regexp.MustCompile(`(?im)^\s*(Traceback \(most recent call last\)|panic:|fatal error:|FATAL ERROR:)`),
	EventDone:     regexp.MustCompile(`(?i)task complete`),
}

// toolCallPattern finds the ⏺ ToolName(...) line to show in approval context.
var toolCallPattern = regexp.MustCompile(`⏺\s+\S`)

// extractApprovalContext returns the last tool-call line from a wider output window.
func extractApprovalContext(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if toolCallPattern.MatchString(lines[i]) {
			return strings.TrimSpace(lines[i])
		}
	}
	return "Claude Code needs approval"
}

// DetectEvents scans pane output for structured events (approval/crash/done).
func DetectEvents(sessionName, output string) []Event {
	var events []Event
	now := time.Now()
	for eventType, pat := range structuredPatterns {
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
	sessions  map[string]string
	events    chan Event
	interval  time.Duration
	lastHash  map[string]uint64
	lastEvent map[string]map[EventType]time.Time
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

// HasSession returns true if the session is already registered.
func (w *Watcher) HasSession(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.sessions[name]
	return ok
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
		output, err := tmux.CapturePaneOutput(name, 20, true)
		if err != nil {
			continue
		}

		// Skip if pane content unchanged.
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

		// --- EventOutput: last 15 lines as a live pane snapshot, 1s cooldown ---
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

		// --- Structured events (approval/crash/done) for push + approval popup ---
		// Only scan the bottom 5 lines to avoid false matches in historical output.
		tail := strings.Join(lines[max(0, len(lines)-5):], "\n")
		structured := DetectEvents(name, tail)

		// Enrich approval with a wider context window to find the ⏺ tool call.
		for i, ev := range structured {
			if ev.Type == EventApproval {
				ctx50, _ := tmux.CapturePaneOutput(name, 50, true)
				structured[i].Content = extractApprovalContext(ctx50)
			}
		}
		for _, ev := range structured {
			w.tryEmit(name, ev, 2*time.Second)
		}
	}
}

// FeedOutput emits only an EventOutput for a direct session, skipping structured
// event detection. Used during the resize window to keep the UI updated without
// risking false approval/crash/done events from terminal redraw content.
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

// Feed processes a chunk of raw PTY output for a direct (non-tmux) session.
// It runs the same event detection and cooldown logic as the poll loop,
// but is driven by streaming bytes instead of tmux capture-pane polling.
func (w *Watcher) Feed(sessionName, output string) {
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

	tail := strings.Join(lines[max(0, len(lines)-5):], "\n")
	for _, ev := range DetectEvents(sessionName, tail) {
		w.tryEmit(sessionName, ev, 2*time.Second)
	}
}

// tryEmit fires ev if the per-(session,type) cooldown has elapsed.
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
