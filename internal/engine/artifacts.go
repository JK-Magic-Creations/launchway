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
	"path"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Artifacts. A step's artifacts (artifacts: in its pipeline, and the SBOM
// files of reports.sbom) are files its runner keeps once the step has run.
// The runner copies each one out of the workspace, hashing it, and uploads
// it in chunks:
//
//  1. BeginArtifact declares the artifact and its files (path, size,
//     SHA-256, mode). The server checks the limits, reserves the bytes
//     against the project's quota, and links files whose content it already
//     keeps (found by SHA-256 and size) so that they are not sent again. It
//     answers with how much of each file it has, so a runner that asks
//     again after a failure carries on where it was.
//  2. PutArtifactChunk sends one chunk of a file, in order, with its
//     SHA-256. Any server may take any chunk: the state of each upload
//     (bytes received, the running SHA-256 of the content so far, the parts
//     stored) is in artifact_blobs, and the blob store keeps the parts. When
//     the last chunk arrives, the content's SHA-256 is checked against the
//     one declared, and the blob store completes the object.
//  3. CommitArtifact checks every file is stored and marks the artifact
//     kept until it expires.
//
// Contents are kept once per upload under a key of their own
// (artifacts/sha256/<ab>/<sha256>/<blob id>), and shared by every file with
// the same content that is declared while they are kept. Removing contents
// is safe with several servers: a blob is removed only once no kept or
// uploading artifact refers to it and it has not been used for
// blobGraceMs, by the one server whose conditional update marks it
// "deleting"; declaring or committing a file that uses a blob refreshes its
// used_at in the same conditional way, so the two can never cross (see
// pruneArtifacts).
//
// Artifacts are not masked: secrets in a step's output are hidden in its
// log, but files it keeps are kept as they are.

// ArtifactOptions configure artifacts. Zero fields take the defaults.
type ArtifactOptions struct {
	// Store keeps the contents. Nil means this server keeps no artifacts:
	// steps that declare some are told so, and fetches fail.
	Store blobstore.Store
	// StoreProblem says why Store is nil, for those messages.
	StoreProblem string
	// Choice says how the store is chosen: StoreGiven (Store as it is),
	// StoreDatabase (the database the engine opens), or StoreAuto (nothing
	// configured: the database on PostgreSQL, Store otherwise; see
	// chooseStore).
	Choice string
	// DatabaseLimits are the database store's limits (its WriteLock and Now
	// are the engine's).
	DatabaseLimits blobstore.DatabaseOptions
	// MaxFileBytes is the largest file an artifact may hold. Default 1 GiB.
	MaxFileBytes int64
	// MaxRunBytes is how many bytes of artifacts one run may keep. Default
	// 4 GiB.
	MaxRunBytes int64
	// MaxProjectBytes is how many bytes of artifacts a project may keep
	// over all its runs. Default 50 GiB.
	MaxProjectBytes int64
	// DefaultRetain is how long an artifact is kept when its pipeline does
	// not say; default 30 days. MaxRetain is the longest any is kept;
	// default 90 days.
	DefaultRetain time.Duration
	MaxRetain     time.Duration
}

// Defaults for ArtifactOptions.
const (
	DefaultArtifactFileBytes    = 1 << 30
	DefaultArtifactRunBytes     = 4 << 30
	DefaultArtifactProjectBytes = 50 << 30
	DefaultArtifactRetain       = 30 * 24 * time.Hour
	DefaultArtifactMaxRetain    = 90 * 24 * time.Hour
)

// ArtifactChunkBytes is the size of every chunk of an upload but the last.
// It is at least the blob store's smallest part.
const ArtifactChunkBytes = 8 << 20

// MaxArtifactFiles is how many files one artifact may hold.
const MaxArtifactFiles = 10000

// maxArtifactPath is the longest path of a file in an artifact.
const maxArtifactPath = 1024

// blobGraceMs is how long a blob nothing refers to is kept before it is
// removed: long enough for a file declared with it to be committed.
const blobGraceMs = int64(time.Hour / time.Millisecond)

// staleUploadMs is how long an upload may make no progress before it is
// abandoned.
const staleUploadMs = int64(24 * time.Hour / time.Millisecond)

func (o ArtifactOptions) withDefaults() (ArtifactOptions, error) {
	if o.MaxFileBytes == 0 {
		o.MaxFileBytes = DefaultArtifactFileBytes
	}
	if o.MaxRunBytes == 0 {
		o.MaxRunBytes = DefaultArtifactRunBytes
	}
	if o.MaxProjectBytes == 0 {
		o.MaxProjectBytes = DefaultArtifactProjectBytes
	}
	if o.DefaultRetain == 0 {
		o.DefaultRetain = DefaultArtifactRetain
	}
	if o.MaxRetain == 0 {
		o.MaxRetain = DefaultArtifactMaxRetain
	}
	switch {
	case o.MaxFileBytes < 0 || o.MaxRunBytes < 0 || o.MaxProjectBytes < 0 || o.DefaultRetain < 0 || o.MaxRetain < 0:
		return o, errors.New("artifact limits cannot be negative")
	case o.MaxRunBytes < o.MaxFileBytes:
		return o, fmt.Errorf("artifacts: max_run_mib (%s) must be at least max_file_mib (%s)", sizeText(o.MaxRunBytes), sizeText(o.MaxFileBytes))
	case o.MaxProjectBytes < o.MaxRunBytes:
		return o, fmt.Errorf("artifacts: the project quota (%s) must be at least max_run_mib (%s)", sizeText(o.MaxProjectBytes), sizeText(o.MaxRunBytes))
	case o.DefaultRetain < pipeline.MinArtifactRetain || o.MaxRetain < o.DefaultRetain:
		return o, fmt.Errorf("artifacts: retain must be at least 1h, and max_retain at least retain")
	}
	if o.Store == nil && o.StoreProblem == "" {
		o.StoreProblem = "no artifact store is configured"
	}
	return o, nil
}

