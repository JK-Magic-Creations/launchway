package imagebuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

// A builder is a BuildKit daemon the runner is connected to.
type builder struct {
	cfg       BuilderConfig
	c         *client.Client
	platforms []string // what it builds, normalised (linux/amd64)
	native    string   // the daemon's own platform
}

// connectTimeout bounds how long the runner waits to reach a builder.
const connectTimeout = 20 * time.Second

// connect dials a builder and asks what it can build. Errors say in plain
// words what is wrong: the address cannot be reached, or TLS failed.
func connect(ctx context.Context, b BuilderConfig) (*builder, error) {
	var opts []client.ClientOpt
	if strings.HasPrefix(b.Address, "tcp://") && !b.Insecure {
		host := b.ServerName
		if host == "" {
			if u, err := url.Parse(b.Address); err == nil {
				host = u.Hostname()
			}
		}
		opts = append(opts, client.WithServerConfig(host, b.CAFile))
		if b.CertFile != "" {
			opts = append(opts, client.WithCredentials(b.CertFile, b.KeyFile))
		}
	}
	for _, f := range []string{b.CAFile, b.CertFile, b.KeyFile} {
		if f == "" || b.Insecure {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			return nil, fmt.Errorf("builder %s: cannot read %s: %v", b.Name, f, errors.Unwrap(err))
		}
	}
	c, err := client.New(ctx, b.Address, opts...)
	if err != nil {
		return nil, fmt.Errorf("builder %s (%s): %v", b.Name, b.Address, err)
	}
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	workers, err := c.ListWorkers(cctx)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("builder %s (%s) cannot be reached: %s", b.Name, b.Address, plainGRPC(err))
	}
	have := map[string]bool{}
	native := ""
	for _, w := range workers {
		if native == "" && len(w.Platforms) > 0 {
			native = platforms.Format(platforms.Normalize(w.Platforms[0]))
		}
		for _, p := range w.Platforms {
			have[platforms.Format(platforms.Normalize(p))] = true
		}
	}
	out := &builder{cfg: b, c: c, native: native}
	if len(b.Platforms) > 0 {
		for _, p := range b.Platforms {
			if have[normPlatform(p)] {
				out.platforms = append(out.platforms, normPlatform(p))
			}
		}
	} else {
		for p := range have {
			out.platforms = append(out.platforms, p)
		}
	}
	slices.Sort(out.platforms)
	return out, nil
}

// plainGRPC turns a gRPC error into its message, without the wrapping
// that says nothing to a person.
func plainGRPC(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "desc = "); i >= 0 {
		msg = strings.Trim(msg[i+len("desc = "):], `"`)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || strings.Contains(msg, "context deadline exceeded") {
		return fmt.Sprintf("no answer within %s (%s)", connectTimeout, msg)
	}
	return msg
}

func normPlatform(p string) string {
	sp, err := platforms.Parse(p)
	if err != nil {
		return p
	}
	return platforms.Format(platforms.Normalize(sp))
}

// solveRequest is one build on one builder: the Dockerfile for some
// platforms, exported as an OCI layout to the runner.
type solveRequest struct {
	contextDir    string
	dockerfileDir string
	dockerfile    string // file name in dockerfileDir
	target        string
	platforms     []string
	buildArgs     map[string]string
	secrets       map[string][]byte
	hosts         map[string]hostAuth
	cacheFrom     []string // registry cache refs to read, in order
	cacheTo       string   // registry cache ref to write; empty: none
	provenance    bool
	builderID     string
	vcsSource     string
	vcsRevision   string
	out           string // the OCI tar, written here
	maxBytes      int64
	log           func(string)
}

// solveStats are what the runner learnt while a build ran.
type solveStats struct {
	steps, cached int
}

// solve runs one build. Its progress goes to req.log line by line,
// formatted as BuildKit's plain output.
func (b *builder) solve(ctx context.Context, req solveRequest) (*solveStats, error) {
	ctxFS, err := fsutil.NewFS(req.contextDir)
	if err != nil {
		return nil, fmt.Errorf("the build context %s: %w", req.contextDir, err)
	}
	dfFS, err := fsutil.NewFS(req.dockerfileDir)
	if err != nil {
		return nil, fmt.Errorf("the Dockerfile's directory: %w", err)
	}
	attrs := map[string]string{"filename": req.dockerfile}
	if req.target != "" {
		attrs["target"] = req.target
	}
	if len(req.platforms) > 0 {
		attrs["platform"] = strings.Join(req.platforms, ",")
	}
	for k, v := range req.buildArgs {
		attrs["build-arg:"+k] = v
	}
	if req.provenance {
		p := "mode=max,version=v1"
		if req.builderID != "" {
			p += ",builder-id=" + req.builderID
		}
		attrs["attest:provenance"] = p
		if req.vcsSource != "" {
			attrs["vcs:source"] = req.vcsSource
		}
		if req.vcsRevision != "" {
			attrs["vcs:revision"] = req.vcsRevision
		}
	} else {
		attrs["attest:provenance"] = ""
	}
	tarOut, err := os.OpenFile(req.out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer tarOut.Close()
	limited := &capWriter{w: tarOut, left: req.maxBytes}
	opt := client.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
		LocalMounts:   map[string]fsutil.FS{"context": ctxFS, "dockerfile": dfFS},
		Session: []session.Attachable{
			secretsprovider.FromMap(req.secrets),
			newSessionAuth(req.hosts),
		},
		Exports: []client.ExportEntry{{
			Type:  client.ExporterOCI,
			Attrs: map[string]string{"oci-mediatypes": "true"},
			Output: func(map[string]string) (io.WriteCloser, error) {
				return nopCloser{limited}, nil
			},
		}},
	}
	for _, ref := range req.cacheFrom {
		opt.CacheImports = append(opt.CacheImports, client.CacheOptionsEntry{Type: "registry", Attrs: map[string]string{"ref": ref}})
	}
	if req.cacheTo != "" {
		opt.CacheExports = []client.CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{
			"ref": req.cacheTo, "mode": "max", "oci-mediatypes": "true", "image-manifest": "true", "ignore-error": "true"}}}
	}

	ch := make(chan *client.SolveStatus)
	stats := &solveStats{}
	var cacheFailed atomic.Bool
	// Every line goes out as the builder reports it (see progressPrinter).
	pp := newProgressPrinter(func(line string) {
		if cacheTrouble(line) {
			cacheFailed.Store(true)
		}
		req.log(line)
	})
	g, gctx := errgroup.WithContext(ctx)
	var solveErr error
	g.Go(func() error {
		_, solveErr = b.c.Solve(gctx, nil, opt, ch)
		return nil
	})
	g.Go(func() error { drainProgress(ch, pp, stats); return nil })
	_ = g.Wait()
	if cacheFailed.Load() {
		req.log("Image cache: the build output above mentions a cache that could not be read or written. The build is not affected: a cache that cannot be read is skipped, and a failed export is ignored (ignore-error), so this run's layers may not be there for the next one.")
	}
	if limited.over {
		return stats, fmt.Errorf("the image is larger than this runner accepts (%s); see images.max_image_mib", mib(req.maxBytes))
	}
	if solveErr != nil {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		return stats, buildError(solveErr)
	}
	return stats, tarOut.Close()
}

