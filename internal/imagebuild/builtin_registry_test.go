package imagebuild

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// runAuth lets a run credential in and nothing else.
type runAuth struct {
	svc    *registry.Service
	signer registry.Signer
}

func (a runAuth) Authenticate(_ *http.Request, _, secret string) (*registry.Principal, error) {
	c, err := a.signer.VerifyRun(secret)
	if err != nil {
		return nil, nil
	}
	return a.svc.RunPrincipal(c), nil
}
func (runAuth) Blocked(http.ResponseWriter, *http.Request) bool           { return false }
func (runAuth) Denied(*http.Request, *registry.Principal, registry.Event) {}
func (runAuth) Source(*http.Request) string                               { return "test" }

// The runner side against the real built-in registry: the step's own
// configuration, the run's credential, a push, and the scan result and SBOM
// attached to it, and found by the registry's own listing.
func TestAnImageStepsRegistryRoundTripAgainstTheBuiltInRegistry(t *testing.T) {
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := registry.New(registry.Options{DB: enginedb.New(eng.Database()), Blobs: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	signer := registry.Signer{Key: []byte("0123456789abcdef0123456789abcdef")}
	srv := httptest.NewServer(&registry.Handler{Svc: svc, Auth: runAuth{svc, signer}, Signer: signer, Base: func(r *http.Request) string { return "http://" + r.Host }})
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	cred, _ := signer.MintRun("cart", "run_1", true, 3600e9)
	cfg := (&Config{}).WithBuiltin(Builtin{Host: host, Insecure: true, Username: "launchway-run", Push: true}, "cart")
	rc, err := cfg.Registry(BuiltinName)
	if err != nil {
		t.Fatal(err)
	}
	if !rc.MayPush("cart", "cart/api") || rc.MayPush("cart", "shop/api") {
		t.Fatal("push rules")
	}
	r, err := newRemote(BuiltinName, rc, "launchway-run", cred)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	img, _ := random.Image(2048, 2)
	ref, _ := r.tag("cart/api", "1.0")
	if err := remote.Write(ref, img, r.options(ctx)...); err != nil {
		t.Fatalf("a run could not push with its credential: %v", err)
	}
	d, _ := img.Digest()
	// What the runner does once Build has pushed: attach the scan result and the SBOM of each platform.
	work := t.TempDir()
	sbomDir := filepath.Join(work, OutDir, SBOMDir)
	if err := os.MkdirAll(sbomDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbomDir, "linux-amd64.cdx.json"), []byte(`{"bomFormat":"CycloneDX"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged []string
	req := Request{Project: "cart", Tags: []Tag{{Registry: BuiltinName, Repository: "cart/api", Tag: "1.0"}}, SBOM: true, WorkDir: work,
		Lookup: func(context.Context, pipeline.SecretRef) (string, error) { return cred, nil }, Log: func(l string) { logged = append(logged, l) }}
	res := &Result{Pushed: true, Digest: d.String(), Platforms: []PlatformImage{{Platform: "linux/amd64", Digest: d.String()}}, Scan: &ScanSummary{Critical: 2, High: 3}}
	AttachToBuiltin(ctx, cfg, req, res, nil)
	if len(logged) == 0 {
		t.Fatal("nothing was said of the scan result")
	}
	// A failed build, or one that pushed nothing, attaches nothing.
	AttachToBuiltin(ctx, cfg, req, res, errors.New("failed"))
	AttachToBuiltin(ctx, cfg, req, &Result{Digest: d.String()}, nil)
	got, err := svc.InspectImage(ctx, "cart/api", "1.0")
	if err != nil || !got.HasSBOM || got.Vulnerabilities == nil || got.Vulnerabilities.Critical != 2 || got.Vulnerabilities.High != 3 || len(got.Attached) != 2 {
		t.Fatalf("the registry's view of the image: %+v %v", got, err)
	}
	// Another project's repository is out of reach of this run.
	other, _ := r.tag("shop/api", "1.0")
	if err := remote.Write(other, img, r.options(ctx)...); err == nil {
		t.Fatal("a run pushed to another project")
	}
}
