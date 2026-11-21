package plain_test

import (
	"os"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// No Git client a test starts may prompt a person (see testlib.NoPrompt).
func TestMain(m *testing.M) {
	testlib.NoPrompt()
	os.Exit(m.Run())
}
