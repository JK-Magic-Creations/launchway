package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
	rls "github.com/jk-magic-creations/launchway/internal/release"
)

// Releases (schema 57). A release is the unit that is versioned, approved,
// promoted through Airfields and reported on. It is made as a draft, may become
// a candidate, and is published; a published release never changes: its
// version, commit, tag, assets and image digests are fixed, and a correction is
// a new release. It can be withdrawn (yanked) with a reason, which keeps it in
// the record and stops it being promoted.

// Release states.
const (
	ReleaseDraft     = "draft"
	ReleaseCandidate = "candidate"
	ReleasePublished = "published"
	ReleaseYanked    = "yanked"
)

// ReleaseSettings is how a project's releases are named and ordered.
type ReleaseSettings struct {
	Project   string `json:"project"`
	Scheme    string `json:"scheme"`
	TagPrefix string `json:"tag_prefix"`
	// Public says the project's published releases may be downloaded without
	// signing in. It only takes effect when the installation allows public
	// content; the server decides.
	Public    bool   `json:"public"`
	Changelog string `json:"changelog"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// ReleaseDigest is an image a release's Sortie built: what is promoted is this
// digest, never a rebuilt tag.
type ReleaseDigest struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
	Step   string `json:"step,omitempty"`
}

// Release is a release as the interfaces show it.
type Release struct {
	ID          string          `json:"id"`
	Project     string          `json:"project"`
	Version     string          `json:"version"`
	Scheme      string          `json:"scheme"`
	State       string          `json:"state"`
	Title       string          `json:"title,omitempty"`
	Notes       string          `json:"notes"`
	NotesSource string          `json:"notes_source,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	SHA         string          `json:"sha"`
	Tag         string          `json:"tag,omitempty"`
	TagObject   string          `json:"tag_object,omitempty"`
	TagCreated  bool            `json:"tag_created,omitempty"` // Launchway made the tag; false: adopted
	Prerelease  bool            `json:"prerelease"`
	Public      bool            `json:"public,omitempty"`
	RunID       string          `json:"run_id,omitempty"`
	RunNumber   int             `json:"run_number,omitempty"`
	Digests     []ReleaseDigest `json:"digests"`
	Previous    string          `json:"previous,omitempty"`
	CommitTime  int64           `json:"commit_time,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CreatedBy   string          `json:"created_by,omitempty"`
	CandidateAt int64           `json:"candidate_at,omitempty"`
	CandidateBy string          `json:"candidate_by,omitempty"`
	PublishedAt int64           `json:"published_at,omitempty"`
	PublishedBy string          `json:"published_by,omitempty"`
	YankedAt    int64           `json:"yanked_at,omitempty"`
	YankedBy    string          `json:"yanked_by,omitempty"`
	YankReason  string          `json:"yank_reason,omitempty"`
	Assets      []ReleaseAsset  `json:"assets,omitempty"`
	// Immutable says nothing about the release can change any more but its
	// withdrawal.
	Immutable bool `json:"immutable"`
}

const releaseCols = `id, project, version, scheme, state, title, notes, notes_source, branch, sha, tag, tag_object, tag_created, prerelease, public,
	run_id, run_number, digests, previous, commit_time, created_at, created_by, candidate_at, candidate_by, published_at, published_by,
	yanked_at, yanked_by, yank_reason`

type rowScanner interface{ Scan(...any) error }

func scanRelease(r rowScanner) (*Release, error) {
	var x Release
	var tagCreated, pre, pub int
	var digests string
	err := r.Scan(&x.ID, &x.Project, &x.Version, &x.Scheme, &x.State, &x.Title, &x.Notes, &x.NotesSource, &x.Branch, &x.SHA, &x.Tag, &x.TagObject,
		&tagCreated, &pre, &pub, &x.RunID, &x.RunNumber, &digests, &x.Previous, &x.CommitTime, &x.CreatedAt, &x.CreatedBy, &x.CandidateAt,
		&x.CandidateBy, &x.PublishedAt, &x.PublishedBy, &x.YankedAt, &x.YankedBy, &x.YankReason)
	if err != nil {
		return nil, err
	}
	x.TagCreated, x.Prerelease, x.Public = tagCreated == 1, pre == 1, pub == 1
	x.Digests = []ReleaseDigest{}
	_ = json.Unmarshal([]byte(digests), &x.Digests)
	x.Immutable = x.State == ReleasePublished || x.State == ReleaseYanked
	return &x, nil
}

func (e *Engine) needReleases() error {
	if !e.knows(schemaReleases) {
		return fmt.Errorf("%w: this database is older than releases (schema %d)", ErrConflict, schemaReleases)
	}
	return nil
}

// ReleaseSettingsOf returns a project's settings, the defaults when none were saved.
func (e *Engine) ReleaseSettingsOf(project string) (ReleaseSettings, error) {
	s := ReleaseSettings{Project: project, Scheme: rls.SchemeSemver, TagPrefix: "v", Changelog: "CHANGELOG.md"}
	if err := e.needReleases(); err != nil {
		return s, err
	}
	var pub int
	err := e.db.QueryRow(`SELECT scheme, tag_prefix, public, changelog, updated_at, updated_by FROM release_settings WHERE project = ?`, project).
		Scan(&s.Scheme, &s.TagPrefix, &pub, &s.Changelog, &s.UpdatedAt, &s.UpdatedBy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	s.Public = pub == 1
	return s, nil
}

// SetReleaseSettings saves a project's settings. The scheme cannot change once
// the project has a published release, because the order of its versions
// would change under it.
func (e *Engine) SetReleaseSettings(s ReleaseSettings, actor string) (ReleaseSettings, error) {
	if err := e.needReleases(); err != nil {
		return s, err
	}
	if !rls.ValidScheme(s.Scheme) {
		return s, fmt.Errorf("%w: scheme %q: use one of %s", ErrInvalid, s.Scheme, strings.Join(rls.Schemes, ", "))
	}
	if !rls.ValidPrefix(s.TagPrefix) {
		return s, fmt.Errorf("%w: tag_prefix %q: letters, digits and . _ / - only, up to 20", ErrInvalid, s.TagPrefix)
	}
	if s.Changelog == "" {
		s.Changelog = "CHANGELOG.md"
	}
	if len(s.Changelog) > 200 || strings.Contains(s.Changelog, "..") || strings.HasPrefix(s.Changelog, "/") || strings.ContainsAny(s.Changelog, "\x00\n\\") {
		return s, fmt.Errorf("%w: changelog must be a path inside the repository", ErrInvalid)
	}
	old, err := e.ReleaseSettingsOf(s.Project)
	if err != nil {
		return s, err
	}
	if old.Scheme != s.Scheme {
		var n int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM releases WHERE project = ? AND state IN ('candidate','published','yanked')`, s.Project).Scan(&n); err != nil {
			return s, err
		}
		if n > 0 {
			return s, fmt.Errorf("%w: %s already has %d candidate or published releases written as %s; the scheme cannot change under them", ErrConflict, s.Project, n, old.Scheme)
		}
	}
	s.UpdatedAt, s.UpdatedBy = e.now().UnixMilli(), actor
	_, err = e.db.Exec(`INSERT INTO release_settings(project, scheme, tag_prefix, public, changelog, updated_at, updated_by) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(project) DO UPDATE SET scheme = excluded.scheme, tag_prefix = excluded.tag_prefix, public = excluded.public,
		changelog = excluded.changelog, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		s.Project, s.Scheme, s.TagPrefix, b2i(s.Public), s.Changelog, s.UpdatedAt, s.UpdatedBy)
	return s, err
}

// NewRelease asks for a draft.
type NewRelease struct {
	Project string
	Version string
	Title   string
	Notes   string
	// NotesSource says where generated notes came from.
	NotesSource string
	Branch      string
	// SHA is the commit released. For an adopted tag it is what the tag points to.
	SHA        string
	CommitTime int64
	// Tag and TagObject are set when an existing tag is adopted.
	Tag       string
	TagObject string
	// RunID is the Sortie that built the release; its image digests are recorded.
	RunID string
	// AllowLower lets a version lower than the highest so far be made (a
	// release from a maintenance branch).
	AllowLower bool
	Actor      string
}

// HighestRelease returns the highest version of a project that is a candidate,
// published or yanked ("" for none), in the project's scheme.
func (e *Engine) HighestRelease(project string) (string, string, error) {
	set, err := e.ReleaseSettingsOf(project)
	if err != nil {
		return "", "", err
	}
	rows, err := e.db.Query(`SELECT version FROM releases WHERE project = ? AND state <> 'draft' ORDER BY created_at`, project)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	hi := ""
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", "", err
		}
		if hi == "" || rls.Compare(set.Scheme, v, hi) > 0 || set.Scheme == rls.SchemeCreated {
			hi = v
		}
	}
	return hi, set.Scheme, rows.Err()
}

// previousPublished is the newest published release below a version, for the "since" of its notes.
func (e *Engine) previousPublished(project, scheme, version, exceptID string) (string, error) {
	rows, err := e.db.Query(`SELECT version FROM releases WHERE project = ? AND state IN ('published','yanked') AND id <> ? ORDER BY created_at`, project, exceptID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		if scheme == rls.SchemeCreated {
			prev = v
			continue
		}
		if rls.Compare(scheme, v, version) < 0 && (prev == "" || rls.Compare(scheme, v, prev) > 0) {
			prev = v
		}
	}
	return prev, rows.Err()
}

// CreateRelease makes a draft.
func (e *Engine) CreateRelease(n NewRelease) (*Release, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	set, err := e.ReleaseSettingsOf(n.Project)
	if err != nil {
		return nil, err
	}
	v, err := rls.CheckVersion(set.Scheme, n.Version)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if len(n.SHA) < 7 || len(n.SHA) > 64 || strings.Trim(n.SHA, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("%w: a release needs the commit it releases, as a hexadecimal id", ErrInvalid)
	}
	if len(n.Notes) > 200_000 || len(n.Title) > 200 {
		return nil, fmt.Errorf("%w: the notes are limited to 200,000 characters and the title to 200", ErrInvalid)
	}
	hi, _, err := e.HighestRelease(n.Project)
	if err != nil {
		return nil, err
	}
	if err := rls.MayRelease(set.Scheme, hi, v, n.AllowLower); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrConflict, err)
	}
	r := &Release{ID: ids.New("rel"), Project: n.Project, Version: v, Scheme: set.Scheme, State: ReleaseDraft, Title: n.Title, Notes: n.Notes,
		NotesSource: n.NotesSource, Branch: n.Branch, SHA: n.SHA, Tag: n.Tag, TagObject: n.TagObject, CommitTime: n.CommitTime,
		Prerelease: rls.IsPrerelease(set.Scheme, v), CreatedAt: e.now().UnixMilli(), CreatedBy: n.Actor, Digests: []ReleaseDigest{}}
	if r.Previous, err = e.previousPublished(n.Project, set.Scheme, v, r.ID); err != nil {
		return nil, err
	}
	if n.RunID != "" {
		if err := e.attachRun(r, n.RunID); err != nil {
			return nil, err
		}
	}
	dj, _ := json.Marshal(r.Digests)
	_, err = e.db.Exec(`INSERT INTO releases(id, project, version, scheme, state, title, notes, notes_source, branch, sha, tag, tag_object, tag_created,
		prerelease, public, run_id, run_number, digests, previous, commit_time, created_at, created_by, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,?,0,?,?,?,?,?,?,?,?)`,
		r.ID, r.Project, r.Version, r.Scheme, r.State, r.Title, r.Notes, r.NotesSource, r.Branch, r.SHA, r.Tag, r.TagObject,
		b2i(r.Prerelease), r.RunID, r.RunNumber, string(dj), r.Previous, r.CommitTime, r.CreatedAt, r.CreatedBy, r.CreatedAt)
	if err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: %s already has a release %s", ErrConflict, n.Project, v)
		}
		return nil, err
	}
	return r, nil
}

func isUnique(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate key")
}

// attachRun records which Sortie built a release and the images it made.
func (e *Engine) attachRun(r *Release, runID string) error {
	var project, sha string
	var number int
	err := e.db.QueryRow(`SELECT project, number, sha FROM runs WHERE id = ?`, runID).Scan(&project, &number, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return err
	}
	if project != r.Project {
		return fmt.Errorf("%w: run %s belongs to %s, not %s", ErrConflict, runID, project, r.Project)
	}
	if sha != "" && !strings.HasPrefix(r.SHA, sha) && !strings.HasPrefix(sha, r.SHA) {
		return fmt.Errorf("%w: run #%d built commit %s, not the release's commit %s; a release carries what was built from its own commit", ErrConflict, number, shortSHA(sha), shortSHA(r.SHA))
	}
	r.RunID, r.RunNumber = runID, number
	imgs, err := e.RunImages(runID)
	if err != nil {
		return err
	}
	r.Digests = []ReleaseDigest{}
	for _, im := range imgs {
		if im.Pushed && im.Digest != "" {
			r.Digests = append(r.Digests, ReleaseDigest{Image: im.Image, Digest: im.Digest, Step: im.Step})
		}
	}
	return nil
}

func shortSHA(s string) string { return s[:min(8, len(s))] }

// GetRelease finds a release by id.
func (e *Engine) GetRelease(id string) (*Release, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	r, err := scanRelease(e.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: release %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if r.Assets, err = e.releaseAssets(r.ID); err != nil {
		return nil, err
	}
	return r, nil
}

// FindRelease finds a release by project and version (or tag).
func (e *Engine) FindRelease(project, version string) (*Release, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	var id string
	err := e.db.QueryRow(`SELECT id FROM releases WHERE project = ? AND (version = ? OR tag = ?) ORDER BY created_at DESC LIMIT 1`, project, version, version).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s has no release %s", ErrNotFound, project, version)
	}
	if err != nil {
		return nil, err
	}
	return e.GetRelease(id)
}

// ReleaseFilter narrows a list.
type ReleaseFilter struct {
	Project string
	State   string
	Limit   int
	// Visible says whether a project may be seen by the caller; nil: all.
	Visible func(project string) bool
}

// ListReleases lists releases, newest first.
func (e *Engine) ListReleases(f ReleaseFilter) ([]Release, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	q, args := `SELECT `+releaseCols+` FROM releases WHERE 1=1`, []any{}
	if f.Project != "" {
		q += ` AND project = ?`
		args = append(args, f.Project)
	}
	if f.State != "" {
		q += ` AND state = ?`
		args = append(args, f.State)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		if f.Visible != nil && !f.Visible(r.Project) {
			continue
		}
		out = append(out, *r)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, rows.Err()
}

func (e *Engine) mutable(id string) (*Release, error) {
	r, err := e.GetRelease(id)
	if err != nil {
		return nil, err
	}
	if r.Immutable {
		return nil, fmt.Errorf("%w: release %s %s is %s and cannot change; a correction is a new release", ErrConflict, r.Project, r.Version, r.State)
	}
	return r, nil
}

// EditRelease changes a draft's or candidate's title and notes.
func (e *Engine) EditRelease(id string, title, notes *string) (*Release, error) {
	r, err := e.mutable(id)
	if err != nil {
		return nil, err
	}
	if title != nil {
		if len(*title) > 200 {
			return nil, fmt.Errorf("%w: the title is limited to 200 characters", ErrInvalid)
		}
		r.Title = *title
	}
	if notes != nil {
		if len(*notes) > 200_000 {
			return nil, fmt.Errorf("%w: the notes are limited to 200,000 characters", ErrInvalid)
		}
		r.Notes = *notes
		r.NotesSource = "edited"
	}
	res, err := e.db.Exec(`UPDATE releases SET title = ?, notes = ?, notes_source = ?, updated_at = ? WHERE id = ? AND state IN ('draft','candidate')`,
		r.Title, r.Notes, r.NotesSource, e.now().UnixMilli(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: the release was published while you were editing it", ErrConflict)
	}
	return r, nil
}

// SetReleaseNotes replaces the generated notes of a draft with a new text and says where it came from.
func (e *Engine) SetReleaseNotes(id, notes, source string) error {
	if _, err := e.mutable(id); err != nil {
		return err
	}
	res, err := e.db.Exec(`UPDATE releases SET notes = ?, notes_source = ?, updated_at = ? WHERE id = ? AND state IN ('draft','candidate')`, notes, source, e.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: the release was published meanwhile", ErrConflict)
	}
	return nil
}

// DeleteRelease removes a draft. A candidate or published release is never deleted.
func (e *Engine) DeleteRelease(id string) error {
	r, err := e.GetRelease(id)
	if err != nil {
		return err
	}
	if r.State != ReleaseDraft {
		return fmt.Errorf("%w: only a draft is deleted; a %s release stays in the record (withdraw it instead)", ErrConflict, r.State)
	}
	res, err := e.db.Exec(`DELETE FROM releases WHERE id = ? AND state = 'draft'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: the release is no longer a draft", ErrConflict)
	}
	e.dropAssets(id)
	_, _ = e.db.Exec(`DELETE FROM release_assets WHERE release_id = ?`, id)
	return nil
}

