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
	if reg.Count() != 0 {
		t.Errorf("expected 0 devices, got %d", reg.Count())
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
	if reg.Count() != 1 {
		t.Errorf("expected 1 device after register, got %d", reg.Count())
	}

	reg.Unregister("token-abc")
	if reg.Count() != 0 {
		t.Errorf("expected 0 devices after unregister, got %d", reg.Count())
	}
}

func TestApprovalPayload(t *testing.T) {
	p := ApprovalPayload("my-session")
	if p.Session != "my-session" {
		t.Errorf("expected session my-session, got %s", p.Session)
	}
	if p.Aps.Category != "APPROVAL" {
		t.Errorf("expected category APPROVAL, got %s", p.Aps.Category)
	}
}
