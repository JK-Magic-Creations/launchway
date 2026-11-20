package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
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

// ---- the policy ----

// CachePolicy is the configuration of caches: whether they are kept at all,
// and how much. It is held in the database, as the installation's default
// and as an override for a project, and an administrator changes it.
type CachePolicy struct {
	Enabled bool `json:"enabled"`
	// MaxProjectGiB is how much one project's entries may add up to, and
	// MaxEntryGiB the most one entry may hold. They are GiB, and may be
	// fractions (0.05 is about 51 MiB).
	MaxProjectGiB float64 `json:"max_project_gib"`
	MaxEntryGiB   float64 `json:"max_entry_gib"`
	// IdleDays is how long an entry nobody restored is kept.
	IdleDays int `json:"idle_days"`
}

// The policy as compiled in: what an installation has until an
// administrator sets one.
var defaultCachePolicy = CachePolicy{Enabled: true, MaxProjectGiB: 10, MaxEntryGiB: 2, IdleDays: 7}

// CachePolicyView is a policy with where it comes from.
type CachePolicyView struct {
	CachePolicy
	// Source is project (this project's own), installation (set for every
	// project) or default (nothing was ever set: the values compiled in).
	Source string `json:"source"`
	// Project is the project an override is for; empty for the installation's.
	Project   string `json:"project,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// cacheLimits is a policy in bytes.
type cacheLimits struct {
	enabled         bool
	maxProject      int64
	maxEntry        int64
	idle            time.Duration
	source, project string
	updatedAt       int64
	updatedBy       string
}

const gib = float64(1 << 30)

func (l cacheLimits) view() CachePolicyView {
	return CachePolicyView{CachePolicy: CachePolicy{Enabled: l.enabled, MaxProjectGiB: float64(l.maxProject) / gib,
		MaxEntryGiB: float64(l.maxEntry) / gib, IdleDays: int(l.idle / (24 * time.Hour))},
		Source: l.source, Project: l.project, UpdatedAt: l.updatedAt, UpdatedBy: l.updatedBy}
}

func limitsOf(p CachePolicy) cacheLimits {
	return cacheLimits{enabled: p.Enabled, maxProject: int64(math.Round(p.MaxProjectGiB * gib)),
		maxEntry: int64(math.Round(p.MaxEntryGiB * gib)), idle: time.Duration(p.IdleDays) * 24 * time.Hour}
}

// CheckCachePolicy returns why a policy cannot be used, or nil.
func CheckCachePolicy(p CachePolicy) error {
	switch {
	case math.IsNaN(p.MaxProjectGiB) || math.IsInf(p.MaxProjectGiB, 0) || p.MaxProjectGiB < 1.0/1024 || p.MaxProjectGiB > 1<<20:
		return fmt.Errorf("%w: max_project_gib is between 0.001 (about 1 MiB) and 1048576", ErrInvalid)
	case math.IsNaN(p.MaxEntryGiB) || math.IsInf(p.MaxEntryGiB, 0) || p.MaxEntryGiB < 1.0/1024 || p.MaxEntryGiB > 1<<10:
		return fmt.Errorf("%w: max_entry_gib is between 0.001 (about 1 MiB) and 1024", ErrInvalid)
	case p.MaxEntryGiB > p.MaxProjectGiB:
		return fmt.Errorf("%w: max_entry_gib (%g) is more than max_project_gib (%g): an entry could never be kept", ErrInvalid, p.MaxEntryGiB, p.MaxProjectGiB)
	case p.IdleDays < 1 || p.IdleDays > 3650:
		return fmt.Errorf("%w: idle_days is between 1 and 3650", ErrInvalid)
	}
	return nil
}

func (e *Engine) readCacheLimits(q querier, project string) (cacheLimits, error) {
	for _, row := range []struct{ project, source string }{{project, "project"}, {"", "installation"}} {
		if row.project == "" && row.source == "project" {
			continue
		}
		var l cacheLimits
		var enabled int
		var idleDays int
		err := q.QueryRow(`SELECT enabled, max_project_bytes, max_entry_bytes, idle_days, updated_at, updated_by FROM cache_policy WHERE project = ?`,
			row.project).Scan(&enabled, &l.maxProject, &l.maxEntry, &idleDays, &l.updatedAt, &l.updatedBy)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return l, err
		}
		l.enabled, l.idle, l.source = enabled != 0, time.Duration(idleDays)*24*time.Hour, row.source
		if row.source == "project" {
			l.project = project
		}
		return l, nil
	}
	l := limitsOf(defaultCachePolicy)
	l.source = "default"
	return l, nil
}

// CachePolicyFor returns the policy that applies to a project: its own, or
// the installation's.
func (e *Engine) CachePolicyFor(project string) (CachePolicyView, error) {
	if !e.knows(schemaCache) {
		return CachePolicyView{}, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.readCacheLimits(e.db, project)
	return l.view(), err
}

// CachePolicyInstallation returns the installation's policy.
func (e *Engine) CachePolicyInstallation() (CachePolicyView, error) {
	if !e.knows(schemaCache) {
		return CachePolicyView{}, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.readCacheLimits(e.db, "")
	return l.view(), err
}

// SetCachePolicy replaces the installation's policy (project empty) or a
// project's override. It returns the policy that applied before and after.
func (e *Engine) SetCachePolicy(project string, p CachePolicy, actor string) (before, after CachePolicyView, err error) {
	if !e.knows(schemaCache) {
		return before, after, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if err := CheckCachePolicy(p); err != nil {
		return before, after, err
	}
	l := limitsOf(p)
	err = e.runWrite(func(tx *txx) error {
		b, err := e.readCacheLimits(tx, project)
		if err != nil {
			return err
		}
		before = b.view()
		now := e.now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO cache_policy(project, enabled, max_project_bytes, max_entry_bytes, idle_days, updated_at, updated_by)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(project) DO UPDATE SET enabled = excluded.enabled, max_project_bytes = excluded.max_project_bytes,
			max_entry_bytes = excluded.max_entry_bytes, idle_days = excluded.idle_days, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			project, boolInt(l.enabled), l.maxProject, l.maxEntry, p.IdleDays, now, actor); err != nil {
			return err
		}
		a, err := e.readCacheLimits(tx, project)
		after = a.view()
		return err
	})
	return before, after, err
}

// ClearCachePolicy removes a project's override, so the installation's
// policy applies to it again.
func (e *Engine) ClearCachePolicy(project, actor string) (before, after CachePolicyView, err error) {
	if !e.knows(schemaCache) {
		return before, after, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if project == "" {
		return before, after, fmt.Errorf("%w: the installation's policy cannot be removed, only changed", ErrInvalid)
	}
	err = e.runWrite(func(tx *txx) error {
		b, err := e.readCacheLimits(tx, project)
		if err != nil {
			return err
		}
		before = b.view()
		if _, err := tx.Exec(`DELETE FROM cache_policy WHERE project = ?`, project); err != nil {
			return err
		}
		a, err := e.readCacheLimits(tx, project)
		after = a.view()
		return err
	})
	return before, after, err
}

// ---- scopes ----

// SetDefaultBranch tells the engine how to find a project's default branch,
// which decides whose cache entries are the default scope. The engine knows
// projects only by name; the server that knows their repositories sets this
// once, before it takes runs. Without it, every project's is main.
func (e *Engine) SetDefaultBranch(fn func(project string) string) {
	e.defaultBranch.Store(&fn)
}

func (e *Engine) projectDefaultBranch(project string) string {
	if fn := e.defaultBranch.Load(); fn != nil {
		if b := (*fn)(project); b != "" {
			return b
		}
	}
	return "main"
}

// cacheScopes says which scopes a run reads, in the order it reads them, and
// which one it writes (empty if none, with why).
type cacheScopes struct {
	read     []string
	write    string
	whyNone  string // why nothing is written
	scopeOf  string // a plain description for the log: "the default branch", ...
	readable map[string]bool
}

func (s cacheScopes) canRead(scope string) bool { return slices.Contains(s.read, scope) }

// scopesOf works out the scopes of a run from what the server recorded of
// how it started. Nothing a runner says takes part.
func (e *Engine) scopesOf(st *RunState) cacheScopes {
	t := st.Trigger
	def := e.projectDefaultBranch(st.Project)
	var s cacheScopes
	switch {
	case st.Draft != nil || t.Event == pipeline.EventDraft:
		// Unreviewed text: it reads what the default branch built and saves
		// nothing, so that whatever it writes cannot reach anything else.
		s.read = []string{cacheScopeDefault}
		s.whyNone = "a draft run saves no cache; it restores the default branch's"
	case t.Event == pipeline.EventPullRequest:
		if t.PullRequest > 0 {
			own := cacheScopePull + strconv.Itoa(t.PullRequest)
			s.read, s.write = []string{own, cacheScopeDefault}, own
		} else {
			s.read = []string{cacheScopeDefault}
			s.whyNone = "a pull-request run without a pull request number saves no cache"
		}
	case t.Tag != "":
		own := cacheScopeRef + nonEmpty(t.Ref, "refs/tags/"+t.Tag)
		s.read, s.write = []string{own, cacheScopeDefault}, own
	case t.Branch != "" && t.Branch == def && (t.Ref == "" || t.Ref == "refs/heads/"+def):
		s.read, s.write = []string{cacheScopeDefault}, cacheScopeDefault
	case t.Branch != "":
		own := cacheScopeRef + nonEmpty(t.Ref, "refs/heads/"+t.Branch)
		s.read, s.write = []string{own, cacheScopeDefault}, own
	case t.Ref != "":
		own := cacheScopeRef + t.Ref
		s.read, s.write = []string{own, cacheScopeDefault}, own
	default:
		s.read = []string{cacheScopeDefault}
		s.whyNone = "this run was not started from a branch, tag or pull request, so it saves no cache"
	}
	return s
}

// cacheContext is what every runner call about a cache is checked against:
// the lease's run, step and attempt, the declared cache, and the scopes.
type cacheContext struct {
	l      leaseRef
	st     *RunState
	def    *pipeline.Step
	scopes cacheScopes
	limits cacheLimits
	store  blobstore.Store
}

// cacheFor checks a runner's call: the lease is live, the step declares a
// cache of that name, and the server knows caches. It renews the lease, as
// an upload does.
func (e *Engine) cacheFor(leaseID, name string) (*cacheContext, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.leaseFor(leaseID)
	if err != nil {
		return nil, err
	}
	st, err := e.runState(l.runID)
	if err != nil {
		return nil, err
	}
	def := st.Pipeline.Step(l.stepID)
	if def == nil {
		return nil, ErrLeaseLost
	}
	if name != "" {
		found := false
		for _, c := range def.Cache {
			found = found || c.Name == name
		}
		if !found {
			return nil, fmt.Errorf("%w: step %s declares no cache %q", ErrInvalid, l.stepID, name)
		}
	}
	if st.Project == "" {
		return nil, fmt.Errorf("%w: this run belongs to no project, so it has no cache", ErrInvalid)
	}
	limits, err := e.readCacheLimits(e.db, st.Project)
	if err != nil {
		return nil, err
	}
	return &cacheContext{l: l, st: st, def: def, scopes: e.scopesOf(st), limits: limits}, nil
}

// ---- looking up ----

// CacheLookup asks for the entry a step should restore.
type CacheLookup struct {
	Name     string `json:"name"`
	Key      string `json:"key"`      // 64 hex digits
	Platform string `json:"platform"` // linux/amd64
	// Fallback is "latest" to take the newest entry of the name and platform
	// that the run may read when none has the key.
	Fallback string `json:"fallback,omitempty"`
}

// CacheEntry is an entry a runner restores.
type CacheEntry struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Platform string `json:"platform"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
	SHA256   string `json:"sha256"`
	// Exact says the key is the one asked for; false for a fallback.
	Exact bool `json:"exact"`
}

