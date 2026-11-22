package store

import (
	"context"
	"errors"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	istore "github.com/jk-magic-creations/launchway/internal/store"
)

// Old addresses: a repository that was renamed or moved to another owner keeps answering at the address it had until
// another repository takes that address. The statements are in internal/store.

// ResolveAlias returns the live repository that used to be at path. A repository in the trash does not answer.
func (s *Store) ResolveAlias(ctx context.Context, path string) (forge.Repository, bool, error) {
	id, ok, err := istore.RepoAliasTarget(ctx, s.db, path)
	if err != nil || !ok {
		return forge.Repository{}, false, err
	}
	r, err := s.Repository(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return forge.Repository{}, false, nil
	}
	if err != nil || InTrash(r) || r.Path == path {
		return forge.Repository{}, false, err
	}
	return r, true, nil
}

// OldAddress is an address a repository used to have.
type OldAddress struct {
	Path    string    `json:"path"`
	MovedAt time.Time `json:"moved_at"`
	MovedBy string    `json:"moved_by,omitempty"`
}

// OldAddresses lists a repository's old addresses, newest first.
func (s *Store) OldAddresses(ctx context.Context, id string) ([]OldAddress, error) {
	rows, err := istore.RepoAliasesOf(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	out := make([]OldAddress, 0, len(rows))
	for _, r := range rows {
		out = append(out, OldAddress{Path: r.Path, MovedAt: time.UnixMilli(r.CreatedAt).UTC(), MovedBy: r.CreatedBy})
	}
	return out, nil
}

// keepOldAddress is called when a repository leaves path for another: the old address becomes an alias.
func (s *Store) keepOldAddress(ctx context.Context, before forge.Repository, newPath, by string) error {
	if InTrash(before) || before.Path == newPath {
		return nil
	}
	return istore.AddRepoAlias(ctx, s.db, before.Path, before.ID, ms(s.now()), by)
}

// takeAddress is called when a repository is made or moved to path: nothing is an alias of it any more.
func (s *Store) takeAddress(ctx context.Context, path string) error {
	return istore.DeleteRepoAliasesAt(ctx, s.db, path)
}
