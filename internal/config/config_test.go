package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefault(t *testing.T) {
	// Point config to a temp dir
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	defer os.Unsetenv("HOME")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Port != 7242 {
		t.Errorf("expected default port 7242, got %d", cfg.Port)
	}
	if cfg.PollInterval != 500 {
		t.Errorf("expected default poll interval 500, got %d", cfg.PollInterval)
	}
	// Config file should now exist
	configPath := filepath.Join(tmp, ".opencapy", "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("config file was not created")
	}
}
