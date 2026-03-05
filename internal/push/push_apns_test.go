package push

import (
	"testing"

	"github.com/richardyc/opencapy/internal/config"
)

// TestInitAPNs_MissingFile verifies that InitAPNs gracefully falls back when the
// .p8 key file does not exist (no error returned).
func TestInitAPNs_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	reg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	cfg := config.APNsConfig{
		KeyPath:  "/nonexistent/path/AuthKey_XXXXXXXXXX.p8",
		KeyID:    "XXXXXXXXXX",
		TeamID:   "YYYYYYYYYY",
		BundleID: "dev.opencapy.app",
	}

	if err := reg.InitAPNs(cfg); err != nil {
		t.Errorf("InitAPNs with missing file should not error, got: %v", err)
	}
	if reg.apnsClient != nil {
		t.Errorf("expected apnsClient to be nil after missing-file fallback")
	}
}

// TestInitAPNs_EmptyConfig verifies that InitAPNs gracefully falls back when
// the config is empty (no error returned).
func TestInitAPNs_EmptyConfig(t *testing.T) {
	tmp := t.TempDir()
	reg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if err := reg.InitAPNs(config.APNsConfig{}); err != nil {
		t.Errorf("InitAPNs with empty config should not error, got: %v", err)
	}
	if reg.apnsClient != nil {
		t.Errorf("expected apnsClient to be nil after empty-config fallback")
	}
}
