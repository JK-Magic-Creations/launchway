package forks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Service makes forks and template repositories and keeps forks current.
type Service struct {
	// Repos makes and finds repositories; ByID finds one by its ID.
	Repos forge.Repositories
	ByID  func(ctx context.Context, id string) (forge.Repository, error)
	// Git is the repository storage; DB the database the fork rows are in.
	Git *storage.Store
	DB  store.CtxDB
	// PerPersonPerHour and PerHour limit how fast forks and template
	// repositories are made, by one person and in all (defaults 20 and 300).
	PerPersonPerHour, PerHour int
	Now                       func() time.Time
	Log                       *slog.Logger
	// Sign, when set, signs the commits the service makes (a sync's merge, a template's first commit) with the
	// installation's key, as the pull-request service signs its own; nil: they are not signed.
	Sign func(ctx context.Context, payload []byte) ([]byte, error)
}

// runner makes the runner of a scratch repository, signing what it commits.
func (s *Service) runner(dir string, env []string) runner {
	return runner{dir: dir, limits: s.Git.Limits(), env: env, sign: s.Sign, note: s.noteSign}
}

func (s *Service) noteSign(err error) {
	if err != nil {
		s.log().Warn("a commit Launchway made could not be signed with the installation's key", "err", err)
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ErrRateLimited is the forge's: the API answers 429.
var ErrRateLimited = forge.ErrRateLimited

// limit refuses when the person (or everyone) made too many in the last hour.
func (s *Service) limit(ctx context.Context, by string) error {
	pp, all := s.PerPersonPerHour, s.PerHour
	if pp <= 0 {
		pp = 20
	}
	if all <= 0 {
		all = 300
	}
	since := s.now().Add(-time.Hour).UnixMilli()
	if n, err := store.ForksMadeBy(ctx, s.DB, by, since); err != nil {
		return err
	} else if n >= pp {
		return fmt.Errorf("%w: at most %d forks and template repositories an hour for one person", ErrRateLimited, pp)
	}
	if n, err := store.ForksMadeSince(ctx, s.DB, since); err != nil {
		return err
	} else if n >= all {
		return fmt.Errorf("%w: this server is making forks as fast as it is allowed to; try again in a few minutes", ErrRateLimited)
	}
	return nil
}

// Info reads the fork row of a repository.
func (s *Service) Info(ctx context.Context, id string) (Info, error) {
	return store.ForkInfoOf(ctx, s.DB, id)
}

// Switch is whether forks of a repository are allowed now, and why.
func (s *Service) Switch(ctx context.Context, repo forge.Repository) (bool, string, error) {
	i, err := s.Info(ctx, repo.ID)
	if err != nil {
		return false, "", err
	}
	owner, set, err := store.OwnerAllowForks(ctx, s.DB, repo.Home)
	if err != nil {
		return false, "", err
	}
	allow, why := AllowForks(i.AllowForks, owner, set, repo.Visibility)
	return allow, why, nil
}

// NewFork says what a fork is made as.
type NewFork struct {
	// Owner and Name are where the fork goes; Name defaults to the parent's.
	Owner, Name string
	// Visibility defaults to the parent's and may not be wider.
	Visibility forge.Visibility
	// By is who makes it.
	By string
}

// Fork makes a fork of the repository parent. The caller has checked that the
// person may read the parent and may make repositories under the owner.
func (s *Service) Fork(ctx context.Context, parent forge.Repository, n NewFork) (forge.Repository, error) {
	pinfo, err := s.Info(ctx, parent.ID)
	if err != nil {
		return forge.Repository{}, err
	}
	if strings.HasPrefix(parent.Path, store.TrashPrefix) {
		return forge.Repository{}, fmt.Errorf("%w: the repository was deleted", ErrRelation)
	}
	if allow, why, err := s.Switch(ctx, parent); err != nil {
		return forge.Repository{}, err
	} else if !allow {
		return forge.Repository{}, fmt.Errorf("%w: %s", ErrForksOff, why)
	}
	vis, err := ForkVisibility(parent.Visibility, n.Visibility)
	if err != nil {
		return forge.Repository{}, err
	}
	if n.Name == "" {
		n.Name = parent.Name
	}
	if n.Owner == "" {
		n.Owner = n.By
	}
	if err := s.limit(ctx, n.By); err != nil {
		return forge.Repository{}, err
	}
	refs, err := s.Git.Refs(ctx, parent.ID)
	if err != nil {
		return forge.Repository{}, err
	}
	child, err := s.Repos.Create(ctx, forge.NewRepository{Home: n.Owner, Name: n.Name, DefaultBranch: parent.DefaultBranch, Visibility: vis})
	if err != nil {
		return forge.Repository{}, err
	}
	undo := func(cause error) (forge.Repository, error) {
		bg := context.WithoutCancel(ctx)
		_ = s.Repos.Delete(bg, child.Path)
		if _, perr := s.Git.Purge(bg, child.ID); perr != nil {
			s.log().Warn("a failed fork's storage could not be purged", "repository", child.Path, "err", perr)
		}
		return forge.Repository{}, cause
	}
	if _, err := s.Git.Fork(ctx, parent.ID, child.ID); err != nil {
		return undo(fmt.Errorf("copying %s: %w", parent.Path, err))
	}
	root := pinfo.RootID
	if root == "" {
		root = parent.ID
	}
	if err := store.RecordFork(ctx, s.DB, child.ID, parent.ID, root, refs[branchRef(parent.DefaultBranch)], n.By, s.now().UnixMilli()); err != nil {
		return undo(err)
	}
	return child, nil
}

func branchRef(b string) string { return "refs/heads/" + b }

// Parent is a fork's parent, or ErrRelation when the repository is no fork or
// its parent is deleted.
func (s *Service) Parent(ctx context.Context, fork Info) (forge.Repository, error) {
	if fork.ParentID == "" {
		return forge.Repository{}, fmt.Errorf("%w: this repository is not a fork", ErrRelation)
	}
	pi, err := s.Info(ctx, fork.ParentID)
	if errors.Is(err, store.ErrNoRepository) || err == nil && pi.Trashed {
		return forge.Repository{}, fmt.Errorf("%w: its parent was deleted", ErrRelation)
	}
	if err != nil {
		return forge.Repository{}, err
	}
	return s.ByID(ctx, fork.ParentID)
}

// Forks lists the live forks made directly from a repository.
func (s *Service) Forks(ctx context.Context, id string, limit int) ([]Info, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return store.ForksOf(ctx, s.DB, id, limit)
}

// SetAllowForks sets a repository's switch (-1 follows the default).
func (s *Service) SetAllowForks(ctx context.Context, id string, v int) error {
	if v < -1 || v > 1 {
		return fmt.Errorf("%w: allow forks is on, off or default", forge.ErrInvalid)
	}
	return store.SetAllowForks(ctx, s.DB, id, v)
}

// SetOwnerAllowForks sets the switch of every repository of an owner that has
// none of its own; unset removes it.
func (s *Service) SetOwnerAllowForks(ctx context.Context, owner string, allow, unset bool, by string) error {
	return store.SetOwnerAllowForks(ctx, s.DB, owner, allow, unset, by, s.now().UnixMilli())
}

// SameNetwork is pulls.Service.SameNetwork over the fork rows.
func (s *Service) SameNetwork(ctx context.Context, a, b string) (bool, error) {
	ia, err := s.Info(ctx, a)
	if err != nil {
		return false, err
	}
	ib, err := s.Info(ctx, b)
	if err != nil {
		return false, err
	}
	return SameNetwork(ia, ib), nil
}

// OwnerSwitch reads an owner's switch: set is false when the owner has none.
func (s *Service) OwnerSwitch(ctx context.Context, owner string) (allow, set bool, err error) {
	return store.OwnerAllowForks(ctx, s.DB, owner)
}
