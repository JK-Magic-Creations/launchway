package gitexec

import (
	"context"
	"os"
	"os/exec"
)

// UserOutput runs the person's own git in their own working copy, with their
// whole environment: their credential helper, their ssh configuration, their
// settings. It exists for the command-line tool only (for example fetching a
// pull request's branch into the repository the person is standing in), where
// that is exactly what is wanted. Nothing on a server or a runner may use it:
// there, git handles other people's repositories and Run's hardening applies.
// It never asks on the terminal: a missing credential is an error.
func UserOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // the person's own git, with arguments the command line builds
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.Output()
	return string(b), err
}