// MakeCandidate moves a draft to candidate: it can be promoted for testing
// (to the Airfields whose checklist accepts a candidate) but is not yet released.
func (e *Engine) MakeCandidate(id, actor string) (*Release, error) {
	r, err := e.GetRelease(id)
	if err != nil {
		return nil, err
	}
	if r.State != ReleaseDraft {
		return nil, fmt.Errorf("%w: only a draft becomes a candidate; this one is %s", ErrConflict, r.State)
	}
	now := e.now().UnixMilli()
	res, err := e.db.Exec(`UPDATE releases SET state = 'candidate', candidate_at = ?, candidate_by = ?, updated_at = ? WHERE id = ? AND state = 'draft'`, now, actor, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: the release changed state meanwhile", ErrConflict)
	}
	return e.GetRelease(id)
}

// PublishInput is what publishing fixes.
type PublishInput struct {
	// Tag is the tag the release is published under, and TagObject the
	// annotated tag object's id; TagCreated says Launchway created it.
	Tag        string
	TagObject  string
	TagCreated bool
	Actor      string
	// Public asks for public downloads; it is refused unless the installation allows them and the project's settings say so.
	Public bool
}

// PublishRelease publishes a draft or candidate. From then on nothing but its
// withdrawal changes.
func (e *Engine) PublishRelease(id string, in PublishInput) (*Release, error) {
	r, err := e.GetRelease(id)
	if err != nil {
		return nil, err
	}
	if r.State != ReleaseDraft && r.State != ReleaseCandidate {
		return nil, fmt.Errorf("%w: release %s %s is already %s", ErrConflict, r.Project, r.Version, r.State)
	}
	if in.Tag == "" {
		return nil, fmt.Errorf("%w: a release is published under a tag", ErrInvalid)
	}
	if strings.TrimSpace(r.Notes) == "" {
		// Notes are not required by the engine (a gate may require them) but a release with none says so.
		r.Notes = ""
	}
	// The version must still be higher than every published release unless it came from a maintenance branch.
	now := e.now().UnixMilli()
	res, err := e.db.Exec(`UPDATE releases SET state = 'published', tag = ?, tag_object = ?, tag_created = ?, public = ?, published_at = ?, published_by = ?, updated_at = ?
		WHERE id = ? AND state IN ('draft','candidate')`, in.Tag, in.TagObject, b2i(in.TagCreated), b2i(in.Public), now, in.Actor, now, id)
	if err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: the tag %s is already used by another release", ErrConflict, in.Tag)
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: the release changed state meanwhile", ErrConflict)
	}
	return e.GetRelease(id)
}

