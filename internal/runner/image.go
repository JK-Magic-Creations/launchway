package runner

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// Image steps on the runner. The step runs nothing in the run's pod or
// container: the runner sends the build context from the workspace to a
// builder (images.builders), and pushes and signs what comes back with the
// registries and keys its configuration names. Build secrets are read like
// any step's secrets and go to the builder over the build's session; they
// and the registry credentials are masked in everything the step logs.

// imageMasker masks the step's secrets and every credential read while
// building.
type imageMasker struct {
	mu     sync.Mutex
	values map[string]string
	m      *secrets.Masker
}

func (im *imageMasker) add(name, v string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.values[name] = v
	im.m, _ = secrets.NewMasker(im.values)
}

func (im *imageMasker) Mask(s string) string {
	im.mu.Lock()
	m := im.m
	im.mu.Unlock()
	return m.Mask(s)
}

// execFor is the executor whose view of the files a step's reports and
// artifacts are collected through: for an image step, the runner's own
// output directory for it.
func (r *Runner) execFor(job *engine.Job) executor.Executor {
	if job.ImageBuild != nil || job.AI != nil {
		return &executor.Host{}
	}
	return r.Exec
}

// runImage runs an image step attempt.
func (r *Runner) runImage(ctx context.Context, job *engine.Job, sys func(string, ...any), out executor.Sink) (engine.Result, func(string) collected) {
	b := job.ImageBuild
	sys("Runner %s builds images", r.Info.Name)
	if job.TakenOverFrom != "" {
		sys("This run moves here from runner %s, which stopped responding or left. It continues in a fresh workspace: files that earlier steps wrote are not in it.", job.TakenOverFrom)
	}
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	if r.Images == nil || len(r.Images.Builders) == 0 {
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseInfrastructure, Error: "this runner has no image builders configured (images.builders)"}, nil
	}
	values, failed := r.resolveSecrets(ctx, job, sys)
	if failed != nil {
		return *failed, nil
	}
	mask := &imageMasker{values: map[string]string{}}
	for k, v := range values {
		mask.add(k, v)
	}
	if _, short := secrets.NewMasker(values); len(short) > 0 {
		for _, name := range short {
			sys("Build secret %s has a value, or a line of one, shorter than %d characters; it is not masked in the output", name, secrets.MinMaskLen)
		}
	}
	ws, err := r.workspace(ctx, job, sys)
	if err != nil {
		sys("Could not prepare the workspace: %v", err)
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseInfrastructure, Error: "preparing the workspace: " + err.Error()}, nil
	}
	work := filepath.Join(r.DataDir, "workspaces", "image-"+job.LeaseID)
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o700); err != nil {
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseInfrastructure, Error: err.Error()}, nil
	}
	meta := filepath.Join(r.DataDir, "steps", job.LeaseID)
	defer os.RemoveAll(meta)
	spec := executor.Spec{
		RunID: job.RunID, Containers: containerSpecs(job), Container: job.Container,
		Name: "launchway-" + job.LeaseID, Image: job.Image, Workspace: ws, WorkspaceID: workspaceID(job), MetaDir: meta, Mask: mask.Mask,
	}
	outDir := filepath.Join(work, "build", imagebuild.OutDir)
	collectSpec := spec
	collectSpec.Workspace = outDir
	inner := r.collector(job, collectSpec, sys, nil)
	collect := func(outcome string) collected {
		defer os.RemoveAll(work)
		if inner == nil {
			return collected{}
		}
		_ = os.MkdirAll(outDir, 0o700)
		return inner(outcome)
	}
	if failed := r.fetch(ctx, job, spec, sys, out); failed != nil {
		_ = os.RemoveAll(work)
		return *failed, nil
	}
	tctx, cancel := context.WithTimeout(ctx, job.Timeout.D())
	defer cancel()
	ctxDir, dfPath, err := r.buildContext(tctx, job, spec, ws, work, sys)
	if err != nil {
		msg := mask.Mask(err.Error())
		sys("%s", msg)
		if errors.Is(err, executor.ErrInfrastructure) {
			return engine.Result{Outcome: "lost", Cause: pipeline.CauseInfrastructure, Error: msg}, nil
		}
		return engine.Result{Outcome: "failed", Error: msg}, collect
	}

	buildSecrets := map[string][]byte{}
	for id, v := range values {
		buildSecrets[id] = []byte(v)
	}
	var tags []imagebuild.Tag
	for _, t := range b.Tags {
		tags = append(tags, imagebuild.Tag{Registry: t.Registry, Repository: t.Repository, Tag: t.Tag})
	}
	req := imagebuild.Request{
		Project: job.Project, Event: b.Event, ContextDir: ctxDir, DockerfilePath: dfPath,
		ContextName: b.Context, DockerfileName: b.File, Target: b.Target, Platforms: b.Platforms,
		BuildArgs: b.BuildArgs, Secrets: buildSecrets, Tags: tags, Push: b.Push,
		// A server older than this field sends none; for a pull-request or
		// draft run that means no.
		PullCredentials: b.PullCredentials || (b.Event != pipeline.EventPullRequest && b.Event != pipeline.EventDraft),
		SBOM:            b.SBOM, Provenance: b.Provenance,
		Source:   r.imageSource(job),
		RunnerID: r.runnerURI(),
		Version:  r.Info.Version,
		WorkDir:  filepath.Join(work, "build"),
		Lookup: func(ctx context.Context, ref pipeline.SecretRef) (string, error) {
			v, err := r.lookup(ctx, job.Project, ref, sys)
			if err == nil {
				mask.add("credential "+ref.String(), v)
			}
			return v, err
		},
		Log: func(line string) {
			out("stdout", mask.Mask(line))
		},
	}
	if b.Cache != nil {
		req.Cache = &imagebuild.Repo{Registry: b.Cache.Registry, Repository: b.Cache.Repository}
		// Whose cache this run writes is the server's decision: a job
		// without it (an older server) writes none.
		req.CacheScope, req.CacheWrite, req.CacheNoProtected = b.CacheScope, b.CacheWrite, b.CacheNoProtected
	}
	if b.Sign != nil {
		req.Sign = &imagebuild.Sign{Runner: b.Sign.Runner, Key: b.Sign.Key, Password: b.Sign.Password}
	}
	if b.Scan != nil {
		req.Scan = &imagebuild.Scan{FailOn: b.Scan.FailOn, Enforce: b.Scan.Mode == "enforce"}
	}
	start := time.Now()
	res, err := imagebuild.Build(tctx, r.Images, req)
	took := time.Since(start).Round(100 * time.Millisecond)
	record := imageRecord(res)
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		sys("Timed out after %s", job.Timeout.D())
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseTimeout, Error: fmt.Sprintf("timed out after %s", job.Timeout.D()), Image: record}, collect
	case err != nil && ctx.Err() != nil:
		return engine.Result{Outcome: "canceled", Image: record}, collect
	case err != nil:
		msg := mask.Mask(err.Error())
		sys("Failed after %s: %s", took, msg)
		return engine.Result{Outcome: "failed", Error: msg, Image: record}, collect
	}
	sys("Succeeded in %s", took)
	return engine.Result{Outcome: "succeeded", Image: record}, collect
}

