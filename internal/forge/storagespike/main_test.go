package storagespike

import (
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	HookMain() // git starts this binary as its hooks and as the receive-pack wrapper
	os.Exit(m.Run())
}