// CacheLookupResult is the answer to a lookup: an entry, or nothing and,
// when it is not simply that none exists, why.
type CacheLookupResult struct {
	Entry *CacheEntry `json:"entry,omitempty"`
	// Reason says why there is no entry although one might: caching is
	// turned off, the server keeps no store.
	Reason string `json:"reason,omitempty"`
}

func checkCacheKey(key, platform string) error {
	if !validSHA256(key) {
		return fmt.Errorf("%w: a cache key is 64 lower-case hex digits", ErrInvalid)
	}
	if !cachePlatformRe.MatchString(platform) {
		return fmt.Errorf("%w: %q is not a platform such as linux/amd64", ErrInvalid, clipText(platform, 40))
	}
	return nil
}

// LookupCache finds the entry a step restores: the one with the key in the
// run's own scope, then in the default scope; with fallback latest, failing
// that the newest entry of the name and platform in the run's own scope and
// then in the default scope. Each hit counts as a use of the entry (it
// makes it newer for eviction).
func (e *Engine) LookupCache(leaseID string, in CacheLookup) (*CacheLookupResult, error) {
	if err := checkCacheKey(in.Key, in.Platform); err != nil {
		return nil, err
	}
	if in.Fallback != "" && in.Fallback != pipeline.CacheFallbackLatest && in.Fallback != pipeline.CacheFallbackNone {
		return nil, fmt.Errorf("%w: fallback is none or latest", ErrInvalid)
	}
	cc, err := e.cacheFor(leaseID, in.Name)
	if err != nil {
		return nil, err
	}
	if !cc.limits.enabled {
		return &CacheLookupResult{Reason: "caching is turned off for this project (cache policy)"}, nil
	}
	store, err := e.storeOrErr()
	if err != nil {
		return &CacheLookupResult{Reason: strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")}, nil
	}
	project := cc.st.Project
	find := func(exact bool) (*CacheEntry, error) {
		for _, scope := range cc.scopes.read {
			q := `SELECT id, key, scope, platform, bytes, files, sha256 FROM caches
				WHERE project = ? AND scope = ? AND name = ? AND platform = ? AND status = ? AND store = ?`
			args := []any{project, scope, in.Name, in.Platform, cacheKept, store.Name()}
			if exact {
				q += ` AND key = ?`
				args = append(args, in.Key)
			}
			q += ` ORDER BY created_at DESC, id DESC LIMIT 1`
			en := &CacheEntry{Exact: exact}
			err := e.db.QueryRow(q, args...).Scan(&en.ID, &en.Key, &en.Scope, &en.Platform, &en.Bytes, &en.Files, &en.SHA256)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			return en, nil
		}
		return nil, nil
	}
	en, err := find(true)
	if err == nil && en == nil && in.Fallback == pipeline.CacheFallbackLatest {
		en, err = find(false)
	}
	if err != nil || en == nil {
		return &CacheLookupResult{}, err
	}
	// The hit counts only if the entry is still kept: another server may
	// have evicted it since it was read.
	used := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		used = false
		res, err := tx.Exec(`UPDATE caches SET last_used_at = ?, hits = hits + 1 WHERE id = ? AND status = ?`, now, en.ID, cacheKept)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		used = n == 1
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !used {
		return &CacheLookupResult{}, nil
	}
	return &CacheLookupResult{Entry: en}, nil
}

// OpenCache opens an entry's bytes for the step attempt a lease holds, from
// offset. Only entries that the step's own lookups may return: of the
// run's project, in a scope the run reads, under a name its step declares.
func (e *Engine) OpenCache(ctx context.Context, leaseID, id string, offset int64) (io.ReadCloser, int64, string, error) {
	row, err := cacheRow(e.db, id)
	if err != nil {
		return nil, 0, "", err
	}
	cc, err := e.cacheFor(leaseID, row.Name)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			err = fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
		}
		return nil, 0, "", err
	}
	if row.Project != cc.st.Project || !cc.scopes.canRead(row.Scope) || row.Status != cacheKept {
		return nil, 0, "", fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
	}
	if !cc.limits.enabled {
		return nil, 0, "", fmt.Errorf("%w: caching is turned off for this project", ErrNotFound)
	}
	if offset < 0 || offset > row.Bytes {
		return nil, 0, "", fmt.Errorf("%w: offset %d is outside the entry", ErrInvalid, offset)
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, 0, "", err
	}
	if row.Store != store.Name() {
		return nil, 0, "", fmt.Errorf("%w: cache entry %s is kept in the %s store, which this server does not use", ErrNotFound, id, row.Store)
	}
	r, err := store.Open(ctx, row.BlobKey, offset, -1)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, 0, "", fmt.Errorf("%w: the bytes of cache entry %s are no longer kept", ErrNotFound, id)
	}
	return r, row.Bytes, row.SHA256, err
}