// YankRelease withdraws a candidate or published release, with a reason. It
// stays in the record, is marked, and is no longer promoted.
func (e *Engine) YankRelease(id, reason, actor string) (*Release, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return nil, fmt.Errorf("%w: say why the release is withdrawn (1 to 1000 characters); it is recorded with it", ErrInvalid)
	}
	r, err := e.GetRelease(id)
	if err != nil {
		return nil, err
	}
	if r.State != ReleasePublished && r.State != ReleaseCandidate {
		return nil, fmt.Errorf("%w: only a candidate or published release is withdrawn; this one is %s", ErrConflict, r.State)
	}
	now := e.now().UnixMilli()
	res, err := e.db.Exec(`UPDATE releases SET state = 'yanked', yanked_at = ?, yanked_by = ?, yank_reason = ?, updated_at = ? WHERE id = ? AND state IN ('published','candidate')`,
		now, actor, reason, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: the release changed state meanwhile", ErrConflict)
	}
	return e.GetRelease(id)
}

// ReleaseProject returns a release's project (for permission checks).
func (e *Engine) ReleaseProject(id string) (string, error) {
	var p string
	err := e.db.QueryRow(`SELECT project FROM releases WHERE id = ?`, id).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: release %s", ErrNotFound, id)
	}
	return p, err
}

// PublicReleasesAllowed says whether this installation allows public content:
// downloads of a release's files with no sign-in. It is off until an
// administrator of every project turns it on, and a project still has to ask
// for it in its own settings, and a release at publication.
func (e *Engine) PublicReleasesAllowed() (bool, error) {
	if err := e.needReleases(); err != nil {
		return false, err
	}
	var pub int
	err := e.db.QueryRow(`SELECT public FROM release_settings WHERE project = ''`).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return pub == 1, err
}

// SetPublicReleasesAllowed turns the installation's permission for public release downloads on or off.
func (e *Engine) SetPublicReleasesAllowed(allow bool, actor string) error {
	if err := e.needReleases(); err != nil {
		return err
	}
	_, err := e.db.Exec(`INSERT INTO release_settings(project, scheme, tag_prefix, public, changelog, updated_at, updated_by) VALUES('', 'semver', '', ?, '', ?, ?)
		ON CONFLICT(project) DO UPDATE SET public = excluded.public, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		b2i(allow), e.now().UnixMilli(), actor)
	return err
}
