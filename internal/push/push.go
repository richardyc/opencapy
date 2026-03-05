package push

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"github.com/richardyc/opencapy/internal/config"
)

// DeviceToken represents a registered iOS device for push notifications.
type DeviceToken struct {
	Token    string `json:"token"`
	ClientID string `json:"client_id"`
}

// Registry holds registered device tokens, persisted to ~/.opencapy/devices.json
type Registry struct {
	mu         sync.RWMutex
	devices    map[string]DeviceToken // token -> DeviceToken
	path       string
	apnsClient *apns2.Client // nil if APNs not configured
	bundleID   string
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

// InitAPNs loads the .p8 key and initialises the APNs client.
// If the key file is missing or config is empty, it logs a note and falls back to stub.
func (r *Registry) InitAPNs(cfg config.APNsConfig) error {
	if cfg.KeyPath == "" || cfg.KeyID == "" || cfg.TeamID == "" {
		log.Println("[push] APNs config incomplete — running in stub mode")
		return nil
	}

	authKey, err := token.AuthKeyFromFile(cfg.KeyPath)
	if err != nil {
		log.Printf("[push] APNs key load failed (%v) — running in stub mode", err)
		return nil // graceful fallback, not an error
	}

	t := &token.Token{
		AuthKey: authKey,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
	}

	client := apns2.NewTokenClient(t)
	if cfg.Production {
		client = client.Production()
	} else {
		client = client.Development()
	}

	bundleID := cfg.BundleID
	if bundleID == "" {
		bundleID = "dev.opencapy.app"
	}

	r.mu.Lock()
	r.apnsClient = client
	r.bundleID = bundleID
	r.mu.Unlock()

	log.Printf("[push] APNs initialised (bundleID=%s, production=%v)", bundleID, cfg.Production)
	return nil
}

// Send delivers push notifications. Uses real APNs when client is initialised and
// devices are registered; falls back to stub logging otherwise.
func (r *Registry) Send(payload Payload) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.apnsClient != nil && len(r.devices) > 0 {
		data, err := json.Marshal(payload)
		if err != nil {
			log.Printf("[push] marshal payload: %v", err)
			return
		}
		for _, dt := range r.devices {
			n := &apns2.Notification{
				DeviceToken: dt.Token,
				Topic:       r.bundleID,
				Payload:     data,
			}
			res, err := r.apnsClient.Push(n)
			if err != nil {
				log.Printf("[push] APNs send to %s: %v", dt.Token, err)
			} else if res.StatusCode != 200 {
				log.Printf("[push] APNs non-200 for %s: %d %s", dt.Token, res.StatusCode, res.Reason)
			} else {
				log.Printf("[push] APNs delivered to %s (apns-id=%s)", dt.Token, res.ApnsID)
			}
		}
		return
	}

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
