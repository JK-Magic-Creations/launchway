package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Media types the registry accepts as manifests.
const (
	MediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Keys of annotations the registry computes and stores with a manifest, in
// the manifest's annotations column. They are Launchway's own and a client
// cannot set them (they are removed from anything pushed).
const (
	AnnPlatforms = "io.launchway.platforms"
	AnnTotalSize = "io.launchway.total_size"
)

// Keys a scan result attached to an image carries, so the Images tab can
// summarize without reading the report.
const (
	AnnVulnCritical = "io.launchway.vulnerabilities.critical"
	AnnVulnHigh     = "io.launchway.vulnerabilities.high"
	AnnVulnMedium   = "io.launchway.vulnerabilities.medium"
	AnnVulnLow      = "io.launchway.vulnerabilities.low"
	AnnScanner      = "io.launchway.scanner"
)

func isImageMedia(mt string) bool { return mt == MediaOCIManifest || mt == MediaDockerManifest }
func isIndexMedia(mt string) bool { return mt == MediaOCIIndex || mt == MediaDockerList }

type descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Platform     *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant,omitempty"`
	} `json:"platform,omitempty"`
}

type manifestDoc struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType"`
	Config        *descriptor       `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Manifests     []descriptor      `json:"manifests"`
	Subject       *descriptor       `json:"subject"`
	Annotations   map[string]string `json:"annotations"`
}

// Manifest is a manifest as the registry lists it.
type Manifest struct {
	Repo         string            `json:"repository"`
	Digest       string            `json:"digest"`
	MediaType    string            `json:"media_type"`
	ArtifactType string            `json:"artifact_type,omitempty"`
	Size         int64             `json:"size"`
	TotalSize    int64             `json:"total_size"`
	Subject      string            `json:"subject,omitempty"`
	Tags         []string          `json:"tags"`
	Platforms    []string          `json:"platforms,omitempty"`
	CreatedAt    int64             `json:"created_at"`
	CreatedBy    string            `json:"created_by,omitempty"`
	RunID        string            `json:"run_id,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// Who is a caller, as the registry records it.
type Who struct {
	Name string // a person, or "run <id>"
	Run  string // the run, for a run's credential
}

// PutManifest stores a manifest under a tag or a digest, after checking that
// everything it names is in the repository. It returns the manifest's digest
// and whether the repository had the manifest before.
func (s *Service) PutManifest(ctx context.Context, repo, ref, mediaType string, body []byte, who Who) (string, error) {
	if err := s.ensureStore(); err != nil {
		return "", err
	}
	if int64(len(body)) > s.limits.MaxManifestBytes {
		return "", newErr(http.StatusRequestEntityTooLarge, CodeTooLarge,
			fmt.Sprintf("this manifest is %d bytes, more than the %d the registry accepts", len(body), s.limits.MaxManifestBytes), "")
	}
	digest, mediaType, doc, err := s.readManifestRequest(ref, mediaType, body)
	if err != nil {
		return "", err
	}
	r, err := s.ensureRepo(ctx, repo, who.Name)
	if err != nil {
		return "", err
	}
	eff, err := s.EffectivePolicy(ctx, repo)
	if err != nil {
		return "", err
	}
	immutable := !IsDigest(ref) && matchAny(eff.ImmutableTags, ref)
	if immutable {
		if cur, ok, err := s.tagDigest(ctx, s.db, repo, ref); err != nil {
			return "", err
		} else if ok && cur != digest {
			return "", immutableErr(repo, ref, cur)
		}
	}

	pm, err := s.checkManifestContent(ctx, repo, mediaType, body, doc)
	if err != nil {
		return "", err
	}
	total := pm.total
	// The manifest's own bytes are a blob of the repository, like any other.
	if err := s.checkQuota(ctx, r, digest, int64(len(body))); err != nil {
		return "", err
	}
	key := blobKey(r.Org, digest)
	if _, got, err := s.putStream(ctx, key, strings.NewReader(string(body)), digest, 0); err != nil {
		return "", err
	} else if got != digest {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		return "", newErr(http.StatusInternalServerError, CodeUnavailable, "the manifest was damaged while stored", "")
	}
	if _, err := s.commitBlob(ctx, repo, r.Org, digest, int64(len(body)), key, who.Name); err != nil {
		return "", err
	}
	rec := manifestRecord{repo: repo, ref: ref, digest: digest, mediaType: mediaType, size: len(body), who: who, immutable: immutable, eff: eff, pm: pm}
	if err := s.recordManifest(ctx, rec); err != nil {
		return "", err
	}
	s.emit(ctx, Event{Type: "push", Repo: repo, Digest: digest, Tag: tagOf(ref), Actor: who.Name, Run: who.Run, Size: total})
	return digest, nil
}

