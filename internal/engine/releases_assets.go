package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Asset kinds.
const (
	AssetFile  = "file"  // content kept with the release
	AssetImage = "image" // an image digest the Sortie built; no content
	AssetSBOM  = "sbom"  // a summary of the software bill of materials
	AssetScan  = "scan"  // a summary of the findings of the scans
)

// MaxAssetBytes is the largest file a release keeps.
const MaxAssetBytes = 2 << 30

var assetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]{0,199}$`)

// ReleaseAsset is a file or image of a release, with where it came from.
type ReleaseAsset struct {
	ID         string `json:"id"`
	ReleaseID  string `json:"release_id,omitempty"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	Image      string `json:"image,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Detail     string `json:"detail,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	RunNumber  int    `json:"run_number,omitempty"`
	StepID     string `json:"step_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	// SourceSHA is the commit the Sortie that built it ran at.
	SourceSHA string `json:"source_sha,omitempty"`
	AddedBy   string `json:"added_by"`
	AddedAt   int64  `json:"added_at"`
	// Uploaded is true for a file somebody uploaded rather than one a Sortie built.
	Uploaded bool `json:"uploaded,omitempty"`
}

const assetCols = `id, release_id, name, kind, size, sha256, image, digest, detail, run_id, run_number, step_id, artifact_id, source_sha, added_by, added_at`

func scanAsset(r rowScanner) (ReleaseAsset, error) {
	var a ReleaseAsset
	err := r.Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Kind, &a.Size, &a.SHA256, &a.Image, &a.Digest, &a.Detail, &a.RunID, &a.RunNumber, &a.StepID, &a.ArtifactID, &a.SourceSHA, &a.AddedBy, &a.AddedAt)
	a.Uploaded = a.Kind == AssetFile && a.RunID == ""
	return a, err
}

func (e *Engine) releaseAssets(releaseID string) ([]ReleaseAsset, error) {
	rows, err := e.db.Query(`SELECT `+assetCols+` FROM release_assets WHERE release_id = ? ORDER BY kind, name`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReleaseAsset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Provenance says which Sortie built an asset.
type Provenance struct {
	RunID      string
	StepID     string
	ArtifactID string
}

// AddReleaseFile keeps a file with a draft or candidate release, from a
// reader: its size and SHA-256 are worked out as it is stored. If want is not
// empty the content must have exactly that SHA-256.
func (e *Engine) AddReleaseFile(ctx context.Context, releaseID, name, want string, body io.Reader, prov Provenance, actor string) (*ReleaseAsset, error) {
	r, err := e.mutable(releaseID)
	if err != nil {
		return nil, err
	}
	if !assetNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: the file name %q: letters, digits and . _ + @ - , starting with a letter or digit, up to 200", ErrInvalid, name)
	}
	for _, a := range r.Assets {
		if a.Name == name {
			return nil, fmt.Errorf("%w: the release already has a file named %s; assets are not replaced, remove it from the draft first", ErrConflict, name)
		}
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	id := ids.New("ras")
	key := "releases/" + r.ID + "/" + id
	size, sum, err := putObject(ctx, store, key, body)
	if err != nil {
		return nil, err
	}
	if want != "" && want != sum {
		_ = store.Delete(ctx, key)
		return nil, fmt.Errorf("%w: the content's SHA-256 is %s, not the %s that was given", ErrInvalid, sum, want)
	}
	a := ReleaseAsset{ID: id, ReleaseID: r.ID, Name: name, Kind: AssetFile, Size: size, SHA256: sum, RunID: prov.RunID, StepID: prov.StepID,
		ArtifactID: prov.ArtifactID, AddedBy: actor, AddedAt: e.now().UnixMilli()}
	if prov.RunID != "" {
		_ = e.db.QueryRow(`SELECT number, sha FROM runs WHERE id = ?`, prov.RunID).Scan(&a.RunNumber, &a.SourceSHA)
	}
	if err := e.insertAsset(a, key); err != nil {
		_ = store.Delete(ctx, key)
		return nil, err
	}
	return &a, nil
}

func (e *Engine) insertAsset(a ReleaseAsset, key string) error {
	// The release must still be open when the row goes in: the state is part of the statement.
	res, err := e.db.Exec(`INSERT INTO release_assets(id, release_id, name, kind, size, sha256, store_key, image, digest, detail, run_id, run_number, step_id, artifact_id, source_sha, added_by, added_at)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM releases WHERE id = ? AND state IN ('draft','candidate'))`,
		a.ID, a.ReleaseID, a.Name, a.Kind, a.Size, a.SHA256, key, a.Image, a.Digest, a.Detail, a.RunID, a.RunNumber, a.StepID, a.ArtifactID, a.SourceSHA, a.AddedBy, a.AddedAt, a.ReleaseID)
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("%w: the release already has an asset named %s", ErrConflict, a.Name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: the release was published or withdrawn meanwhile; its assets cannot change", ErrConflict)
	}
	return nil
}

// AddReleaseCargo copies a file of a Sortie's Cargo into a draft or candidate
// release. The copy is the release's own: the Cargo may expire, the release
// never loses it.
func (e *Engine) AddReleaseCargo(ctx context.Context, releaseID, runID, artifactID, path, name, actor string) (*ReleaseAsset, error) {
	r, err := e.mutable(releaseID)
	if err != nil {
		return nil, err
	}
	project, err := e.RunProject(runID)
	if err != nil {
		return nil, err
	}
	if project != r.Project {
		return nil, fmt.Errorf("%w: run %s belongs to %s, not %s", ErrConflict, runID, project, r.Project)
	}
	var sha string
	if err := e.db.QueryRow(`SELECT sha FROM runs WHERE id = ?`, runID).Scan(&sha); err != nil {
		return nil, err
	}
	if sha != "" && (len(r.SHA) < 7 || !hasPrefix(sha, r.SHA) && !hasPrefix(r.SHA, sha)) {
		return nil, fmt.Errorf("%w: the Sortie ran at commit %s, not the release's commit %s; assets must come from the commit being released", ErrConflict, releaseSHA(sha), releaseSHA(r.SHA))
	}
	f, err := e.OpenArtifactFile(ctx, runID, artifactID, path, 0)
	if err != nil {
		return nil, err
	}
	defer f.Body.Close()
	if name == "" {
		name = baseName(path)
	}
	var step string
	_ = e.db.QueryRow(`SELECT step_id FROM artifacts WHERE id = ?`, artifactID).Scan(&step)
	return e.AddReleaseFile(ctx, releaseID, name, f.SHA256, f.Body, Provenance{RunID: runID, ArtifactID: artifactID, StepID: step}, actor)
}

func hasPrefix(a, b string) bool { return len(b) <= len(a) && a[:len(b)] == b }

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// AddReleaseRunOutputs records what a Sortie made that is not a file: its
// image digests, and summaries of its SBOM and scans if there are any.
func (e *Engine) AddReleaseRunOutputs(releaseID, runID, actor string) ([]ReleaseAsset, error) {
	r, err := e.mutable(releaseID)
	if err != nil {
		return nil, err
	}
	probe := *r
	if err := e.attachRun(&probe, runID); err != nil {
		return nil, err
	}
	imgs, err := e.RunImages(runID)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, a := range r.Assets {
		have[a.Name] = true
	}
	var added []ReleaseAsset
	add := func(a ReleaseAsset) error {
		if have[a.Name] {
			return nil
		}
		a.ID, a.ReleaseID, a.AddedBy, a.AddedAt = ids.New("ras"), r.ID, actor, e.now().UnixMilli()
		a.RunID, a.RunNumber, a.SourceSHA = runID, probe.RunNumber, r.SHA
		if err := e.insertAsset(a, ""); err != nil {
			return err
		}
		have[a.Name] = true
		added = append(added, a)
		return nil
	}
	for _, im := range imgs {
		if !im.Pushed || im.Digest == "" {
			continue
		}
		if err := add(ReleaseAsset{Name: "image-" + im.Step, Kind: AssetImage, Image: im.Image, Digest: im.Digest, StepID: im.Step,
			Detail: fmt.Sprintf("signed %v, SBOM %v", im.Signed, im.SBOM)}); err != nil {
			return nil, err
		}
		if im.SBOM {
			if err := add(ReleaseAsset{Name: "sbom-" + im.Step, Kind: AssetSBOM, Digest: im.Digest, StepID: im.Step, Detail: "software bill of materials kept with the image"}); err != nil {
				return nil, err
			}
		}
		if im.Scan != nil {
			if err := add(ReleaseAsset{Name: "scan-" + im.Step, Kind: AssetScan, Digest: im.Digest, StepID: im.Step,
				Detail: fmt.Sprintf("critical %d, high %d, medium %d, low %d", im.Scan.Critical, im.Scan.High, im.Scan.Medium, im.Scan.Low)}); err != nil {
				return nil, err
			}
		}
	}
	// Digests are recorded on the release itself, which is what a promotion deploys.
	if len(probe.Digests) > 0 || probe.RunID != "" {
		if err := e.setReleaseRun(r.ID, probe); err != nil {
			return nil, err
		}
	}
	return added, nil
}

func (e *Engine) setReleaseRun(id string, p Release) error {
	dj := jsonString(p.Digests)
	res, err := e.db.Exec(`UPDATE releases SET run_id = ?, run_number = ?, digests = ?, updated_at = ? WHERE id = ? AND state IN ('draft','candidate')`,
		p.RunID, p.RunNumber, dj, e.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: the release was published meanwhile", ErrConflict)
	}
	return nil
}

// RemoveReleaseAsset removes an asset from a draft or candidate release.
func (e *Engine) RemoveReleaseAsset(ctx context.Context, releaseID, name string) error {
	r, err := e.mutable(releaseID)
	if err != nil {
		return err
	}
	var key string
	err = e.db.QueryRow(`SELECT store_key FROM release_assets WHERE release_id = ? AND name = ?`, r.ID, name).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: the release has no asset %s", ErrNotFound, name)
	}
	if err != nil {
		return err
	}
	res, err := e.db.Exec(`DELETE FROM release_assets WHERE release_id = ? AND name = ? AND EXISTS (SELECT 1 FROM releases WHERE id = ? AND state IN ('draft','candidate'))`, r.ID, name, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: the release was published meanwhile; its assets cannot change", ErrConflict)
	}
	if key != "" {
		if st, err := e.storeOrErr(); err == nil {
			_ = st.Delete(ctx, key)
		}
	}
	return nil
}

func (e *Engine) dropAssets(releaseID string) {
	st, err := e.storeOrErr()
	if err != nil {
		return
	}
	rows, err := e.db.Query(`SELECT store_key FROM release_assets WHERE release_id = ? AND store_key <> ''`, releaseID)
	if err != nil {
		return
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			keys = append(keys, k)
		}
	}
	if rows.Err() != nil {
		return
	}
	for _, k := range keys {
		_ = st.Delete(context.Background(), k)
	}
}

// OpenReleaseAsset opens a file asset of a release for download. A release that
// is only a draft may be read by those who may see drafts; the caller decides.
func (e *Engine) OpenReleaseAsset(ctx context.Context, releaseID, name string) (io.ReadCloser, *ReleaseAsset, error) {
	return e.OpenReleaseAssetAt(ctx, releaseID, name, 0)
}

// OpenReleaseAssetAt opens a release file from a byte offset, so a download can resume and a range can be answered
// without reading what comes before it.
func (e *Engine) OpenReleaseAssetAt(ctx context.Context, releaseID, name string, offset int64) (io.ReadCloser, *ReleaseAsset, error) {
	var key string
	row := e.db.QueryRow(`SELECT `+assetCols+`, store_key FROM release_assets WHERE release_id = ? AND name = ?`, releaseID, name)
	var a ReleaseAsset
	err := row.Scan(&a.ID, &a.ReleaseID, &a.Name, &a.Kind, &a.Size, &a.SHA256, &a.Image, &a.Digest, &a.Detail, &a.RunID, &a.RunNumber, &a.StepID, &a.ArtifactID, &a.SourceSHA, &a.AddedBy, &a.AddedAt, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: the release has no asset %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, nil, err
	}
	if key == "" {
		return nil, nil, fmt.Errorf("%w: %s is a record of an image, not a file (its digest is %s)", ErrNotFound, name, a.Digest)
	}
	st, err := e.storeOrErr()
	if err != nil {
		return nil, nil, err
	}
	rc, err := st.Open(ctx, key, offset, -1)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, nil, fmt.Errorf("%w: the content of %s is missing from the store", ErrNotFound, name)
	}
	return rc, &a, err
}

// putObject stores a reader under a key in parts and returns its size and SHA-256.
func putObject(ctx context.Context, st blobstore.Store, key string, body io.Reader) (int64, string, error) {
	up, err := st.CreateUpload(ctx, key)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	buf := make([]byte, blobstore.MinPartSize)
	var parts []blobstore.Part
	var total int64
	fail := func(err error) (int64, string, error) {
		_ = st.AbortUpload(ctx, key, up)
		return 0, "", err
	}
	for n := 1; ; n++ {
		k, err := io.ReadFull(body, buf)
		if k > 0 {
			total += int64(k)
			if total > MaxAssetBytes {
				return fail(fmt.Errorf("%w: a release file is limited to %d bytes", ErrInvalid, int64(MaxAssetBytes)))
			}
			h.Write(buf[:k])
			et, perr := st.UploadPart(ctx, key, up, n, buf[:k])
			if perr != nil {
				return fail(perr)
			}
			parts = append(parts, blobstore.Part{Number: n, ETag: et})
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return fail(err)
		}
	}
	if len(parts) == 0 { // an empty file is one empty part
		et, err := st.UploadPart(ctx, key, up, 1, nil)
		if err != nil {
			return fail(err)
		}
		parts = append(parts, blobstore.Part{Number: 1, ETag: et})
	}
	if err := st.CompleteUpload(ctx, key, up, parts); err != nil {
		return fail(err)
	}
	return total, hex.EncodeToString(h.Sum(nil)), nil
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
