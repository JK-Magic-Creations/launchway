package storagespike

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	HookMain() // git starts this binary as its hooks and as the receive-pack wrapper
	os.Exit(m.Run())
}
