package engine

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Image steps (schema 15; see internal/pipeline/image.go and
// internal/imagebuild). The server works out the step's values and decides
// whether it may push; a runner that can build images (FeatureImages)
// builds, scans, pushes and signs, and reports what it made. The report is
// a summary in the event that ends the attempt (Payload.Image), and the
// images and image_refs tables are derived from it. The SBOM, the
// vulnerabilities and the provenance are the step's reports and artifacts,
// kept like any step's (schema 12).
//
// Pull-request and draft runs never push: the server tells the runner so
// (ImageJob.Push), and the runner, which never reads push credentials for
// such a run, refuses a push again on its own.

// FeatureImages is what a runner with image builders says it can do.
const FeatureImages = "images"

// ImageJob is what a runner needs to build an image step's image.
type ImageJob struct {
	Context   string            `json:"context"`
	File      string            `json:"file"`
	Target    string            `json:"target,omitempty"`
	Platforms []string          `json:"platforms,omitempty"`
	BuildArgs map[string]string `json:"build_args,omitempty"`
	Tags      []ImageJobTag     `json:"tags"`
	Push      bool              `json:"push"`
	// PullCredentials says the build may have registries' pull credentials
	// (schema 15): pull-request and draft runs only when they may read
	// secrets. A runner treats a job without it, for such a run, as no.
	PullCredentials bool                `json:"pull_credentials,omitempty"`
	Cache           *ImageJobRepo       `json:"cache,omitempty"`
	SBOM            bool                `json:"sbom,omitempty"`
	Provenance      bool                `json:"provenance,omitempty"`
	Sign            *pipeline.ImageSign `json:"sign,omitempty"`
	Scan            *pipeline.ImageScan `json:"scan,omitempty"`
	// What provenance records: the run's event, ref and commit, where its
	// source and libraries came from (credentials removed).
	Event      string            `json:"event"`
	Ref        string            `json:"ref,omitempty"`
	Repository string            `json:"repository,omitempty"`
	Libraries  []ImageJobLibrary `json:"libraries,omitempty"`
}

// ImageJobTag is one name to push under, its tag worked out.
type ImageJobTag struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// ImageJobRepo is a repository of a registry.
type ImageJobRepo struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
}

// ImageJobLibrary is a library the run used, for provenance.
type ImageJobLibrary struct {
	Name    string `json:"name"`
	Repo    string `json:"repo,omitempty"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit"`
}

// ImageRecord is what an image step attempt made, as its runner reports
// it and as it is kept. Never a credential, never a secret's value.
type ImageRecord struct {
	Digest     string          `json:"digest"`
	MediaType  string          `json:"media_type,omitempty"`
	Size       int64           `json:"size"`
	Platforms  []ImagePlatform `json:"platforms"`
	Image      string          `json:"image,omitempty"` // host/repository@digest, when pushed
	Tags       []string        `json:"tags"`
	Pushed     bool            `json:"pushed"`
	NotPushed  string          `json:"not_pushed,omitempty"`
	Signed     bool            `json:"signed"`
	KeyID      string          `json:"key_id,omitempty"`
	KeySource  string          `json:"key_source,omitempty"`
	Signatures []string        `json:"signatures,omitempty"`
	Provenance string          `json:"provenance,omitempty"` // "", stored or attached
	SBOM       bool            `json:"sbom"`
	Scan       *ImageScanSum   `json:"scan,omitempty"`
	Builders   []string        `json:"builders"`
	Steps      int             `json:"steps"`
	Cached     int             `json:"cached"`
	Cache      string          `json:"cache,omitempty"`
	BaseImages []string        `json:"base_images,omitempty"`
}

// ImagePlatform is one platform's image.
type ImagePlatform struct {
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
}

// ImageScanSum is what the vulnerability scan found, and the policy.
type ImageScanSum struct {
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	Medium   int    `json:"medium"`
	Low      int    `json:"low"`
	Unknown  int    `json:"unknown"`
	FailOn   string `json:"fail_on,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Failed   bool   `json:"failed,omitempty"`
}

var (
	imgDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imgRefRe    = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,400}$`)
	purlRe      = regexp.MustCompile(`^pkg:[A-Za-z0-9._:/@?=&%+-]{1,400}$`)
)