// readManifestRequest checks what a manifest PUT says about itself: the
// reference, the digest it hashes to, that it is JSON, and its media type.
func (s *Service) readManifestRequest(ref, mediaType string, body []byte) (string, string, manifestDoc, error) {
	digest := SHA256(body)
	if IsDigest(ref) {
		if err := ValidDigest(ref); err != nil {
			return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
		}
		if strings.HasPrefix(ref, "sha512:") {
			h, _ := newHash(ref)
			_, _ = h.Write(body)
			digest = fmt.Sprintf("sha512:%x", h.Sum(nil))
		}
		if digest != ref {
			return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeDigestInvalid, fmt.Sprintf("the manifest's digest is %s, not the %s in the address", digest, ref), "")
		}
	} else if err := ValidTag(ref); err != nil {
		return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeTagInvalid, err.Error(), "")
	}
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeManifestInvalid, "the manifest is not valid JSON: "+err.Error(), "")
	}
	if doc.MediaType != "" && mediaType != "" && doc.MediaType != mediaType {
		return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeManifestInvalid, fmt.Sprintf("the manifest says it is %s but was sent as %s", doc.MediaType, mediaType), "")
	}
	if mediaType == "" {
		mediaType = doc.MediaType
	}
	if !isImageMedia(mediaType) && !isIndexMedia(mediaType) {
		return "", "", manifestDoc{}, newErr(http.StatusBadRequest, CodeManifestInvalid,
			fmt.Sprintf("media type %q is not accepted: send an OCI or Docker v2 image manifest or index", mediaType), "Schema 1 manifests are not supported; push with a current client.")
	}
	return digest, mediaType, doc, nil
}

// manifestRecord is everything recordManifest writes.
type manifestRecord struct {
	repo, ref, digest, mediaType string
	size                         int
	who                          Who
	immutable                    bool
	eff                          Effective
	pm                           parsedManifest
}

// recordManifest writes a manifest, what it needs, its tag and the age of
// everything it uses in one transaction, so that garbage collection never
// takes a blob a manifest being pushed names.
func (s *Service) recordManifest(ctx context.Context, m manifestRecord) error {
	now := s.now()
	repo, digest, who := m.repo, m.digest, m.who
	return s.withTx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO registry_manifests(repo, digest, media_type, artifact_type, size, subject, created_at, created_by, run_id, annotations)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (repo, digest) DO NOTHING`,
			repo, digest, m.mediaType, m.pm.artifactType, m.size, m.pm.subject, now, who.Name, who.Run, string(m.pm.annJSON)); err != nil {
			return err
		}
		for _, n := range m.pm.needs {
			if _, err := tx.Exec(ctx, `INSERT INTO registry_manifest_refs(repo, manifest, ref, kind) VALUES(?, ?, ?, ?) ON CONFLICT (repo, manifest, ref) DO NOTHING`,
				repo, digest, n.digest, n.kind); err != nil {
				return err
			}
			if n.kind != "manifest" {
				res, err := tx.Exec(ctx, `UPDATE registry_repo_blobs SET linked_at = ? WHERE repo = ? AND digest = ?`, now, repo, n.digest)
				if err != nil {
					return err
				}
				if c, _ := res.RowsAffected(); c == 0 {
					return newErr(http.StatusBadRequest, CodeManifestBlobUnknown, "blob "+n.digest+" was removed while the manifest was being pushed", "Push again.")
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE registry_repo_blobs SET linked_at = ? WHERE repo = ? AND digest = ?`, now, repo, digest); err != nil {
			return err
		}
		if !IsDigest(m.ref) {
			if err := s.setTag(ctx, tx, repo, m.ref, digest, who.Name, m.immutable, m.eff); err != nil {
				return err
			}
		}
		return s.touchRepo(ctx, tx, repo)
	})
}

