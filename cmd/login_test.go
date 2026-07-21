package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/givfi/givmo-cli/internal/auth"
	"github.com/givfi/givmo-cli/internal/output"
)

// failingDiscoverer fails both discovery calls, forcing the conventional-endpoint
// fallback path in discoverAuthServer.
type failingDiscoverer struct{}

func (failingDiscoverer) ProtectedResource(context.Context, string) (*auth.ProtectedResourceMetadata, error) {
	return nil, errors.New("discovery down")
}

func TestLoginCommand_RejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "65536"} {
		t.Run(port, func(t *testing.T) {
			cmd := newLoginCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"--port", port})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected invalid port error")
			}
			if got := output.AsError(err).Code; got != output.ExitValidation {
				t.Errorf("exit code = %d, want %d", got, output.ExitValidation)
			}
			if !strings.Contains(err.Error(), port) {
				t.Errorf("error %q does not name bad port %s", err, port)
			}
		})
	}
}

func (failingDiscoverer) AuthServer(context.Context, string) (*auth.AuthServerMetadata, error) {
	return nil, errors.New("discovery down")
}

func TestFallbackAuthServerMetadata_ConnectPaths(t *testing.T) {
	md := fallbackAuthServerMetadata("https://api-dev.givmo.io/")
	if md.AuthorizationEndpoint != "https://api-dev.givmo.io/connect/oauth/authorize" {
		t.Errorf("authorize endpoint = %q", md.AuthorizationEndpoint)
	}
	if md.TokenEndpoint != "https://api-dev.givmo.io/connect/oauth/token" {
		t.Errorf("token endpoint = %q", md.TokenEndpoint)
	}
	if md.Issuer != "https://api-dev.givmo.io" {
		t.Errorf("issuer = %q", md.Issuer)
	}
}

func TestDiscoverAuthServer_FallsBackToConnectPaths(t *testing.T) {
	md, err := discoverAuthServer(context.Background(), failingDiscoverer{},
		"https://mcp-dev.givmo.io", "https://api-dev.givmo.io", true)
	if err != nil {
		t.Fatalf("discoverAuthServer: %v", err)
	}
	if md.AuthorizationEndpoint != "https://api-dev.givmo.io/connect/oauth/authorize" {
		t.Errorf("fallback authorize = %q", md.AuthorizationEndpoint)
	}
	if md.TokenEndpoint != "https://api-dev.givmo.io/connect/oauth/token" {
		t.Errorf("fallback token = %q", md.TokenEndpoint)
	}
}

func TestValidateAuthServerURL_SchemeGuard(t *testing.T) {
	cases := []struct {
		name          string
		url           string
		allowLoopback bool
		wantOK        bool
	}{
		{"https always ok", "https://connect.givmo.io/oauth/authorize", false, true},
		{"https ok on sandbox too", "https://connect-dev.givmo.io", true, true},
		{"plain http rejected in prod", "http://connect.givmo.io/oauth/token", false, false},
		{"http loopback 127 allowed off-prod", "http://127.0.0.1:8080/authorize", true, true},
		{"http localhost allowed off-prod", "http://localhost:8080/authorize", true, true},
		{"http ::1 allowed off-prod", "http://[::1]:8080/authorize", true, true},
		{"http loopback REJECTED in prod", "http://127.0.0.1:8080/authorize", false, false},
		{"http non-loopback rejected even off-prod", "http://169.254.169.254/latest", true, false},
		{"http public host rejected even off-prod", "http://evil.example/authorize", true, false},
		{"garbage rejected", "::::not a url", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthServerURL(tc.url, tc.allowLoopback)
			if tc.wantOK && err != nil {
				t.Errorf("expected OK, got error: %v", err)
			}
			if !tc.wantOK {
				if err == nil {
					t.Errorf("expected rejection for %q (allowLoopback=%v)", tc.url, tc.allowLoopback)
				} else if output.AsError(err).Code != output.ExitValidation {
					t.Errorf("rejection should be ExitValidation, got %d", output.AsError(err).Code)
				}
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "[::1]", "127.5.5.5"} {
		if !isLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"169.254.169.254", "evil.example", "8.8.8.8", "givmo.io"} {
		if isLoopbackHost(h) {
			t.Errorf("%q should NOT be loopback", h)
		}
	}
}
