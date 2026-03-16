package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionInfo holds persistent metadata for a session.
type SessionInfo struct {
	ProjectPath     string     `json:"project_path"`
	ClaudeSessionID string     `json:"claude_session_id,omitempty"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
}

// Registry maps session names to session metadata, persisted to disk.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]SessionInfo
	path     string
}

func registryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".opencapy", "sessions.json")
}

// Load reads the registry from ~/.opencapy/sessions.json.
func Load() (*Registry, error) {
	p := registryPath()

	r := &Registry{
		sessions: make(map[string]SessionInfo),
		path:     p,
	}

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}

	// Tolerate empty or corrupted files — start with a fresh registry.
	if len(data) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(data, &r.sessions); err != nil {
		return r, nil
	}
	return r, nil
}

// Register adds or updates a session's project path.
func (r *Registry) Register(sessionName, projectPath string) error {
	r.mu.Lock()
	info := r.sessions[sessionName]
	info.ProjectPath = projectPath
	r.sessions[sessionName] = info
	r.mu.Unlock()
	return r.Save()
}

// SetClaudeSessionID binds a Claude Code session ID to a session.
func (r *Registry) SetClaudeSessionID(sessionName, claudeSessionID string) error {
	r.mu.Lock()
	info := r.sessions[sessionName]
	info.ClaudeSessionID = claudeSessionID
	r.sessions[sessionName] = info
	r.mu.Unlock()
	return r.Save()
}

// SetCreatedAt persists the creation time for a session (only if not already set).
func (r *Registry) SetCreatedAt(sessionName string, t time.Time) {
	r.mu.Lock()
	info := r.sessions[sessionName]
	if info.CreatedAt == nil {
		info.CreatedAt = &t
		r.sessions[sessionName] = info
	}
	r.mu.Unlock()
}

// GetCreatedAt returns the persisted creation time for a session.
func (r *Registry) GetCreatedAt(sessionName string) (time.Time, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.sessions[sessionName]
	if !ok || info.CreatedAt == nil {
		return time.Time{}, false
	}
	return *info.CreatedAt, true
}

// Unregister removes a session from the registry.
func (r *Registry) Unregister(sessionName string) error {
	r.mu.Lock()
	delete(r.sessions, sessionName)
	r.mu.Unlock()
	return r.Save()
}

// GetProject returns the project path for a session.
func (r *Registry) GetProject(sessionName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.sessions[sessionName]
	return info.ProjectPath, ok
}

// GetClaudeSessionID returns the Claude Code session ID for a session.
func (r *Registry) GetClaudeSessionID(sessionName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.sessions[sessionName]
	if !ok || info.ClaudeSessionID == "" {
		return "", false
	}
	return info.ClaudeSessionID, true
}

// All returns a copy of all session->project mappings.
func (r *Registry) All() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.sessions))
	for k, v := range r.sessions {
		out[k] = v.ProjectPath
	}
	return out
}

// AllProjects returns a deduplicated list of all project paths in the registry.
func (r *Registry) AllProjects() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, info := range r.sessions {
		if !seen[info.ProjectPath] {
			seen[info.ProjectPath] = true
			out = append(out, info.ProjectPath)
		}
	}
	return out
}

// Save writes the registry to disk atomically (write tmp + rename)
// so a kill during write never corrupts the file.
func (r *Registry) Save() error {
	r.mu.RLock()
	data, err := json.MarshalIndent(r.sessions, "", "  ")
	r.mu.RUnlock()
	if err != nil {
		return err
	}

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
