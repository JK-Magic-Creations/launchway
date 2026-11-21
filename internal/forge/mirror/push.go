package mirror

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Push mirrors copy a hosted repository to another server: every accepted
// update (a push to it, a pull mirror's sync, stopping mirroring) marks the
// repository's push mirrors due, and the scheduler pushes the branches, tags
// and notes with force and prune, so the other side is an exact copy. A
// failure is retried with backoff (a minute, doubling, at most an hour); each
// mirror has its own status and lag, and one that stays behind raises an alert
// (see checkBehind). Credentials are references, as for pull mirrors.

// pushSpecs are the refspecs of a push: the managed namespaces, forced.
var pushSpecs = []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "+refs/notes/*:refs/notes/*"}

// SetPush adds a push mirror to a repository.
func (s *Service) SetPush(ctx context.Context, repoPath string, in Spec, by string) (Mirror, error) {
	r, err := s.repoByPath(ctx, repoPath)
	if err != nil {
		return Mirror{}, err
	}
	m, err := in.mirror(Push, s.o.Guard.AllowFiles)
	if err != nil {
		return m, err
	}
	if err := s.o.Guard.Check(ctx, m.URL); err != nil {
		return m, fmt.Errorf("%w: mirror: %v", forge.ErrInvalid, err)
	}
	have, err := s.Of(ctx, r.ID)
	if err != nil {
		return m, err
	}
	n := 0
	for _, h := range have {
		if h.Direction == Push {
			n++
		}
	}
	if n >= maxPushMirrors {
		return m, fmt.Errorf("%w: a repository may have at most %d push mirrors", forge.ErrQuota, maxPushMirrors)
	}
	m.RepoID, m.CreatedBy = r.ID, by
	m, err = s.insert(ctx, m)
	if err != nil {
		return m, err
	}
	s.markDirty(ctx, r.ID)
	s.kick()
	return m, nil
}

// Pushed tells the service a repository changed, so that its push mirrors
// copy the change. It returns at once; the copying is asynchronous.
func (s *Service) Pushed(ctx context.Context, repoID string) {
	s.markDirty(ctx, repoID)
	s.kick()
}

func (s *Service) markDirty(ctx context.Context, repoID string) {
	now := ms(s.now())
	_, err := s.db.Exec(context.WithoutCancel(ctx), `UPDATE repo_mirrors SET dirty_since = CASE WHEN dirty_since = 0 THEN ? ELSE dirty_since END, next_at = CASE WHEN failures = 0 THEN ? ELSE next_at END
		WHERE repo_id = ? AND direction = 'push' AND enabled = 1`, now, now, repoID)
	if err != nil {
		s.o.Log.Warn("noting that a repository changed for its push mirrors", "repository", repoID, "err", err)
	}
}

func (s *Service) finishPush(ctx context.Context, m Mirror, started time.Time, gen int64, refs int, err error) {
	ctx = context.WithoutCancel(ctx)
	now := s.now()
	if err == nil {
		cur, gerr := s.sto.Generation(ctx, m.RepoID)
		next, dirty := now.Add(m.Interval), int64(0)
		if gerr == nil && cur > gen {
			// Something was accepted while this run pushed: it still has to go.
			next, dirty = now, ms(now)
			if again, e := s.Get(ctx, m.ID); e == nil && !again.DirtySince.IsZero() {
				dirty = ms(again.DirtySince)
			}
		}
		_, uerr := s.db.Exec(ctx, `UPDATE repo_mirrors SET state = ?, last_try_at = ?, last_ok_at = ?, last_error = '', failures = 0, next_at = ?, ref_count = ?,
			synced_gen = ?, dirty_since = ? WHERE id = ?`, StateOK, ms(started), ms(now), ms(next), refs, gen, dirty, m.ID)
		if uerr != nil {
			s.o.Log.Warn("recording a push mirror's success", "mirror", m.ID, "err", uerr)
		}
		// A change accepted between reading the generation and recording the
		// result must not be lost: look once more.
		if again, gerr := s.sto.Generation(ctx, m.RepoID); gerr == nil && again > gen && dirty == 0 {
			s.markDirty(ctx, m.RepoID)
			s.kick()
		}
		return
	}
	msg := s.clean(m, err.Error())
	_, uerr := s.db.Exec(ctx, `UPDATE repo_mirrors SET state = ?, last_try_at = ?, last_error = ?, failures = failures + 1, next_at = ?,
		dirty_since = CASE WHEN dirty_since = 0 THEN ? ELSE dirty_since END WHERE id = ?`,
		StateError, ms(started), msg, ms(now.Add(backoff(m.Failures+1))), ms(now), m.ID)
	if uerr != nil {
		s.o.Log.Warn("recording a push mirror's failure", "mirror", m.ID, "err", uerr)
	}
	s.o.Log.Warn("a push mirror failed", "repository", s.pathOf(ctx, m.RepoID), "error", msg)
}

// runPush copies the repository's refs to the mirror's address. It returns the
// generation it pushed and how many refs there are.
func (s *Service) runPush(ctx context.Context, m Mirror) (int64, int, error) {
	if err := s.o.Guard.Check(ctx, m.URL); err != nil {
		return 0, 0, err
	}
	r, err := s.remoteFor(m)
	if err != nil {
		return 0, 0, err
	}
	gen, err := s.sto.Generation(ctx, m.RepoID)
	if err != nil {
		return 0, 0, err
	}
	refs := 0
	err = s.sto.WithCache(ctx, m.RepoID, func(dir string) error {
		out, err := s.git(ctx, remote{}, dir, 10*time.Minute, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/tags", "refs/notes")
		if err != nil {
			return err
		}
		refs = len(strings.Fields(out))
		if refs == 0 {
			// Pushing nothing with prune would delete everything on the other
			// side: an empty repository has nothing to give.
			return nil
		}
		args := append([]string{"push", "--force", "--prune", "--no-verify", "--quiet", "--", m.URL}, pushSpecs...)
		_, err = s.git(ctx, r, dir, s.o.MaxDuration, args...)
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	return gen, refs, nil
}
