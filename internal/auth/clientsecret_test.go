package auth

import "testing"

func TestResolveClientID_Precedence(t *testing.T) {
	t.Setenv("GIVMO_CLIENT_ID", "gci-test-override")
	if got := ResolveClientID(); got != "gci-test-override" {
		t.Errorf("ResolveClientID env = %q, want %q", got, "gci-test-override")
	}

	t.Setenv("GIVMO_CLIENT_ID", "")
	if got := ResolveClientID(); got != ClientID {
		t.Errorf("ResolveClientID default = %q, want %q", got, ClientID)
	}
}

func TestResolveClientSecret_Precedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIVMO_HOME", dir)
	t.Setenv("GIVMO_TOKEN_BACKEND", "file")

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.SaveClientSecret("sandbox", "test-stored-secret"); err != nil {
		t.Fatalf("SaveClientSecret: %v", err)
	}

	t.Setenv("GIVMO_CLIENT_SECRET", "test-env-secret")
	secret, source, err := ResolveClientSecret(store, "sandbox")
	if err != nil {
		t.Fatalf("ResolveClientSecret env: %v", err)
	}
	if secret != "test-env-secret" || source != "env" {
		t.Errorf("env resolution = (%q, %q), want env value/source", secret, source)
	}

	t.Setenv("GIVMO_CLIENT_SECRET", "")
	secret, source, err = ResolveClientSecret(store, "sandbox")
	if err != nil {
		t.Fatalf("ResolveClientSecret store: %v", err)
	}
	if secret != "test-stored-secret" || source != "store" {
		t.Errorf("store resolution = (%q, %q), want store value/source", secret, source)
	}

	secret, source, err = ResolveClientSecret(store, "production")
	if err != nil {
		t.Fatalf("ResolveClientSecret absent: %v", err)
	}
	if secret != "" || source != "" {
		t.Errorf("absent resolution = (%q, %q), want empty values", secret, source)
	}
}
