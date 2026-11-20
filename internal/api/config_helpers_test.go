package api

import (
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// bootForTests is the bootstrap file of the test servers: sign-in, and the
// admins group as the first administrators.
const bootForTests = `
egress:
  allow_local: true
auth:
  mode: oidc
  oidc:
    issuer: https://sso.example.test/
    client_id: launchway
    client_secret_env: LAUNCHWAY_TEST_UNUSED
    redirect_url: https://launchway.example.test/auth/callback
  admins:
    groups: [admins]
`

// testSettings is the configuration store of a test server.
func testSettings(t testing.TB, eng *engine.Engine) *settings.Service {
	t.Helper()
	boot, _, err := config.ParseSections([]byte(bootForTests))
	if err != nil {
		t.Fatal(err)
	}
	return settings.New(eng, boot, quiet())
}
