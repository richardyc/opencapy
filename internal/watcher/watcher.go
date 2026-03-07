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
	EventThinking EventType = "thinking"
	EventFileEdit EventType = "file_edit"
	EventIdle     EventType = "idle"
	EventCrash    EventType = "crash"
	EventDone     EventType = "done"
	EventInput    EventType = "input"
)

// Event represents a detected CC event in a tmux pane.
type Event struct {
	Type      EventType `json:"type"`
	Session   string    `json:"session"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

var patterns = map[EventType]*regexp.Regexp{
	// Claude Code prompts approval with numbered options like "❯ 1. Yes" / "1 Yes"
	EventApproval: regexp.MustCompile(`(?i)(do you want to proceed|\[y/n\]|❯\s*1\.?\s*yes|^\s*1\s+yes)`),
	// Claude Code thinking spinner starts with ✶ or ⏺
	EventThinking: regexp.MustCompile(`✶|⏺\s+\w`),
	// Claude Code shows Edit/Write/Create tool calls with a leading ⏺
	EventFileEdit: regexp.MustCompile(`⏺\s+(Edit|Write|Create)\(`),
	// Idle: shell prompt alone on a line (zsh ❯ or bash $)
	EventIdle: regexp.MustCompile(`(?m)^❯\s*$`),
	// Crash: language-specific crash indicators at line start, not general "error:"
	EventCrash: regexp.MustCompile(`(?im)^\s*(Traceback \(most recent call last\)|panic:|fatal error:|FATAL ERROR:)`),
	// Done: Claude Code's specific completion phrase
	EventDone: regexp.MustCompile(`(?i)(task complete|claude code (is done|has finished|finished the task))`),
	// Input: Claude Code user input line "❯ <text>" — non-empty prompt
	EventInput: regexp.MustCompile(`(?m)^❯\s+\S.+`),
}

// cooldowns defines per-event-type debounce durations.
// Input events use a longer window to avoid re-firing the same command.
var cooldowns = map[EventType]time.Duration{
	EventInput: 10 * time.Second,
}

// toolCallPattern matches Claude Code tool invocation lines like "⏺ Bash(...)" or "⏺ Edit(...)".
var toolCallPattern = regexp.MustCompile(`⏺\s+\w[\w\s]*\(`)

// extractApprovalContext scans the full pane output (top→bottom) and returns
// the last tool-call line seen before the approval prompt. Falls back to a
// generic message if no tool call is found.
func extractApprovalContext(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if toolCallPattern.MatchString(lines[i]) {
			return strings.TrimSpace(lines[i])
		}
	}
	return "Claude Code needs approval"
}

// DetectEvents scans pane output and returns matched events.
func DetectEvents(sessionName, output string) []Event {
	var events []Event
	now := time.Now()

	for eventType, pat := range patterns {
		match := pat.FindString(output)
		if match == "" {
			continue
		}
		content := match
		if eventType == EventInput {
			// Strip the leading "❯ " prompt marker to surface just the command text.
			content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(match), "❯"))
			// Skip numbered approval option lines like "1. Yes", "2. No (safer…)"
			if len(content) >= 2 && content[0] >= '0' && content[0] <= '9' && content[1] == '.' {
				continue
			}
			if content == "" {
				continue
			}
		}
		events = append(events, Event{
			Type:      eventType,
			Session:   sessionName,
			Content:   content,
			Timestamp: now,
		})
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
		// Capture 20 lines for hash (change detection), but only run pattern
		// matching against the last 5 lines so we detect the *current* pane
		// state and not historical output (e.g., code diffs shown by Claude Code).
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

		// Only analyse the bottom 5 lines for current state detection.
		lines := strings.Split(output, "\n")
		if len(lines) > 5 {
			lines = lines[len(lines)-5:]
		}
		tail := strings.Join(lines, "\n")

		events := DetectEvents(name, tail)
		// For approval events, enrich the content with the actual tool call
		// from the broader pane context rather than just the regex match text.
		for i, ev := range events {
			if ev.Type == EventApproval {
				events[i].Content = extractApprovalContext(output)
			}
		}
		for _, ev := range events {
			// 2-second cooldown per (session, event type)
			w.mu.Lock()
			if w.lastEvent[name] == nil {
				w.lastEvent[name] = make(map[EventType]time.Time)
			}
			cd := 2 * time.Second
			if d, ok := cooldowns[ev.Type]; ok {
				cd = d
			}
			if last, ok := w.lastEvent[name][ev.Type]; ok && time.Since(last) < cd {
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
