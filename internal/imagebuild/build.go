// Package imagebuild builds, scans, signs and pushes container images for
// image steps (kind: image). It runs in the runner, never in a step's pod
// or container.
//
// The build itself happens in a BuildKit daemon the runner's operator runs
// (Config.Builders): rootless BuildKit cannot run inside a pod held to Pod
// Security "restricted" (it needs user namespaces), so it runs apart, in a
// namespace of its own with the few relaxations it needs (see
// deploy/buildkit/README.md), and the step pods stay restricted. The
// runner is BuildKit's client: it sends the build context from the run's
// workspace, answers the builder's questions for build secrets and
// registry credentials over the build's session (so neither is ever in a
// pod, a layer, a log line, an event or the database), receives the image
// as an OCI layout, has it scanned, and pushes and signs it itself with the
// credentials its configuration names. Pipelines name registries and say
// what to tag; they never hold an address or a credential.
package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Request is one image step attempt, with its values worked out.
type Request struct {
	Project string
	// Event started the run: push, manual, schedule, webhook,
	// pull_request or draft. Pull-request and draft runs never push and
	// never read push credentials.
	Event string

	ContextDir     string // the build context, on the runner
	DockerfilePath string // the Dockerfile, on the runner
	// As written in the pipeline, for the record.
	ContextName, DockerfileName string

	Target    string
	Platforms []string
	BuildArgs map[string]string
	// Secrets are the build secrets by ID, already read; a Dockerfile uses
	// them with RUN --mount=type=secret,id=<id>.
	Secrets map[string][]byte

	Tags  []Tag
	Push  bool
	Cache *Repo
	// CacheScope says whose build cache this run writes: empty for the
	// protected cache (a default-branch run), else a tag-safe name for the
	// branch, tag or pull request (the server decides it). Every run reads
	// the protected cache, unless CacheNoProtected (a fork's pull request),
	// and its own scope's. A run writes only its own scope, and only if
	// CacheWrite.
	CacheScope       string
	CacheWrite       bool
	CacheNoProtected bool
	// PullCredentials says the run may have registries' pull credentials:
	// always for runs started by a push, by hand, a schedule or a webhook;
	// for pull-request and draft runs only when they may read secrets. The
	// server decides it.
	PullCredentials bool

	SBOM       bool
	Provenance bool
	Sign       *Sign
	Scan       *Scan

	Source   Source
	RunnerID string // the runner, as a URI, for provenance
	Version  string // Launchway's version

	// WorkDir is the runner's scratch directory for this attempt; the
	// files the step keeps (SBOMs, SARIF, provenance) are written in
	// WorkDir/out.
	WorkDir string
	// Lookup reads a secret: registry credentials and a pipeline's signing
	// key. It is the runner's resolver, for the run's project.
	Lookup func(ctx context.Context, ref pipeline.SecretRef) (string, error)
	// Log receives progress, one line at a time. The caller masks it.
	Log func(string)
}

// Tag is one name to push the image under.
type Tag struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// Repo is a repository of a registry.
type Repo struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
}

// Sign says how to sign: with the runner's own key (Runner), or a key the
// pipeline names in a secret store, with its password.
type Sign struct {
	Runner   bool
	Key      *pipeline.SecretRef
	Password *pipeline.SecretRef
}

// Scan is the vulnerability scan's policy: report only, or fail the step
// (before anything is pushed) when a finding is at FailOn or worse.
type Scan struct {
	FailOn  string // critical, high, medium or low
	Enforce bool
}