type manifestNeed struct {
	digest, kind string
	size         int64
}

// parsedManifest is what a manifest says, checked against the repository.
type parsedManifest struct {
	needs        []manifestNeed
	annJSON      []byte
	total        int64
	artifactType string
	subject      string
}

// checkManifestContent reads what a manifest needs and checks that the
// repository has all of it, and works out what the registry stores beside it.
func (s *Service) checkManifestContent(ctx context.Context, repo, mediaType string, body []byte, doc manifestDoc) (parsedManifest, error) {
	var annotations = map[string]string{}
	for k, v := range doc.Annotations {
		if !strings.HasPrefix(k, "io.launchway.platforms") && !strings.HasPrefix(k, "io.launchway.total_size") {
			annotations[k] = v
		}
	}
	needs, platforms, total, err := listNeeds(mediaType, doc)
	if err != nil {
		return parsedManifest{}, err
	}
	total += int64(len(body))
	if doc.Subject != nil && ValidDigest(doc.Subject.Digest) != nil {
		return parsedManifest{}, newErr(http.StatusBadRequest, CodeManifestInvalid, "the subject digest is not valid", "")
	}
	children, err := s.verifyNeeds(ctx, repo, needs)
	if err != nil {
		return parsedManifest{}, err
	}
	total += children
	sort.Strings(platforms)
	if len(platforms) > 0 {
		annotations[AnnPlatforms] = strings.Join(platforms, ",")
	}
	annotations[AnnTotalSize] = fmt.Sprint(total)
	annJSON, _ := json.Marshal(annotations)
	artifactType := doc.ArtifactType
	if artifactType == "" && doc.Config != nil && doc.Config.MediaType != "application/vnd.oci.image.config.v1+json" && doc.Config.MediaType != "application/vnd.docker.container.image.v1+json" {
		artifactType = doc.Config.MediaType // an OCI artifact: its type is its config's
	}
	subject := ""
	if doc.Subject != nil {
		subject = doc.Subject.Digest
	}

	return parsedManifest{needs: needs, annJSON: annJSON, total: total, artifactType: artifactType, subject: subject}, nil
}

// listNeeds reads what a manifest names (an image's config and layers, an
// index's manifests), the platforms an index lists, and the bytes it adds up
// to apart from the manifests it lists.
func listNeeds(mediaType string, doc manifestDoc) (needs []manifestNeed, platforms []string, total int64, err error) {
	if isImageMedia(mediaType) {
		if doc.Config == nil || ValidDigest(doc.Config.Digest) != nil {
			return nil, nil, 0, newErr(http.StatusBadRequest, CodeManifestInvalid, "the manifest has no valid config descriptor", "")
		}
		needs = append(needs, manifestNeed{doc.Config.Digest, "config", doc.Config.Size})
		total += doc.Config.Size
		for _, l := range doc.Layers {
			if ValidDigest(l.Digest) != nil {
				return nil, nil, 0, newErr(http.StatusBadRequest, CodeManifestInvalid, fmt.Sprintf("layer digest %q is not valid", l.Digest), "")
			}
			needs = append(needs, manifestNeed{l.Digest, "layer", l.Size})
			total += l.Size
		}
		return needs, nil, total, nil
	}
	for _, m := range doc.Manifests {
		if ValidDigest(m.Digest) != nil {
			return nil, nil, 0, newErr(http.StatusBadRequest, CodeManifestInvalid, fmt.Sprintf("manifest digest %q is not valid", m.Digest), "")
		}
		needs = append(needs, manifestNeed{m.Digest, "manifest", m.Size})
		if m.Platform != nil && m.Platform.OS != "" && m.Platform.OS != "unknown" {
			p := m.Platform.OS + "/" + m.Platform.Architecture
			if m.Platform.Variant != "" {
				p += "/" + m.Platform.Variant
			}
			platforms = append(platforms, p)
		}
	}
	return needs, platforms, 0, nil
}

