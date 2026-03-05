package fsevent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileEvent represents a watched file change.
type FileEvent struct {
	ProjectPath string    `json:"project_path"` // root project dir
	FilePath    string    `json:"file_path"`    // full path to changed file
	RelPath     string    `json:"rel_path"`     // path relative to project root
	Content     string    `json:"content"`      // new file content (empty if deleted)
	Deleted     bool      `json:"deleted"`
	Timestamp   time.Time `json:"timestamp"`
}

// WatchedPaths returns the relative paths we watch within a project.
var WatchedPaths = []string{
	"CLAUDE.md",
	".claude/rules",
	".cursor/index.mdc",
	".cursor/rules",
	".vscode/settings.json",
	".vscode/launch.json",
	".vscode/tasks.json",
}

// Watcher watches project metadata files and emits FileEvents.
type Watcher struct {
	watcher  *fsnotify.Watcher
	events   chan FileEvent
	projects map[string]bool // project paths being watched

	// debounce: tracks pending timers per file path
	mu      sync.Mutex
	timers  map[string]*time.Timer
	watched map[string]string // watched path -> project path
}

// New creates a new file watcher.
func New() (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		watcher:  fw,
		events:   make(chan FileEvent, 64),
		projects: make(map[string]bool),
		timers:   make(map[string]*time.Timer),
		watched:  make(map[string]string),
	}, nil
}

// AddProject adds a project directory to watch.
// Watches all WatchedPaths that exist in the project dir.
// Non-existent paths are silently skipped.
func (w *Watcher) AddProject(projectPath string) error {
	// Check that projectPath itself exists
	if _, err := os.Stat(projectPath); err != nil {
		if os.IsNotExist(err) {
			log.Printf("[fsevent] project path does not exist, skipping: %s", projectPath)
			return nil
		}
		return nil // silently skip stat errors
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.projects[projectPath] {
		return nil // already watching
	}
	w.projects[projectPath] = true

	for _, rel := range WatchedPaths {
		full := filepath.Join(projectPath, rel)
		info, err := os.Stat(full)
		if err != nil {
			// silently skip non-existent paths
			continue
		}

		if info.IsDir() {
			// watch the directory itself
			if err := w.watcher.Add(full); err != nil {
				log.Printf("[fsevent] could not watch dir %s: %v", full, err)
				continue
			}
			w.watched[full] = projectPath
			log.Printf("[fsevent] watching dir: %s", full)
		} else {
			// watch the parent dir for file events (fsnotify is dir-level)
			dir := filepath.Dir(full)
			if err := w.watcher.Add(dir); err != nil {
				log.Printf("[fsevent] could not watch dir %s: %v", dir, err)
				continue
			}
			w.watched[full] = projectPath
			log.Printf("[fsevent] watching file: %s", full)
		}
	}

	return nil
}

// RemoveProject stops watching a project directory.
func (w *Watcher) RemoveProject(projectPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.projects[projectPath] {
		return
	}
	delete(w.projects, projectPath)

	for path, proj := range w.watched {
		if proj == projectPath {
			_ = w.watcher.Remove(path)
			delete(w.watched, path)
		}
	}
}

// Start begins watching. Blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	defer w.watcher.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleFsEvent(ev)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[fsevent] watcher error: %v", err)
		}
	}
}

// Events returns the read-only event channel.
func (w *Watcher) Events() <-chan FileEvent {
	return w.events
}

func (w *Watcher) handleFsEvent(ev fsnotify.Event) {
	filePath := ev.Name

	// Only care about write/create/remove ops
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}

	// Find the project for this file
	projectPath := w.findProject(filePath)
	if projectPath == "" {
		return
	}

	// Only emit events for files we actually care about
	if !w.isWatchedFile(filePath, projectPath) {
		return
	}

	deleted := ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0

	// Debounce: cancel existing timer for this file, reset to 300ms
	w.mu.Lock()
	if t, ok := w.timers[filePath]; ok {
		t.Stop()
	}
	w.timers[filePath] = time.AfterFunc(300*time.Millisecond, func() {
		w.mu.Lock()
		delete(w.timers, filePath)
		w.mu.Unlock()
		w.emitEvent(projectPath, filePath, deleted)
	})
	w.mu.Unlock()
}

func (w *Watcher) emitEvent(projectPath, filePath string, deleted bool) {
	relPath, err := filepath.Rel(projectPath, filePath)
	if err != nil {
		relPath = filePath
	}

	fe := FileEvent{
		ProjectPath: projectPath,
		FilePath:    filePath,
		RelPath:     relPath,
		Deleted:     deleted,
		Timestamp:   time.Now(),
	}

	if !deleted {
		content, err := os.ReadFile(filePath)
		if err == nil {
			fe.Content = string(content)
		} else if os.IsNotExist(err) {
			fe.Deleted = true
		}
	}

	select {
	case w.events <- fe:
	default:
		log.Printf("[fsevent] event channel full, dropping event for %s", filePath)
	}
}

// findProject returns the project path for a given file path.
func (w *Watcher) findProject(filePath string) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Direct match (file itself was registered)
	if proj, ok := w.watched[filePath]; ok {
		return proj
	}

	// Check if it's a file inside a watched directory
	dir := filepath.Dir(filePath)
	if proj, ok := w.watched[dir]; ok {
		return proj
	}

	// Check all registered projects — file may be inside a watched dir
	for watchedPath, proj := range w.watched {
		info, err := os.Stat(watchedPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			// Check if filePath is under this dir
			rel, err := filepath.Rel(watchedPath, filePath)
			if err == nil && rel != ".." && len(rel) > 0 && rel[0] != '.' {
				return proj
			}
		}
	}

	return ""
}

// isWatchedFile returns true if the file path matches one of our watched patterns.
func (w *Watcher) isWatchedFile(filePath, projectPath string) bool {
	relPath, err := filepath.Rel(projectPath, filePath)
	if err != nil {
		return false
	}

	for _, watched := range WatchedPaths {
		// Exact file match
		if relPath == watched {
			return true
		}
		// File inside a watched directory
		dir := filepath.Dir(relPath)
		if dir == watched {
			return true
		}
	}
	return false
}