// Artifact statuses, in artifacts.status.
const (
	artUploading = "uploading"
	artKept      = "kept"
	artFailed    = "failed"  // never completed, or its attempt was lost
	artExpired   = "expired" // its retention ended
	artDeleted   = "deleted" // someone deleted it
)

// Blob states, in artifact_blobs.state.
const (
	blobUploading = "uploading"
	blobStored    = "stored"
	blobDeleting  = "deleting"
)

// emptyBlob stands for the content of every empty file: nothing is stored.
const emptyBlob = "empty"

// ErrQuota means an artifact would go beyond a limit on how much is kept.
var ErrQuota = errors.New("too large")

// ChunkOffsetError says a chunk did not start where the upload is; the
// runner should send from Received.
type ChunkOffsetError struct{ Received int64 }

func (e *ChunkOffsetError) Error() string {
	return fmt.Sprintf("%v: the upload has %d bytes; send the chunk that starts there", ErrConflict, e.Received)
}

func (e *ChunkOffsetError) Unwrap() error { return ErrConflict }

// ArtifactFileInfo describes one file of an artifact.
type ArtifactFileInfo struct {
	Path   string `json:"path"` // relative to the workspace, with /
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // hex
	Mode   uint32 `json:"mode"`   // 0644, or 0755 for a file someone may run
}

// BeginArtifact is what a runner declares before uploading an artifact.
type BeginArtifact struct {
	Name  string             `json:"name"`
	Files []ArtifactFileInfo `json:"files"`
}

// BegunArtifact says how far each file of an artifact has been uploaded.
type BegunArtifact struct {
	ID         string      `json:"id"`
	ChunkBytes int64       `json:"chunk_bytes"`
	Files      []BegunFile `json:"files"`
}

// BegunFile is one file's progress: Received bytes, Done once the server
// has its content (sent, or already kept from before).
type BegunFile struct {
	Path     string `json:"path"`
	Received int64  `json:"received"`
	Done     bool   `json:"done"`
}

// JobArtifacts says what a runner keeps after a step, within which limits.
type JobArtifacts struct {
	Keep         []JobArtifact `json:"keep"`
	MaxFileBytes int64         `json:"max_file_bytes"`
	MaxBytes     int64         `json:"max_bytes"` // for one run
	ChunkBytes   int64         `json:"chunk_bytes"`
	// Problem, when not empty, is why none can be kept on this server.
	Problem string `json:"problem,omitempty"`
}

// JobArtifact is one artifact to keep after a step.
type JobArtifact struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	// When is success, failure or always (see pipeline.Artifact.KeepAfter).
	When string `json:"when"`
	// MaxFiles and MaxBytes, when set, are tighter limits for this one
	// artifact (test attachments and HTML reports, schema 18), and Label
	// what the step's log calls it. Older runners ignore them; the server
	// enforces the limits anyway.
	MaxFiles int    `json:"max_files,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
	Label    string `json:"label,omitempty"`
}

// KeepAfter reports whether the artifact is kept after an attempt that
// ended with outcome.
func (a JobArtifact) KeepAfter(outcome string) bool {
	return (&pipeline.Artifact{When: a.When}).KeepAfter(outcome)
}

// JobFetch is an artifact a runner puts in the workspace before a step.
type JobFetch struct {
	From       string             `json:"from"`
	Name       string             `json:"name"`
	Into       string             `json:"into,omitempty"`
	ArtifactID string             `json:"artifact_id,omitempty"`
	Files      []ArtifactFileInfo `json:"files,omitempty"`
	// Problem, when not empty, says why it cannot be fetched; the step then
	// fails, saying so.
	Problem string `json:"problem,omitempty"`
}

// ArtifactSummary is an artifact as recorded in the event that ends its
// step attempt, and as listed.
type ArtifactSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind,omitempty"` // "sbom" for the SBOM files of reports.sbom
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// ---- configuration of a job ----

// jobArtifacts says what a runner keeps after a step, or nil if nothing.
func (c *cmd) jobArtifacts(def *pipeline.Step) *JobArtifacts {
	if !c.e.knows(schemaArtifacts) {
		return nil
	}
	var keep []JobArtifact
	for _, a := range def.Artifacts {
		keep = append(keep, JobArtifact{Name: a.Name, Paths: append([]string(nil), a.Paths...), When: nonEmpty(a.When, pipeline.ArtifactOnSuccess)})
	}
	if def.Reports != nil && len(def.Reports.SBOM) > 0 {
		keep = append(keep, JobArtifact{Name: pipeline.SBOMArtifact, Paths: append([]string(nil), def.Reports.SBOM...), When: pipeline.ArtifactAlways})
	}
	keep = append(keep, c.reportArtifacts(def)...)
	if len(keep) == 0 {
		return nil
	}
	o := c.e.ArtifactOptions()
	ja := &JobArtifacts{Keep: keep, MaxFileBytes: o.MaxFileBytes, MaxBytes: o.MaxRunBytes, ChunkBytes: ArtifactChunkBytes}
	if o.Store == nil {
		ja.Problem = "this server keeps no artifacts: " + o.StoreProblem
	}
	return ja
}

