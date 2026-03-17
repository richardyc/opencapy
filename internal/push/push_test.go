package push

import (
	"testing"
)

func TestLoad(t *testing.T) {
	tmp := t.TempDir()
	reg, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(reg.devices) != 0 {
		t.Errorf("expected 0 devices, got %d", len(reg.devices))
	}
}

func TestRegisterUnregister(t *testing.T) {
	tmp := t.TempDir()
	reg, err := Load(tmp)
	if err != nil {
		t.Fatal(err)
	}

	if err := reg.Register("token-abc", "client-1"); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	if len(reg.devices) != 1 {
		t.Errorf("expected 1 device after register, got %d", len(reg.devices))
	}

	reg.Unregister("token-abc")
	if len(reg.devices) != 0 {
		t.Errorf("expected 0 devices after unregister, got %d", len(reg.devices))
	}
}