// clean bounds what a runner sent before it is kept, and drops what does
// not look like what it should be. It reports false if nothing usable is
// left (no digest).
func (r *ImageRecord) clean() bool {
	if r == nil || !imgDigestRe.MatchString(r.Digest) {
		return false
	}
	line := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		for len(s) > n {
			_, size := utf8.DecodeLastRuneInString(s)
			s = s[:len(s)-size]
		}
		return s
	}
	refs := func(list []string, n int) []string {
		var out []string
		for _, s := range list {
			if imgRefRe.MatchString(s) && len(out) < n {
				out = append(out, s)
			}
		}
		return out
	}
	r.MediaType = line(r.MediaType, 100)
	if !imgRefRe.MatchString(r.Image) {
		r.Image = ""
	}
	r.Tags = refs(r.Tags, pipeline.MaxImageTags)
	if r.Tags == nil {
		r.Tags = []string{}
	}
	var ps []ImagePlatform
	for _, p := range r.Platforms {
		if imgDigestRe.MatchString(p.Digest) && len(ps) < 32 {
			p.Platform = line(p.Platform, 60)
			ps = append(ps, p)
		}
	}
	if ps == nil {
		ps = []ImagePlatform{}
	}
	r.Platforms = ps
	r.NotPushed = line(r.NotPushed, 200)
	r.KeyID = line(r.KeyID, 64)
	r.KeySource = line(r.KeySource, 300)
	r.Signatures = refs(r.Signatures, 100)
	switch r.Provenance {
	case "", "stored", "attached":
	default:
		r.Provenance = ""
	}
	r.Builders = refs(r.Builders, 16)
	if r.Builders == nil {
		r.Builders = []string{}
	}
	r.Cache = line(r.Cache, 300)
	var bases []string
	for _, b := range r.BaseImages {
		// Package URLs: pkg:docker/alpine@3.22?platform=linux%2Famd64.
		if purlRe.MatchString(b) && len(bases) < 50 {
			bases = append(bases, b)
		}
	}
	r.BaseImages = bases
	if r.Scan != nil {
		r.Scan.FailOn, r.Scan.Mode = line(r.Scan.FailOn, 10), line(r.Scan.Mode, 10)
	}
	return true
}

// imageJob works out an image step's values for the runner. An error says
// which value could not be had (an output a step did not write, say).
func (c *cmd) imageJob(def *pipeline.Step) (*ImageJob, error) {
	st := c.st
	b := def.ImageBuild
	if b == nil {
		return nil, fmt.Errorf("the step says nothing to build")
	}
	j := &ImageJob{Context: b.Context, File: b.File, Target: b.Target, Platforms: slices.Clone(b.Platforms),
		SBOM: b.SBOM, Provenance: b.Provenance, Sign: b.Sign, Scan: b.Scan,
		Event: st.Trigger.Event, Ref: st.Trigger.Ref, Repository: redact.URLs(st.Source.URL), Tags: []ImageJobTag{}}
	if len(b.BuildArgs) > 0 {
		j.BuildArgs = map[string]string{}
		for _, k := range slices.Sorted(maps.Keys(b.BuildArgs)) {
			v, err := c.resolve(def, b.BuildArgs[k])
			if err != nil {
				return nil, fmt.Errorf("build argument %s: %w", k, err)
			}
			if strings.ContainsAny(v, "\x00") || len(v) > 4096 {
				return nil, fmt.Errorf("build argument %s: the value is not text of at most 4096 bytes", k)
			}
			j.BuildArgs[k] = v
		}
	}
	for i, t := range b.Tags {
		v, err := c.resolve(def, t.Tag)
		if err != nil {
			return nil, fmt.Errorf("tag %d (%s): %w", i+1, t.Repository, err)
		}
		if !pipelineTagRe.MatchString(v) {
			return nil, fmt.Errorf("tag %d (%s): %q is not a valid tag (letters, digits, _, . and -, up to 128, not starting with . or -)", i+1, t.Repository, clip(v, 60))
		}
		j.Tags = append(j.Tags, ImageJobTag{Registry: t.Registry, Repository: t.Repository, Tag: v})
	}
	if b.Cache != nil {
		j.Cache = &ImageJobRepo{Registry: b.Cache.Registry, Repository: b.Cache.Repository}
	}
	// Pull requests and drafts never push, whatever the pipeline at their
	// head says; that pipeline is not reviewed.
	j.Push = b.Pushes() && len(j.Tags) > 0 && st.Trigger.Event != pipeline.EventPullRequest && st.Draft == nil
	// Pull credentials are secrets too: a Dockerfile can send whatever they
	// pull anywhere.
	j.PullCredentials = st.secretsAllowed()
	for _, l := range st.Source.Libraries {
		lj := ImageJobLibrary{Name: l.Name, Repo: redact.URLs(l.URL), Commit: l.Commit}
		if lib := st.Pipeline.Library(l.Name); lib != nil {
			lj.Version = lib.Wanted()
		}
		j.Libraries = append(j.Libraries, lj)
	}
	return j, nil
}

var pipelineTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// imageOutputs are the outputs an image step attempt sets.
func imageOutputs(r *ImageRecord) map[string]string {
	if r == nil {
		return nil
	}
	return map[string]string{"digest": r.Digest, "image": r.Image, "tags": strings.Join(r.Tags, ","),
		"pushed": strconv.FormatBool(r.Pushed)}
}

// persistImages writes what image steps made to the derived tables.
func (c *cmd) persistImages() error {
	st := c.st
	for _, ev := range c.events {
		p := ev.Data
		if p.Image == nil || (ev.Type != StepFinished && ev.Type != StepCanceled) {
			continue
		}
		r := p.Image
		summary, err := json.Marshal(r)
		if err != nil {
			return err
		}
		outcome := p.Outcome
		if ev.Type == StepCanceled {
			outcome = "canceled"
		}
		if _, err := c.tx.Exec(`INSERT INTO images(run_id, step_id, attempt, project, run_number, digest, image, pushed, signed, outcome, summary, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET digest = excluded.digest, image = excluded.image, pushed = excluded.pushed,
				signed = excluded.signed, outcome = excluded.outcome, summary = excluded.summary`,
			st.ID, p.Step, p.Attempt, st.Project, st.Number, r.Digest, r.Image, boolInt(r.Pushed), boolInt(r.Signed), outcome,
			string(summary), ev.At); err != nil {
			return err
		}
		if _, err := c.tx.Exec(`DELETE FROM image_refs WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, p.Step, p.Attempt); err != nil {
			return err
		}
		add := func(kind, repo, tag, digest, platform string) error {
			_, err := c.tx.Exec(`INSERT INTO image_refs(run_id, step_id, attempt, project, run_number, kind, repository, tag, digest, platform, pushed, created_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, st.ID, p.Step, p.Attempt, st.Project, st.Number, kind, repo, tag, digest, platform,
				boolInt(r.Pushed), ev.At)
			return err
		}
		repoOf := func(ref string) (string, string) {
			i := strings.LastIndex(ref, ":")
			if i < 0 || strings.Contains(ref[i:], "/") {
				return ref, ""
			}
			return ref[:i], ref[i+1:]
		}
		firstRepo := ""
		if r.Image != "" {
			firstRepo, _, _ = strings.Cut(r.Image, "@")
		}
		if err := add("index", firstRepo, "", r.Digest, ""); err != nil {
			return err
		}
		for _, t := range r.Tags {
			repo, tag := repoOf(t)
			if err := add("tag", repo, tag, r.Digest, ""); err != nil {
				return err
			}
		}
		for _, pl := range r.Platforms {
			if pl.Digest == r.Digest {
				continue
			}
			if err := add("platform", firstRepo, "", pl.Digest, pl.Platform); err != nil {
				return err
			}
		}
	}
	return nil
}