// Result is what an image step made.
type Result struct {
	Digest    string          `json:"digest"` // of what was (or would be) pushed: the index, or the one image
	MediaType string          `json:"media_type"`
	Size      int64           `json:"size"` // every platform's image, compressed
	Platforms []PlatformImage `json:"platforms"`
	// Image is the pushed image by digest (host/repository@sha256:...), the
	// first tag's repository; empty when nothing was pushed.
	Image  string   `json:"image,omitempty"`
	Tags   []string `json:"tags"` // host/repository:tag, pushed or not
	Pushed bool     `json:"pushed"`
	// NotPushed says why nothing was pushed, when it was not.
	NotPushed string `json:"not_pushed,omitempty"`
	Signed    bool   `json:"signed"`
	// KeyID identifies the signing key (the start of its public key's
	// SHA-256); KeySource is "runner" or the secret it came from.
	KeyID      string   `json:"key_id,omitempty"`
	KeySource  string   `json:"key_source,omitempty"`
	Signatures []string `json:"signatures,omitempty"` // the .sig and .att tags written
	// Provenance is "" (none asked for), "stored" (kept as an artifact;
	// the builder's own is in the image's index), or "attached" (also signed
	// and attached as an attestation).
	Provenance string       `json:"provenance,omitempty"`
	SBOM       bool         `json:"sbom"`
	Scan       *ScanSummary `json:"scan,omitempty"`
	Builders   []string     `json:"builders"`
	Steps      int          `json:"steps"`  // build steps run
	Cached     int          `json:"cached"` // of those, taken from the cache
	Cache      string       `json:"cache,omitempty"`
	BaseImages []string     `json:"base_images,omitempty"`
}

// Files the step keeps, under WorkDir/out.
const (
	OutDir         = "out"
	SBOMDir        = "sbom"     // sbom/<platform>.cdx.json
	ScanDir        = "security" // security/<platform>.sarif
	ProvenanceFile = "provenance/launchway.intoto.json"
	BuilderProvDir = "provenance/builder" // builder/<platform>.intoto.json
)

// PolicyError means the vulnerability scan's policy failed the step.
type PolicyError struct{ Summary ScanSummary }

func (e *PolicyError) Error() string {
	return fmt.Sprintf("the scan found %d vulnerabilities at %s or worse (scan.fail_on: %s, mode: enforce); nothing was pushed",
		e.Summary.atLeast(e.Summary.FailOn), e.Summary.FailOn, e.Summary.FailOn)
}

