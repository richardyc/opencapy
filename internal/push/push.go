package push

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// DeviceToken represents a registered iOS device for push notifications.
type DeviceToken struct {
	Token    string `json:"token"`
	ClientID string `json:"client_id"`
}

// Registry holds registered device tokens, persisted to ~/.opencapy/devices.json
type Registry struct {
	mu      sync.RWMutex
	devices map[string]DeviceToken // token -> DeviceToken
	path    string
}

// Payload is the APNs push notification payload.
type Payload struct {
	Aps     ApsPayload `json:"aps"`
	Session string     `json:"session,omitempty"`
	Event   string     `json:"event,omitempty"`
}

// ApsPayload is the APNs-specific payload.
type ApsPayload struct {
	Alert            AlertPayload `json:"alert"`
	Sound            string       `json:"sound,omitempty"`
	Badge            int          `json:"badge,omitempty"`
	ContentAvailable int          `json:"content-available,omitempty"`
	Category         string       `json:"category,omitempty"`
}

// AlertPayload holds the notification title and body.
type AlertPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Load reads the device registry from disk. Creates an empty registry if missing.
func Load(configDir string) (*Registry, error) {
	path := filepath.Join(configDir, "devices.json")
	r := &Registry{
		devices: make(map[string]DeviceToken),
		path:    path,
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// No existing file — start fresh.
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read devices file: %w", err)
	}

	var list []DeviceToken
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse devices file: %w", err)
	}
	for _, dt := range list {
		r.devices[dt.Token] = dt
	}
	return r, nil
}

// Register adds or updates a device token and persists the registry.
func (r *Registry) Register(token, clientID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.devices[token] = DeviceToken{Token: token, ClientID: clientID}
	return r.save()
}

// Unregister removes a device token and persists the registry.
func (r *Registry) Unregister(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.devices, token)
	_ = r.save()
}

// save writes the registry to disk. Caller must hold mu (write lock).
func (r *Registry) save() error {
	list := make([]DeviceToken, 0, len(r.devices))
	for _, dt := range r.devices {
		list = append(list, dt)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal devices: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(r.path, data, 0o600)
}

// Send logs the push payload (stub — real APNs delivery wired in v0.6).
func (r *Registry) Send(payload Payload) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	data, _ := json.MarshalIndent(payload, "", "  ")
	log.Printf("[push stub] would send to %d devices:\n%s", len(r.devices), string(data))
}

// Devices returns all registered device tokens.
func (r *Registry) Devices() []DeviceToken {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]DeviceToken, 0, len(r.devices))
	for _, dt := range r.devices {
		list = append(list, dt)
	}
	return list
}

// Count returns number of registered devices.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices)
}

// ApprovalPayload builds an approval push payload.
func ApprovalPayload(session string) Payload {
	return Payload{
		Aps: ApsPayload{
			Alert: AlertPayload{
				Title: "Approval needed",
				Body:  fmt.Sprintf("[%s] Claude Code needs your input", session),
			},
			Sound:    "default",
			Category: "APPROVAL",
		},
		Session: session,
		Event:   "approval",
	}
}

// CrashPayload builds a crash push payload.
func CrashPayload(session, detail string) Payload {
	return Payload{
		Aps: ApsPayload{
			Alert: AlertPayload{
				Title: "Session crashed",
				Body:  fmt.Sprintf("[%s] %s", session, detail),
			},
			Sound: "default",
		},
		Session: session,
		Event:   "crash",
	}
}

// DonePayload builds a task complete push payload.
func DonePayload(session string) Payload {
	return Payload{
		Aps: ApsPayload{
			Alert: AlertPayload{
				Title: "Task complete",
				Body:  fmt.Sprintf("[%s] Claude Code finished the task", session),
			},
			Sound: "default",
		},
		Session: session,
		Event:   "done",
	}
}