// addArtifactsToJob fills in what a runner keeps after the step and what
// it fetches before it. It runs in the transaction that leases the step.
func (c *cmd) addArtifactsToJob(job *Job) error {
	if !c.e.knows(schemaArtifacts) {
		return nil
	}
	def := c.st.Pipeline.Step(job.StepID)
	if def == nil {
		return nil
	}
	job.Artifacts = c.jobArtifacts(def)
	for _, f := range stepFetches(c.st, def) {
		if jf, skip, err := c.resolveStepFetch(def, f); err != nil {
			return err
		} else if !skip {
			job.Fetch = append(job.Fetch, jf)
		}
	}
	return nil
}

// fetchSource is the step attempt whose artifacts a fetch reads: the
// attempt of the step that succeeded in this run, or, for a step a re-run
// reused, the attempt in the run where it ran.
//
// A step that runs after a failure (when: { upstream: failed | always })
// may fetch from a step that failed: from its last attempt, which keeps
// what its artifacts' when: failure or always says.
func fetchSource(st *RunState, def *pipeline.Step, from string, member bool) (run string, attempt int, problem string) {
	s := st.Steps[from]
	switch {
	case s == nil:
		return "", 0, from + " is not a step of this run"
	case s.ReusedFrom != "":
		return s.ReusedFrom, s.ReusedAttempt, ""
	case s.Status == StepFailed && def != nil && (def.When.UpstreamRule() != pipeline.UpstreamSucceeded || (member && s.Allowed)):
		return st.ID, s.Attempt, ""
	case s.Status != StepSucceeded:
		return "", 0, fmt.Sprintf("%s has not succeeded (it is %s), so it kept nothing to fetch", from, s.Status)
	}
	return st.ID, s.Attempt, ""
}

// resolveFetch finds the artifact a fetch names, and its files.
func (c *cmd) resolveFetch(def *pipeline.Step, f *pipeline.Fetch, member bool) (JobFetch, error) {
	jf := JobFetch{From: f.From, Name: f.Name, Into: f.Into}
	if c.e.arts.Store == nil {
		jf.Problem = "this server keeps no artifacts: " + c.e.arts.StoreProblem
		return jf, nil
	}
	run, attempt, problem := fetchSource(c.st, def, f.From, member)
	if problem != "" {
		jf.Problem = problem
		return jf, nil
	}
	a, err := findArtifact(c.tx, run, f.From, attempt, f.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		jf.Problem = fmt.Sprintf("%s kept no artifact %s (its files matched nothing, or could not be kept: its log says why)", f.From, f.Name)
		return jf, nil
	case err != nil:
		return jf, err
	case a.Status != artKept:
		jf.Problem = fmt.Sprintf("%s's artifact %s is %s", f.From, f.Name, a.statusText())
		return jf, nil
	}
	files, err := artifactFiles(c.tx, a.ID)
	if err != nil {
		return jf, err
	}
	jf.ArtifactID = a.ID
	for _, fl := range files {
		jf.Files = append(jf.Files, fl.ArtifactFileInfo)
	}
	return jf, nil
}

// ---- rows ----

type artifactRow struct {
	ID, RunID, StepID string
	Attempt           int
	Project           string
	RunNumber         int
	Name, Kind        string
	Status            string
	Files             int
	Bytes             int64
	RetainMs          int64
	CreatedAt         int64
	KeptAt            int64
	ExpiresAt         int64
	EndedAt           int64
	EndedBy           string
	Note              string
}

const artifactCols = `id, run_id, step_id, attempt, project, run_number, name, kind, status, files, bytes, retain_ms,
	created_at, kept_at, expires_at, ended_at, ended_by, note`

func scanArtifact(r interface{ Scan(...any) error }) (*artifactRow, error) {
	a := &artifactRow{}
	err := r.Scan(&a.ID, &a.RunID, &a.StepID, &a.Attempt, &a.Project, &a.RunNumber, &a.Name, &a.Kind, &a.Status, &a.Files,
		&a.Bytes, &a.RetainMs, &a.CreatedAt, &a.KeptAt, &a.ExpiresAt, &a.EndedAt, &a.EndedBy, &a.Note)
	return a, err
}