// verifyNeeds checks that the repository has everything a manifest names and
// returns the bytes of the manifests an index lists.
func (s *Service) verifyNeeds(ctx context.Context, repo string, needs []manifestNeed) (int64, error) {
	var total int64
	for _, n := range needs {
		if n.kind == "manifest" {
			var size int64
			var ann string
			err := s.db.QueryRow(ctx, `SELECT size, annotations FROM registry_manifests WHERE repo = ? AND digest = ?`, repo, n.digest).Scan(&size, &ann)
			if isNoRows(err) {
				return 0, newErr(http.StatusBadRequest, CodeManifestBlobUnknown, "the index names manifest "+n.digest+", which this repository does not have", "Push the image manifests before the index that lists them.")
			}
			if err != nil {
				return 0, err
			}
			total += childTotal(ann, size)
			continue
		}
		b, err := s.BlobInfo(ctx, repo, n.digest)
		if err != nil {
			if asError(err).Code == CodeBlobUnknown {
				return 0, newErr(http.StatusBadRequest, CodeManifestBlobUnknown, "the manifest needs blob "+n.digest+", which this repository does not have", "Upload every layer and the config before the manifest.")
			}
			return 0, err
		}
		if n.size > 0 && b.Size != n.size {
			return 0, newErr(http.StatusBadRequest, CodeManifestInvalid, fmt.Sprintf("blob %s is %d bytes but the manifest says %d", n.digest, b.Size, n.size), "")
		}
	}
	return total, nil
}

func tagOf(ref string) string {
	if IsDigest(ref) {
		return ""
	}
	return ref
}

func immutableErr(repo, tag, cur string) *Error {
	return newErr(http.StatusConflict, CodeTagImmutable,
		fmt.Sprintf("tag %s of %s is immutable and already points at %s", tag, repo, cur),
		"Push a new tag (a new version), or ask a project administrator to change the repository's immutable tag patterns.")
}

func childTotal(annotations string, size int64) int64 {
	var m map[string]string
	if json.Unmarshal([]byte(annotations), &m) == nil {
		var n int64
		if _, err := fmt.Sscan(m[AnnTotalSize], &n); err == nil && n > 0 {
			return n
		}
	}
	return size
}

func (s *Service) tagDigest(ctx context.Context, q execer, repo, tag string) (string, bool, error) {
	var d string
	err := q.QueryRow(ctx, `SELECT digest FROM registry_tags WHERE repo = ? AND tag = ?`, repo, tag).Scan(&d)
	if isNoRows(err) {
		return "", false, nil
	}
	return d, err == nil, err
}

// setTag points a tag at a manifest inside a transaction.
func (s *Service) setTag(ctx context.Context, tx Tx, repo, tag, digest, by string, immutable bool, eff Effective) error {
	now := s.now()
	cur, ok, err := s.tagDigest(ctx, tx, repo, tag)
	if err != nil {
		return err
	}
	if ok && cur == digest {
		return nil
	}
	if !ok {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM registry_tags WHERE repo = ?`, repo).Scan(&n); err != nil {
			return err
		}
		if n >= s.limits.MaxTagsPerRepository {
			return newErr(http.StatusForbidden, CodeDenied, fmt.Sprintf("%s already has %d tags, the most the registry allows", repo, n),
				"Delete tags you no longer need, or set a retention rule so old ones are removed.")
		}
	}
	if immutable {
		if ok {
			return immutableErr(repo, tag, cur)
		}
		res, err := tx.Exec(ctx, `INSERT INTO registry_tags(repo, tag, digest, updated_at, updated_by) VALUES(?, ?, ?, ?, ?) ON CONFLICT (repo, tag) DO NOTHING`, repo, tag, digest, now, by)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			cur, _, _ := s.tagDigest(ctx, tx, repo, tag)
			return immutableErr(repo, tag, cur)
		}
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO registry_tags(repo, tag, digest, updated_at, updated_by) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT (repo, tag) DO UPDATE SET digest = excluded.digest, updated_at = excluded.updated_at, updated_by = excluded.updated_by`, repo, tag, digest, now, by)
	return err
}

