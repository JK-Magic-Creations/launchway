package auth

import "testing"

func TestClaimsNameAndEmail(t *testing.T) {
	c := NewOIDC(OIDCOptions{})
	for _, tc := range []struct {
		claims      map[string]any
		name, email string
	}{
		{map[string]any{"preferred_username": "dana", "name": " Dana Smith ", "email": "dana@example.com"}, "Dana Smith", "dana@example.com"},
		{map[string]any{"preferred_username": "dana", "email": "dana@example.com", "email_verified": false}, "", ""},
		{map[string]any{"preferred_username": "dana", "email": "Dana <dana@example.com>"}, "", ""},
		{map[string]any{"preferred_username": "dana", "email": "not-an-address", "name": "bad\nname"}, "", ""},
		{map[string]any{"preferred_username": "dana", "email": "a@b@c"}, "", ""},
	} {
		got, err := c.claims("sub", tc.claims)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != tc.name || got.Email != tc.email {
			t.Errorf("%v: name %q email %q, want %q %q", tc.claims, got.Name, got.Email, tc.name, tc.email)
		}
	}
}