func (a *artifactRow) statusText() string {
	switch a.Status {
	case artExpired:
		return "no longer kept: it expired"
	case artDeleted:
		return "no longer kept: " + nonEmpty(a.EndedBy, "someone") + " deleted it"
	case artFailed:
		return "not kept: " + nonEmpty(a.Note, "its upload did not complete")
	case artUploading:
		return "still being uploaded"
	}
	return a.Status
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func findArtifact(q querier, run, step string, attempt int, name string) (*artifactRow, error) {
	a, err := scanArtifact(q.QueryRow(`SELECT `+artifactCols+` FROM artifacts WHERE run_id = ? AND step_id = ? AND attempt = ? AND name = ?`,
		run, step, attempt, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, name)
	}
	return a, err
}

func artifactByID(q querier, id string) (*artifactRow, error) {
	a, err := scanArtifact(q.QueryRow(`SELECT `+artifactCols+` FROM artifacts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, id)
	}
	return a, err
}

type artifactFile struct {
	ArtifactFileInfo
	BlobID string
}

func artifactFiles(q querier, artifactID string) ([]artifactFile, error) {
	rows, err := q.Query(`SELECT path, size, sha256, mode, blob_id FROM artifact_files WHERE artifact_id = ? ORDER BY path`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []artifactFile
	for rows.Next() {
		var f artifactFile
		if err := rows.Scan(&f.Path, &f.Size, &f.SHA256, &f.Mode, &f.BlobID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

type blobRow struct {
	ID, SHA256 string
	Size       int64
	Key, State string
	UploadID   string
	Received   int64
	HashState  string
	Parts      []blobstore.Part
	UsedAt     int64
}

func getBlob(q querier, id string) (*blobRow, error) {
	b := &blobRow{}
	var parts string
	err := q.QueryRow(`SELECT id, sha256, size, store_key, state, upload_id, received, hash_state, parts, used_at
		FROM artifact_blobs WHERE id = ?`, id).Scan(&b.ID, &b.SHA256, &b.Size, &b.Key, &b.State, &b.UploadID, &b.Received,
		&b.HashState, &parts, &b.UsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: blob %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(parts), &b.Parts); err != nil {
		return nil, fmt.Errorf("blob %s: reading its parts: %w", id, err)
	}
	return b, nil
}

// blobKey is where a blob's content is kept in the store.
func blobKey(sum, id string) string {
	return "artifacts/sha256/" + sum[:2] + "/" + sum + "/" + strings.ReplaceAll(id, "_", "-")
}

// ---- checks on what runners send ----

// CheckArtifactPath checks a path of a file in an artifact, as a runner
// declares it: relative, with /, no . or .. parts, printable.
func CheckArtifactPath(p string) error {
	switch {
	case p == "" || len(p) > maxArtifactPath:
		return fmt.Errorf("%w: a file path must have 1 to %d bytes", ErrInvalid, maxArtifactPath)
	case !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) || strings.Contains(p, "\\"):
		return fmt.Errorf("%w: %q is not a path", ErrInvalid, p)
	case strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%w: %q is not a clean path relative to the workspace", ErrInvalid, p)
	}
	return nil
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

// leaseFor finds a lease, failing with ErrLeaseLost if it is gone, and
// renews it: an upload in progress keeps it alive like a heartbeat.
func (e *Engine) leaseFor(leaseID string) (leaseRef, error) {
	unlock := e.writeLock()
	defer unlock()
	var l leaseRef
	err := e.db.QueryRow(`UPDATE leases SET expires_at = ? WHERE id = ? RETURNING run_id, step_id, attempt`,
		e.now().Add(e.ttl).UnixMilli(), leaseID).Scan(&l.runID, &l.stepID, &l.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrLeaseLost
	}
	return l, err
}

// storeOrErr returns the blob store, or why there is none.
func (e *Engine) storeOrErr() (blobstore.Store, error) {
	if e.arts.Store == nil {
		return nil, fmt.Errorf("%w: this server keeps no artifacts: %s", ErrConflict, e.arts.StoreProblem)
	}
	return e.arts.Store, nil
}

// ---- uploading ----

// BeginArtifact declares an artifact of the step attempt a lease holds, or
// finds the one declared before with the same files, and says how much of
// each file the server has.
func (e *Engine) BeginArtifact(leaseID string, in BeginArtifact) (*BegunArtifact, error) {
	if !e.knows(schemaArtifacts) {
		return nil, fmt.Errorf("%w: this server does not know artifacts", ErrConflict)
	}
	if _, err := e.storeOrErr(); err != nil {
		return nil, err
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
	kind, retain := "", time.Duration(0)
	maxFiles, maxBytes := MaxArtifactFiles, e.live().arts.MaxRunBytes
	switch rk, rf, rb := e.reportArtifactKind(def, in.Name); {
	case in.Name == pipeline.SBOMArtifact && def.Reports != nil && len(def.Reports.SBOM) > 0:
		kind, retain = "sbom", 0
	case rk != "":
		kind, maxFiles, maxBytes = rk, rf, min(rb, e.live().arts.MaxRunBytes)
	default:
		found := false
		for _, a := range def.Artifacts {
			if a.Name == in.Name {
				found, retain = true, a.Retain.D()
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: step %s declares no artifact %q", ErrInvalid, l.stepID, in.Name)
		}
	}
	if retain == 0 {
		retain = e.live().arts.DefaultRetain
	}
	retain = min(retain, e.live().arts.MaxRetain)
	switch n := len(in.Files); {
	case n == 0:
		return nil, fmt.Errorf("%w: an artifact needs at least one file", ErrInvalid)
	case n > maxFiles:
		return nil, fmt.Errorf("%w: %s holds at most %d files; this one has %d", ErrQuota, artifactWhat(kind), maxFiles, n)
	}
	files := slices.Clone(in.Files)
	slices.SortFunc(files, func(a, b ArtifactFileInfo) int { return strings.Compare(a.Path, b.Path) })
	var total int64
	for i, f := range files {
		if err := CheckArtifactPath(f.Path); err != nil {
			return nil, err
		}
		if i > 0 && files[i-1].Path == f.Path {
			return nil, fmt.Errorf("%w: %s is listed twice", ErrInvalid, f.Path)
		}
		switch {
		case f.Size < 0:
			return nil, fmt.Errorf("%w: %s has a negative size", ErrInvalid, f.Path)
		case f.Size > e.maxFileBytes():
			return nil, fmt.Errorf("%w: %s is %s; the server keeps files of at most %s", ErrQuota, f.Path, sizeText(f.Size), sizeText(e.maxFileBytes()))
		case !validSHA256(f.SHA256):
			return nil, fmt.Errorf("%w: %s: the SHA-256 must be 64 lower-case hex digits", ErrInvalid, f.Path)
		}
		files[i].Mode = 0o644
		if f.Mode&0o111 != 0 {
			files[i].Mode = 0o755
		}
		total += f.Size
	}
	if total > maxBytes {
		if kind != "" && kind != "sbom" {
			return nil, fmt.Errorf("%w: %s is %s; at most %s is kept", ErrQuota, artifactWhat(kind), sizeText(total), sizeText(maxBytes))
		}
		return nil, fmt.Errorf("%w: the artifact is %s; a run keeps at most %s", ErrQuota, sizeText(total), sizeText(e.live().arts.MaxRunBytes))
	}
	if u, ok := e.arts.Store.(blobstore.UsageReporter); ok {
		// Say so before anything is sent, not half-way through.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		usage, err := u.Usage(ctx)
		cancel()
		if err == nil && usage.LimitBytes > 0 && usage.UsedBytes+total > usage.LimitBytes {
			return nil, fmt.Errorf("%w: the %s artifact store is full: it holds %s of its limit of %s, and this artifact is %s. "+
				"Delete artifacts or shorten their retention, raise the limit (artifacts.database.max_total_gib), or move to S3",
				ErrQuota, e.arts.Store.Name(), sizeText(usage.UsedBytes), sizeText(usage.LimitBytes), sizeText(total))
		}
	}

	var out *BegunArtifact
	err = e.artifactTx(func(tx *txx, now int64) error {
		out = nil
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		if a, err := findArtifact(tx, l.runID, l.stepID, l.attempt, in.Name); err == nil {
			// Declared before: the same files carry on; anything else is a
			// mistake.
			if a.Status != artUploading {
				return fmt.Errorf("%w: artifact %s of this attempt is already %s", ErrConflict, in.Name, a.Status)
			}
			if err := resetLostContent(tx, a.ID); err != nil {
				return err
			}
			have, err := artifactFiles(tx, a.ID)
			if err != nil {
				return err
			}
			if len(have) != len(files) {
				return fmt.Errorf("%w: artifact %s was declared with other files", ErrConflict, in.Name)
			}
			for i, h := range have {
				if h.ArtifactFileInfo != files[i] {
					return fmt.Errorf("%w: artifact %s was declared with other files (%s differs)", ErrConflict, in.Name, h.Path)
				}
			}
			out, err = progress(tx, a.ID, have)
			return err
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO artifact_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, st.Project); err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE artifact_usage SET bytes = bytes + ? WHERE project = ? AND bytes + ? <= ?`,
			total, st.Project, total, e.live().arts.MaxProjectBytes)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var used int64
			_ = tx.QueryRow(`SELECT bytes FROM artifact_usage WHERE project = ?`, st.Project).Scan(&used)
			return fmt.Errorf("%w: project %s keeps %s of artifacts; with this one's %s it would be over its quota of %s",
				ErrQuota, st.Project, sizeText(used), sizeText(total), sizeText(e.live().arts.MaxProjectBytes))
		}
		// The project's row is now locked until the end of this
		// transaction, so the run's total below cannot change underneath.
		var runBytes int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(bytes), 0) FROM artifacts WHERE run_id = ? AND status IN (?, ?)`,
			l.runID, artUploading, artKept).Scan(&runBytes); err != nil {
			return err
		}
		if runBytes+total > e.live().arts.MaxRunBytes {
			return fmt.Errorf("%w: the run keeps %s of artifacts; with this one's %s it would be over its limit of %s",
				ErrQuota, sizeText(runBytes), sizeText(total), sizeText(e.live().arts.MaxRunBytes))
		}
		id := ids.New("art")
		if _, err := tx.Exec(`INSERT INTO artifacts(id, run_id, step_id, attempt, project, run_number, name, kind, status, files, bytes,
				retain_ms, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, l.runID, l.stepID, l.attempt, st.Project, st.Number, in.Name, kind, artUploading, len(files), total,
			retain.Milliseconds(), now); err != nil {
			return err
		}
		have := make([]artifactFile, len(files))
		for i, f := range files {
			blob := ""
			if f.Size == 0 {
				blob = emptyBlob
			} else {
				// Content kept already is not sent again. Refreshing used_at
				// only while the blob is stored keeps it from being removed
				// before this artifact is committed (see pruneArtifacts).
				// Only content in this server's store: what is recorded in
				// another (before artifacts.store changed, and not copied yet)
				// cannot be read here. Content from before schema 17 recorded
				// no store; it is in this one unless this is the database store,
				// which did not exist then.
				q, args := `SELECT id FROM artifact_blobs WHERE sha256 = ? AND size = ? AND state = ?`, []any{f.SHA256, f.Size, blobStored}
				if e.knows(schemaBlobs) {
					q += ` AND (store = ? OR (store = '' AND ? <> 'database'))`
					args = append(args, e.arts.Store.Name(), e.arts.Store.Name())
				}
				var cand string
				err := tx.QueryRow(q+` ORDER BY created_at LIMIT 1`, args...).Scan(&cand)
				switch {
				case errors.Is(err, sql.ErrNoRows):
				case err != nil:
					return err
				default:
					res, err := tx.Exec(`UPDATE artifact_blobs SET used_at = ? WHERE id = ? AND state = ?`, now, cand, blobStored)
					if err != nil {
						return err
					}
					if n, _ := res.RowsAffected(); n == 1 {
						blob = cand
					}
				}
			}
			if _, err := tx.Exec(`INSERT INTO artifact_files(artifact_id, path, size, sha256, mode, blob_id) VALUES(?,?,?,?,?,?)`,
				id, f.Path, f.Size, f.SHA256, f.Mode, blob); err != nil {
				return err
			}
			have[i] = artifactFile{ArtifactFileInfo: f, BlobID: blob}
		}
		out, err = progress(tx, id, have)
		return err
	})
	return out, err
}

