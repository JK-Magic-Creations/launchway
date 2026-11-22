package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// The release published to an outside Git host: the engine keeps its state
// (schema 56); the API's worker does the publishing. See
// docs/development/release-publish-outside.md.

// Outside states.
const (
	OutsidePending   = store.OutsidePending
	OutsidePublished = store.OutsidePublished
	OutsideFailed    = store.OutsideFailed
)

// ReleaseOutside is the state of one release on one mirror's outside host.
type ReleaseOutside struct {
	ID        string `json:"id"`
	ReleaseID string `json:"release_id"`
	MirrorID  string `json:"mirror_id"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	// Reason says why it failed or is waiting, with credentials removed.
	Reason    string `json:"reason,omitempty"`
	Attempts  int    `json:"attempts"`
	NextAt    int64  `json:"next_at,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func outsideOf(r store.OutsideRow) ReleaseOutside {
	return ReleaseOutside{ID: r.ID, ReleaseID: r.ReleaseID, MirrorID: r.MirrorID, State: r.State, URL: r.URL, Reason: r.Reason,
		Attempts: r.Attempts, NextAt: r.NextAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func outsides(rows []store.OutsideRow, err error) ([]ReleaseOutside, error) {
	out := make([]ReleaseOutside, 0, len(rows))
	for _, r := range rows {
		out = append(out, outsideOf(r))
	}
	return out, err
}

func (e *Engine) needOutside() error {
	if !e.knows(schemaOutside) {
		return fmt.Errorf("%w: this database is older than the outside-host release (schema %d)", ErrConflict, schemaOutside)
	}
	return nil
}

// QueueReleaseOutside records that a release is to be published on a mirror's
// outside host. It changes nothing when the pair has a row already.
func (e *Engine) QueueReleaseOutside(releaseID, mirrorID string) error {
	if err := e.needOutside(); err != nil {
		return err
	}
	defer e.writeLock()()
	return store.QueueOutside(e.db, ids.New("rou"), releaseID, mirrorID, e.now().UnixMilli())
}

// ReleaseOutsideDue lists the pending rows whose time has come.
func (e *Engine) ReleaseOutsideDue(limit int) ([]ReleaseOutside, error) {
	if !e.knows(schemaOutside) {
		return nil, nil
	}
	return outsides(store.OutsideDue(e.db, e.now().UnixMilli(), limit))
}

// ReleaseOutsideOf lists a release's outside states, one per mirror.
func (e *Engine) ReleaseOutsideOf(releaseID string) ([]ReleaseOutside, error) {
	if !e.knows(schemaOutside) {
		return []ReleaseOutside{}, nil
	}
	return outsides(store.OutsideOfRelease(e.db, releaseID))
}

// ReleaseOutsideOfMirror lists the newest outside states of a mirror.
func (e *Engine) ReleaseOutsideOfMirror(mirrorID string, limit int) ([]ReleaseOutside, error) {
	if !e.knows(schemaOutside) {
		return []ReleaseOutside{}, nil
	}
	return outsides(store.OutsideOfMirror(e.db, mirrorID, limit))
}

// ReleaseOutsideState returns one release's state on one mirror.
func (e *Engine) ReleaseOutsideState(releaseID, mirrorID string) (ReleaseOutside, bool, error) {
	if !e.knows(schemaOutside) {
		return ReleaseOutside{}, false, nil
	}
	r, ok, err := store.OutsideOf(e.db, releaseID, mirrorID)
	return outsideOf(r), ok, err
}

// ClaimReleaseOutside takes a pending row for this worker for lease
// milliseconds; false means another worker has it.
func (e *Engine) ClaimReleaseOutside(id string, leaseMS int64) (bool, error) {
	defer e.writeLock()()
	return store.ClaimOutside(e.db, id, e.now().UnixMilli(), leaseMS)
}

// FinishReleaseOutside records the result of an attempt: a state, where the
// release is outside, why not (already redacted), the attempts so far and,
// for a pending row, when to try next (milliseconds since the epoch).
func (e *Engine) FinishReleaseOutside(id, state, url, reason string, attempts int, nextAt int64) error {
	defer e.writeLock()()
	return store.SetOutside(e.db, id, state, url, reason, attempts, nextAt, e.now().UnixMilli())
}

// RetryReleaseOutside makes a failed row pending again; it reports whether a
// failed row was found.
func (e *Engine) RetryReleaseOutside(releaseID, mirrorID string) (bool, error) {
	if err := e.needOutside(); err != nil {
		return false, err
	}
	defer e.writeLock()()
	return store.RetryOutside(e.db, releaseID, mirrorID, e.now().UnixMilli())
}
