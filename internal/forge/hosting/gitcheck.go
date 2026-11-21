package hosting

import (
	"context"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// MinGit is the oldest git that can host. The reference-transaction hook is the
// push's commit point (git 2.28), and landing a change without a working tree
// needs merge-tree --write-tree (git 2.38). The hosting tests, which attack every
// step of a push, pass on this version and on every newer one that was tried
// (docs/development/forge-storage.md, "Git version"); make test-git-compat runs
// them on exactly this version, so a change here is a change to its Dockerfile.
var MinGit = [2]int{2, 38}

// MinGitText is MinGit as it is written for people, such as "2.38".
func MinGitText() string { return fmt.Sprintf("%d.%d", MinGit[0], MinGit[1]) }

// GitTooOld is the plain refusal for a git below the minimum.
func GitTooOld(v gitexec.Version) error {
	return fmt.Errorf("git hosting needs git %s or newer, and this server has git %s; install a newer git (hosting stays off until then; everything else works)", MinGitText(), v)
}

// CheckGit finds the installed git and refuses one that cannot host. The
// version is returned either way it can be read.
func CheckGit(ctx context.Context) (gitexec.Version, error) {
	v, err := gitexec.Installed(ctx)
	if err != nil {
		return v, fmt.Errorf("git hosting needs git %s or newer on this server, and git cannot be run: %w", MinGitText(), err)
	}
	if !v.AtLeast(MinGit[0], MinGit[1]) {
		return v, GitTooOld(v)
	}
	return v, nil
}
