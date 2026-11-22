package imagebuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/tonistiigi/fsutil"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

// The SBOM and the vulnerability scan are made by the scanner image (Trivy
// by default), run inside the builder like any build step: never in the
// runner, and never in the step's pod. It reads the image as the runner
// received it (the OCI layout, sent to the builder as a local source), so
// what is scanned is exactly what is pushed. Its vulnerability database is
// kept in a cache on the builder between scans; the scan steps are never
// taken from the build cache, so every scan uses the current database.

// scanRequest says what to make for one platform's image.
type scanRequest struct {
	layoutDir string
	platform  string // the image to read, when the layout has several
	sbom      bool
	vulns     bool
	outDir    string // gets sbom.cdx.json and scan.sarif
	log       func(string)
}

// scannerEnvironment is the environment every run of the scanner gets. The scanner must not dial out to ask whether a
// newer version exists (TRIVY_SKIP_VERSION_CHECK, for the pinned 0.76 line; the same setting is --skip-version-check
// in scripts/security): a Station whose egress is closed would wait for that call, and it would tell a third party
// that a scan ran.
var scannerEnvironment = [][2]string{
	{"TRIVY_CACHE_DIR", "/cache"},
	{"TRIVY_NO_PROGRESS", "true"},
	{"TRIVY_DISABLE_TELEMETRY", "true"},
	{"TRIVY_SKIP_VERSION_CHECK", "true"},
}

var repoRe = regexp.MustCompile(`^[a-z0-9.\-:/_]+$`)

// scan runs the scanner on a builder.
func (b *builder) scan(ctx context.Context, image string, cfg ScannerConfig, req scanRequest) error {
	native := platforms.DefaultSpec()
	if len(b.platforms) > 0 {
		if p, err := platforms.Parse(b.nativePlatform()); err == nil {
			native = p
		}
	}
	base := llb.Image(image, llb.Platform(native))
	in := llb.Local("image")
	var env []llb.RunOption
	for _, kv := range scannerEnvironment {
		env = append(env, llb.AddEnv(kv[0], kv[1]))
	}
	env = append(env,
		llb.AddMount("/in", in, llb.Readonly),
		llb.AddMount("/cache", llb.Scratch(), llb.AsPersistentCacheDir("launchway-trivy-cache", llb.CacheMountLocked)),
		llb.IgnoreCache)
	dbArgs := func() []string {
		var a []string
		if cfg.DBRepository != "" && repoRe.MatchString(cfg.DBRepository) {
			a = append(a, "--db-repository", cfg.DBRepository)
		}
		if cfg.JavaDBRepository != "" && repoRe.MatchString(cfg.JavaDBRepository) {
			a = append(a, "--java-db-repository", cfg.JavaDBRepository)
		}
		return a
	}
	sbomArgs := []string{"trivy", "image", "--input", "/in", "--format", "cyclonedx", "--output", "/out/sbom.cdx.json", "--quiet"}
	if req.platform != "" {
		sbomArgs = append(sbomArgs, "--platform", req.platform)
	}
	run1 := base.Run(append(env, llb.Args(sbomArgs), llb.WithCustomName("[scan] SBOM of "+nonEmptyStr(req.platform, "the image")))...)
	out := run1.AddMount("/out", llb.Scratch())
	if req.vulns {
		args := append([]string{"trivy", "sbom", "/out/sbom.cdx.json", "--format", "sarif", "--output", "/out/scan.sarif", "--quiet"}, dbArgs()...)
		run2 := base.Run(append(env, llb.Args(args), llb.WithCustomName("[scan] vulnerabilities in "+nonEmptyStr(req.platform, "the image")))...)
		out = run2.AddMount("/out", out)
	}
	def, err := out.Marshal(ctx, llb.Platform(native))
	if err != nil {
		return err
	}
	layoutFS, err := fsutil.NewFS(req.layoutDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(req.outDir, 0o700); err != nil {
		return err
	}
	ch := make(chan *client.SolveStatus)
	done := make(chan struct{})
	go func() {
		defer close(done)
		seen := map[string]bool{}
		for s := range ch {
			for _, v := range s.Vertexes {
				if v.Error != "" && !seen[v.Digest.String()+"e"] {
					seen[v.Digest.String()+"e"] = true
					req.log(fmt.Sprintf("%s: %s", v.Name, v.Error))
				}
				if v.Started != nil && !seen[v.Digest.String()] && strings.HasPrefix(v.Name, "[scan]") {
					seen[v.Digest.String()] = true
					req.log(v.Name)
				}
			}
			for _, l := range s.Logs {
				for _, line := range strings.Split(strings.TrimRight(string(l.Data), "\n"), "\n") {
					if strings.TrimSpace(line) != "" {
						req.log("  " + line)
					}
				}
			}
		}
	}()
	_, err = b.c.Solve(ctx, def, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{"image": layoutFS},
		Exports:     []client.ExportEntry{{Type: client.ExporterLocal, OutputDir: req.outDir}},
	}, ch)
	<-done
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the scanner (%s) failed: %s", image, plainGRPC(err))
	}
	return nil
}

func (b *builder) nativePlatform() string {
	if b.native != "" {
		return b.native
	}
	return platforms.Format(platforms.DefaultSpec())
}

func nonEmptyStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ScanSummary is what the vulnerability scan found, by severity.
type ScanSummary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	// FailOn and Mode are the policy the step was built with; Failed says
	// the policy failed it (mode enforce, a finding at FailOn or worse).
	FailOn string `json:"fail_on,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// atLeast counts findings at sev or worse.
func (s ScanSummary) atLeast(sev string) int {
	n := 0
	switch sev {
	case "low":
		n += s.Low
		fallthrough
	case "medium":
		n += s.Medium
		fallthrough
	case "high":
		n += s.High
		fallthrough
	case "critical":
		n += s.Critical
	}
	return n
}

// summarise reads the SARIF files the scanner wrote, with the parser the
// findings view uses, so the step decides on the same counts people see.
func summarise(ctx context.Context, files []string) (ScanSummary, error) {
	var s ScanSummary
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return s, err
		}
		r, err := findings.Parse(ctx, data, findings.Limits{})
		if err != nil {
			return s, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		s.Critical += r.Counts.Critical
		s.High += r.Counts.High
		s.Medium += r.Counts.Medium
		s.Low += r.Counts.Low
		s.Unknown += r.Counts.Unknown
	}
	return s, nil
}