// DiscardCache ends an entry that a runner could not restore because its
// bytes are not what they were saved as: damaged on the way, or in the
// store, or not an archive Launchway made. Entries are never changed, so
// without this a damaged one would be found, fail and be found again until
// it fell idle. Only an entry the lease's step could restore may be
// discarded, and only the run's own project's.
func (e *Engine) DiscardCache(leaseID, id, why string) error {
	row, err := cacheRow(e.db, id)
	if err != nil {
		return err
	}
	cc, err := e.cacheFor(leaseID, row.Name)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			err = fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
		}
		return err
	}
	if row.Project != cc.st.Project || !cc.scopes.canRead(row.Scope) || row.Status != cacheKept {
		return fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
	}
	e.log.Warn("a runner could not restore a cache entry and it is ended", "project", row.Project, "entry", id, "why", why)
	return e.endCacheByID(id, "runner", "could not be restored: "+cleanText(why, 150))
}

// ---- saving ----

// BeginCache declares an entry to save.
type BeginCache struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Platform string `json:"platform"`
	Bytes    int64  `json:"bytes"`  // of the archive
	Files    int    `json:"files"`  // in it
	SHA256   string `json:"sha256"` // of the archive
}

// BegunCache says what to send. Exists says the entry is already kept (or
// being saved by another run): nothing is sent, and that is not an error.
type BegunCache struct {
	ID         string `json:"id,omitempty"`
	Exists     bool   `json:"exists,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Received   int64  `json:"received"`
	ChunkBytes int64  `json:"chunk_bytes"`
}

func cacheBytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<30))) + " GiB"
	default:
		return sizeText(n)
	}
}

// BeginCache declares an entry of the step attempt a lease holds, in the
// scope the server decides, or finds that it exists. A declaration the
// policy refuses (caching is off, the entry is over the limits, this run
// may not save) fails with ErrForbidden or ErrQuota, saying why in plain
// words; the runner logs it and the step's outcome does not change.
func (e *Engine) BeginCache(leaseID string, in BeginCache) (*BegunCache, error) {
	if err := checkCacheKey(in.Key, in.Platform); err != nil {
		return nil, err
	}
	switch {
	case in.Bytes <= 0:
		return nil, fmt.Errorf("%w: an entry holds at least one byte", ErrInvalid)
	case in.Files < 0 || in.Files > MaxCacheFiles:
		return nil, fmt.Errorf("%w: an entry holds at most %d files", ErrInvalid, MaxCacheFiles)
	case !validSHA256(in.SHA256):
		return nil, fmt.Errorf("%w: the SHA-256 must be 64 lower-case hex digits", ErrInvalid)
	}
	cc, err := e.cacheFor(leaseID, in.Name)
	if err != nil {
		return nil, err
	}
	switch {
	case !cc.limits.enabled:
		return nil, fmt.Errorf("%w: caching is turned off for this project (cache policy)", ErrForbidden)
	case cc.scopes.write == "":
		return nil, fmt.Errorf("%w: %s", ErrForbidden, cc.scopes.whyNone)
	case in.Bytes > cc.limits.maxEntry:
		return nil, fmt.Errorf("%w: this cache is %s; an entry may hold at most %s (cache policy max_entry_gib)",
			ErrQuota, cacheBytesText(in.Bytes), cacheBytesText(cc.limits.maxEntry))
	case in.Bytes > cc.limits.maxProject:
		return nil, fmt.Errorf("%w: this cache is %s; a project may keep at most %s of caches (cache policy max_project_gib)",
			ErrQuota, cacheBytesText(in.Bytes), cacheBytesText(cc.limits.maxProject))
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	project, scope := cc.st.Project, cc.scopes.write
	same := func(r *cacheRowT) bool {
		return r.CreatedRun == cc.l.runID && r.CreatedStep == cc.l.stepID && r.CreatedAttempt == cc.l.attempt && r.SHA256 == in.SHA256 && r.Bytes == in.Bytes
	}
	// Entries are immutable: one that exists is not replaced.
	existing := func(q querier) (*cacheRowT, error) {
		r, err := scanCache(q.QueryRow(`SELECT `+cacheCols+` FROM caches WHERE project = ? AND scope = ? AND name = ? AND key = ? AND platform = ?
			AND status IN (?, ?)`, project, scope, in.Name, in.Key, in.Platform, cacheUploading, cacheKept))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return r, err
	}
	if r, err := existing(e.db); err != nil {
		return nil, err
	} else if r != nil && !(r.Status == cacheUploading && same(r)) {
		return existsAnswer(r, scope), nil
	}
	id := ids.New("cch")
	blobKey := "caches/" + strings.ReplaceAll(id, "_", "-")
	uploadID, err := store.CreateUpload(context.Background(), blobKey)
	if err != nil {
		return nil, fmt.Errorf("starting the upload of the cache: %w", err)
	}
	var out *BegunCache
	won := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		out, won = nil, false
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		r, err := existing(tx)
		if err != nil {
			return err
		}
		if r != nil {
			if r.Status == cacheUploading && same(r) {
				// This attempt asked before and carries on.
				out = &BegunCache{ID: r.ID, Scope: scope, Received: r.Received, ChunkBytes: ArtifactChunkBytes}
				return nil
			}
			out = existsAnswer(r, scope)
			return nil
		}
		if _, err := tx.Exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, store, sha256, bytes, files, created_run, created_step,
			created_attempt, created_at, last_used_at, status, upload_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, scope, in.Name, in.Key, in.Platform, blobKey, store.Name(), in.SHA256, in.Bytes, in.Files, cc.l.runID, cc.l.stepID,
			cc.l.attempt, now, now, cacheUploading, uploadID); err != nil {
			return err
		}
		won = true
		out = &BegunCache{ID: id, Scope: scope, ChunkBytes: ArtifactChunkBytes}
		return nil
	})
	if err != nil || !won {
		_ = store.AbortUpload(context.Background(), blobKey, uploadID)
	}
	return out, err
}

func existsAnswer(r *cacheRowT, scope string) *BegunCache {
	why := "an entry with this key was saved already, and entries are never changed"
	if r.Status == cacheUploading {
		why = "another run is saving an entry with this key"
	}
	return &BegunCache{Exists: true, Reason: why, Scope: scope, ChunkBytes: ArtifactChunkBytes}
}

// PutCacheChunk stores one chunk of an entry being saved: the bytes from
// offset, whose SHA-256 is sum. It returns how many bytes of the entry the
// server has. A chunk the server has already is accepted again without
// effect; one that does not start where the upload is gives a
// *ChunkOffsetError.
func (e *Engine) PutCacheChunk(ctx context.Context, leaseID, id string, offset int64, sum string, data []byte) (int64, error) {
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return 0, fmt.Errorf("%w: the chunk does not match its SHA-256; it was damaged on the way", ErrInvalid)
	}
	if len(data) == 0 || len(data) > ArtifactChunkBytes || offset < 0 || offset%ArtifactChunkBytes != 0 {
		return 0, fmt.Errorf("%w: a chunk starts at a multiple of %d bytes and holds 1 to %d bytes", ErrInvalid, ArtifactChunkBytes, ArtifactChunkBytes)
	}
	store, err := e.storeOrErr()
	if err != nil {
		return 0, err
	}
	r, cc, err := e.uploadingCache(leaseID, id)
	if err != nil {
		return 0, err
	}
	end := offset + int64(len(data))
	if end > r.Bytes || (end < r.Bytes && len(data) != ArtifactChunkBytes) {
		return 0, fmt.Errorf("%w: every chunk but the last holds %d bytes, and none goes past the entry's %d bytes", ErrInvalid, ArtifactChunkBytes, r.Bytes)
	}
	if r.Received >= end {
		return r.Received, nil // taken before
	}
	if offset != r.Received {
		return 0, &ChunkOffsetError{Received: r.Received}
	}
	h := sha256.New()
	if r.HashState != "" {
		raw, err := base64.StdEncoding.DecodeString(r.HashState)
		if err == nil {
			err = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(raw)
		}
		if err != nil {
			return 0, fmt.Errorf("cache entry %s: reading the state of its SHA-256: %w", r.ID, err)
		}
	}
	h.Write(data)
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return 0, err
	}
	n := int(offset/ArtifactChunkBytes) + 1
	etag, err := store.UploadPart(ctx, r.BlobKey, r.UploadID, n, data)
	if errors.Is(err, blobstore.ErrFull) || errors.Is(err, blobstore.ErrTooLarge) {
		return 0, fmt.Errorf("%w: storing the cache: %v", ErrQuota, err)
	}
	if err != nil {
		return 0, fmt.Errorf("storing part %d of the cache: %w", n, err)
	}
	parts := append(slices.Clone(r.Parts), blobstore.Part{Number: n, ETag: etag})
	partsJSON, _ := json.Marshal(parts)
	moved := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		moved = false
		res, err := tx.Exec(`UPDATE caches SET received = ?, hash_state = ?, parts = ?, last_used_at = ? WHERE id = ? AND status = ? AND received = ?`,
			end, base64.StdEncoding.EncodeToString(state), string(partsJSON), now, r.ID, cacheUploading, r.Received)
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		moved = k == 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	if !moved {
		// Another server took the same chunk at the same moment, or the
		// entry ended.
		nr, err := cacheRow(e.db, r.ID)
		if err != nil {
			return 0, err
		}
		if nr.Status == cacheUploading && nr.Received >= end {
			return nr.Received, nil
		}
		if nr.Status != cacheUploading {
			return 0, fmt.Errorf("%w: the cache entry is %s", ErrConflict, nr.Status)
		}
		return 0, &ChunkOffsetError{Received: nr.Received}
	}
	if end < r.Bytes {
		return end, nil
	}
	if hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		// What arrived is not what was declared: give the entry up.
		_ = e.endCacheByID(r.ID, "", "the bytes received did not have the SHA-256 declared")
		_ = store.AbortUpload(ctx, r.BlobKey, r.UploadID)
		return 0, fmt.Errorf("%w: checksum mismatch: the archive does not have the SHA-256 it was declared with; save it again", ErrInvalid)
	}
	r.Received, r.Parts = end, parts
	_ = cc
	return end, e.completeCache(ctx, store, r)
}

// completeCache makes a fully received entry's object. Completing twice is
// harmless, so a server that finds an entry received but not completed
// (its server stopped in between) finishes the job.
func (e *Engine) completeCache(ctx context.Context, store blobstore.Store, r *cacheRowT) error {
	if err := store.CompleteUpload(ctx, r.BlobKey, r.UploadID, r.Parts); err != nil {
		if size, serr := store.Stat(ctx, r.BlobKey); serr != nil || size != r.Bytes {
			return fmt.Errorf("completing the upload: %w", err)
		}
	}
	return nil
}

// uploadingCache finds an entry being saved by the step attempt a lease
// holds.
func (e *Engine) uploadingCache(leaseID, id string) (*cacheRowT, *cacheContext, error) {
	r, err := cacheRow(e.db, id)
	if err != nil {
		return nil, nil, err
	}
	cc, err := e.cacheFor(leaseID, "")
	if err != nil {
		return nil, nil, err
	}
	if r.CreatedRun != cc.l.runID || r.CreatedStep != cc.l.stepID || r.CreatedAttempt != cc.l.attempt || r.Project != cc.st.Project {
		return nil, nil, fmt.Errorf("%w: cache entry %s is not of this step attempt", ErrNotFound, id)
	}
	if r.Status != cacheUploading {
		return nil, nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, r.Status)
	}
	return r, cc, nil
}

// errCacheMoved says an entry changed under a commit's transaction.
var errCacheMoved = errors.New("the cache entry changed under the commit")

// CommittedCache says what committing did.
type CommittedCache struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
	// Evicted is how many of the project's other entries were ended to make
	// room, and EvictedBytes their size.
	Evicted      int   `json:"evicted,omitempty"`
	EvictedBytes int64 `json:"evicted_bytes,omitempty"`
	// Exists says the entry was already committed: nothing changed.
	Exists bool `json:"exists,omitempty"`
}

// CommitCache makes an entry whose bytes have all arrived a kept one. If
// that would take the project past its quota, the project's least recently
// used entries are ended in the same transaction until it fits; if it
// cannot fit, the entry is refused (ErrQuota) and ended. Two servers
// committing one entry at once: one wins, the other is told it exists.
func (e *Engine) CommitCache(ctx context.Context, leaseID, id string) (*CommittedCache, error) {
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	r, err := cacheRow(e.db, id)
	if err != nil {
		return nil, err
	}
	cc, err := e.cacheFor(leaseID, r.Name)
	if err != nil {
		return nil, err
	}
	if r.CreatedRun != cc.l.runID || r.CreatedStep != cc.l.stepID || r.CreatedAttempt != cc.l.attempt || r.Project != cc.st.Project {
		return nil, fmt.Errorf("%w: cache entry %s is not of this step attempt", ErrNotFound, id)
	}
	switch r.Status {
	case cacheKept:
		return &CommittedCache{ID: r.ID, Bytes: r.Bytes, Exists: true}, nil
	case cacheUploading:
	default:
		return nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, r.Status)
	}
	if !cc.limits.enabled {
		_ = e.endCacheByID(r.ID, "", "caching was turned off while it was saved")
		return nil, fmt.Errorf("%w: caching is turned off for this project (cache policy)", ErrForbidden)
	}
	if r.Received != r.Bytes {
		return nil, fmt.Errorf("%w: not every byte has been sent yet: the server has %d of %d", ErrConflict, r.Received, r.Bytes)
	}
	if err := e.completeCache(ctx, store, r); err != nil {
		return nil, err
	}
	var out *CommittedCache
	var refused error
	err = e.artifactTx(func(tx *txx, now int64) error {
		out, refused = nil, nil
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		// Every transaction that changes a project's usage and an entry
		// takes the usage row first, so that servers doing both at once
		// cannot wait for each other.
		if _, err := tx.Exec(`INSERT INTO cache_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, r.Project); err != nil {
			return err
		}
		evicted, freed := 0, int64(0)
		for {
			res, err := tx.Exec(`UPDATE cache_usage SET bytes = bytes + ? WHERE project = ? AND bytes + ? <= ?`,
				r.Bytes, r.Project, r.Bytes, cc.limits.maxProject)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				break
			}
			var victim string
			err = tx.QueryRow(`SELECT id FROM caches WHERE project = ? AND status = ? ORDER BY last_used_at, created_at, id LIMIT 1`,
				r.Project, cacheKept).Scan(&victim)
			if errors.Is(err, sql.ErrNoRows) {
				refused = fmt.Errorf("%w: this cache is %s, which does not fit in the project's %s of caches (cache policy max_project_gib)",
					ErrQuota, cacheBytesText(r.Bytes), cacheBytesText(cc.limits.maxProject))
				return nil // the transaction ends without evicting anything it cannot use
			}
			if err != nil {
				return err
			}
			n, size, err := endKeptCache(tx, victim, "", "evicted: the project's caches were over their quota", now)
			if err != nil {
				return err
			}
			evicted += n
			freed += size
		}
		res, err := tx.Exec(`UPDATE caches SET status = ?, last_used_at = ?, hash_state = '', parts = '[]' WHERE id = ? AND status = ?`,
			cacheKept, now, r.ID, cacheUploading)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another server committed it first (or it ended): this
			// transaction's changes, the usage it added and the entries it
			// evicted among them, are rolled back.
			return errCacheMoved
		}
		out = &CommittedCache{ID: r.ID, Bytes: r.Bytes, Evicted: evicted, EvictedBytes: freed}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCacheMoved) || isConflict(err) {
			// Two committers of the same entry: whoever lost sees it kept.
			if cur, gerr := cacheRow(e.db, id); gerr == nil && cur.Status == cacheKept {
				return &CommittedCache{ID: r.ID, Bytes: r.Bytes, Exists: true}, nil
			} else if gerr == nil {
				return nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, cur.Status)
			}
		}
		return nil, err
	}
	if refused != nil {
		_ = e.endCacheByID(r.ID, "", "did not fit in the project's quota")
		return nil, refused
	}
	return out, nil
}