// resetLostContent forgets, for an artifact's files, content that is being
// or has been removed, so that those files are sent again.
func resetLostContent(tx *txx, artifactID string) error {
	_, err := tx.Exec(`UPDATE artifact_files SET blob_id = '' WHERE artifact_id = ? AND blob_id NOT IN ('', ?)
		AND blob_id NOT IN (SELECT id FROM artifact_blobs WHERE state IN (?, ?))`, artifactID, emptyBlob, blobUploading, blobStored)
	return err
}

// progress says how much of each file the server has.
func progress(q querier, id string, files []artifactFile) (*BegunArtifact, error) {
	out := &BegunArtifact{ID: id, ChunkBytes: ArtifactChunkBytes, Files: make([]BegunFile, len(files))}
	for i, f := range files {
		bf := BegunFile{Path: f.Path}
		switch f.BlobID {
		case "":
		case emptyBlob:
			bf.Done = true
		default:
			b, err := getBlob(q, f.BlobID)
			switch {
			case errors.Is(err, ErrNotFound):
			case err != nil:
				return nil, err
			case b.State == blobStored:
				bf.Received, bf.Done = b.Size, true
			case b.State == blobUploading:
				bf.Received = b.Received
			}
		}
		out.Files[i] = bf
	}
	return out, nil
}