// Build makes the image. On an error the Result holds what was made so
// far (the scan, the files written), for the runner to keep.
func Build(ctx context.Context, cfg *Config, req Request) (*Result, error) {
	res := &Result{Tags: []string{}, Platforms: []PlatformImage{}, Builders: []string{}}
	if cfg == nil || len(cfg.Builders) == 0 {
		return res, errors.New("this runner has no image builders configured (images.builders); ask its operator, or run the step on a runner that has")
	}
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	started := time.Now()
	out := filepath.Join(req.WorkDir, OutDir)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return res, err
	}
	pullRequestish := req.Event == pipeline.EventPullRequest || req.Event == pipeline.EventDraft
	if req.Push && pullRequestish {
		return res, fmt.Errorf("a %s run never pushes", strings.ReplaceAll(req.Event, "_", "-"))
	}

	// What may be pushed where, before anything is built.
	rs := &registrySet{cfg: cfg, req: req, regs: map[string]*remoteRegistry{}}
	open := func(regName string, push bool) (*remoteRegistry, error) { return rs.open(ctx, regName, push) }
	for _, t := range req.Tags {
		rc, err := cfg.Registry(t.Registry)
		if err != nil {
			return res, err
		}
		res.Tags = append(res.Tags, rc.Host+"/"+t.Repository+":"+t.Tag)
		if req.Push && !rc.MayPush(req.Project, t.Repository) {
			return res, fmt.Errorf("project %s may not push to %s/%s: the runner's configuration (images.registries.%s.push) does not allow it",
				req.Project, rc.Host, t.Repository, t.Registry)
		}
		if req.Push && rc.Credentials == nil {
			return res, fmt.Errorf("registry %s has no credentials to push with (images.registries.%s.credentials)", t.Registry, t.Registry)
		}
	}
	cacheWrite := false
	if req.Cache != nil {
		rc, err := cfg.Registry(req.Cache.Registry)
		if err != nil {
			return res, err
		}
		// The protected cache is written only by a run that pushes (a
		// default-branch run); another scope's by a run the server lets
		// write it. Nothing a pull request or a branch builds ever lands in
		// the protected cache.
		wants := req.CacheWrite && (req.CacheScope != "" || req.Push)
		cacheWrite = wants && rc.Credentials != nil && rc.MayPush(req.Project, req.Cache.Repository)
		if wants && !cacheWrite {
			log(fmt.Sprintf("The cache %s/%s is only read: project %s may not push there", rc.Host, req.Cache.Repository, req.Project))
		}
	}
	hosts, err := rs.builderHosts(ctx, cacheWrite)
	if err != nil {
		return res, err
	}
	for _, n := range rs.notes {
		log(n)
	}
	if req.Push {
		checked := map[Repo]bool{}
		for _, t := range req.Tags {
			k := Repo{t.Registry, t.Repository}
			if checked[k] {
				continue
			}
			checked[k] = true
			r, err := open(t.Registry, true)
			if err != nil {
				return res, err
			}
			if err := r.checkPush(t.Repository); err != nil {
				return res, err
			}
		}
	}
	var signWith *signer
	if req.Sign != nil && req.Push {
		var err error
		if signWith, res.KeySource, err = loadSigning(ctx, cfg, req); err != nil {
			return res, err
		}
	}

	// Build, on one builder or one per group of platforms.
	var builders []*builder
	var unreachable []string
	defer func() {
		for _, b := range builders {
			b.c.Close()
		}
	}()
	for _, bc := range cfg.Builders {
		if !bc.serves(req.Project) {
			continue
		}
		b, err := connect(ctx, bc)
		if err != nil {
			log(err.Error())
			unreachable = append(unreachable, err.Error())
			continue
		}
		builders = append(builders, b)
	}
	if len(builders) == 0 {
		if len(unreachable) == 0 {
			return res, fmt.Errorf("no builder on this runner serves project %s (images.builders[].projects)", req.Project)
		}
		return res, fmt.Errorf("no image builder could be reached for project %s: %s", req.Project, strings.Join(unreachable, "; "))
	}
	groups, err := pick(builders, req.Project, req.Platforms)
	if err != nil {
		return res, err
	}
	type part struct {
		b         *builder
		platforms []string
		built     *built
		stats     *solveStats
		err       error
	}
	var parts []*part
	for _, b := range builders {
		if ps, ok := groups[b]; ok {
			parts = append(parts, &part{b: b, platforms: ps})
			res.Builders = append(res.Builders, b.cfg.Name)
		}
	}
	cacheRef := func(p *part) (from []string, to string) {
		if req.Cache == nil {
			return nil, ""
		}
		rc, _ := cfg.Registry(req.Cache.Registry)
		suffix := ""
		if len(parts) > 1 {
			suffix = "-" + strings.ReplaceAll(strings.Join(p.platforms, "_"), "/", "-")
		}
		repo := rc.Host + "/" + req.Cache.Repository + ":"
		res.Cache = rc.Host + "/" + req.Cache.Repository
		return cacheRefs(repo, req.CacheScope, req.CacheNoProtected, suffix, cacheWrite)
	}
	dfDir, dfName := cleanDockerfile(req.DockerfilePath)
	var wg sync.WaitGroup
	var logMu sync.Mutex
	for i, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prefix := ""
			if len(parts) > 1 {
				prefix = "[" + p.b.cfg.Name + "] "
			}
			from, to := cacheRef(p)
			if i == 0 && req.Cache != nil {
				logMu.Lock()
				log(cacheNote(from, to, req.CacheScope))
				logMu.Unlock()
			}
			if len(parts) == 1 || i == 0 {
				what := "for " + nonEmptyStr(strings.Join(p.platforms, ", "), "its own platform")
				logMu.Lock()
				log(fmt.Sprintf("%sBuilding on %s (%s) %s", prefix, p.b.cfg.Name, p.b.cfg.Address, what))
				logMu.Unlock()
			}
			tarPath := filepath.Join(req.WorkDir, fmt.Sprintf("image-%d.tar", i))
			p.stats, p.err = p.b.solve(ctx, solveRequest{
				contextDir: req.ContextDir, dockerfileDir: dfDir, dockerfile: dfName, target: req.Target,
				platforms: p.platforms, buildArgs: req.BuildArgs, secrets: req.Secrets, hosts: hosts,
				cacheFrom: from, cacheTo: to, provenance: req.Provenance, builderID: req.RunnerID,
				vcsSource: req.Source.Repository, vcsRevision: req.Source.Commit,
				out: tarPath, maxBytes: cfg.maxImageBytes(),
				log: func(s string) { logMu.Lock(); log(prefix + s); logMu.Unlock() },
			})
			if p.err != nil {
				return
			}
			dir := filepath.Join(req.WorkDir, fmt.Sprintf("layout-%d", i))
			if p.err = unpackLayout(tarPath, dir); p.err == nil {
				p.built, p.err = openLayout(dir)
			}
			_ = os.Remove(tarPath)
		}()
	}
	wg.Wait()
	for _, p := range parts {
		if p.stats != nil {
			res.Steps += p.stats.steps
			res.Cached += p.stats.cached
		}
	}
	for _, p := range parts {
		if p.err != nil {
			if len(parts) > 1 {
				return res, fmt.Errorf("building on %s: %w", p.b.cfg.Name, p.err)
			}
			return res, p.err
		}
	}

	// What was built.
	var root remote.Taggable
	var rootDigest v1.Hash
	if len(parts) == 1 {
		b := parts[0].built
		rootDigest = b.root.Digest
		res.MediaType = string(b.root.MediaType)
		if b.index != nil {
			root = b.index
		} else {
			root = b.image
		}
	} else {
		var all []*built
		for _, p := range parts {
			all = append(all, p.built)
		}
		idx, err := merge(all)
		if err != nil {
			return res, fmt.Errorf("making one index of the platforms' images: %w", err)
		}
		root = idx
		if rootDigest, err = idx.Digest(); err != nil {
			return res, err
		}
		mt, _ := idx.MediaType()
		res.MediaType = string(mt)
	}
	res.Digest = rootDigest.String()
	builderProv := map[string]json.RawMessage{}
	for _, p := range parts {
		pis, err := p.built.platformImages()
		if err != nil {
			return res, err
		}
		res.Platforms = append(res.Platforms, pis...)
		bp, err := p.built.builderProvenance()
		if err != nil {
			return res, fmt.Errorf("reading the builder's provenance: %w", err)
		}
		maps.Copy(builderProv, bp)
	}
	slices.SortFunc(res.Platforms, func(a, b PlatformImage) int { return strings.Compare(a.Platform, b.Platform) })
	for _, p := range res.Platforms {
		res.Size += p.Size
	}
	log(fmt.Sprintf("Built %s: %s, %s", res.Digest, platformList(res.Platforms), sizeText(res.Size)))
	bases := baseImages(builderProv)
	for _, d := range bases {
		res.BaseImages = append(res.BaseImages, d.URI)
	}
	if req.Provenance {
		for pl, raw := range builderProv {
			f := filepath.Join(out, BuilderProvDir, fileSafe(nonEmptyStr(pl, "image"))+".intoto.json")
			if err := writeFile(f, raw); err != nil {
				return res, err
			}
		}
	}

	// SBOM and scan, per platform, before anything is pushed.
	if req.SBOM || req.Scan != nil {
		var sarifs []string
		for _, p := range parts {
			pis, _ := p.built.platformImages()
			for _, pi := range pis {
				tmp := filepath.Join(req.WorkDir, "scan-"+fileSafe(nonEmptyStr(pi.Platform, "image")))
				platform := ""
				if len(pis) > 1 {
					platform = pi.Platform
				}
				err := p.b.scan(ctx, cfg.scannerImage(), cfg.Scanner, scanRequest{
					layoutDir: p.built.dir, platform: platform, sbom: true, vulns: req.Scan != nil, outDir: tmp, log: log,
				})
				if err != nil {
					return res, err
				}
				name := fileSafe(nonEmptyStr(pi.Platform, "image"))
				if req.SBOM {
					if err := moveFile(filepath.Join(tmp, "sbom.cdx.json"), filepath.Join(out, SBOMDir, name+".cdx.json")); err != nil {
						return res, fmt.Errorf("the scanner wrote no SBOM: %w", err)
					}
					res.SBOM = true
				}
				if req.Scan != nil {
					f := filepath.Join(out, ScanDir, name+".sarif")
					if err := moveFile(filepath.Join(tmp, "scan.sarif"), f); err != nil {
						return res, fmt.Errorf("the scanner wrote no results: %w", err)
					}
					sarifs = append(sarifs, f)
				}
			}
		}
		if req.Scan != nil {
			sum, err := summarise(ctx, sarifs)
			if err != nil {
				return res, fmt.Errorf("reading the scan's results: %w", err)
			}
			sum.FailOn, sum.Mode = req.Scan.FailOn, "report"
			if req.Scan.Enforce {
				sum.Mode = "enforce"
			}
			res.Scan = &sum
			log(fmt.Sprintf("Vulnerabilities: %d critical, %d high, %d medium, %d low, %d unknown",
				sum.Critical, sum.High, sum.Medium, sum.Low, sum.Unknown))
			if req.Scan.Enforce && req.Scan.FailOn != "" && sum.atLeast(req.Scan.FailOn) > 0 {
				res.Scan.Failed = true
				res.NotPushed = "the vulnerability scan failed the step"
				return res, &PolicyError{Summary: sum}
			}
		}
	}

	// Push, sign and attest.
	if !req.Push {
		switch {
		case pullRequestish:
			res.NotPushed = "a " + strings.ReplaceAll(req.Event, "_", "-") + " run never pushes"
		case len(req.Tags) == 0:
			res.NotPushed = "the step names no tags"
		default:
			res.NotPushed = "push: false"
		}
		log("Not pushed: " + res.NotPushed)
	}
	type pushedRepo struct {
		reg  *remoteRegistry
		repo string
	}
	var pushedTo []pushedRepo
	if req.Push && len(req.Tags) > 0 {
		order := []Repo{}
		byRepo := map[Repo][]string{}
		for _, t := range req.Tags {
			k := Repo{t.Registry, t.Repository}
			if _, ok := byRepo[k]; !ok {
				order = append(order, k)
			}
			byRepo[k] = append(byRepo[k], t.Tag)
		}
		for _, k := range order {
			r, err := open(k.Registry, true)
			if err != nil {
				return res, err
			}
			refs, err := r.push(ctx, root, k.Repository, byRepo[k])
			if err != nil {
				return res, err
			}
			for _, ref := range refs {
				log("Pushed " + ref.Ref)
			}
			if res.Image == "" {
				res.Image = r.cfg.Host + "/" + k.Repository + "@" + res.Digest
			}
			pushedTo = append(pushedTo, pushedRepo{r, k.Repository})
		}
		res.Pushed = true
	}

	var stmt []byte
	if req.Provenance {
		var subjects []subject
		for _, pt := range pushedTo {
			subjects = append(subjects, subject{Name: pt.reg.cfg.Host + "/" + pt.repo, Digest: map[string]string{"sha256": rootDigest.Hex}})
		}
		if len(subjects) == 0 {
			subjects = append(subjects, subject{Name: firstName(req, cfg), Digest: map[string]string{"sha256": rootDigest.Hex}})
		}
		var secretIDs []string
		for id := range req.Secrets {
			secretIDs = append(secretIDs, id)
		}
		slices.Sort(secretIDs)
		pin := provenanceInput{
			BuilderID: req.RunnerID, InvocationID: req.Source.RunURL, Started: started, Finished: time.Now(),
			Source: req.Source, Builders: res.Builders, BaseImages: bases, Subjects: subjects, Version: req.Version,
			Image: buildParams{Context: req.ContextName, Dockerfile: req.DockerfileName, Target: req.Target,
				Platforms: req.Platforms, BuildArgs: req.BuildArgs, Secrets: secretIDs, Tags: res.Tags},
		}
		if pin.InvocationID == "" {
			pin.InvocationID = fmt.Sprintf("launchway:run/%s/step/%s/attempt/%d", req.Source.Run, req.Source.Step, req.Source.Attempt)
		}
		if stmt, err = pin.statement(); err != nil {
			return res, err
		}
		if err := writeFile(filepath.Join(out, ProvenanceFile), stmt); err != nil {
			return res, err
		}
		res.Provenance = "stored"
	}
	if req.Sign != nil && !res.Pushed {
		log("Not signed: nothing was pushed")
	}
	if signWith != nil && res.Pushed {
		res.KeyID = signWith.KeyID
		for _, pt := range pushedTo {
			digests := []v1.Hash{rootDigest}
			for _, pi := range res.Platforms {
				if h, err := v1.NewHash(pi.Digest); err == nil && h != rootDigest {
					digests = append(digests, h)
				}
			}
			for _, d := range digests {
				tag, err := signWith.sign(ctx, pt.reg, pt.repo, d)
				if err != nil {
					return res, err
				}
				res.Signatures = append(res.Signatures, tag)
			}
			if stmt != nil {
				tag, err := signWith.attest(ctx, pt.reg, pt.repo, rootDigest, SLSAProvenanceV1, stmt)
				if err != nil {
					return res, err
				}
				res.Signatures = append(res.Signatures, tag)
				res.Provenance = "attached"
			}
			if req.SBOM {
				for _, pi := range res.Platforms {
					f := filepath.Join(out, SBOMDir, fileSafe(nonEmptyStr(pi.Platform, "image"))+".cdx.json")
					st, err := sbomStatement(f, pt.reg.cfg.Host+"/"+pt.repo, pi.Digest)
					if err != nil {
						return res, err
					}
					d, _ := v1.NewHash(pi.Digest)
					tag, err := signWith.attest(ctx, pt.reg, pt.repo, d, CycloneDXPredicate, st)
					if err != nil {
						return res, err
					}
					res.Signatures = append(res.Signatures, tag)
				}
			}
		}
		res.Signed = true
		log(fmt.Sprintf("Signed %s with key %s (%s)", res.Digest, res.KeyID, res.KeySource))
	}
	return res, nil
}

