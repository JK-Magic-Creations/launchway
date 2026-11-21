package api

import (
	"os"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

func TestMain(m *testing.M) {
	gitexec.Reexec() // git's hooks of a hosted repository are symlinks to this binary
	os.Exit(m.Run())
}
