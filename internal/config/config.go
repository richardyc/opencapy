package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds daemon configuration.
type Config struct {
	Port         int    `json:"port"`          // default 7242
	PollInterval int    `json:"poll_interval"` // ms, default 500
	LogPath      string `json:"log_path"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".opencapy", "config.json")
}

// Load reads ~/.opencapy/config.json, creating a default if missing.
// Environment variable OPENCAPY_PORT overrides the port.
func Load() (*Config, error) {
	cfg := &Config{
		Port:         7242,
		PollInterval: 500,
		LogPath:      "/tmp/opencapy.log",
	}

	p := configPath()
	data, err := os.ReadFile(p)
	if err == nil {
		_ = json.Unmarshal(data, cfg)
	}

	// Env override
	if envPort := os.Getenv("OPENCAPY_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			cfg.Port = p
		}
	}

	return cfg, nil
}

// Save writes the config to disk.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(configPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	return os.WriteFile(configPath(), data, 0o644)
}
