package cmd

import "testing"

func TestLooksLikeEIN(t *testing.T) {
	yes := []string{"12-3456789", "123456789"}
	for _, s := range yes {
		if !looksLikeEIN(s) {
			t.Errorf("%q should look like an EIN", s)
		}
	}
	no := []string{
		"ch_abc123",   // opaque charity id
		"clean water", // name query
		"12-345",      // too short
		"1234567890",  // too long
		"12 3456789",  // space, not a dash
		"abcdefghi",   // letters
		"",            // empty
	}
	for _, s := range no {
		if looksLikeEIN(s) {
			t.Errorf("%q should NOT look like an EIN", s)
		}
	}
}
