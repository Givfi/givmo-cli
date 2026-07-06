// Package config manages the CLI's per-profile configuration: which Givmo
// environment (sandbox vs production) commands talk to, and the API + auth-
// server base URLs for each. Configuration resolves from, in priority order:
//
//  1. environment variables (GIVMO_PROFILE, GIVMO_API_BASE, GIVMO_AUTH_BASE)
//  2. the on-disk config file (~/.givmo/config.json)
//  3. built-in defaults per profile
//
// The API/auth base URLs are configurable so the CLI is "ready-inert": the
// production endpoints (mcp.givmo.io / api.givmo.io) are not all live yet, and
// pointing a profile at a dev host must not require a recompile.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile names.
const (
	ProfileSandbox    = "sandbox"
	ProfileProduction = "production"
)

// Endpoints holds the resolved base URLs a command talks to.
type Endpoints struct {
	// APIBase is the REST + MCP host base (e.g. https://mcp.givmo.io).
	APIBase string `json:"api_base"`
	// AuthBase is the Givmo Connect authorization-server base (RFC 8414),
	// e.g. https://api.givmo.io.
	AuthBase string `json:"auth_base"`
}

// Profile is one named environment configuration.
type Profile struct {
	Name      string    `json:"name"`
	Endpoints Endpoints `json:"endpoints"`
}

// Config is the on-disk configuration document.
type Config struct {
	// ActiveProfile is the currently selected profile name.
	ActiveProfile string `json:"active_profile"`
	// Profiles maps profile name -> profile. Missing/empty profiles fall back
	// to built-in defaults.
	Profiles map[string]Profile `json:"profiles,omitempty"`
}

// defaultEndpoints returns the built-in base URLs for a known profile. The
// production defaults point at the real hosts; the sandbox defaults point at a
// dev host placeholder so nothing hard-breaks before the endpoints light up.
func defaultEndpoints(profile string) Endpoints {
	switch profile {
	case ProfileProduction:
		return Endpoints{
			APIBase:  "https://mcp.givmo.io",
			AuthBase: "https://api.givmo.io",
		}
	case ProfileSandbox:
		return Endpoints{
			// Sandbox / dev host placeholder — overridable per-profile.
			APIBase:  "https://mcp-dev.givmo.io",
			AuthBase: "https://api-dev.givmo.io",
		}
	default:
		return Endpoints{}
	}
}

// KnownProfiles lists the profile names the CLI understands.
func KnownProfiles() []string { return []string{ProfileSandbox, ProfileProduction} }

// IsKnownProfile reports whether name is a recognized profile.
func IsKnownProfile(name string) bool {
	for _, p := range KnownProfiles() {
		if p == name {
			return true
		}
	}
	return false
}

// Dir returns the CLI's config/state directory (~/.givmo), honoring
// GIVMO_HOME for tests and overrides. It does NOT create the directory.
func Dir() (string, error) {
	if h := os.Getenv("GIVMO_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".givmo"), nil
}

// Path returns the config file path (~/.givmo/config.json).
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file if present. A missing file is not an error — it
// returns a zeroed Config that resolves entirely from defaults.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config file %s is corrupt: %w", p, err)
	}
	return &c, nil
}

// Save writes the config file with 0600 perms (0700 dir). Config carries no
// secrets, but we keep the directory private since tokens live alongside it.
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, "config.json")
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Resolve returns the effective active profile with endpoints, applying the
// precedence rules (env > file > defaults). The profileOverride argument (from
// a global --profile flag) takes highest precedence for the profile NAME when
// non-empty; endpoint env overrides still apply on top.
func (c *Config) Resolve(profileOverride string) (Profile, error) {
	name := c.resolveName(profileOverride)
	if !IsKnownProfile(name) {
		return Profile{}, fmt.Errorf("unknown profile %q (known: %s)", name, strings.Join(KnownProfiles(), ", "))
	}

	// Start from defaults, layer the file's stored profile, then env.
	ep := defaultEndpoints(name)
	if c.Profiles != nil {
		if stored, ok := c.Profiles[name]; ok {
			if stored.Endpoints.APIBase != "" {
				ep.APIBase = stored.Endpoints.APIBase
			}
			if stored.Endpoints.AuthBase != "" {
				ep.AuthBase = stored.Endpoints.AuthBase
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("GIVMO_API_BASE")); v != "" {
		ep.APIBase = v
	}
	if v := strings.TrimSpace(os.Getenv("GIVMO_AUTH_BASE")); v != "" {
		ep.AuthBase = v
	}
	return Profile{Name: name, Endpoints: ep}, nil
}

// resolveName picks the active profile name by precedence.
func (c *Config) resolveName(override string) string {
	if override != "" {
		return override
	}
	if v := strings.TrimSpace(os.Getenv("GIVMO_PROFILE")); v != "" {
		return v
	}
	if c.ActiveProfile != "" {
		return c.ActiveProfile
	}
	return ProfileProduction
}

// SetActiveProfile records the active profile and persists defaults for it if
// the profile is not yet present in the file.
func (c *Config) SetActiveProfile(name string) error {
	if !IsKnownProfile(name) {
		return fmt.Errorf("unknown profile %q (known: %s)", name, strings.Join(KnownProfiles(), ", "))
	}
	c.ActiveProfile = name
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if _, ok := c.Profiles[name]; !ok {
		c.Profiles[name] = Profile{Name: name, Endpoints: defaultEndpoints(name)}
	}
	return nil
}

// IsProduction reports whether the resolved profile is the production
// environment (used to gate the loud banner before mutating operations).
func (p Profile) IsProduction() bool { return p.Name == ProfileProduction }