// ResolveManifest finds a manifest of a repository by tag or digest and
// returns its row and, if withBody, its bytes.
func (s *Service) ResolveManifest(ctx context.Context, repo, ref string, withBody bool) (Manifest, []byte, error) {
	digest := ref
	if !IsDigest(ref) {
		if err := ValidTag(ref); err != nil {
			return Manifest{}, nil, newErr(http.StatusBadRequest, CodeTagInvalid, err.Error(), "")
		}
		d, ok, err := s.tagDigest(ctx, s.db, repo, ref)
		if err != nil {
			return Manifest{}, nil, err
		}
		if !ok {
			return Manifest{}, nil, newErr(http.StatusNotFound, CodeManifestUnknown, fmt.Sprintf("%s has no tag %s", repo, ref), "")
		}
		digest = d
	} else if err := ValidDigest(ref); err != nil {
		return Manifest{}, nil, newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
	}
	m, err := s.manifestRow(ctx, repo, digest)
	if err != nil {
		return m, nil, err
	}
	if !withBody {
		return m, nil, nil
	}
	b, err := s.BlobInfo(ctx, repo, digest)
	if err != nil {
		return m, nil, newErr(http.StatusNotFound, CodeManifestUnknown, "the manifest's bytes are not in this repository", "")
	}
	body, err := s.readAll(ctx, b)
	return m, body, err
}

func (s *Service) manifestRow(ctx context.Context, repo, digest string) (Manifest, error) {
	m := Manifest{Repo: repo, Digest: digest, Tags: []string{}}
	var ann string
	err := s.db.QueryRow(ctx, `SELECT media_type, artifact_type, size, subject, created_at, created_by, run_id, annotations FROM registry_manifests WHERE repo = ? AND digest = ?`,
		repo, digest).Scan(&m.MediaType, &m.ArtifactType, &m.Size, &m.Subject, &m.CreatedAt, &m.CreatedBy, &m.RunID, &ann)
	if isNoRows(err) {
		return m, newErr(http.StatusNotFound, CodeManifestUnknown, fmt.Sprintf("%s has no manifest %s", repo, digest), "")
	}
	if err != nil {
		return m, err
	}
	fillAnnotations(&m, ann)
	return m, nil
}

func fillAnnotations(m *Manifest, ann string) {
	m.Annotations = map[string]string{}
	_ = json.Unmarshal([]byte(ann), &m.Annotations)
	if v := m.Annotations[AnnPlatforms]; v != "" {
		m.Platforms = strings.Split(v, ",")
	}
	m.TotalSize = m.Size
	if v := m.Annotations[AnnTotalSize]; v != "" {
		_, _ = fmt.Sscan(v, &m.TotalSize)
	}
}

// ListTags returns a repository's tags in order, after last, at most n
// (n <= 0: all), and whether more follow.
func (s *Service) ListTags(ctx context.Context, repo string, n int, last string) ([]string, bool, error) {
	if _, err := s.repoRow(ctx, repo); err != nil {
		return nil, false, err
	}
	q := `SELECT tag FROM registry_tags WHERE repo = ? AND tag > ? ORDER BY tag`
	args := []any{repo, last}
	if n > 0 {
		q += ` LIMIT ?`
		args = append(args, n+1)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, false, err
		}
		out = append(out, t)
	}
	more := false
	if n > 0 && len(out) > n {
		out, more = out[:n], true
	}
	return out, more, rows.Err()
}

