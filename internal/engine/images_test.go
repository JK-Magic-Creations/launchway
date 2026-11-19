package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const imagePipeline = `pipeline: shop
on: { push: { branches: [main] }, pull_request: {} }
steps:
  test:
    image: golang
    run: echo version=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    needs: test
    build_args: { VERSION: { output: test.version }, SHA: { run: sha } }
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
      - { registry: harbor, repository: team/shop, tag: latest }
    sbom: true
    scan: { fail_on: critical, mode: enforce }
    sign: true
  smoke:
    image: alpine
    needs: image
    run: echo $LAUNCHWAY_NEEDS_IMAGE_DIGEST
`

var imageRunner = RunnerInfo{ID: "runner-images", Name: "builder", Features: FeatureArtifacts + " " + FeatureImages}

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func takeAs(t *testing.T, e *Engine, r RunnerInfo) *Job {
	t.Helper()
	j, err := e.Lease(context.Background(), r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a queued step, found none")
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	return j
}

func record() *ImageRecord {
	return &ImageRecord{Digest: testDigest, MediaType: "application/vnd.oci.image.index.v1+json", Size: 123456,
		Platforms: []ImagePlatform{{Platform: "linux/amd64", Digest: "sha256:" + strings.Repeat("2", 64), Size: 123000}},
		Image:     "harbor.example.com/team/shop@" + testDigest,
		Tags:      []string{"harbor.example.com/team/shop:0123abcd", "harbor.example.com/team/shop:latest"},
		Pushed:    true, Signed: true, KeyID: "abcdef0123456789", KeySource: "runner", Provenance: "attached", SBOM: true,
		Scan:     &ImageScanSum{High: 2, FailOn: "critical", Mode: "enforce"},
		Builders: []string{"shared"}, Steps: 5, Cached: 3}
}

func TestImageStepRunsAndIsRecorded(t *testing.T) {
	e, _ := newEngine(t)
	sha := "0123abcd0123abcd0123abcd0123abcd0123abcd"
	id := create(t, e, imagePipeline, Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: sha})
	if sum, _ := e.Summary(id); sum.MinSchema != schemaImages {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	// A runner without image builders is never given any step of the run:
	// its steps share a workspace, and the image is built from it.
	none(t, e)
	ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1.4.2"})
	j := takeAs(t, e, imageRunner)
	b := j.ImageBuild
	if j.StepID != "image" || b == nil || !b.Push || b.Event != "push" || b.Ref != "refs/heads/main" {
		t.Fatalf("job: %+v %+v", j, b)
	}
	if b.BuildArgs["VERSION"] != "1.4.2" || b.BuildArgs["SHA"] != sha || len(b.Tags) != 2 || b.Tags[0].Tag != sha[:8] || b.Tags[1].Tag != "latest" {
		t.Fatalf("values: %+v %+v", b.BuildArgs, b.Tags)
	}
	if !b.SBOM || !b.Provenance || b.Sign == nil || !b.Sign.Runner || b.Scan.FailOn != "critical" || j.Script != "" || j.Image != "" {
		t.Fatalf("job: %+v", b)
	}
	if j.Reports == nil || len(j.Reports.Findings) != 1 || j.Artifacts == nil || len(j.Artifacts.Keep) != 2 {
		t.Fatalf("what it keeps: %+v %+v", j.Reports, j.Artifacts)
	}
	zero := 0
	// The runner's own outputs are not taken: the image's are.
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Outputs: map[string]string{"digest": "forged"}, Image: record()}); err != nil {
		t.Fatal(err)
	}
	smoke := takeAs(t, e, imageRunner)
	if smoke.Env["LAUNCHWAY_NEEDS_IMAGE_DIGEST"] != testDigest || smoke.Env["LAUNCHWAY_NEEDS_IMAGE_PUSHED"] != "true" ||
		smoke.Env["LAUNCHWAY_NEEDS_IMAGE_IMAGE"] != "harbor.example.com/team/shop@"+testDigest {
		t.Fatalf("env: %v", smoke.Env)
	}
	ok(t, e, smoke, nil)
	want(t, statuses(t, e, id), "run", "succeeded")

	imgs, err := e.RunImages(id)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("%v %+v", err, imgs)
	}
	if v := imgs[0]; v.Step != "image" || v.Attempt != 1 || v.Outcome != "succeeded" || !v.Signed || v.Scan.High != 2 || v.Cached != 3 {
		t.Fatalf("view: %+v", v)
	}
	// Which run built this digest, by the index's and by a platform's.
	for _, d := range []string{testDigest, "sha256:" + strings.Repeat("2", 64)} {
		hits, err := e.SearchImages(ImageQuery{Digest: d}, nil)
		if err != nil || len(hits) == 0 || hits[0].RunID != id || hits[0].Step != "image" {
			t.Fatalf("search %s: %v %+v", d, err, hits)
		}
	}
	// The latest image of a repository, and by tag.
	hits, err := e.SearchImages(ImageQuery{Repository: "harbor.example.com/team/shop", Tag: "latest"}, nil)
	if err != nil || len(hits) != 1 || hits[0].Digest != testDigest || hits[0].Tag != "latest" || !hits[0].Signed {
		t.Fatalf("by repository: %v %+v", err, hits)
	}
	if hits, _ := e.SearchImages(ImageQuery{Digest: testDigest}, []string{"other"}); len(hits) != 0 {
		t.Fatal("a project the caller may not see was searched")
	}
	if _, err := e.SearchImages(ImageQuery{Digest: "sha256:short"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest: %v", err)
	}
	if _, err := e.SearchImages(ImageQuery{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty query: %v", err)
	}
}

// Pull-request and draft runs build but never push, and their image steps
// may not read build secrets unless the project allows it.
func TestImageStepNeverPushesForPullRequestsOrDrafts(t *testing.T) {
	e, _ := newEngine(t)
	text := strings.Replace(imagePipeline, "  test:\n    image: golang\n    run: echo version=1.4.2 >> \"$LAUNCHWAY_OUTPUTS\"\n", "  test:\n    image: golang\n    run: echo version=1 >> \"$LAUNCHWAY_OUTPUTS\"\n", 1)
	for _, mk := range []func() string{
		func() string { return createPR(t, e, text, 3, "abcdef0123456789abcdef0123456789abcdef01", false) },
		func() string { return createDraft(t, e, text, false) },
	} {
		id := mk()
		ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1"})
		j := takeAs(t, e, imageRunner)
		if j.RunID != id || j.ImageBuild.Push {
			t.Fatalf("a pull-request or draft run's image step may push: %+v", j.ImageBuild)
		}
		zero := 0
		rec := record()
		rec.Pushed, rec.Image, rec.Signed = false, "", false
		if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Image: rec}); err != nil {
			t.Fatal(err)
		}
		ok(t, e, takeAs(t, e, imageRunner), nil)
	}
	withSecret := strings.Replace(imagePipeline, "    sbom: true\n", "    sbom: true\n    secrets: [{ id: npm, secret: { store: vault, key: ci/npm } }]\n", 1)
	id := createPR(t, e, withSecret, 4, "bcdef0123456789abcdef0123456789abcdef012", false)
	ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1"})
	if j, _ := e.Lease(context.Background(), imageRunner, 0); j != nil {
		t.Fatalf("leased %s/%s", j.RunID, j.StepID)
	}
	st := statuses(t, e, id)
	want(t, st, "image", "failed")
}