// leaseHeld fails with ErrLeaseLost if the lease no longer exists.
func leaseHeld(tx *txx, leaseID string) error {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM leases WHERE id = ?`, leaseID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

// artifactTx runs fn in a transaction, retrying on conflicts between
// servers.
func (e *Engine) artifactTx(fn func(tx *txx, now int64) error) error {
	for attempt := 1; ; attempt++ {
		err := func() error {
			unlock := e.writeLock()
			defer unlock()
			tx, err := e.db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := fn(tx, e.now().UnixMilli()); err != nil {
				return err
			}
			return tx.Commit()
		}()
		if err == nil || !isConflict(err) || attempt == maxAttempts {
			return err
		}
		time.Sleep(time.Duration(attempt) * 3 * time.Millisecond)
	}
}

// uploadingFile is a file of an artifact being uploaded, checked against the
// lease that sends it.
func (e *Engine) uploadingFile(l leaseRef, artifactID, filePath string) (*artifactRow, *artifactFile, error) {
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return nil, nil, err
	}
	if a.RunID != l.runID || a.StepID != l.stepID || a.Attempt != l.attempt {
		return nil, nil, fmt.Errorf("%w: artifact %s is not of this step attempt", ErrNotFound, artifactID)
	}
	if a.Status != artUploading {
		return nil, nil, fmt.Errorf("%w: artifact %s is %s", ErrConflict, a.Name, a.Status)
	}
	f := &artifactFile{}
	err = e.db.QueryRow(`SELECT path, size, sha256, mode, blob_id FROM artifact_files WHERE artifact_id = ? AND path = ?`,
		artifactID, filePath).Scan(&f.Path, &f.Size, &f.SHA256, &f.Mode, &f.BlobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: artifact %s has no file %s", ErrNotFound, a.Name, filePath)
	}
	return a, f, err
}

// PutArtifactChunk stores one chunk of a file of an artifact: the bytes
// from offset, whose SHA-256 is sum. It returns how many bytes of the file
// the server has. A chunk the server has already is accepted again without
// effect; one that does not start where the upload is gives a
// *ChunkOffsetError.
func (e *Engine) PutArtifactChunk(ctx context.Context, leaseID, artifactID, filePath string, offset int64, sum string, data []byte) (int64, error) {
	store, err := e.storeOrErr()
	if err != nil {
		return 0, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return 0, fmt.Errorf("%w: the chunk does not match its SHA-256; it was damaged on the way", ErrInvalid)
	}
	if len(data) == 0 || len(data) > ArtifactChunkBytes || offset < 0 || offset%ArtifactChunkBytes != 0 {
		return 0, fmt.Errorf("%w: a chunk starts at a multiple of %d bytes and holds 1 to %d bytes", ErrInvalid, ArtifactChunkBytes, ArtifactChunkBytes)
	}
	l, err := e.leaseFor(leaseID)
	if err != nil {
		return 0, err
	}
	_, f, err := e.uploadingFile(l, artifactID, filePath)
	if err != nil {
		return 0, err
	}
	end := offset + int64(len(data))
	if end > f.Size || (end < f.Size && len(data) != ArtifactChunkBytes) {
		return 0, fmt.Errorf("%w: every chunk but the last holds %d bytes, and none goes past the file's %d bytes", ErrInvalid, ArtifactChunkBytes, f.Size)
	}
	switch f.BlobID {
	case emptyBlob:
		return 0, nil
	case "":
		if offset != 0 {
			return 0, &ChunkOffsetError{Received: 0}
		}
		if f.BlobID, err = e.startBlob(ctx, store, artifactID, f); err != nil {
			return 0, err
		}
	}
	b, err := getBlob(e.db, f.BlobID)
	if err != nil {
		return 0, err
	}
	switch {
	case b.State == blobStored:
		return b.Size, nil
	case b.State != blobUploading:
		return 0, fmt.Errorf("%w: the upload of %s was abandoned; declare the artifact again", ErrConflict, filePath)
	case b.Received >= end:
		if b.Received == b.Size {
			return b.Size, e.completeBlob(ctx, store, b)
		}
		return b.Received, nil
	case offset != b.Received:
		return 0, &ChunkOffsetError{Received: b.Received}
	}
	h := sha256.New()
	if b.HashState != "" {
		raw, err := base64.StdEncoding.DecodeString(b.HashState)
		if err == nil {
			err = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(raw)
		}
		if err != nil {
			return 0, fmt.Errorf("blob %s: reading the state of its SHA-256: %w", b.ID, err)
		}
	}
	h.Write(data)
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return 0, err
	}
	n := int(offset/ArtifactChunkBytes) + 1
	etag, err := store.UploadPart(ctx, b.Key, b.UploadID, n, data)
	if errors.Is(err, blobstore.ErrFull) || errors.Is(err, blobstore.ErrTooLarge) {
		return 0, fmt.Errorf("%w: storing %s: %v", ErrQuota, filePath, err)
	}
	if err != nil {
		return 0, fmt.Errorf("storing part %d of %s: %w", n, filePath, err)
	}
	parts := append(slices.Clone(b.Parts), blobstore.Part{Number: n, ETag: etag})
	partsJSON, _ := json.Marshal(parts)
	moved := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		res, err := tx.Exec(`UPDATE artifact_blobs SET received = ?, hash_state = ?, parts = ?, used_at = ?
			WHERE id = ? AND state = ? AND received = ?`,
			end, base64.StdEncoding.EncodeToString(state), string(partsJSON), now, b.ID, blobUploading, b.Received)
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
		// Another server took the same chunk at the same moment.
		nb, err := getBlob(e.db, b.ID)
		if err != nil {
			return 0, err
		}
		if nb.State == blobStored || nb.Received >= end {
			return nb.Received, nil
		}
		return 0, &ChunkOffsetError{Received: nb.Received}
	}
	if end < b.Size {
		return end, nil
	}
	if hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
		// The file as received is not the file declared. Start it again.
		_ = e.artifactTx(func(tx *txx, now int64) error {
			if _, err := tx.Exec(`UPDATE artifact_files SET blob_id = '' WHERE artifact_id = ? AND path = ? AND blob_id = ?`,
				artifactID, filePath, b.ID); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE artifact_blobs SET state = ?, used_at = ? WHERE id = ? AND state = ?`, blobDeleting, now, b.ID, blobUploading)
			return err
		})
		_ = store.AbortUpload(ctx, b.Key, b.UploadID)
		return 0, fmt.Errorf("%w: checksum mismatch: the content of %s does not have the SHA-256 it was declared with; send it again from the start", ErrInvalid, filePath)
	}
	b.Received, b.Parts = end, parts
	return b.Size, e.completeBlob(ctx, store, b)
}

