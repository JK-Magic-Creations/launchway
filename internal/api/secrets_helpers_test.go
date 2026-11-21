package api

import (
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// testRootKey makes a root key holder for a test.
func testRootKey(t *testing.T) keyring.Holder {
	t.Helper()
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// testSecrets starts the sealing service of a test server with a root key,
// as a real server has: stored secrets are sealed, and webhook secrets and
// Git over SSH are allowed.
func testSecrets(t *testing.T, eng *engine.Engine) *sealing.Service {
	t.Helper()
	svc, err := sealing.Start(t.Context(), sealing.Options{DB: eng.Database(), Holder: testRootKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
