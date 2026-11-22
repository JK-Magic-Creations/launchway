package store

// The parts of the repository table that only hosted repositories (the
// launchway provider) need: creating one that must not already exist, moving
// one, and the trash. A deleted repository is not removed: its row moves to
// the path TrashPrefix+id (its home and name stay, so it can be restored) and
// its data stays in the repository storage until a purge is built.
// TODO: a deleted_at column in the next schema would replace the path marker.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// TrashPrefix starts the path of a repository in the trash. No valid
// repository name starts with a dot, so no live repository has such a path.
const TrashPrefix = ".trash/"

// InTrash reports whether a repository was deleted.
func InTrash(r forge.Repository) bool { return strings.HasPrefix(r.Path, TrashPrefix) }

// CreateRepository stores a new repository and gives it an ID. Unlike
// PutRepository it never updates: a path in use is ErrExists. The ID is
// chosen by the caller when r.ID is set (the storage layer names its
// repository by it).
func (s *Store) CreateRepository(ctx context.Context, r forge.Repository) (forge.Repository, error) {
	if r.Visibility == "" {
		r.Visibility = forge.Private
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	if strings.HasPrefix(r.Path, TrashPrefix) {
		return r, fmt.Errorf("%w: repository: path %q is reserved", forge.ErrInvalid, r.Path)
	}
	if r.ID == "" {
		r.ID = ids.New("rep")
	}
	now := s.now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now
	_, err := s.db.Exec(ctx, `INSERT INTO forge_repositories(`+repoCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Connection, r.Home, r.Name, r.Path, "", r.DefaultBranch, string(r.Visibility), b2i(r.Archived), b2i(r.Mirror),
		r.Clone.HTTPS, r.Clone.SSH, ms(now), ms(now))
	if err != nil {
		if isUnique(err) {
			return r, fmt.Errorf("%w: repository %s", ErrExists, r.Path)
		}
		return r, err
	}
	return r, s.takeAddress(ctx, r.Path)
}

// MoveRepository gives a repository a new home, name and path (and the clone
// address that goes with it). A path in use is ErrExists.
func (s *Store) MoveRepository(ctx context.Context, id, home, name, path string, clone forge.CloneAddresses) error {
	before, err := s.Repository(ctx, id)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET home = ?, name = ?, path = ?, clone_https = ?, clone_ssh = ?, updated_at = ? WHERE id = ?`,
		home, name, path, clone.HTTPS, clone.SSH, ms(s.now()), id)
	if err != nil && isUnique(err) {
		return fmt.Errorf("%w: repository %s", ErrExists, path)
	}
	if err := affected(res, err, "repository "+id); err != nil {
		return err
	}
	// The address it had keeps answering until another repository takes it; the new address is no one's alias.
	if err := s.keepOldAddress(ctx, before, path, ""); err != nil {
		return err
	}
	return s.takeAddress(ctx, path)
}

// TrashRepository moves a repository to the trash.
func (s *Store) TrashRepository(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET path = ?, updated_at = ? WHERE id = ? AND path NOT LIKE ?`,
		TrashPrefix+id, ms(s.now()), id, TrashPrefix+"%")
	return affected(res, err, "repository "+id)
}

// RestoreRepository brings a repository back from the trash to the path its
// home and name give, or ErrExists when that path has been taken since.
func (s *Store) RestoreRepository(ctx context.Context, id string) error {
	r, err := s.Repository(ctx, id)
	if err != nil {
		return err
	}
	if !InTrash(r) {
		return fmt.Errorf("%w: repository %s is not in the trash", forge.ErrInvalid, id)
	}
	path := r.Name
	if r.Home != "" {
		path = r.Home + "/" + r.Name
	}
	return s.MoveRepository(ctx, id, r.Home, r.Name, path, r.Clone)
}

// SetVisibility changes who may read a repository.
func (s *Store) SetVisibility(ctx context.Context, id string, v forge.Visibility) error {
	switch v {
	case forge.Private, forge.Internal, forge.Public:
	default:
		return fmt.Errorf("%w: unknown visibility %q", forge.ErrInvalid, v)
	}
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET visibility = ?, updated_at = ? WHERE id = ?`, string(v), ms(s.now()), id)
	return affected(res, err, "repository "+id)
}

// SetDefaultBranchOf changes a repository's default branch.
func (s *Store) SetDefaultBranchOf(ctx context.Context, id, branch string) error {
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET default_branch = ?, updated_at = ? WHERE id = ?`, branch, ms(s.now()), id)
	return affected(res, err, "repository "+id)
}

// SetArchived archives or unarchives a repository.
func (s *Store) SetArchived(ctx context.Context, id string, on bool) error {
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET archived = ?, updated_at = ? WHERE id = ?`, b2i(on), ms(s.now()), id)
	return affected(res, err, "repository "+id)
}

// Trashed lists a connection's deleted repositories.
func (s *Store) Trashed(ctx context.Context, connection string) ([]forge.Repository, error) {
	all, err := s.Repositories(ctx, connection)
	if err != nil {
		return nil, err
	}
	var out []forge.Repository
	for _, r := range all {
		if InTrash(r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// CountRepositories counts a connection's live repositories under a home.
func (s *Store) CountRepositories(ctx context.Context, connection, home string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM forge_repositories WHERE connection_id = ? AND home = ? AND path NOT LIKE ?`,
		connection, home, TrashPrefix+"%").Scan(&n)
	return n, err
}

// SetMirror marks a repository as a mirror (it takes no pushes; only its
// mirror sync writes to it) or lifts that.
func (s *Store) SetMirror(ctx context.Context, id string, on bool) error {
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET mirror = ?, updated_at = ? WHERE id = ?`, b2i(on), ms(s.now()), id)
	return affected(res, err, "repository "+id)
}