// ---- ending entries ----

// endKeptCache ends a kept entry and gives its bytes back to its project's
// usage. It is conditional on the entry being kept, so of several servers
// ending one entry exactly one does, and the bytes are given back once. It
// returns how many entries it ended (0 or 1) and their size. The caller has
// taken the project's usage row (see CommitCache).
func endKeptCache(tx *txx, id, by, note string, now int64) (int, int64, error) {
	var project string
	var bytes int64
	err := tx.QueryRow(`UPDATE caches SET status = ?, ended_at = ?, ended_by = ? WHERE id = ? AND status = ? RETURNING project, bytes`,
		cacheEnded, now, clipNote(by, note), id, cacheKept).Scan(&project, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	_, err = tx.Exec(`UPDATE cache_usage SET bytes = CASE WHEN bytes > ? THEN bytes - ? ELSE 0 END WHERE project = ?`, bytes, bytes, project)
	return 1, bytes, err
}

func clipNote(by, note string) string {
	switch {
	case by == "":
		return cleanText(note, 200)
	case note == "":
		return by
	}
	return cleanText(by+": "+note, 200)
}

// endCacheByID ends an entry, kept or uploading.
func (e *Engine) endCacheByID(id, by, note string) error {
	r, err := cacheRow(e.db, id)
	if err != nil {
		return err
	}
	return e.artifactTx(func(tx *txx, now int64) error {
		_, err := endCacheTx(tx, r, by, note, now)
		return err
	})
}

// endCacheTx ends an entry in whichever of uploading or kept it is.
func endCacheTx(tx *txx, r *cacheRowT, by, note string, now int64) (bool, error) {
	if _, err := tx.Exec(`INSERT INTO cache_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, r.Project); err != nil {
		return false, err
	}
	n, _, err := endKeptCache(tx, r.ID, by, note, now)
	if err != nil || n == 1 {
		return n == 1, err
	}
	res, err := tx.Exec(`UPDATE caches SET status = ?, ended_at = ?, ended_by = ? WHERE id = ? AND status = ?`,
		cacheEnded, now, clipNote(by, note), r.ID, cacheUploading)
	if err != nil {
		return false, err
	}
	k, _ := res.RowsAffected()
	return k == 1, nil
}

// ---- listing and deleting ----

// CacheEntryView is an entry as listed.
type CacheEntryView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Key        string `json:"key"`
	Scope      string `json:"scope"`
	Platform   string `json:"platform"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	Status     string `json:"status"` // uploading or kept
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Hits       int64  `json:"hits"`
	// RunID and RunNumber are the run that wrote it.
	RunID     string `json:"run_id"`
	RunNumber int    `json:"run_number"`
}

// CacheTotals compares what a project keeps with its quota.
type CacheTotals struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// QuotaBytes is the most the project may keep, MaxEntryBytes the most
	// one entry may hold, and IdleDays how long an entry nobody restored is
	// kept.
	QuotaBytes    int64 `json:"quota_bytes"`
	MaxEntryBytes int64 `json:"max_entry_bytes"`
	IdleDays      int   `json:"idle_days"`
}

// CacheList is a project's caches.
type CacheList struct {
	Project string           `json:"project"`
	Entries []CacheEntryView `json:"entries"`
	Totals  CacheTotals      `json:"totals"`
	Policy  CachePolicyView  `json:"policy"`
}

// Caches lists a project's entries, newest use first, with what it keeps
// against its quota.
func (e *Engine) Caches(project string) (*CacheList, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	lim, err := e.readCacheLimits(e.db, project)
	if err != nil {
		return nil, err
	}
	out := &CacheList{Project: project, Entries: []CacheEntryView{}, Policy: lim.view()}
	rows, err := e.db.Query(`SELECT c.id, c.name, c.key, c.scope, c.platform, c.bytes, c.files, c.status, c.created_at, c.last_used_at, c.hits,
		c.created_run, COALESCE(r.number, 0) FROM caches c LEFT JOIN runs r ON r.id = c.created_run
		WHERE c.project = ? AND c.status IN (?, ?) ORDER BY c.last_used_at DESC, c.created_at DESC, c.id DESC`, project, cacheKept, cacheUploading)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v CacheEntryView
		if err := rows.Scan(&v.ID, &v.Name, &v.Key, &v.Scope, &v.Platform, &v.Bytes, &v.Files, &v.Status, &v.CreatedAt, &v.LastUsedAt, &v.Hits,
			&v.RunID, &v.RunNumber); err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, v)
		if v.Status == cacheKept {
			out.Totals.Entries++
			out.Totals.Bytes += v.Bytes
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Totals.QuotaBytes, out.Totals.MaxEntryBytes, out.Totals.IdleDays = lim.maxProject, lim.maxEntry, int(lim.idle/(24*time.Hour))
	return out, nil
}

// CachesDeleted says what a delete ended.
type CachesDeleted struct {
	Ended int   `json:"ended"`
	Bytes int64 `json:"bytes"`
}

// DeleteCaches ends one entry of a project (by id), or every entry of a
// name (all scopes, keys and platforms). The next run that wanted one
// starts without it. Exactly one of id and name is given.
func (e *Engine) DeleteCaches(project, id, name, actor string) (*CachesDeleted, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if (id == "") == (name == "") {
		return nil, fmt.Errorf("%w: say which entry (id) or which cache (name), not both and not neither", ErrInvalid)
	}
	var victims []*cacheRowT
	if id != "" {
		r, err := cacheRow(e.db, id)
		if err != nil || r.Project != project || (r.Status != cacheKept && r.Status != cacheUploading) {
			return nil, fmt.Errorf("%w: no cache entry %s in %s", ErrNotFound, clipText(id, 60), project)
		}
		victims = append(victims, r)
	} else {
		if !pipeline.ValidArtifactName(name) {
			return nil, fmt.Errorf("%w: %q is not a cache name", ErrInvalid, clipText(name, 60))
		}
		rows, err := e.db.Query(`SELECT `+cacheCols+` FROM caches WHERE project = ? AND name = ? AND status IN (?, ?)`, project, name, cacheKept, cacheUploading)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r, err := scanCache(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			victims = append(victims, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(victims) == 0 {
			return nil, fmt.Errorf("%w: %s has no cache called %s", ErrNotFound, project, name)
		}
	}
	out := &CachesDeleted{}
	for _, r := range victims {
		err := e.artifactTx(func(tx *txx, now int64) error {
			ended, err := endCacheTx(tx, r, actor, "deleted", now)
			if ended {
				out.Ended++
				if r.Status == cacheKept {
					out.Bytes += r.Bytes
				}
			}
			return err
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

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

// ---- the pruner ----

// pruneCaches ends entries idle for longer than their project's policy
// says, uploads that stalled or whose step attempt is gone, and entries
// over a quota that was lowered, and removes the bytes of ended entries.
// Every server does this, possibly at the same moment, which is safe: every
// change of an entry is conditional on its status, so an entry is ended
// once and its bytes given back once, and the bytes of an ended entry are
// removed by the one server whose conditional update claims them (a claim
// whose server stopped is taken over after a while).
func (e *Engine) pruneCaches() {
	if !e.knows(schemaCache) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	now := e.now().UnixMilli()
	if err := e.expireCaches(now); err != nil {
		e.log.Warn("ending idle caches; trying again later", "err", err)
	}
	if err := e.failStaleCacheUploads(now); err != nil {
		e.log.Warn("ending abandoned cache uploads; trying again later", "err", err)
	}
	if err := e.trimCaches(); err != nil {
		e.log.Warn("trimming caches to their quota; trying again later", "err", err)
	}
	if e.arts.Store == nil {
		return
	}
	if err := e.removeCacheBytes(ctx, e.arts.Store, now); err != nil {
		e.log.Warn("removing the bytes of ended caches; trying again later", "err", err)
	}
	e.collectCacheStrays(ctx)
}

// expireCaches ends kept entries not restored for their project's idle
// days.
func (e *Engine) expireCaches(now int64) error {
	projects, err := e.idsOf(`SELECT DISTINCT project FROM caches WHERE status = ?`, cacheKept)
	if err != nil {
		return err
	}
	n := 0
	for _, project := range projects {
		lim, err := e.readCacheLimits(e.db, project)
		if err != nil {
			return err
		}
		cutoff := now - lim.idle.Milliseconds()
		list, err := e.idsOf(`SELECT id FROM caches WHERE project = ? AND status = ? AND last_used_at < ? ORDER BY last_used_at LIMIT ?`,
			project, cacheKept, cutoff, pruneBatchCaches)
		if err != nil {
			return err
		}
		for _, id := range list {
			err := e.artifactTx(func(tx *txx, now int64) error {
				if _, err := tx.Exec(`INSERT INTO cache_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, project); err != nil {
					return err
				}
				k, _, err := endKeptCache(tx, id, "", fmt.Sprintf("not used for %d days", int(lim.idle/(24*time.Hour))), now)
				n += k
				return err
			})
			if err != nil {
				return err
			}
		}
	}
	if n > 0 {
		e.log.Info("idle caches ended", "count", n)
	}
	return nil
}

// failStaleCacheUploads ends uploads that made no progress for 48 hours,
// and uploads whose step attempt no runner holds any more.
func (e *Engine) failStaleCacheUploads(now int64) error {
	list, err := e.idsOf(`SELECT c.id FROM caches c WHERE c.status = ? AND (c.last_used_at < ? OR (c.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.run_id = c.created_run AND l.step_id = c.created_step AND l.attempt = c.created_attempt))) LIMIT ?`,
		cacheUploading, now-cacheStaleUpload.Milliseconds(), now-cacheOrphanedUpload.Milliseconds(), pruneBatchCaches)
	if err != nil {
		return err
	}
	for _, id := range list {
		r, err := cacheRow(e.db, id)
		if err != nil {
			continue
		}
		if err := e.artifactTx(func(tx *txx, now int64) error {
			_, err := endCacheTx(tx, r, "", "the upload did not complete", now)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// trimCaches ends the least recently used entries of projects that keep
// more than their quota allows, which happens when the quota is lowered.
func (e *Engine) trimCaches() error {
	rows, err := e.db.Query(`SELECT project, bytes FROM cache_usage WHERE bytes > 0`)
	if err != nil {
		return err
	}
	type use struct {
		project string
		bytes   int64
	}
	var all []use
	for rows.Next() {
		var u use
		if err := rows.Scan(&u.project, &u.bytes); err != nil {
			rows.Close()
			return err
		}
		all = append(all, u)
	}
	rows.Close()
	for _, u := range all {
		lim, err := e.readCacheLimits(e.db, u.project)
		if err != nil {
			return err
		}
		if u.bytes <= lim.maxProject {
			continue
		}
		err = e.artifactTx(func(tx *txx, now int64) error {
			var used int64
			if err := tx.QueryRow(`SELECT bytes FROM cache_usage WHERE project = ?`, u.project).Scan(&used); err != nil {
				return err
			}
			for used > lim.maxProject {
				var victim string
				err := tx.QueryRow(`SELECT id FROM caches WHERE project = ? AND status = ? ORDER BY last_used_at, created_at, id LIMIT 1`,
					u.project, cacheKept).Scan(&victim)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				_, freed, err := endKeptCache(tx, victim, "", "evicted: the project's caches were over their quota", now)
				if err != nil {
					return err
				}
				used -= freed
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// removeCacheBytes deletes the bytes of ended entries, and then their rows.
func (e *Engine) removeCacheBytes(ctx context.Context, store blobstore.Store, now int64) error {
	type cand struct {
		id, key, upload, status string
		endedAt                 int64
	}
	rows, err := e.db.Query(`SELECT id, blob_key, upload_id, status, ended_at FROM caches
		WHERE (status = ? AND ended_at < ?) OR (status = ? AND ended_at < ?) ORDER BY ended_at LIMIT ?`,
		cacheEnded, now-cacheEndedGrace.Milliseconds(), cacheRemoving, now-cacheOrphanedUpload.Milliseconds(), pruneBatchCaches)
	if err != nil {
		return err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.key, &c.upload, &c.status, &c.endedAt); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	removed := 0
	for _, c := range cands {
		var claimed bool
		err := e.artifactTx(func(tx *txx, now int64) error {
			res, err := tx.Exec(`UPDATE caches SET status = ?, ended_at = ? WHERE id = ? AND status = ? AND ended_at = ?`,
				cacheRemoving, now, c.id, c.status, c.endedAt)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			claimed = k == 1
			return nil
		})
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if c.upload != "" {
			if err := store.AbortUpload(ctx, c.key, c.upload); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
				return fmt.Errorf("abandoning the upload of cache entry %s: %w", c.id, err)
			}
		}
		if err := store.Delete(ctx, c.key); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("deleting the bytes of cache entry %s: %w", c.id, err)
		}
		err = e.artifactTx(func(tx *txx, now int64) error {
			_, err := tx.Exec(`DELETE FROM caches WHERE id = ? AND status = ?`, c.id, cacheRemoving)
			return err
		})
		if err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed the bytes of ended caches", "count", removed)
	}
	return nil
}

// collectCacheStrays deletes objects under caches/ that no entry refers to
// (left by a server that stopped between writing the bytes and recording
// the entry), in the database store, which can list its keys. Like
// removeStrays, it reads keys in pages from where the last pass stopped.
func (e *Engine) collectCacheStrays(ctx context.Context) {
	d, ok := e.arts.Store.(*blobstore.Database)
	if !ok {
		return
	}
	if err := e.removeCacheStrays(ctx, d); err != nil {
		e.log.Warn("removing objects of the database store that no cache entry refers to; trying again later", "err", err)
	}
}

func (e *Engine) removeCacheStrays(ctx context.Context, d *blobstore.Database) error {
	keys, err := d.Keys(ctx, "caches/", e.cacheStrayAfter, pruneBatchCaches)
	if err != nil {
		return err
	}
	if len(keys) < pruneBatchCaches {
		e.cacheStrayAfter = "" // start over next time
	} else {
		e.cacheStrayAfter = keys[len(keys)-1]
	}
	cutoff := e.now().Add(-strayGrace).UnixMilli()
	removed := 0
	for _, k := range keys {
		var one int
		err := e.db.QueryRow(`SELECT 1 FROM caches WHERE blob_key = ?`, k).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var created int64
		if err := e.db.QueryRow(`SELECT created_at FROM blob_objects WHERE key = ?`, k).Scan(&created); err != nil || created > cutoff {
			continue
		}
		if err := d.Delete(ctx, k); err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed objects of the database store that no cache entry refers to", "count", removed)
	}
	return nil
}
