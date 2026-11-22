package forge

import "time"

// TrashedRepository is a deleted repository as the trash shows it: what it was
// called, who deleted it and when, and when it will be removed for good. Path
// is the address it had (owner/name), which a restore gives back unless
// another repository has taken it.
type TrashedRepository struct {
	Repository
	DeletedAt time.Time `json:"deleted_at"`
	// DeletedBy is who moved it to the trash; empty for a repository deleted
	// before the trash kept that.
	DeletedBy string `json:"deleted_by"`
	// PurgeAt is when the background worker removes it for good (the deletion
	// plus the retention that applies now).
	PurgeAt time.Time `json:"purge_at"`
	// Purging says its removal has begun and will be finished: it can no
	// longer be restored.
	Purging bool `json:"purging,omitempty"`
	// NameTaken says the address it had belongs to another repository now, so
	// a restore must name another one.
	NameTaken bool `json:"name_taken,omitempty"`
}

// PurgeSummary is what a permanent removal did.
type PurgeSummary struct {
	Path         string `json:"path"`
	Packs        int    `json:"packs"`
	BlobsDeleted int    `json:"blobs_deleted"`
	BlobsKept    int    `json:"blobs_kept"`
	Bytes        int64  `json:"bytes"`
}
