package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	istore "github.com/jk-magic-creations/launchway/internal/store"
)

// The trash of hosted repositories: who deleted one and when (the statements are in internal/store), the list with
// those notes, and the removal of a repository's own rows. Moving a repository to the trash and back is in hosted.go.

// NoteTrashed records who moved a repository to the trash.
func (s *Store) NoteTrashed(ctx context.Context, id, by string) error {
	return istore.RecordTrash(ctx, s.db, id, ms(s.now()), by)
}

// ForgetTrashNote drops the note of a repository that was restored.
func (s *Store) ForgetTrashNote(ctx context.Context, id string) error {
	return istore.ClearTrash(ctx, s.db, id)
}

// TrashList lists a connection's deleted repositories with their notes, oldest deletion first. The Path of each is
// the address it had, not the internal trash path. PurgeAt is set from the retention given.
func (s *Store) TrashList(ctx context.Context, connection string, retention time.Duration) ([]forge.TrashedRepository, error) {
	repos, err := s.Trashed(ctx, connection)
	if err != nil {
		return nil, err
	}
	notes, err := istore.TrashRows(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := make([]forge.TrashedRepository, 0, len(repos))
	for _, r := range repos {
		t := forge.TrashedRepository{Repository: r}
		t.Path = originalPath(r)
		t.DeletedAt = r.UpdatedAt
		if n, ok := notes[r.ID]; ok {
			t.DeletedBy = n.DeletedBy
			if n.DeletedAt > 0 {
				t.DeletedAt = time.UnixMilli(n.DeletedAt).UTC()
			}
			t.Purging = n.PurgeStartedAt > 0
		}
		t.PurgeAt = t.DeletedAt.Add(retention)
		if _, err := s.RepositoryByPath(ctx, connection, t.Path); err == nil {
			t.NameTaken = true
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DeletedAt.Equal(out[j].DeletedAt) {
			return out[i].DeletedAt.Before(out[j].DeletedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func originalPath(r forge.Repository) string {
	if r.Home == "" {
		return r.Name
	}
	return r.Home + "/" + r.Name
}

// RestoreRepositoryAs brings a repository back from the trash under the given owner and name (the ones it had when
// they are empty). A path another repository has is ErrExists. A repository whose removal has begun is refused.
func (s *Store) RestoreRepositoryAs(ctx context.Context, id, home, name string, clone forge.CloneAddresses) error {
	r, err := s.Repository(ctx, id)
	if err != nil {
		return err
	}
	if !InTrash(r) {
		return fmt.Errorf("%w: repository %s is not in the trash", forge.ErrInvalid, id)
	}
	rows, err := istore.TrashRows(ctx, s.db)
	if err != nil {
		return err
	}
	if rows[id].PurgeStartedAt > 0 {
		return fmt.Errorf("%w: repository %s is being removed for good", forge.ErrInvalid, id)
	}
	if home == "" {
		home = r.Home
	}
	if name == "" {
		name = r.Name
	}
	path := name
	if home != "" {
		path = home + "/" + name
	}
	if err := s.MoveRepository(ctx, id, home, name, path, clone); err != nil {
		return err
	}
	return istore.ClearTrash(ctx, s.db, id)
}

// BeginPurge marks the removal of a trashed repository as begun, so that one that stops half way is found again.
func (s *Store) BeginPurge(ctx context.Context, id string) error {
	r, err := s.Repository(ctx, id)
	if err != nil {
		return err
	}
	if !InTrash(r) {
		return fmt.Errorf("%w: repository %s is not in the trash", forge.ErrInvalid, id)
	}
	return istore.MarkPurgeStarted(ctx, s.db, id, ms(s.now()))
}

// UnfinishedPurges lists repositories whose removal began and did not finish.
func (s *Store) UnfinishedPurges(ctx context.Context) ([]string, error) {
	return istore.PurgingRepositories(ctx, s.db)
}

// DeleteRepositoryRows removes a trashed repository's own rows and the repository itself (after its Git storage went).
func (s *Store) DeleteRepositoryRows(ctx context.Context, id string) error {
	_, err := istore.DeleteRepoRows(ctx, s.db, id, TrashPrefix)
	return err
}