// registrySet opens the registries a build uses, each once, with the
// credentials the build may use.
type registrySet struct {
	cfg   *Config
	req   Request
	regs  map[string]*remoteRegistry
	notes []string // what the log says once, such as why pull credentials were withheld
}

// mayPull reports whether this build may have a registry's pull
// credentials, and if not, why.
func (rs *registrySet) mayPull(name string, rc RegistryConfig) (bool, string) {
	req := rs.req
	switch {
	case rc.PullCredentials == nil:
		return false, ""
	case !rc.MayPull(req.Project):
		return false, fmt.Sprintf("Registry %s: its pull credentials are not for project %s (images.registries.%s.pull_projects), "+
			"so the build pulls from it anonymously: private base images there cannot be pulled", name, req.Project, name)
	case !req.PullCredentials:
		return false, fmt.Sprintf("Registry %s: this %s run may not read secrets, so the build pulls from it anonymously: "+
			"private base images there cannot be pulled (the project's setting for secrets in pull requests decides)",
			name, strings.ReplaceAll(req.Event, "_", "-"))
	}
	return true, ""
}

func (rs *registrySet) open(ctx context.Context, regName string, push bool) (*remoteRegistry, error) {
	if r, ok := rs.regs[regName]; ok {
		return r, nil
	}
	rc, err := rs.cfg.Registry(regName)
	if err != nil {
		return nil, err
	}
	var user, pass string
	var cr *Credentials
	switch {
	case push && rc.Credentials != nil:
		cr = rc.Credentials
	default:
		if ok, _ := rs.mayPull(regName, rc); ok {
			cr = rc.PullCredentials
		}
	}
	if cr != nil {
		if user, pass, err = readCredentials(ctx, rs.req.Lookup, cr); err != nil {
			return nil, fmt.Errorf("registry %s: %w", regName, err)
		}
	}
	r, err := newRemote(regName, rc, user, pass)
	if err != nil {
		return nil, err
	}
	rs.regs[regName] = r
	return r, nil
}

