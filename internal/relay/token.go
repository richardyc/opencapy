// Package relay manages the persistent relay pairing token and relay URL.
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultRelayURL = "wss://relay.opencapy.dev"
	tokenFileName   = "relay_token.json"
)

type tokenFile struct {
	Token string `json:"token"`
}

func tokenPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".opencapy", tokenFileName)
}

// LoadOrCreate reads the relay token from disk, generating a new one if absent.
// The token is a 32-byte (64 hex char) random value — 256 bits of entropy.
func LoadOrCreate() (string, error) {
	data, err := os.ReadFile(tokenPath())
	if err == nil {
		var tf tokenFile
		if json.Unmarshal(data, &tf) == nil && len(tf.Token) == 64 {
			return tf.Token, nil
		}
	}
	return generate()
}

func generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("relay token: %w", err)
	}
	token := hex.EncodeToString(b)

	dir := filepath.Dir(tokenPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	data, _ := json.MarshalIndent(tokenFile{Token: token}, "", "  ")
	if err := os.WriteFile(tokenPath(), data, 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// PairURL returns the deep-link URL that the iOS app scans to configure itself.
func PairURL(token, machineName, relayBaseURL string) string {
	return fmt.Sprintf(
		"opencapy://pair?type=relay&token=%s&relay=%s&name=%s",
		token, relayBaseURL, machineName,
	)
}

// WSURL returns the WebSocket URL for a given role (mac or ios).
func WSURL(token, relayBaseURL, role string) string {
	return fmt.Sprintf("%s/relay/%s?role=%s", relayBaseURL, token, role)
}
