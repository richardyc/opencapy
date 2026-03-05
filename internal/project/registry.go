package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Registry maps session names to project paths (cwd at creation time).
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]string // session name -> project path
	path     string            // path to registry file
}

func registryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".opencapy", "sessions.json")
}

// Load reads the registry from ~/.opencapy/sessions.json.
// Creates the file with an empty registry if it doesn't exist.
func Load() (*Registry, error) {
	p := registryPath()

	r := &Registry{
		sessions: make(map[string]string),
		path:     p,
	}

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, &r.sessions); err != nil {
		return nil, err
	}
	return r, nil
}

// Register adds a session->project mapping.
func (r *Registry) Register(sessionName, projectPath string) error {
	r.mu.Lock()
	r.sessions[sessionName] = projectPath
	r.mu.Unlock()
	return r.Save()
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
	p, ok := r.sessions[sessionName]
	return p, ok
}

// All returns a copy of all session->project mappings.
func (r *Registry) All() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.sessions))
	for k, v := range r.sessions {
		out[k] = v
	}
	return out
}

// AllProjects returns a deduplicated list of all project paths in the registry.
func (r *Registry) AllProjects() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, path := range r.sessions {
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// Save writes the registry to disk.
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

	return os.WriteFile(r.path, data, 0o644)
}