// builderHosts is what the builder may be told, by registry host: the push
// credentials of the registries this build pushes to (its tags, and the
// cache when it writes it), and the pull credentials of the others when the
// project and the run may have them. Nothing else, and no secret store is
// read for credentials the build may not have.
func (rs *registrySet) builderHosts(ctx context.Context, cacheWrite bool) (map[string]hostAuth, error) {
	req := rs.req
	usedForPush := map[string]bool{}
	if req.Push {
		for _, t := range req.Tags {
			usedForPush[t.Registry] = true
		}
	}
	if cacheWrite && req.Cache != nil {
		usedForPush[req.Cache.Registry] = true
	}
	hosts := map[string]hostAuth{}
	for _, regName := range slices.Sorted(maps.Keys(rs.cfg.Registries)) {
		rc := rs.cfg.Registries[regName]
		if !usedForPush[regName] {
			ok, why := rs.mayPull(regName, rc)
			if why != "" {
				rs.notes = append(rs.notes, why)
			}
			if !ok {
				continue
			}
		}
		r, err := rs.open(ctx, regName, usedForPush[regName])
		if err != nil {
			return nil, err
		}
		hosts[trimHost(rc.Host)] = r.hostAuth()
	}
	return hosts, nil
}

// loadSigning reads the key to sign with: the runner's own, or the one the
// pipeline names.
func loadSigning(ctx context.Context, cfg *Config, req Request) (*signer, string, error) {
	switch {
	case req.Sign.Runner:
		if cfg.Signing == nil || cfg.Signing.KeyFile == "" {
			return nil, "", errors.New("sign: true signs with the runner's own key, and this runner has none (images.signing.key_file)")
		}
		key, err := os.ReadFile(cfg.Signing.KeyFile)
		if err != nil {
			return nil, "", fmt.Errorf("the runner's signing key: %w", err)
		}
		var pass []byte
		if cfg.Signing.PasswordFile != "" {
			// A key without a password has no password file (a Secret
			// without that key mounts no file).
			pass, err = os.ReadFile(cfg.Signing.PasswordFile)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("the runner's signing key's password: %w", err)
			}
			pass = []byte(strings.TrimRight(string(pass), "\r\n"))
		}
		s, err := loadSigner(key, pass)
		return s, "runner", err
	case req.Sign.Key != nil:
		key, err := req.Lookup(ctx, *req.Sign.Key)
		if err != nil {
			return nil, "", fmt.Errorf("the signing key (%s): %w", req.Sign.Key, err)
		}
		var pass string
		if req.Sign.Password != nil {
			if pass, err = req.Lookup(ctx, *req.Sign.Password); err != nil {
				return nil, "", fmt.Errorf("the signing key's password (%s): %w", req.Sign.Password, err)
			}
		}
		s, err := loadSigner([]byte(key), []byte(pass))
		return s, req.Sign.Key.String(), err
	}
	return nil, "", errors.New("say how to sign")
}

