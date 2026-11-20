//go:build !unix

package gitexec

import (
	"context"
	"errors"
	"os/exec"
)

// ErrLimitsUnavailable: resource limits need a Unix system.
var ErrLimitsUnavailable = errors.New("git resource limits need a Unix system")

const reexecEnv = "LAUNCHWAY_GITEXEC_LIMITS"

func setGroup(cmd *exec.Cmd) {}

// Reexec does nothing on this system.
func Reexec() {}

func limitedCommand(context.Context, Limits, string, []string) (*exec.Cmd, error) {
	return nil, ErrLimitsUnavailable
}
