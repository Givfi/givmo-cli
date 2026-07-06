package mcpbridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ServerName is the key under which the Givmo MCP server is registered in a
// client's config file.
const ServerName = "givmo"

// ClientKind enumerates supported MCP clients.
type ClientKind string

const (
	ClientClaudeCode ClientKind = "claude-code"
	ClientCursor     ClientKind = "cursor"
)

// ServerSpec is the config block written for the Givmo MCP server. It launches
// the CLI's own stdio bridge, so the client inherits the CLI's authenticated
// session. Fields match the common `mcpServers` schema used by both clients.
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// DefaultServerSpec returns the spec that runs `givmo mcp serve`. cliPath is the
// absolute path to the givmo binary; profile pins the environment.
func DefaultServerSpec(cliPath, profile string) ServerSpec {
	args := []string{"mcp", "serve"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	return ServerSpec{Command: cliPath, Args: args}
}

// InstallResult reports what an install did.
type InstallResult struct {
	Client     ClientKind `json:"client"`
	ConfigPath string     `json:"config_path"`
	BackupPath string     `json:"backup_path,omitempty"`
	Created    bool       `json:"created"`
	Updated    bool       `json:"updated"`
	ServerName string     `json:"server_name"`
}

// ConfigPathFor returns the default config file path for a client, honoring
// GIVMO_HOME for testability (both clients keep JSON config under the user's
// home; we approximate their conventional locations).
func ConfigPathFor(kind ClientKind, home string) (string, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	switch kind {
	case ClientClaudeCode:
		// Claude Code reads ~/.claude.json (mcpServers live at the top level).
		return filepath.Join(home, ".claude.json"), nil
	case ClientCursor:
		// Cursor reads ~/.cursor/mcp.json.
		return filepath.Join(home, ".cursor", "mcp.json"), nil
	default:
		return "", fmt.Errorf("unsupported client %q (use claude-code or cursor)", kind)
	}
}

// InstallServer writes/updates the Givmo MCP server entry in the client's
// config at configPath. It is IDEMPOTENT: an identical existing entry is a
// no-op; a differing entry is overwritten after backing up the file. All other
// keys/servers in the file are preserved.
func InstallServer(configPath string, spec ServerSpec) (*InstallResult, error) {
	res := &InstallResult{ConfigPath: configPath, ServerName: ServerName}

	root := map[string]json.RawMessage{}
	existing, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(existing) > 0 {
			if uerr := json.Unmarshal(existing, &root); uerr != nil {
				return nil, fmt.Errorf("existing config %s is not valid JSON: %w", configPath, uerr)
			}
		}
	case os.IsNotExist(err):
		res.Created = true
	default:
		return nil, err
	}

	// Read the current mcpServers object (create if absent).
	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok && len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &servers); uerr != nil {
			return nil, fmt.Errorf("existing mcpServers is not an object: %w", uerr)
		}
	}

	newEntry, _ := json.Marshal(spec)

	// Idempotency: if the existing entry is byte-equal (after normalization),
	// do nothing.
	if cur, ok := servers[ServerName]; ok {
		if jsonEqual(cur, newEntry) {
			res.Updated = false
			return res, nil
		}
		res.Updated = true
	}

	// Back up before overwriting a pre-existing file.
	if !res.Created {
		backup := fmt.Sprintf("%s.givmo-backup-%s", configPath, time.Now().UTC().Format("20060102T150405Z"))
		if werr := os.WriteFile(backup, existing, 0o600); werr != nil {
			return nil, fmt.Errorf("could not write backup: %w", werr)
		}
		res.BackupPath = backup
	}

	servers[ServerName] = newEntry
	serversRaw, _ := json.Marshal(servers)
	root["mcpServers"] = serversRaw

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	if mkerr := os.MkdirAll(filepath.Dir(configPath), 0o700); mkerr != nil {
		return nil, mkerr
	}
	if werr := os.WriteFile(configPath, append(out, '\n'), 0o600); werr != nil {
		return nil, werr
	}
	return res, nil
}

// jsonEqual reports whether two JSON blobs are semantically equal.
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	an, _ := json.Marshal(av)
	bn, _ := json.Marshal(bv)
	return string(an) == string(bn)
}