func readCredentials(ctx context.Context, lookup func(context.Context, pipeline.SecretRef) (string, error), c *Credentials) (string, string, error) {
	if lookup == nil {
		return "", "", errors.New("no secret stores to read credentials from")
	}
	user := c.Username
	if c.UsernameFrom != nil {
		u, err := lookup(ctx, *c.UsernameFrom)
		if err != nil {
			return "", "", fmt.Errorf("the user name (%s): %w", c.UsernameFrom, err)
		}
		user = u
	}
	pass, err := lookup(ctx, c.Password)
	if err != nil {
		return "", "", fmt.Errorf("the password (%s): %w", c.Password, err)
	}
	return user, pass, nil
}

// sbomStatement wraps a CycloneDX SBOM in an in-toto statement about an
// image, for an attestation.
func sbomStatement(file, name, digest string) ([]byte, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	h, err := v1.NewHash(digest)
	if err != nil {
		return nil, err
	}
	return json.Marshal(statement{Type: "https://in-toto.io/Statement/v1", PredicateType: CycloneDXPredicate,
		Subject: []subject{{Name: name, Digest: map[string]string{h.Algorithm: h.Hex}}}, Predicate: data})
}

func firstName(req Request, cfg *Config) string {
	if len(req.Tags) > 0 {
		if rc, err := cfg.Registry(req.Tags[0].Registry); err == nil {
			return rc.Host + "/" + req.Tags[0].Repository
		}
	}
	return "image"
}

func platformList(ps []PlatformImage) string {
	var names []string
	for _, p := range ps {
		names = append(names, nonEmptyStr(p.Platform, "unknown platform"))
	}
	return strings.Join(names, ", ")
}

func sizeText(n int64) string {
	switch {
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
}

func fileSafe(s string) string { return strings.NewReplacer("/", "-", ":", "-", " ", "-").Replace(s) }

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	return os.Rename(from, to)
}
