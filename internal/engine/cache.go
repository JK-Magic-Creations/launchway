package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Build caches (schema 25). A step's cache: key (see pipeline.Cache) keeps
// directories between runs. An entry is one archive, kept in the artifact
// blob store under caches/, whose bytes are uploaded in chunks exactly as
// artifacts' are, and never changed once committed.
//
// Who may read and write which entry is the security heart of the feature:
// a run of untrusted code (a pull request from anyone who can open one, a
// draft that is not in Git) must never be able to plant bytes that a trusted
// run restores and then runs. So an entry's scope is worked out here, on the
// server, from the run the lease belongs to; a runner's requests carry no
// scope, and a runner cannot name an entry that its run's scopes do not
// reach.
//
//	default       runs of the project's default branch: push, schedule,
//	              webhook, manual on that branch
//	ref:<ref>     any other branch or tag, by its full ref name
//	pull:<n>      a run for pull request n
//
// A run reads its own scope first and then default; it writes only its own
// scope; a pull-request or draft run never writes default or ref:*, and a
// draft run writes nothing. Nothing is read or written across projects.
//
// The store keeps the bytes and the table caches the account:
//
//   - A commit that would take a project past its quota evicts the project's
//     least recently used entries in the same transaction, or is refused.
//     Every counter and every status change is conditional, so several
//     servers committing and evicting at once end each entry once and count
//     its bytes once.
//   - Every server's pruner ends entries idle for longer than the policy
//     says and uploads that stalled, and removes the bytes of ended entries
//     a little later (so that a download in flight finishes).

// schemaCache is the schema that brought build caches.
const schemaCache = pipeline.SchemaCache

// FeatureCache is what a runner that restores and saves caches says it can
// do (RunnerInfo.Features). A step with a cache is only leased to such a
// runner: an older one would run it without restoring anything and without
// saying so.
const FeatureCache = "cache"

// Statuses of an entry, in caches.status. removing is internal: an ended
// entry whose bytes some server is deleting.
const (
	cacheUploading = "uploading"
	cacheKept      = "kept"
	cacheEnded     = "ended"
	cacheRemoving  = "removing"
)

// Scopes.
const (
	cacheScopeDefault = "default"
	cacheScopeRef     = "ref:"
	cacheScopePull    = "pull:"
)

// Limits and timings of caches.
const (
	// MaxCacheFiles bounds how many files one entry may hold.
	MaxCacheFiles = 1000000
	// cacheEndedGrace is how long the bytes of an ended entry stay, so that
	// a restore that began before it ended can finish.
	cacheEndedGrace = 10 * time.Minute
	// cacheStaleUpload is how long an upload may make no progress.
	cacheStaleUpload = 48 * time.Hour
	// cacheOrphanedUpload is how long an upload whose step attempt no runner
	// holds any more is waited for.
	cacheOrphanedUpload = 10 * time.Minute
	pruneBatchCaches    = 200
)

var cachePlatformRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}/[a-z0-9][a-z0-9_.-]{0,31}$`)

// ---- rows ----

type cacheRowT struct {
	ID, Project, Scope, Name, Key, Platform string
	BlobKey, Store, SHA256                  string
	Bytes                                   int64
	Files                                   int
	CreatedRun, CreatedStep                 string
	CreatedAttempt                          int
	CreatedAt, LastUsedAt, Hits             int64
	Status                                  string
	EndedAt                                 int64
	UploadID                                string
	Received                                int64
	HashState                               string
	Parts                                   []blobstore.Part
}

const cacheCols = `id, project, scope, name, key, platform, blob_key, store, sha256, bytes, files, created_run, created_step, created_attempt,
	created_at, last_used_at, hits, status, ended_at, upload_id, received, hash_state, parts`

func scanCache(r interface{ Scan(...any) error }) (*cacheRowT, error) {
	c := &cacheRowT{}
	var parts string
	err := r.Scan(&c.ID, &c.Project, &c.Scope, &c.Name, &c.Key, &c.Platform, &c.BlobKey, &c.Store, &c.SHA256, &c.Bytes, &c.Files, &c.CreatedRun,
		&c.CreatedStep, &c.CreatedAttempt, &c.CreatedAt, &c.LastUsedAt, &c.Hits, &c.Status, &c.EndedAt, &c.UploadID, &c.Received, &c.HashState, &parts)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(parts), &c.Parts); err != nil {
		return nil, fmt.Errorf("cache entry %s: reading its parts: %w", c.ID, err)
	}
	return c, nil
}

func cacheRow(q querier, id string) (*cacheRowT, error) {
	r, err := scanCache(q.QueryRow(`SELECT `+cacheCols+` FROM caches WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: cache entry %s", ErrNotFound, clipText(id, 60))
	}
	return r, err
}

// cacheState is what the engine keeps for build caches between calls. It is
// one field of Engine so that engine.go does not grow with the feature.
type cacheState struct {
	// strayAfter is where removeCacheStrays carries on.
	strayAfter string
	// defaultBranch is how to find a project's default branch (see
	// SetDefaultBranch).
	defaultBranch atomic.Pointer[func(string) string]
}
