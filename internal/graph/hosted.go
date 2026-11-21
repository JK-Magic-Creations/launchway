package graph

import (
	"context"
	"io/fs"
	"testing/fstest"

	"github.com/jk-magic-creations/launchway/internal/depupdate/gitfs"
)

// GitCache gives a hosted repository's bare Git directory (made current) to a
// function. Git hosting's storage is one.
type GitCache interface {
	WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
}

// HostedRepos is Git hosting as the sources read it: the repositories that
// are hosted here, and the files of their default branches, read from the
// server's own copy without a working tree and without running anything from
// the repository.
type HostedRepos struct {
	// Lister returns every hosted repository.
	Lister func(ctx context.Context) ([]Repo, error)
	Git    GitCache
}

// List returns the hosted repositories.
func (h HostedRepos) List(ctx context.Context) ([]Repo, error) { return h.Lister(ctx) }

// Files reads the default branch.
func (h HostedRepos) Files(ctx context.Context, r Repo, extra []string) (fs.FS, string, error) {
	if r.DefaultBranch == "" {
		return fstest.MapFS{}, "", nil
	}
	var tree fs.FS = fstest.MapFS{}
	var commit string
	err := h.Git.WithCache(ctx, r.ID, func(dir string) error {
		c, err := gitfs.Commit(ctx, dir, r.DefaultBranch)
		if err != nil {
			return nil // no such branch yet: an empty repository
		}
		commit = c
		files, _, err := gitfs.Read(ctx, dir, c, append(append([]string{}, extra...), RequirementsPath), gitfs.Limits{})
		tree = files
		return err
	})
	return tree, commit, err
}
