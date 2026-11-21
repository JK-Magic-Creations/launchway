package sealing

import (
	"context"
	"fmt"
	"time"
)

// Sealing the secrets an older release stored in the clear changes what a
// server of that release reads: it takes a sealed value for the value itself
// and fails (sign-in state, the runner token, webhook addresses). A rolling
// update runs servers of both releases at once. So a server that finds such
// secrets does not seal them while it can still see an older server: it
// serves (the values in the clear stay readable to it, which is what the
// "stays readable" path of openStored is for), and a background loop seals
// once no older server has been seen for QuietFor. New secrets are sealed
// from the first moment either way; only what already existed waits.
//
// How an older server is seen: it holds database connections that do not
// name themselves (engine.OlderServers). It cannot help being seen.

const waitingKey = "sealing_waiting"

// deferSealing decides, at start, whether sealing waits for older servers.
// It reports true when the work was handed to the background loop.
func (s *Service) deferSealing(ctx context.Context) (bool, error) {
	if s.olderFn == nil || s.quietFor <= 0 {
		return false, nil
	}
	rem, err := s.remaining(ctx)
	if err != nil {
		return false, err
	}
	if rem == 0 {
		return false, nil // a fresh installation, or already sealed: nothing existed to wait for
	}
	if _, err := s.olderFn(ctx); err != nil {
		// Older servers cannot be looked for here: waiting would be forever.
		s.log.Warn("cannot look for servers of an older release, so the stored secrets are sealed now; during a rolling update an older server that is still running cannot read them (see docs/guide/upgrading.md)", "err", err)
		return false, nil
	}
	s.log.Warn("stored secrets are still in the clear and servers of an older release may be running: sealing them waits until no older server has been seen",
		"unsealed", rem, "quiet_seconds", int(s.quietFor.Seconds()))
	go s.sealWhenQuiet(context.WithoutCancel(ctx))
	return true, nil
}

// sealWhenQuiet polls for older servers and seals once none has been seen for
// QuietFor in a row. One server does the work (the migration lease); the
// others see the result and stop.
func (s *Service) sealWhenQuiet(ctx context.Context) {
	var quietSince time.Time
	for {
		if rem, err := s.remaining(ctx); err == nil && rem == 0 {
			_, _ = s.kdb.Exec(ctx, `DELETE FROM encryption_state WHERE key = ?`, waitingKey)
			_ = s.setState(ctx, "sealed_complete", "1")
			return
		}
		n, err := s.olderFn(ctx)
		now := s.now()
		switch {
		case err != nil:
			s.log.Warn("looking for older servers failed; sealing keeps waiting", "err", err)
			quietSince = time.Time{}
		case n > 0:
			quietSince = time.Time{}
			_ = s.setState(ctx, waitingKey, fmt.Sprintf("%s|%d|%d", s.instance, now.UnixMilli(), n))
		case quietSince.IsZero():
			quietSince = now
			_ = s.setState(ctx, waitingKey, fmt.Sprintf("%s|%d|0", s.instance, now.UnixMilli()))
		case now.Sub(quietSince) >= s.quietFor:
			s.log.Info("no older server has been seen: sealing the stored secrets now")
			if err := s.migrateAtStart(ctx); err != nil {
				s.log.Error("sealing the stored secrets failed", "err", err)
				_ = s.setState(ctx, "migration_last_error", err.Error())
			} else {
				_, _ = s.kdb.Exec(ctx, `DELETE FROM encryption_state WHERE key = ?`, waitingKey)
				if rem, err := s.remaining(ctx); err == nil && rem == 0 {
					return
				}
			}
			quietSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.poll):
		}
	}
}

// Waiting reports that sealing of existing secrets is waiting for older
// servers: how many were seen at the last look, and since when it waits.
func (s *Service) waiting(ctx context.Context) (older int, since int64, ok bool) {
	v, found := s.state(ctx, waitingKey)
	if !found {
		return 0, 0, false
	}
	var inst string
	var ms int64
	var n int
	// instance|millis|count; the instance name has no '|'.
	if _, err := fmt.Sscanf(replaceBars(v), "%s %d %d", &inst, &ms, &n); err != nil {
		return 0, 0, true
	}
	return n, ms, true
}

func replaceBars(v string) string {
	b := []byte(v)
	for i := range b {
		if b[i] == '|' {
			b[i] = ' '
		}
	}
	return string(b)
}