// ListRepositories returns repositories whose names are after last, in
// order, keeping those keep says yes to, at most n (n <= 0: all).
func (s *Service) ListRepositories(ctx context.Context, project string, keep func(ctx context.Context, name string) bool) ([]Repository, error) {
	q := `SELECT name, project, org, created_at, created_by, pushed_at FROM registry_repositories`
	var args []any
	if project != "" {
		q += ` WHERE project = ?`
		args = append(args, project)
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repository
	for rows.Next() {
		var r Repository
		if err := rows.Scan(&r.Name, &r.Project, &r.Org, &r.CreatedAt, &r.CreatedBy, &r.PushedAt); err != nil {
			return nil, err
		}
		if keep == nil || keep(ctx, r.Name) {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// Referrers lists the manifests of a repository whose subject is digest,
// optionally only of one artifact type, as descriptors.
func (s *Service) Referrers(ctx context.Context, repo, digest, artifactType string) ([]Manifest, error) {
	q := `SELECT digest FROM registry_manifests WHERE repo = ? AND subject = ?`
	args := []any{repo, digest}
	if artifactType != "" {
		q += ` AND artifact_type = ?`
		args = append(args, artifactType)
	}
	digests, err := column(ctx, s.db, q+` ORDER BY created_at, digest`, args...)
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, d := range digests {
		m, err := s.manifestRow(ctx, repo, d)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// DeleteTag removes a tag (the image stays, untagged, until retention or an
// explicit delete). An immutable tag cannot be removed.
func (s *Service) DeleteTag(ctx context.Context, repo, tag string, who Who) error {
	eff, err := s.EffectivePolicy(ctx, repo)
	if err != nil {
		return err
	}
	if matchAny(eff.ImmutableTags, tag) {
		d, _, _ := s.tagDigest(ctx, s.db, repo, tag)
		return immutableErr(repo, tag, d)
	}
	res, err := s.db.Exec(ctx, `DELETE FROM registry_tags WHERE repo = ? AND tag = ?`, repo, tag)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return newErr(http.StatusNotFound, CodeManifestUnknown, fmt.Sprintf("%s has no tag %s", repo, tag), "")
	}
	s.emit(ctx, Event{Type: "untag", Repo: repo, Tag: tag, Actor: who.Name, Run: who.Run})
	return nil
}

// DeleteManifest deletes a manifest by digest, with its tags and the
// signatures, SBOMs and attestations attached to it (manifests whose subject
// it is, and cosign-style .sig, .att and .sbom tags). A manifest with an
// immutable tag is refused.
func (s *Service) DeleteManifest(ctx context.Context, repo, digest string, who Who) ([]string, error) {
	if _, err := s.manifestRow(ctx, repo, digest); err != nil {
		return nil, err
	}
	eff, err := s.EffectivePolicy(ctx, repo)
	if err != nil {
		return nil, err
	}
	tags, err := s.tagsOf(ctx, repo, digest)
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		if matchAny(eff.ImmutableTags, t) {
			return nil, immutableErr(repo, t, digest)
		}
	}
	if s.o.Protected != nil && s.o.Protected(ctx, repo, digest) {
		return nil, newErr(http.StatusConflict, CodeDenied, "this image is in use (a deployment refers to it) and cannot be deleted", "Roll the deployment off this digest first.")
	}
	victims := map[string]bool{digest: true}
	var collect func(d string) error
	collect = func(d string) error {
		more, err := column(ctx, s.db, `SELECT digest FROM registry_manifests WHERE repo = ? AND subject = ?`, repo, d)
		if err != nil {
			return err
		}
		_, hexd, _ := strings.Cut(d, ":")
		for _, suffix := range []string{".sig", ".att", ".sbom"} {
			if x, ok, err := s.tagDigest(ctx, s.db, repo, "sha256-"+hexd+suffix); err != nil {
				return err
			} else if ok {
				more = append(more, x)
			}
		}
		for _, x := range more {
			if !victims[x] {
				victims[x] = true
				if err := collect(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(digest); err != nil {
		return nil, err
	}
	var gone []string
	err = s.withTx(ctx, func(tx Tx) error {
		for d := range victims {
			if _, err := tx.Exec(ctx, `DELETE FROM registry_tags WHERE repo = ? AND digest = ?`, repo, d); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM registry_manifest_refs WHERE repo = ? AND manifest = ?`, repo, d); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM registry_manifests WHERE repo = ? AND digest = ?`, repo, d); err != nil {
				return err
			}
			gone = append(gone, d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(gone)
	s.emit(ctx, Event{Type: "delete", Repo: repo, Digest: digest, Actor: who.Name, Run: who.Run, Detail: fmt.Sprintf("%d manifests with attachments", len(gone))})
	return gone, nil
}

// TagsOf lists the tags pointing at a manifest.
func (s *Service) TagsOf(ctx context.Context, repo, digest string) ([]string, error) {
	return s.tagsOf(ctx, repo, digest)
}

func (s *Service) tagsOf(ctx context.Context, repo, digest string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT tag FROM registry_tags WHERE repo = ? AND digest = ? ORDER BY tag`, repo, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Image is what the Images tab shows of one pushed image or artifact: a
// top-level manifest with its tags and what is attached to it.
type Image struct {
	Manifest
	Attached []Manifest `json:"attached,omitempty"`
	// Vulnerabilities, when a scan result is attached.
	Vulnerabilities *VulnSummary `json:"vulnerabilities,omitempty"`
	Signed          bool         `json:"signed"`
	HasSBOM         bool         `json:"has_sbom"`
	HasProvenance   bool         `json:"has_provenance"`
	Children        []string     `json:"children,omitempty"`
}

// VulnSummary counts what a scan found.
type VulnSummary struct {
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
	Scanner  string `json:"scanner,omitempty"`
}

// Artifact types Launchway attaches to an image.
const (
	TypeSignature   = "application/vnd.dev.cosign.artifact.sig.v1+json"
	TypeSBOM        = "application/vnd.cyclonedx+json"
	TypeSPDX        = "application/spdx+json"
	TypeAttestation = "application/vnd.in-toto+json"
	TypeScan        = "application/vnd.launchway.scan.v1+json"
)

// Images lists a repository's images: manifests that are not children of an
// index and not attachments, newest first, with their tags.
func (s *Service) Images(ctx context.Context, repo string) ([]Image, error) {
	if _, err := s.repoRow(ctx, repo); err != nil {
		return nil, err
	}
	digests, err := column(ctx, s.db, `SELECT digest FROM registry_manifests m WHERE repo = ? AND subject = ''
		AND NOT EXISTS (SELECT 1 FROM registry_manifest_refs r WHERE r.repo = m.repo AND r.ref = m.digest AND r.kind = 'manifest')
		ORDER BY created_at DESC, digest`, repo)
	if err != nil {
		return nil, err
	}
	byDigest, tagsOf, err := s.tagMaps(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []Image
	for _, d := range digests {
		// A cosign-style signature tag's manifest is an attachment of another image.
		if isAttachmentOnly(byDigest[d]) {
			continue
		}
		im, err := s.image(ctx, repo, d, byDigest, tagsOf)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

func isAttachmentOnly(tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	for _, t := range tags {
		if !isAttachmentTag(t) {
			return false
		}
	}
	return true
}

func isAttachmentTag(t string) bool {
	if !strings.HasPrefix(t, "sha256-") {
		return false
	}
	for _, suf := range []string{".sig", ".att", ".sbom"} {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

// InspectImage returns one image (by tag or digest) as the Images tab shows it.
func (s *Service) InspectImage(ctx context.Context, repo, ref string) (Image, error) {
	m, _, err := s.ResolveManifest(ctx, repo, ref, false)
	if err != nil {
		return Image{}, err
	}
	byDigest, tagsOf, err := s.tagMaps(ctx, repo)
	if err != nil {
		return Image{}, err
	}
	for _, t := range byDigest {
		sort.Strings(t)
	}
	return s.image(ctx, repo, m.Digest, byDigest, tagsOf)
}

func (s *Service) image(ctx context.Context, repo, digest string, byDigest map[string][]string, tagsOf map[string]string) (Image, error) {
	m, err := s.manifestRow(ctx, repo, digest)
	if err != nil {
		return Image{}, err
	}
	m.Tags = append([]string{}, byDigest[digest]...)
	im := Image{Manifest: m}
	if im.Children, err = column(ctx, s.db, `SELECT ref FROM registry_manifest_refs WHERE repo = ? AND manifest = ? AND kind = 'manifest' ORDER BY ref`, repo, digest); err != nil {
		return im, err
	}
	att, err := s.Referrers(ctx, repo, digest, "")
	if err != nil {
		return im, err
	}
	_, hexd, _ := strings.Cut(digest, ":")
	for _, suffix := range []string{".sig", ".att", ".sbom"} {
		if d, ok := tagsOf["sha256-"+hexd+suffix]; ok {
			if am, err := s.manifestRow(ctx, repo, d); err == nil {
				am.Tags = byDigest[d]
				if am.ArtifactType == "" {
					am.ArtifactType = map[string]string{".sig": TypeSignature, ".att": TypeAttestation, ".sbom": TypeSBOM}[suffix]
				}
				att = append(att, am)
			}
		}
	}
	for _, a := range att {
		im.Attached = append(im.Attached, a)
		if a.ArtifactType == TypeAttestation {
			// A cosign attestation says what it attests in its layer's predicate type.
			sbom, prov := s.attestationKinds(ctx, repo, a.Digest)
			im.HasSBOM, im.HasProvenance = im.HasSBOM || sbom, im.HasProvenance || prov
		}
		switch a.ArtifactType {
		case TypeSignature, "application/vnd.dev.sigstore.bundle.v0.3+json", "application/vnd.dev.sigstore.bundle+json;version=0.3":
			im.Signed = true
		case TypeSBOM, TypeSPDX:
			im.HasSBOM = true
		case TypeScan:
			v := &VulnSummary{Scanner: a.Annotations[AnnScanner]}
			v.Critical, v.High, v.Medium, v.Low = atoi(a.Annotations[AnnVulnCritical]), atoi(a.Annotations[AnnVulnHigh]), atoi(a.Annotations[AnnVulnMedium]), atoi(a.Annotations[AnnVulnLow])
			im.Vulnerabilities = v
		}
	}
	return im, nil
}

func atoi(s string) int {
	var n int
	_, _ = fmt.Sscan(s, &n)
	return n
}

// attestationKinds reads an attestation manifest's layers for what they attest.
func (s *Service) attestationKinds(ctx context.Context, repo, digest string) (sbom, provenance bool) {
	b, err := s.BlobInfo(ctx, repo, digest)
	if err != nil {
		return false, false
	}
	body, err := s.readAll(ctx, b)
	if err != nil {
		return false, false
	}
	var doc struct {
		Layers []struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return false, false
	}
	for _, l := range doc.Layers {
		p := strings.ToLower(l.Annotations["predicateType"])
		sbom = sbom || strings.Contains(p, "cyclonedx") || strings.Contains(p, "spdx")
		provenance = provenance || strings.Contains(p, "slsa") || strings.Contains(p, "provenance")
	}
	return sbom, provenance
}

// tagMaps reads a repository's tags both ways: the tags of each digest
// (sorted) and the digest of each tag.
func (s *Service) tagMaps(ctx context.Context, repo string) (byDigest map[string][]string, tagsOf map[string]string, err error) {
	byDigest, tagsOf = map[string][]string{}, map[string]string{}
	err = scanAll(ctx, s.db, `SELECT tag, digest FROM registry_tags WHERE repo = ? ORDER BY tag`, []any{repo}, func(scan func(...any) error) error {
		var t, d string
		if err := scan(&t, &d); err != nil {
			return err
		}
		byDigest[d] = append(byDigest[d], t)
		tagsOf[t] = d
		return nil
	})
	return byDigest, tagsOf, err
}
