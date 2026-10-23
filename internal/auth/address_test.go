package auth

import "testing"

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