// buildError says why a build failed in a line a person can act on.
func buildError(err error) error {
	msg := plainGRPC(err)
	switch {
	case strings.Contains(msg, "failed to read dockerfile") || strings.Contains(msg, "failed to solve: failed to read"):
		return fmt.Errorf("the Dockerfile could not be read: %s", msg)
	case strings.Contains(msg, "401 Unauthorized") || strings.Contains(msg, "authorization failed") ||
		strings.Contains(msg, "unauthorized"):
		return fmt.Errorf("a registry refused the builder (wrong or missing credentials): %s", msg)
	}
	return errors.New(msg)
}

// capWriter stops writing once more than left bytes came.
type capWriter struct {
	w    io.Writer
	left int64
	over bool
}

func (c *capWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > c.left {
		c.over = true
		return 0, errors.New("too large")
	}
	c.left -= int64(len(b))
	return c.w.Write(b)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func mib(n int64) string { return fmt.Sprintf("%d MiB", n>>20) }

// pick chooses where each platform is built: one builder for all of them
// if one serves the project and builds them all, else one per platform,
// preferring a builder that lists the platform among its own (native).
// It returns platform groups by builder, or an error naming what cannot
// be built.
func pick(builders []*builder, project string, want []string) (map[*builder][]string, error) {
	var mine []*builder
	for _, b := range builders {
		if b.cfg.serves(project) {
			mine = append(mine, b)
		}
	}
	if len(mine) == 0 {
		return nil, fmt.Errorf("no builder on this runner serves project %s (images.builders[].projects)", project)
	}
	if len(want) == 0 {
		return map[*builder][]string{mine[0]: nil}, nil
	}
	for _, b := range mine {
		if containsAll(b.platforms, want) {
			return map[*builder][]string{b: want}, nil
		}
	}
	out := map[*builder][]string{}
	var missing []string
	for _, p := range want {
		var chosen *builder
		for _, b := range mine {
			if slices.Contains(b.platforms, normPlatform(p)) && slices.Contains(b.cfg.Platforms, p) {
				chosen = b // a builder configured for this platform
				break
			}
		}
		if chosen == nil {
			for _, b := range mine {
				if slices.Contains(b.platforms, normPlatform(p)) {
					chosen = b
					break
				}
			}
		}
		if chosen == nil {
			missing = append(missing, p)
			continue
		}
		out[chosen] = append(out[chosen], p)
	}
	if len(missing) > 0 {
		var have []string
		for _, b := range mine {
			have = append(have, fmt.Sprintf("%s: %s", b.cfg.Name, strings.Join(b.platforms, ", ")))
		}
		return nil, fmt.Errorf("no builder for project %s builds %s. The builders build: %s. Add a builder on a machine of that "+
			"architecture (images.builders, with platforms: [...]), or install emulation (binfmt) where the builder runs",
			project, strings.Join(missing, ", "), strings.Join(have, "; "))
	}
	return out, nil
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, normPlatform(w)) {
			return false
		}
	}
	return true
}

// cleanDockerfile splits a Dockerfile path into its directory and name.
func cleanDockerfile(path string) (dir, name string) {
	return filepath.Dir(path), filepath.Base(path)
}

// drainProgress prints each progress event as it arrives, and counts the
// build's steps and cache hits, until ch is closed.
func drainProgress(ch <-chan *client.SolveStatus, pp *progressPrinter, stats *solveStats) {
	counted := map[string]bool{}
	tick := time.NewTicker(partialAfter / 2)
	defer tick.Stop()
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				pp.Close()
				return
			}
			for _, v := range s.Vertexes {
				if v.Completed != nil && !counted[v.Digest.String()] && !strings.HasPrefix(v.Name, "[internal]") &&
					!strings.HasPrefix(v.Name, "[auth]") && strings.HasPrefix(v.Name, "[") {
					counted[v.Digest.String()] = true
					stats.steps++
					if v.Cached {
						stats.cached++
					}
				}
			}
			pp.Update(s)
		case <-tick.C:
			pp.Tick(pp.now())
		}
	}
}
