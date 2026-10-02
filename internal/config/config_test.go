package config

import (
	"testing"
)

func TestResolve_DefaultsPerProfile(t *testing.T) {
	// No env, no file, no override -> production defaults.
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "")
	t.Setenv("GIVMO_AUTH_BASE", "")

	c := &Config{}
	prof, err := c.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if prof.Name != ProfileProduction {
		t.Errorf("default profile = %q, want production", prof.Name)
	}
	if prof.Endpoints.APIBase != "https://mcp.givmo.io" {
		t.Errorf("prod api_base = %q", prof.Endpoints.APIBase)
	}
	// The authorization server's issuer is the MCP host.
	if prof.Endpoints.AuthBase != "https://mcp.givmo.io" {
		t.Errorf("prod auth_base = %q, want the issuer https://mcp.givmo.io", prof.Endpoints.AuthBase)
	}
}

func TestResolve_StoredRetiredDefaultYieldsToTheIssuer(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "")
	t.Setenv("GIVMO_AUTH_BASE", "")

	// What `config use-profile production` wrote before the default changed.
	c := &Config{
		ActiveProfile: ProfileProduction,
		Profiles: map[string]Profile{
			ProfileProduction: {Name: ProfileProduction, Endpoints: Endpoints{
				APIBase:  "https://mcp.givmo.io",
				AuthBase: "https://api.givmo.io",
			}},
		},
	}
	prof, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Endpoints.AuthBase != "https://mcp.givmo.io" {
		t.Errorf("a stored retired default must resolve to the issuer, got %q", prof.Endpoints.AuthBase)
	}

	// Any other stored value is the user's choice and stands.
	c.Profiles[ProfileProduction] = Profile{Name: ProfileProduction, Endpoints: Endpoints{AuthBase: "https://auth.example.test"}}
	prof, err = c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Endpoints.AuthBase != "https://auth.example.test" {
		t.Errorf("a stored custom auth_base must stand, got %q", prof.Endpoints.AuthBase)
	}

	// The environment still wins over everything.
	t.Setenv("GIVMO_AUTH_BASE", "https://api.givmo.io")
	prof, err = c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Endpoints.AuthBase != "https://api.givmo.io" {
		t.Errorf("GIVMO_AUTH_BASE must override, got %q", prof.Endpoints.AuthBase)
	}
}

func TestResolve_SandboxDefaults(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "")
	t.Setenv("GIVMO_AUTH_BASE", "")

	c := &Config{ActiveProfile: ProfileSandbox}
	prof, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Name != ProfileSandbox {
		t.Errorf("profile = %q, want sandbox", prof.Name)
	}
	if prof.Endpoints.APIBase == "" || prof.Endpoints.AuthBase == "" {
		t.Errorf("sandbox endpoints must have defaults, got %+v", prof.Endpoints)
	}
	if prof.IsProduction() {
		t.Error("sandbox must not be production")
	}
}

func TestResolve_PrecedenceOrder(t *testing.T) {
	// Override flag beats env beats file beats default for the NAME.
	t.Setenv("GIVMO_PROFILE", "production")
	c := &Config{ActiveProfile: ProfileSandbox}

	// --profile override wins over the env var.
	prof, err := c.Resolve(ProfileSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if prof.Name != ProfileSandbox {
		t.Errorf("override should win: got %q", prof.Name)
	}

	// Without override, env wins over file.
	prof2, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof2.Name != ProfileProduction {
		t.Errorf("env should win over file: got %q", prof2.Name)
	}
}

func TestResolve_EnvEndpointOverrides(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "https://api.example.test")
	t.Setenv("GIVMO_AUTH_BASE", "https://auth.example.test")

	c := &Config{ActiveProfile: ProfileProduction}
	prof, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Endpoints.APIBase != "https://api.example.test" {
		t.Errorf("env api_base override not applied: %q", prof.Endpoints.APIBase)
	}
	if prof.Endpoints.AuthBase != "https://auth.example.test" {
		t.Errorf("env auth_base override not applied: %q", prof.Endpoints.AuthBase)
	}
}

func TestResolve_StoredProfileEndpointsLayered(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	t.Setenv("GIVMO_API_BASE", "")
	t.Setenv("GIVMO_AUTH_BASE", "")

	c := &Config{
		ActiveProfile: ProfileSandbox,
		Profiles: map[string]Profile{
			ProfileSandbox: {Name: ProfileSandbox, Endpoints: Endpoints{APIBase: "https://custom-sandbox.test"}},
		},
	}
	prof, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Endpoints.APIBase != "https://custom-sandbox.test" {
		t.Errorf("stored api_base not applied: %q", prof.Endpoints.APIBase)
	}
	// auth_base falls back to the sandbox default (stored had none).
	if prof.Endpoints.AuthBase == "" {
		t.Error("auth_base should fall back to default when stored profile omits it")
	}
}

func TestResolve_UnknownProfileErrors(t *testing.T) {
	t.Setenv("GIVMO_PROFILE", "")
	c := &Config{}
	if _, err := c.Resolve("staging"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestSetActiveProfile(t *testing.T) {
	c := &Config{}
	if err := c.SetActiveProfile(ProfileSandbox); err != nil {
		t.Fatal(err)
	}
	if c.ActiveProfile != ProfileSandbox {
		t.Errorf("active = %q", c.ActiveProfile)
	}
	if _, ok := c.Profiles[ProfileSandbox]; !ok {
		t.Error("profile should be materialized with defaults")
	}
	if err := c.SetActiveProfile("nope"); err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIVMO_HOME", dir)

	c := &Config{}
	if err := c.SetActiveProfile(ProfileSandbox); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ActiveProfile != ProfileSandbox {
		t.Errorf("round-trip active = %q", loaded.ActiveProfile)
	}
}

func TestLoad_MissingFileIsNotError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIVMO_HOME", dir)
	c, err := Load()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c.ActiveProfile != "" {
		t.Errorf("expected zeroed config, got %+v", c)
	}
}
