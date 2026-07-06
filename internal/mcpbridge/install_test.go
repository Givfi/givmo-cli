package mcpbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readServers(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("config not JSON: %v", err)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(root["mcpServers"], &servers); err != nil {
		t.Fatalf("mcpServers not an object: %v", err)
	}
	return servers
}

func TestInstallServer_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", ".claude.json") // nested dir must be created
	spec := DefaultServerSpec("/usr/local/bin/givmo", "sandbox")

	res, err := InstallServer(path, spec)
	if err != nil {
		t.Fatalf("InstallServer: %v", err)
	}
	if !res.Created {
		t.Error("expected Created=true for a fresh file")
	}
	if res.BackupPath != "" {
		t.Error("no backup should be made when creating a new file")
	}
	servers := readServers(t, path)
	if _, ok := servers[ServerName]; !ok {
		t.Fatalf("givmo server not written; servers=%v", servers)
	}
}

func TestInstallServer_IdempotentNoChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor-mcp.json")
	spec := DefaultServerSpec("/bin/givmo", "production")

	if _, err := InstallServer(path, spec); err != nil {
		t.Fatal(err)
	}
	res, err := InstallServer(path, spec)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.Updated {
		t.Errorf("second identical install should be a no-op: %+v", res)
	}
	if res.BackupPath != "" {
		t.Error("no backup on a no-op")
	}
}

func TestInstallServer_UpdatesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	// Pre-seed with an unrelated server + a different givmo spec.
	seed := map[string]any{
		"someOtherKey": "keepme",
		"mcpServers": map[string]any{
			"other":    map[string]any{"command": "othercmd"},
			ServerName: map[string]any{"command": "OLD", "args": []string{"x"}},
		},
	}
	sb, _ := json.Marshal(seed)
	if err := os.WriteFile(path, sb, 0o600); err != nil {
		t.Fatal(err)
	}

	spec := DefaultServerSpec("/new/givmo", "sandbox")
	res, err := InstallServer(path, spec)
	if err != nil {
		t.Fatalf("InstallServer: %v", err)
	}
	if !res.Updated {
		t.Error("expected Updated=true when the entry differs")
	}
	if res.BackupPath == "" {
		t.Error("a backup must be written before overwriting")
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Errorf("backup file missing: %v", err)
	}

	servers := readServers(t, path)
	// The unrelated server must be preserved.
	if _, ok := servers["other"]; !ok {
		t.Error("unrelated server was clobbered")
	}
	// The unrelated top-level key must be preserved.
	b, _ := os.ReadFile(path)
	var root map[string]json.RawMessage
	json.Unmarshal(b, &root)
	if string(root["someOtherKey"]) != `"keepme"` {
		t.Errorf("top-level key not preserved: %s", root["someOtherKey"])
	}
	// The givmo entry must now reflect the new command.
	var got ServerSpec
	json.Unmarshal(servers[ServerName], &got)
	if got.Command != "/new/givmo" {
		t.Errorf("givmo command = %q, want /new/givmo", got.Command)
	}
}

func TestConfigPathFor(t *testing.T) {
	home := "/home/tester"
	cc, err := ConfigPathFor(ClientClaudeCode, home)
	if err != nil || cc != filepath.Join(home, ".claude.json") {
		t.Errorf("claude-code path = %q (%v)", cc, err)
	}
	cur, err := ConfigPathFor(ClientCursor, home)
	if err != nil || cur != filepath.Join(home, ".cursor", "mcp.json") {
		t.Errorf("cursor path = %q (%v)", cur, err)
	}
	if _, err := ConfigPathFor("emacs", home); err == nil {
		t.Error("expected error for unsupported client")
	}
}