// ImageView is an image step attempt's image, as the API shows it.
type ImageView struct {
	Step      string `json:"step"`
	Attempt   int    `json:"attempt"`
	Outcome   string `json:"outcome"`
	CreatedAt int64  `json:"created_at"`
	ImageRecord
	// SBOMArtifact and ProvenanceArtifact name the artifacts that hold the
	// SBOM and the provenance, when they were kept.
	SBOMArtifact       string `json:"sbom_artifact,omitempty"`
	ProvenanceArtifact string `json:"provenance_artifact,omitempty"`
}

// RunImages lists what the image steps of a run made: the latest attempt
// of each step that made an image, in pipeline order.
func (e *Engine) RunImages(runID string) ([]ImageView, error) {
	out := []ImageView{}
	if !e.knows(schemaImages) {
		return out, nil
	}
	st, err := e.State(runID)
	if err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT step_id, attempt, outcome, summary, created_at FROM images WHERE run_id = ? ORDER BY step_id, attempt`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	latest := map[string]ImageView{}
	for rows.Next() {
		var v ImageView
		var summary string
		if err := rows.Scan(&v.Step, &v.Attempt, &v.Outcome, &summary, &v.CreatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(summary), &v.ImageRecord) != nil {
			continue
		}
		if prev, ok := latest[v.Step]; !ok || v.Attempt > prev.Attempt {
			latest[v.Step] = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	kept, err := e.keptArtifactNames(runID)
	if err != nil {
		return nil, err
	}
	for _, id := range st.Order {
		v, ok := latest[id]
		if !ok {
			continue
		}
		k := stepAttempt{v.Step, v.Attempt}
		if kept[k][pipeline.SBOMArtifact] {
			v.SBOMArtifact = pipeline.SBOMArtifact
		}
		if kept[k][pipeline.ProvenanceArtifact] {
			v.ProvenanceArtifact = pipeline.ProvenanceArtifact
		}
		out = append(out, v)
	}
	return out, nil
}

// keptArtifactNames lists the artifacts each attempt of a run kept.
func (e *Engine) keptArtifactNames(runID string) (map[stepAttempt]map[string]bool, error) {
	out := map[stepAttempt]map[string]bool{}
	rows, err := e.db.Query(`SELECT step_id, attempt, name FROM artifacts WHERE run_id = ? AND status = ?`, runID, artKept)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k stepAttempt
		var name string
		if err := rows.Scan(&k.step, &k.attempt, &name); err != nil {
			return nil, err
		}
		if out[k] == nil {
			out[k] = map[string]bool{}
		}
		out[k][name] = true
	}
	return out, rows.Err()
}

// ImageHit is a run that built (or pushed) an image.
type ImageHit struct {
	Project    string `json:"project"`
	RunID      string `json:"run_id"`
	RunNumber  int    `json:"run_number"`
	Ref        string `json:"ref"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
	Kind       string `json:"kind"` // index, tag or platform: what matched
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest"`
	Platform   string `json:"platform,omitempty"`
	Pushed     bool   `json:"pushed"`
	Signed     bool   `json:"signed"`
	Outcome    string `json:"outcome"`
	CreatedAt  int64  `json:"created_at"`
}

// ImageQuery is a search for images: by digest (the index's, or a
// platform's), or by repository (host/path, as pushed), optionally its tag.
type ImageQuery struct {
	Digest     string
	Repository string
	Tag        string
	Project    string
	Limit      int // default 50, at most 500
}