// startBlob begins the upload of a file's content, unless another server
// has just done so: it returns the blob the file uses.
func (e *Engine) startBlob(ctx context.Context, store blobstore.Store, artifactID string, f *artifactFile) (string, error) {
	id := ids.New("blob")
	key := blobKey(f.SHA256, id)
	uploadID, err := store.CreateUpload(ctx, key)
	if err != nil {
		return "", fmt.Errorf("starting the upload of %s: %w", f.Path, err)
	}
	won := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		res, err := tx.Exec(`UPDATE artifact_files SET blob_id = ? WHERE artifact_id = ? AND path = ? AND blob_id = ''`, id, artifactID, f.Path)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		won = n == 1
		if !won {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO artifact_blobs(id, sha256, size, store_key, state, upload_id, created_at, used_at)
			VALUES(?,?,?,?,?,?,?,?)`, id, f.SHA256, f.Size, key, blobUploading, uploadID, now, now)
		if err == nil && e.knows(schemaBlobs) {
			_, err = tx.Exec(`UPDATE artifact_blobs SET store = ? WHERE id = ?`, store.Name(), id)
		}
		return err
	})
	if err != nil || !won {
		_ = store.AbortUpload(context.WithoutCancel(ctx), key, uploadID)
	}
	if err != nil {
		return "", err
	}
	if won {
		return id, nil
	}
	var blob string
	err = e.db.QueryRow(`SELECT blob_id FROM artifact_files WHERE artifact_id = ? AND path = ?`, artifactID, f.Path).Scan(&blob)
	return blob, err
}

// completeBlob makes a fully received blob's object and marks it stored.
// Completing twice is harmless, so a server that finds a blob received but
// not stored (its server stopped in between) finishes the job.
func (e *Engine) completeBlob(ctx context.Context, store blobstore.Store, b *blobRow) error {
	if err := store.CompleteUpload(ctx, b.Key, b.UploadID, b.Parts); err != nil {
		if size, serr := store.Stat(ctx, b.Key); serr != nil || size != b.Size {
			return fmt.Errorf("completing the upload: %w", err)
		}
	}
	return e.artifactTx(func(tx *txx, now int64) error {
		_, err := tx.Exec(`UPDATE artifact_blobs SET state = ?, hash_state = '', used_at = ? WHERE id = ? AND state = ?`,
			blobStored, now, b.ID, blobUploading)
		return err
	})
}

// CommitArtifact marks an artifact whose files have all been received as
// kept, until it expires.
func (e *Engine) CommitArtifact(ctx context.Context, leaseID, artifactID string) (*ArtifactSummary, error) {
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	l, err := e.leaseFor(leaseID)
	if err != nil {
		return nil, err
	}
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return nil, err
	}
	if a.RunID != l.runID || a.StepID != l.stepID || a.Attempt != l.attempt {
		return nil, fmt.Errorf("%w: artifact %s is not of this step attempt", ErrNotFound, artifactID)
	}
	if a.Status == artKept {
		return &ArtifactSummary{ID: a.ID, Name: a.Name, Kind: a.Kind, Files: a.Files, Bytes: a.Bytes}, nil
	}
	if a.Status != artUploading {
		return nil, fmt.Errorf("%w: artifact %s is %s", ErrConflict, a.Name, a.Status)
	}
	files, err := artifactFiles(e.db, a.ID)
	if err != nil {
		return nil, err
	}
	var missing []string
	blobs := map[string]bool{}
	for _, f := range files {
		switch f.BlobID {
		case emptyBlob:
			continue
		case "":
			missing = append(missing, f.Path)
			continue
		}
		b, err := getBlob(e.db, f.BlobID)
		if errors.Is(err, ErrNotFound) {
			missing = append(missing, f.Path)
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.State == blobUploading && b.Received == b.Size {
			if err := e.completeBlob(ctx, store, b); err != nil {
				return nil, err
			}
			b.State = blobStored
		}
		if b.State != blobStored {
			missing = append(missing, f.Path)
			continue
		}
		blobs[b.ID] = true
	}
	if len(missing) > 0 {
		_ = e.artifactTx(func(tx *txx, now int64) error { return resetLostContent(tx, a.ID) })
		return nil, fmt.Errorf("%w: not every file has been sent yet: %s", ErrConflict, strings.Join(clipList(missing, 5), ", "))
	}
	var lost []string
	err = e.artifactTx(func(tx *txx, now int64) error {
		lost = nil
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		for id := range blobs {
			res, err := tx.Exec(`UPDATE artifact_blobs SET used_at = ? WHERE id = ? AND state = ?`, now, id, blobStored)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				// Removed in the meantime: the file must be sent again.
				for _, f := range files {
					if f.BlobID == id {
						lost = append(lost, f.Path)
					}
				}
			}
		}
		if len(lost) > 0 {
			for _, p := range lost {
				if _, err := tx.Exec(`UPDATE artifact_files SET blob_id = '' WHERE artifact_id = ? AND path = ?`, a.ID, p); err != nil {
					return err
				}
			}
			return nil
		}
		res, err := tx.Exec(`UPDATE artifacts SET status = ?, kept_at = ?, expires_at = ? + retain_ms WHERE id = ? AND status = ?`,
			artKept, now, now, a.ID, artUploading)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: artifact %s changed while it was committed", ErrConflict, a.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(lost) > 0 {
		return nil, fmt.Errorf("%w: some files must be sent again: %s", ErrConflict, strings.Join(clipList(lost, 5), ", "))
	}
	return &ArtifactSummary{ID: a.ID, Name: a.Name, Kind: a.Kind, Files: a.Files, Bytes: a.Bytes}, nil
}

func clipList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(slices.Clone(list[:n]), fmt.Sprintf("and %d more", len(list)-n))
}

// ---- reading ----

// ArtifactFile is an opened file of an artifact.
type ArtifactFile struct {
	ArtifactFileInfo
	Artifact string // the artifact's name
	Body     io.ReadCloser
}

// openBlob opens a file's content from offset to the end.
func (e *Engine) openBlob(ctx context.Context, f artifactFile, offset int64) (io.ReadCloser, error) {
	if f.BlobID == emptyBlob || offset >= f.Size {
		return io.NopCloser(strings.NewReader("")), nil
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	b, err := getBlob(e.db, f.BlobID)
	if err != nil {
		return nil, err
	}
	if b.State != blobStored {
		return nil, fmt.Errorf("%w: the content of %s is no longer kept", ErrNotFound, f.Path)
	}
	r, err := store.Open(ctx, b.Key, offset, -1)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, e.missingContent(store, b, f.Path)
	}
	return r, err
}

// OpenFetch opens a file of an artifact for the step attempt a lease holds,
// from offset: only artifacts the step's pipeline fetches.
func (e *Engine) OpenFetch(ctx context.Context, leaseID, artifactID, filePath string, offset int64) (*ArtifactFile, error) {
	l, err := e.leaseFor(leaseID)
	if err != nil {
		return nil, err
	}
	st, err := e.runState(l.runID)
	if err != nil {
		return nil, err
	}
	def := st.Pipeline.Step(l.stepID)
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return nil, err
	}
	allowed := false
	if def != nil {
		for _, f := range stepFetches(st, def) {
			run, attempt, problem := fetchSource(st, def, f.From, f.member)
			if problem == "" && a.RunID == run && a.StepID == f.From && a.Attempt == attempt && a.Name == f.Name {
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, fmt.Errorf("%w: step %s does not fetch that artifact", ErrNotFound, l.stepID)
	}
	if a.Status != artKept {
		return nil, fmt.Errorf("%w: artifact %s is %s", ErrNotFound, a.Name, a.statusText())
	}
	return e.openArtifactFile(ctx, a, filePath, offset)
}

func (e *Engine) openArtifactFile(ctx context.Context, a *artifactRow, filePath string, offset int64) (*ArtifactFile, error) {
	f := artifactFile{}
	err := e.db.QueryRow(`SELECT path, size, sha256, mode, blob_id FROM artifact_files WHERE artifact_id = ? AND path = ?`,
		a.ID, filePath).Scan(&f.Path, &f.Size, &f.SHA256, &f.Mode, &f.BlobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: artifact %s has no file %s", ErrNotFound, a.Name, filePath)
	}
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > f.Size {
		return nil, fmt.Errorf("%w: offset %d is outside the file", ErrInvalid, offset)
	}
	body, err := e.openBlob(ctx, f, offset)
	if err != nil {
		return nil, err
	}
	return &ArtifactFile{ArtifactFileInfo: f.ArtifactFileInfo, Artifact: a.Name, Body: body}, nil
}