// A value an image step needs that cannot be had fails the step before
// any runner sees it.
func TestImageStepWithoutItsValueFails(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, imagePipeline, Trigger{Event: "manual", SHA: "0123abcd0123abcd0123abcd0123abcd0123abcd"})
	ok(t, e, takeAs(t, e, imageRunner), nil) // writes no version
	if j, _ := e.Lease(context.Background(), imageRunner, 0); j != nil {
		t.Fatalf("leased %s/%s", j.RunID, j.StepID)
	}
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == "image" && (s.Status != StepFailed || !strings.Contains(s.Error, "test did not write the output version")) {
			t.Fatalf("image step: %s %q", s.Status, s.Error)
		}
	}
}

// What a runner reports is bounded and checked before it is kept.
func TestImageRecordIsCleaned(t *testing.T) {
	r := record()
	r.Tags = append(r.Tags, "bad tag with spaces", strings.Repeat("x", 500))
	r.Platforms = append(r.Platforms, ImagePlatform{Platform: "linux/arm64", Digest: "nope"})
	r.Provenance = "made-up"
	r.NotPushed = strings.Repeat("why ", 200)
	if !r.clean() || len(r.Tags) != 2 || len(r.Platforms) != 1 || r.Provenance != "" || len(r.NotPushed) > 200 {
		t.Fatalf("cleaned: %+v", r)
	}
	if (&ImageRecord{Digest: "sha256:bad"}).clean() || (*ImageRecord)(nil).clean() {
		t.Fatal("a record without a valid digest was kept")
	}
	// A failed attempt keeps what it made, and sets no outputs.
	e, _ := newEngine(t)
	id := create(t, e, imagePipeline, Trigger{Event: "manual", SHA: "0123abcd0123abcd0123abcd0123abcd0123abcd"})
	ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1"})
	j := takeAs(t, e, imageRunner)
	rec := record()
	rec.Pushed, rec.Image, rec.NotPushed = false, "", "the vulnerability scan failed the step"
	rec.Scan.Failed = true
	one := 1
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", ExitCode: &one, Error: "the scan found 1 vulnerability", Image: rec}); err != nil {
		t.Fatal(err)
	}
	imgs, _ := e.RunImages(id)
	if len(imgs) != 1 || imgs[0].Outcome != "failed" || !imgs[0].Scan.Failed || imgs[0].Pushed {
		t.Fatalf("%+v", imgs)
	}
	d, _ := e.GetRun(id)
	for _, s := range d.StepList {
		if s.ID == "image" && len(s.Outputs) != 0 {
			t.Fatalf("a failed image step has outputs %v", s.Outputs)
		}
	}
}