// SearchImages answers "which run built this digest" and "what is the
// latest image of this repository": the newest first. onlyProjects, if not
// nil, limits the search to those projects.
func (e *Engine) SearchImages(q ImageQuery, onlyProjects []string) ([]ImageHit, error) {
	out := []ImageHit{}
	q.Digest, q.Repository, q.Tag = strings.TrimSpace(q.Digest), strings.TrimSpace(q.Repository), strings.TrimSpace(q.Tag)
	switch {
	case q.Digest != "" && !imgDigestRe.MatchString(q.Digest):
		return nil, fmt.Errorf("%w: a digest is sha256: and 64 hex digits", ErrInvalid)
	case q.Repository != "" && !imgRefRe.MatchString(q.Repository):
		return nil, fmt.Errorf("%w: %q is not a repository", ErrInvalid, clip(q.Repository, 60))
	case q.Tag != "" && !pipelineTagRe.MatchString(q.Tag):
		return nil, fmt.Errorf("%w: %q is not a tag", ErrInvalid, clip(q.Tag, 60))
	case q.Digest == "" && q.Repository == "" && q.Project == "":
		return nil, fmt.Errorf("%w: give a digest, a repository or a project", ErrInvalid)
	}
	if !e.knows(schemaImages) || (onlyProjects != nil && len(onlyProjects) == 0) {
		return out, nil
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	q.Limit = min(q.Limit, 500)
	sqlq := `SELECT x.project, x.run_id, x.run_number, r.ref, x.step_id, x.attempt, x.kind, x.repository, x.tag, x.digest, x.platform,
			x.pushed, i.signed, i.outcome, x.created_at
		FROM image_refs x JOIN runs r ON r.id = x.run_id
		JOIN images i ON i.run_id = x.run_id AND i.step_id = x.step_id AND i.attempt = x.attempt WHERE 1 = 1`
	var args []any
	if q.Digest != "" {
		sqlq += ` AND x.digest = ?`
		args = append(args, q.Digest)
	}
	if q.Repository != "" {
		sqlq += ` AND x.repository = ? AND x.kind = 'tag'`
		args = append(args, q.Repository)
	}
	if q.Tag != "" {
		sqlq += ` AND x.tag = ?`
		args = append(args, q.Tag)
	}
	if q.Digest == "" && q.Repository == "" {
		sqlq += ` AND x.kind = 'index'`
	}
	if q.Project != "" {
		sqlq += ` AND x.project = ?`
		args = append(args, q.Project)
	}
	if onlyProjects != nil {
		sqlq += ` AND x.project IN (` + strings.TrimSuffix(strings.Repeat("?,", len(onlyProjects)), ",") + `)`
		for _, p := range onlyProjects {
			args = append(args, p)
		}
	}
	sqlq += ` ORDER BY x.created_at DESC, x.run_id, x.step_id, x.kind LIMIT ?`
	args = append(args, q.Limit)
	rows, err := e.db.Query(sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ImageHit
		var pushed, signed int
		if err := rows.Scan(&h.Project, &h.RunID, &h.RunNumber, &h.Ref, &h.Step, &h.Attempt, &h.Kind, &h.Repository, &h.Tag, &h.Digest,
			&h.Platform, &pushed, &signed, &h.Outcome, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Pushed, h.Signed = pushed != 0, signed != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

// imageRefused fails an image step whose values cannot be worked out (a
// step it reads did not write the output), before any runner sees it. It
// reports whether it did.
func (c *cmd) imageRefused(id string, def *pipeline.Step) bool {
	if !c.e.knows(schemaImages) {
		return false
	}
	if _, err := c.imageJob(def); err != nil {
		c.emit(StepFinished, Payload{Step: id, Outcome: string(StepFailed), Reason: err.Error()})
		return true
	}
	return false
}

// imageRunnerHint says, for a run with image steps that has a step waiting
// for a runner, when no runner that builds images has been seen lately:
// otherwise the run would wait without saying why.
func (e *Engine) imageRunnerHint(st *RunState, steps []StepView) (string, error) {
	if !e.knows(schemaImages) || st.Status.Terminal() || !st.Pipeline.HasImageSteps() {
		return "", nil
	}
	queued := false
	for _, s := range steps {
		queued = queued || s.Status == StepQueuedS
	}
	if !queued {
		return "", nil
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runners WHERE last_seen >= ? AND (features = ? OR features LIKE ? OR features LIKE ?)`,
		e.now().Add(-RunnerOffline).UnixMilli(), FeatureImages, FeatureImages+" %", "% "+FeatureImages+"%").Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	return "This run builds images, so all its steps wait for a runner that can build them, and none has been seen in the last " +
		RunnerOffline.String() + ". A runner builds images when its configuration names image builders (images.builders).", nil
}