// buildContext finds the build context and the Dockerfile on the runner:
// in the workspace itself, or copied out of the run's pod on Kubernetes.
// Neither may lead outside the workspace, through a symbolic link a step
// left there or otherwise.
func (r *Runner) buildContext(ctx context.Context, job *engine.Job, spec executor.Spec, ws, work string, sys func(string, ...any)) (string, string, error) {
	b := job.ImageBuild
	lim := executor.ExportLimits{MaxBytes: r.Images.MaxContextBytes(), MaxFiles: 1_000_000}
	if ex, ok := r.Exec.(executor.DirExporter); ok {
		ctxDir := filepath.Join(work, "context")
		took := time.Now()
		got, err := ex.ExportDir(ctx, spec, b.Context, ctxDir, lim)
		if err != nil {
			if errors.Is(err, executor.ErrTooLarge) {
				return "", "", fmt.Errorf("the build context %s is too large (%v); see images.max_context_mib, or add a .dockerignore", b.Context, err)
			}
			return "", "", fmt.Errorf("copying the build context %s out of the run's pod: %w", b.Context, err)
		}
		if got {
			sys("Copied the build context %s out of the run's pod in %s", b.Context, time.Since(took).Round(100*time.Millisecond))
			if rel, ok := within(b.Context, b.File); ok {
				return ctxDir, filepath.Join(ctxDir, filepath.FromSlash(rel)), nil
			}
			dfDir := filepath.Join(work, "dockerfile")
			_ = os.MkdirAll(dfDir, 0o700)
			df := filepath.Join(dfDir, filepath.Base(b.File))
			if _, err := ex.ExportFile(ctx, spec, b.File, df, 1<<20); err != nil {
				return "", "", fmt.Errorf("the Dockerfile %s: %w", b.File, err)
			}
			return ctxDir, df, nil
		}
		// No pod yet: this is the run's first step, and the checkout is the
		// workspace.
	}
	root, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return "", "", err
	}
	inside := func(rel, what string) (string, error) {
		p, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("the %s %s is not in the workspace: %v", what, rel, errors.Unwrap(err))
		}
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return "", fmt.Errorf("the %s %s leads outside the workspace (through a symbolic link); refused", what, rel)
		}
		return p, nil
	}
	ctxDir, err := inside(b.Context, "build context")
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("the build context %s is not a directory in the workspace", b.Context)
	}
	df, err := inside(b.File, "Dockerfile")
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(df); err != nil || !fi.Mode().IsRegular() {
		return "", "", fmt.Errorf("the Dockerfile %s is not a file in the workspace", b.File)
	}
	return ctxDir, df, nil
}

