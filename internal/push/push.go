package push

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/token"
	"github.com/richardyc/opencapy/internal/config"
)

var ansiRe = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-9;:]*[A-Za-z]|\][^\x07]*(?:\x07|$))`)

// stripANSI removes ANSI/VT100 escape sequences from s.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

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
// Prefers config.json values; falls back to credentials embedded at build time
// (build with -tags release and a credentials_release.go file).
func (r *Registry) InitAPNs(cfg config.APNsConfig) error {
	// Resolve key source: file path > embedded constant
	keyID, teamID := cfg.KeyID, cfg.TeamID
	if (keyID == "" || teamID == "") && embeddedKeyID != "" {
		keyID, teamID = embeddedKeyID, embeddedTeamID
		cfg.Production = true
		log.Println("[push] using embedded APNs credentials")
	}
	if (cfg.KeyPath == "" && embeddedKeyP8 == "") || keyID == "" || teamID == "" {
		log.Println("[push] APNs config incomplete — running in stub mode")
		return nil
	}

	var (
		ecKey *ecdsa.PrivateKey
		err   error
	)
	if cfg.KeyPath != "" {
		ecKey, err = token.AuthKeyFromFile(cfg.KeyPath)
	} else {
		ecKey, err = token.AuthKeyFromBytes([]byte(embeddedKeyP8))
	}
	if err != nil {
		log.Printf("[push] APNs key load failed (%v) — running in stub mode", err)
		return nil
	}

	t := &token.Token{
		AuthKey: ecKey,
		KeyID:   keyID,
		TeamID:  teamID,
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
				Body:  fmt.Sprintf("[%s] %s", session, stripANSI(detail)),
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

// LiveActivityContentState mirrors OpenCapyActivityAttributes.ContentState in Swift.
// Field names must match the Swift struct's JSON encoding (camelCase, no transformation).
type LiveActivityContentState struct {
	SessionName      string  `json:"sessionName"`
	MachineName      string  `json:"machineName"`
	WorkingDirectory string  `json:"workingDirectory"`
	Status           string  `json:"status"`
	LastOutput       string  `json:"lastOutput"`
	NeedsApproval    bool    `json:"needsApproval"`
	ApprovalContent  *string `json:"approvalContent,omitempty"`
}

// liveActivityAps is the aps dict for ActivityKit push notifications.
type liveActivityAps struct {
	Timestamp    int64                     `json:"timestamp"`
	Event        string                    `json:"event"`
	ContentState LiveActivityContentState   `json:"content-state"`
	Alert        *AlertPayload             `json:"alert,omitempty"`
}

type liveActivityPayload struct {
	Aps liveActivityAps `json:"aps"`
}

// SendLiveActivity sends an ActivityKit push to update a specific Live Activity
// identified by its per-activity push token (from iOS activity.pushTokenUpdates).
// This works even when the iOS app is backgrounded or the screen is locked.
func (r *Registry) SendLiveActivity(activityToken string, state LiveActivityContentState) {
	r.mu.RLock()
	client := r.apnsClient
	bundleID := r.bundleID
	r.mu.RUnlock()

	aps := liveActivityAps{
		Timestamp:    time.Now().Unix(),
		Event:        "update",
		ContentState: state,
	}
	if state.NeedsApproval {
		aps.Alert = &AlertPayload{
			Title: "Approval needed",
			Body:  fmt.Sprintf("[%s] Claude Code needs your input", state.SessionName),
		}
	}

	data, err := json.Marshal(liveActivityPayload{Aps: aps})
	if err != nil {
		log.Printf("[push] marshal live activity payload: %v", err)
		return
	}

	if client == nil {
		log.Printf("[push stub] would send live activity update for session %s: %s", state.SessionName, state.Status)
		return
	}

	n := &apns2.Notification{
		DeviceToken: activityToken,
		Topic:       bundleID + ".push-type.liveactivity",
		Payload:     data,
		PushType:    apns2.PushTypeLiveActivity,
	}
	res, err := client.Push(n)
	if err != nil {
		log.Printf("[push] Live Activity APNs send: %v", err)
	} else if res.StatusCode != 200 {
		log.Printf("[push] Live Activity APNs non-200: %d %s", res.StatusCode, res.Reason)
	} else {
		log.Printf("[push] Live Activity update delivered for %s (status=%s)", state.SessionName, state.Status)
	}
}