// A server older than image steps leaves their runs alone, and refuses to
// start one; it still runs everything else.
func TestOlderBuildLeavesImageRunsAlone(t *testing.T) {
	n := schemaImages
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	if pipeline.SchemaImages != schemaImages {
		t.Fatalf("pipeline.SchemaImages %d, engine %d", pipeline.SchemaImages, schemaImages)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var newLog, oldLog syncBuffer
	cur := mustOpenAs(t, addr, clk, n, &newLog)
	img := create(t, cur, imagePipeline, Trigger{Event: "manual", SHA: "0123abcd0123abcd0123abcd0123abcd0123abcd"})
	plain := create(t, cur, linear, Trigger{Event: "manual"})
	for _, id := range []string{img} {
		if sum, _ := cur.Summary(id); sum.MinSchema != n {
			t.Fatalf("run %s has min_schema %d, want %d", id, sum.MinSchema, n)
		}
	}
	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	for _, id := range []string{img} {
		d, err := old.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.NeedsNewerServer {
			t.Fatalf("older build's view of %s: %v", id, d.NotUnderstood)
		}
		if err := old.Cancel(id, "x"); !errors.Is(err, ErrNeedsNewer) {
			t.Fatalf("older build cancelled %s: %v", id, err)
		}
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, imagePipeline), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build made a run with an image step: %v", err)
	}
	for range 3 {
		j := takeAs(t, old, imageRunner)
		if j.RunID != plain || j.ImageBuild != nil {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		ok(t, old, j, nil)
	}
	if j, _ := old.Lease(context.Background(), imageRunner, 0); j != nil {
		t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
	}
	// The current build carries the image run on.
	ok(t, cur, takeAs(t, cur, imageRunner), map[string]string{"version": "1"})
	j := takeAs(t, cur, imageRunner)
	if j.RunID != img || j.ImageBuild == nil {
		t.Fatalf("leased %s/%s", j.RunID, j.StepID)
	}
}

// A deploy step pins the image an image step pushed by its digest.
func TestDeployPinsTheImageByDigest(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	text := strings.Replace(imagePipeline, "  smoke:\n    image: alpine\n    needs: image\n    run: echo $LAUNCHWAY_NEEDS_IMAGE_DIGEST\n", `  deploy:
    kind: deploy
    needs: image
    environment: prod
    argocd:
      application: shop
      write_back: { repo: gitops, branch: main, changes: [{ file: apps/shop.yaml, image: team/shop, digest: { output: image.digest } }] }
`, 1)
	id := createShop(t, e, text, 1)
	if sum, _ := e.Summary(id); sum.MinSchema != schemaImages {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	ok(t, e, takeAs(t, e, imageRunner), map[string]string{"version": "1"})
	j := takeAs(t, e, imageRunner)
	zero := 0
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Image: record()}); err != nil {
		t.Fatal(err)
	}
	d := leaseDeploy(t, e, deployer)
	if d.Deploy == nil || len(d.Deploy.Changes) != 1 || d.Deploy.Changes[0].Digest != testDigest || d.Deploy.Changes[0].Tag != "" {
		t.Fatalf("deploy job: %+v", d.Deploy)
	}
}