// within reports whether file is inside dir (both relative to the
// workspace), and its path relative to dir.
func within(dir, file string) (string, bool) {
	if dir == "." {
		return file, true
	}
	rel, ok := strings.CutPrefix(file, dir+"/")
	return rel, ok
}

// imageSource is what provenance says about where the code came from.
func (r *Runner) imageSource(job *engine.Job) imagebuild.Source {
	b := job.ImageBuild
	s := imagebuild.Source{Repository: b.Repository, Ref: b.Ref, Commit: job.SHA, Event: b.Event, Project: job.Project,
		Pipeline: job.Pipeline, Run: job.RunID, RunNumber: job.RunNumber, Step: job.StepID, Attempt: job.Attempt}
	if r.Client != nil && r.Client.Server != "" {
		s.RunURL = strings.TrimSuffix(r.Client.Server, "/") + "/runs/" + url.PathEscape(job.RunID) + "#step=" + url.QueryEscape(job.StepID) +
			fmt.Sprintf("&attempt=%d", job.Attempt)
	}
	for _, l := range b.Libraries {
		s.Libraries = append(s.Libraries, imagebuild.Library{Name: l.Name, Repository: l.Repo, Version: l.Version, Commit: l.Commit})
	}
	return s
}

// runnerURI names this runner in provenance.
func (r *Runner) runnerURI() string {
	server := "launchway:"
	if r.Client != nil && r.Client.Server != "" {
		server = strings.TrimSuffix(r.Client.Server, "/")
	}
	return server + "/runners/" + url.PathEscape(r.Info.Name)
}

// imageRecord is what the server keeps of a build.
func imageRecord(res *imagebuild.Result) *engine.ImageRecord {
	if res == nil || res.Digest == "" {
		return nil
	}
	rec := &engine.ImageRecord{Digest: res.Digest, MediaType: res.MediaType, Size: res.Size, Image: res.Image, Tags: res.Tags,
		Pushed: res.Pushed, NotPushed: res.NotPushed, Signed: res.Signed, KeyID: res.KeyID, KeySource: res.KeySource,
		Signatures: res.Signatures, Provenance: res.Provenance, SBOM: res.SBOM, Builders: res.Builders, Steps: res.Steps,
		Cached: res.Cached, Cache: res.Cache, BaseImages: res.BaseImages}
	for _, p := range res.Platforms {
		rec.Platforms = append(rec.Platforms, engine.ImagePlatform{Platform: p.Platform, Digest: p.Digest, Size: p.Size})
	}
	if s := res.Scan; s != nil {
		rec.Scan = &engine.ImageScanSum{Critical: s.Critical, High: s.High, Medium: s.Medium, Low: s.Low, Unknown: s.Unknown,
			FailOn: s.FailOn, Mode: s.Mode, Failed: s.Failed}
	}
	return rec
}
