package hosting

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
)

// The trash of repositories: deleted repositories wait here for the retention
// the hosting settings give, can be restored, and are removed for good, with
// their Git storage, by a background worker or by an administrator.

// TrashRetentionSetting is the hosting setting (hosting_settings) that says how
// many days a deleted repository is kept; DefaultTrashDays is its default.
const (
	TrashRetentionSetting = "trash_retention_days"
	DefaultTrashDays      = 30
)

// TrashRetention is how long a deleted repository is kept now.
func (h *Hosting) TrashRetention() time.Duration {
	days := DefaultTrashDays
	if v, ok, err := h.o.Engine.HostingSetting(TrashRetentionSetting); err == nil && ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// NoteDeleted records who moved a repository to the trash.
func (h *Hosting) NoteDeleted(ctx context.Context, id, by string) error {
	return h.Forge.NoteTrashed(ctx, id, by)
}

// TrashList lists the deleted repositories.
func (h *Hosting) TrashList(ctx context.Context) ([]forge.TrashedRepository, error) {
	return h.Forge.TrashList(ctx, h.Connection.ID, h.TrashRetention())
}

// TrashGet finds one deleted repository by its ID.
func (h *Hosting) TrashGet(ctx context.Context, id string) (forge.TrashedRepository, error) {
	all, err := h.TrashList(ctx)
	if err != nil {
		return forge.TrashedRepository{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return forge.TrashedRepository{}, fmt.Errorf("%w: no deleted repository %s", forge.ErrNotFound, id)
}

// TrashRestore brings a deleted repository back under the owner and name it had,
// or under the ones given. An address another repository has is forge.ErrExists.
func (h *Hosting) TrashRestore(ctx context.Context, id, owner, name string) (forge.Repository, error) {
	t, err := h.TrashGet(ctx, id)
	if err != nil {
		return forge.Repository{}, err
	}
	home, nm := owner, name
	if home == "" {
		home = t.Home
	}
	if nm == "" {
		nm = t.Name
	}
	return h.Provider.RestoreAs(ctx, id, home, nm)
}

// TrashPurge removes a deleted repository for good: its Git storage (refs, pack
// rows, and the pack blobs nothing else names) and then its own rows. It can
// stop at any point and be run again.
func (h *Hosting) TrashPurge(ctx context.Context, id string) (forge.PurgeSummary, error) {
	var sum forge.PurgeSummary
	if r, err := h.Forge.Repository(ctx, id); err == nil {
		if !store.InTrash(r) {
			return sum, fmt.Errorf("%w: repository %s is not in the trash", forge.ErrInvalid, id)
		}
		sum.Path = r.Path
		if r.Home != "" {
			sum.Path = r.Home + "/" + r.Name
		} else {
			sum.Path = r.Name
		}
		if err := h.Forge.BeginPurge(ctx, id); err != nil {
			return sum, err
		}
	} else if !errors.Is(err, forge.ErrNotFound) {
		return sum, err
	} else {
		// The row is gone: an earlier removal finished all but its last statements.
		sum.Path = id
	}
	res, err := h.Store.Purge(ctx, id)
	sum.Packs, sum.BlobsDeleted, sum.BlobsKept, sum.Bytes = res.Packs, res.BlobsDeleted, res.BlobsKept, res.Bytes
	if err != nil {
		return sum, err
	}
	return sum, h.Forge.DeleteRepositoryRows(ctx, id)
}

// PurgeExpired removes the deleted repositories whose retention ended and
// finishes the removals that stopped half way. It returns how many it removed.
func (h *Hosting) PurgeExpired(ctx context.Context) int {
	removed := 0
	done := map[string]bool{}
	purge := func(id string) {
		sum, err := h.TrashPurge(ctx, id)
		if err != nil {
			h.log.Warn("hosting maintenance: removing a deleted repository", "repository", sum.Path, "err", err)
			return
		}
		done[id] = true
		removed++
		h.audit("repository.purge", sum.Path, fmt.Sprintf("removed for good after the retention: %d packs, %d blobs deleted (%d bytes), %d kept because another repository names them", sum.Packs, sum.BlobsDeleted, sum.Bytes, sum.BlobsKept))
	}
	all, err := h.TrashList(ctx)
	if err != nil {
		h.log.Warn("hosting maintenance: listing the trash", "err", err)
		return 0
	}
	now := time.Now()
	for _, t := range all {
		if ctx.Err() != nil {
			return removed
		}
		if t.Purging || !t.PurgeAt.After(now) {
			purge(t.ID)
		}
	}
	if left, err := h.Forge.UnfinishedPurges(ctx); err == nil {
		for _, id := range left {
			if ctx.Err() != nil {
				return removed
			}
			if !done[id] {
				purge(id)
			}
		}
	}
	return removed
}

// audit writes a line to the Logbook for what the background worker did.
func (h *Hosting) audit(action, target, detail string) {
	err := h.o.Engine.Audit(engine.AuditEntry{At: time.Now().UnixMilli(), User: "system", Source: "hosting maintenance",
		Action: action, Target: target, Outcome: "ok", Detail: detail})
	if err != nil {
		h.log.Warn("hosting maintenance: the Logbook line was not written", "err", err)
	}
}
