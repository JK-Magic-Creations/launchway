package imagebuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/moby/buildkit/client"

	"github.com/jk-magic-creations/launchway/internal/imagebuild/imagetest"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// env is what the container tests share: registries and builders.
type env struct {
	plain, secure *imagetest.Registry // plain: HTTP, no authentication; secure: TLS and a password
	bk, bkArm     *imagetest.BuildKit // bkArm builds only linux/arm64 (emulated on an amd64 machine)
	secrets       map[string]string   // what Lookup reads
	mu            sync.Mutex
}

func (e *env) lookup(_ context.Context, ref pipeline.SecretRef) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v, ok := e.secrets[ref.String()]
	if !ok {
		return "", fmt.Errorf("no secret %s", ref)
	}
	return v, nil
}

func (e *env) config() *Config {
	return &Config{
		Builders: []BuilderConfig{{Name: "local", Address: e.bk.Address, CAFile: e.bk.CAFile, CertFile: e.bk.CertFile,
			KeyFile: e.bk.KeyFile, ServerName: e.bk.ServerName}},
		Registries: map[string]RegistryConfig{
			"plain": {Host: e.plain.Host, Insecure: true, Push: []PushRule{{Projects: []string{"shop"}, Repositories: []string{"team/**"}}},
				Credentials: &Credentials{Username: "unused", Password: pipeline.SecretRef{Store: "t", Key: "plain"}}},
			"secure": {Host: e.secure.Host, CAFile: e.secure.CAFile,
				Credentials: &Credentials{Username: e.secure.User, Password: pipeline.SecretRef{Store: "t", Key: "secure"}},
				Push:        []PushRule{{Projects: []string{"shop"}, Repositories: []string{"team/shop", "team/cache/*"}}}},
		},
	}
}

func startEnv(t *testing.T) *env {
	imagetest.Skip(t)
	ca := imagetest.NewCA(t)
	e := &env{secrets: map[string]string{}}
	e.plain = imagetest.StartRegistry(t, imagetest.RegistryOptions{})
	e.secure = imagetest.StartRegistry(t, imagetest.RegistryOptions{Auth: true, TLS: ca})
	e.secrets["t:secure"] = e.secure.Password
	e.secrets["t:plain"] = "anything"
	o := imagetest.BuildKitOptions{HTTP: []string{e.plain.Host}, CAs: map[string]string{e.secure.Host: e.secure.CAFile}}
	e.bk = imagetest.StartBuildKit(t, o)
	o.Platforms = []string{"linux/arm64"}
	e.bkArm = imagetest.StartBuildKit(t, o)
	return e
}

// workspace writes a build context with a Dockerfile and returns its
// directory.
func workspace(t *testing.T, dockerfile string, files map[string]string) string {
	dir := t.TempDir()
	files["Dockerfile"] = dockerfile
	for n, c := range files {
		p := filepath.Join(dir, n)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) add(s string) { l.mu.Lock(); l.lines = append(l.lines, s); l.mu.Unlock() }
func (l *logLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (e *env) request(t *testing.T, dir string, logs *logLines) Request {
	return Request{
		Project: "shop", Event: "push", ContextDir: dir, DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextName: ".", DockerfileName: "Dockerfile",
		Source: Source{Repository: "https://git.example/team/shop.git", Ref: "refs/heads/main",
			Commit: "0123456789abcdef0123456789abcdef01234567", Event: "push", Project: "shop", Pipeline: "shop", Run: "run_test", RunNumber: 7,
			Step: "image", Attempt: 1},
		RunnerID: "https://launchway.example/runners/test", Version: "test", WorkDir: t.TempDir(),
		Lookup: e.lookup, Log: logs.add,
	}
}

func remoteOpts(r *imagetest.Registry) []remote.Option {
	var opts []remote.Option
	if r.Password != "" {
		opts = append(opts, remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: r.User, Password: r.Password})))
	}
	if r.CAFile != "" {
		rr, err := newRemote("x", RegistryConfig{Host: r.Host, CAFile: r.CAFile}, "", "")
		if err == nil {
			opts = append(opts, rr.options(context.Background())[2])
		}
	}
	return opts
}

func nameOpts(r *imagetest.Registry) []name.Option {
	if r.CAFile == "" {
		return []name.Option{name.Insecure}
	}
	return nil
}

// layerFiles reads every file of every layer of an image.
func layerFiles(t *testing.T, img v1.Image) map[string][]byte {
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, l := range layers {
		rc, err := l.Uncompressed()
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(rc)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(tr)
			out[h.Name] = b
		}
		rc.Close()
	}
	return out
}

func TestImageBuildContainers(t *testing.T) {
	e := startEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	t.Run("build, push, pull back; the build secret is in no layer or log", func(t *testing.T) {
		token := "npm_" + "Tk" + strings.Repeat("x7", 10) + fmt.Sprint(time.Now().UnixNano()) // made up at test time
		dir := workspace(t, `FROM docker.io/library/alpine:3.22
ARG VERSION
COPY hello.txt /hello.txt
RUN --mount=type=secret,id=npm_token sh -c 'test -s /run/secrets/npm_token && sha256sum /run/secrets/npm_token | cut -c1-12 > /token.sha && echo "built $VERSION" > /version'
`, map[string]string{"hello.txt": "hello\n"})
		logs := &logLines{}
		req := e.request(t, dir, logs)
		req.BuildArgs = map[string]string{"VERSION": "1.2.3"}
		req.Secrets = map[string][]byte{"npm_token": []byte(token)}
		req.Tags = []Tag{{"secure", "team/shop", "v1"}, {"secure", "team/shop", "latest"}}
		req.Push, req.Provenance = true, true
		res, err := Build(ctx, e.config(), req)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, logs)
		}
		if !res.Pushed || res.Digest == "" || res.Image != e.secure.Host+"/team/shop@"+res.Digest {
			t.Fatalf("result: %+v", res)
		}
		if len(res.Tags) != 2 || res.Size <= 0 || len(res.Platforms) != 1 || res.Platforms[0].Platform != "linux/amd64" {
			t.Fatalf("result: %+v", res)
		}
		for _, tag := range []string{"v1", "latest"} {
			ref, _ := name.ParseReference(e.secure.Host+"/team/shop:"+tag, nameOpts(e.secure)...)
			d, err := remote.Head(ref, remoteOpts(e.secure)...)
			if err != nil {
				t.Fatalf("head %s: %v", ref, err)
			}
			if d.Digest.String() != res.Digest {
				t.Fatalf("%s is %s, the step said %s", ref, d.Digest, res.Digest)
			}
		}
		ref, _ := name.ParseReference(res.Image, nameOpts(e.secure)...)
		idx, err := remote.Index(ref, remoteOpts(e.secure)...)
		if err != nil {
			t.Fatal(err)
		}
		im, _ := idx.IndexManifest()
		var image v1.Image
		attestations := 0
		for _, d := range im.Manifests {
			if d.Annotations[annoRefType] == attestation {
				attestations++
				continue
			}
			image, _ = idx.Image(d.Digest)
		}
		if image == nil || attestations != 1 {
			t.Fatalf("index has %d manifests, %d attestations", len(im.Manifests), attestations)
		}
		files := layerFiles(t, image)
		if got := string(files["version"]); got != "built 1.2.3\n" {
			t.Fatalf("/version = %q", got)
		}
		if len(files["token.sha"]) == 0 {
			t.Fatal("the secret was not mounted in the build")
		}
		for name, data := range files {
			if bytes.Contains(data, []byte(token)) {
				t.Fatalf("the build secret is in the image, in %s", name)
			}
		}
		cfg, _ := image.RawConfigFile()
		for _, where := range []string{string(cfg), logs.String(), mustJSON(res)} {
			if strings.Contains(where, token) || strings.Contains(where, e.secure.Password) {
				t.Fatal("a secret or the registry password is in the config, the log or the result")
			}
		}
		// Provenance: Launchway's, stored; the builder's, in the index.
		prov, err := os.ReadFile(filepath.Join(req.WorkDir, OutDir, ProvenanceFile))
		if err != nil {
			t.Fatal(err)
		}
		var st statement
		if err := json.Unmarshal(prov, &st); err != nil || st.PredicateType != SLSAProvenanceV1 || len(st.Subject) != 1 ||
			st.Subject[0].Digest["sha256"] != strings.TrimPrefix(res.Digest, "sha256:") {
			t.Fatalf("provenance: %v %s", err, prov)
		}
		for _, want := range []string{`"commit": "0123456789abcdef0123456789abcdef01234567"`, `"VERSION": "1.2.3"`, `"npm_token"`, BuildType} {
			if !strings.Contains(string(prov), want) {
				t.Fatalf("provenance has no %s:\n%s", want, prov)
			}
		}
		if bp, _ := filepath.Glob(filepath.Join(req.WorkDir, OutDir, BuilderProvDir, "*.json")); len(bp) != 1 {
			t.Fatalf("builder provenance files: %v", bp)
		}
		if len(res.BaseImages) == 0 || !strings.Contains(res.BaseImages[0], "alpine") {
			t.Fatalf("base images: %v", res.BaseImages)
		}
	})

	t.Run("the registry cache is used by a builder that has nothing cached", func(t *testing.T) {
		salt := fmt.Sprint(time.Now().UnixNano())
		df := "FROM docker.io/library/alpine:3.22\nCOPY salt.txt /salt.txt\nRUN cat /salt.txt > /copy && sleep 1\nRUN echo two > /two\n"
		build := func() *Result {
			logs := &logLines{}
			req := e.request(t, workspace(t, df, map[string]string{"salt.txt": salt}), logs)
			req.Tags = []Tag{{"plain", "team/cached", "v1"}}
			req.Push = true
			req.Cache = &Repo{"plain", "team/cache/cached"}
			res, err := Build(ctx, e.config(), req)
			if err != nil {
				t.Fatalf("%v\n%s", err, logs)
			}
			return res
		}
		first := build()
		if first.Cached != 0 {
			t.Fatalf("the first build took %d of %d steps from a cache; the salt should make them new", first.Cached, first.Steps)
		}
		pruneBuilder(t, ctx, e)
		second := build()
		// The image's creation time differs, so its digest does too; the
		// steps after FROM come from the registry cache.
		if second.Cached < 3 {
			t.Fatalf("second build: %d of %d steps cached, digest %s (first %s)", second.Cached, second.Steps, second.Digest, first.Digest)
		}
	})

	t.Run("a pull-request run never pushes", func(t *testing.T) {
		logs := &logLines{}
		req := e.request(t, workspace(t, "FROM docker.io/library/alpine:3.22\nRUN true\n", map[string]string{}), logs)
		req.Event = pipeline.EventPullRequest
		req.Tags = []Tag{{"plain", "team/pr", "v1"}}
		req.Push = true
		if _, err := Build(ctx, e.config(), req); err == nil || !strings.Contains(err.Error(), "never pushes") {
			t.Fatalf("push on a pull request: %v", err)
		}
		// Push credentials are never read for it either.
		e.mu.Lock()
		delete(e.secrets, "t:plain")
		e.mu.Unlock()
		defer func() { e.mu.Lock(); e.secrets["t:plain"] = "anything"; e.mu.Unlock() }()
		req.Push = false
		res, err := Build(ctx, e.config(), req)
		if err != nil {
			t.Fatalf("%v\n%s", err, logs)
		}
		if res.Pushed || res.Digest == "" || !strings.Contains(res.NotPushed, "pull-request") {
			t.Fatalf("result: %+v", res)
		}
		repo, _ := name.NewRepository(e.plain.Host+"/team/pr", name.Insecure)
		if tags, err := remote.List(repo); err == nil && len(tags) > 0 {
			t.Fatalf("tags pushed: %v", tags)
		}
	})

	t.Run("refusals and failures say what is wrong", func(t *testing.T) {
		dir := workspace(t, "FROM docker.io/library/alpine:3.22\nRUN true\n", map[string]string{})
		cases := []struct {
			name string
			edit func(c *Config, r *Request)
			want string
		}{
			{"repository not allowed", func(c *Config, r *Request) { r.Tags = []Tag{{"secure", "other/app", "v1"}} },
				"may not push to " + e.secure.Host + "/other/app"},
			{"unknown registry", func(c *Config, r *Request) { r.Tags = []Tag{{"nope", "team/shop", "v1"}} },
				`registry "nope" is not configured on this runner`},
			{"wrong password", func(c *Config, r *Request) {
				rc := c.Registries["secure"]
				rc.Credentials = &Credentials{Username: e.secure.User, Password: pipeline.SecretRef{Store: "t", Key: "wrong"}}
				c.Registries["secure"] = rc
				r.Tags = []Tag{{"secure", "team/shop", "wrong"}}
			}, "refused the credentials while checking that it may push"},
			{"unreachable registry", func(c *Config, r *Request) {
				c.Registries["gone"] = RegistryConfig{Host: fmt.Sprintf("127.0.0.1:%d", imagetest.FreePort(t)), Insecure: true,
					Credentials: c.Registries["plain"].Credentials, Push: c.Registries["plain"].Push}
				r.Tags = []Tag{{"gone", "team/shop", "v1"}}
			}, "cannot be reached while checking that it may push"},
			{"untrusted TLS", func(c *Config, r *Request) {
				rc := c.Registries["secure"]
				rc.CAFile = ""
				c.Registries["secure"] = rc
				r.Tags = []Tag{{"secure", "team/shop", "tls"}}
			}, "not trusted"},
			{"unreachable builder", func(c *Config, r *Request) {
				c.Builders[0].Address = fmt.Sprintf("tcp://127.0.0.1:%d", imagetest.FreePort(t))
			}, "no image builder could be reached"},
			{"a platform no builder builds", func(c *Config, r *Request) {
				c.Builders[0].Platforms = []string{"linux/amd64"}
				r.Platforms = []string{"linux/amd64", "linux/s390x"}
			}, "builds linux/s390x"},
			{"a Dockerfile that fails", func(c *Config, r *Request) {
				r.ContextDir = workspace(t, "FROM docker.io/library/alpine:3.22\nRUN exit 3\n", map[string]string{})
				r.DockerfilePath = filepath.Join(r.ContextDir, "Dockerfile")
			}, "did not complete successfully: exit code: 3"},
		}
		e.secrets["t:wrong"] = "wrong-" + "password"
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				logs := &logLines{}
				cfg := e.config()
				req := e.request(t, dir, logs)
				req.Tags = []Tag{{"secure", "team/shop", "x"}}
				req.Push = true
				c.edit(cfg, &req)
				_, err := Build(ctx, cfg, req)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("got %v, want %q\n%s", err, c.want, logs)
				}
				if strings.Contains(err.Error(), e.secure.Password) || strings.Contains(err.Error(), "wrong-password") {
					t.Fatalf("the error has a password: %v", err)
				}
				t.Logf("%s: %v", c.name, err)
			})
		}
	})

	t.Run("two builders, one per platform, make one index", func(t *testing.T) {
		logs := &logLines{}
		cfg := e.config()
		cfg.Builders = []BuilderConfig{cfg.Builders[0], {Name: "arm", Address: e.bkArm.Address, CAFile: e.bkArm.CAFile,
			CertFile: e.bkArm.CertFile, KeyFile: e.bkArm.KeyFile, ServerName: e.bkArm.ServerName, Platforms: []string{"linux/arm64"}}}
		cfg.Builders[0].Platforms = []string{"linux/amd64"}
		req := e.request(t, workspace(t, "FROM docker.io/library/alpine:3.22\nRUN uname -m > /arch\n", map[string]string{}), logs)
		req.Platforms = []string{"linux/amd64", "linux/arm64"}
		req.Tags = []Tag{{"plain", "team/multi", "v1"}}
		req.Push, req.Provenance = true, true
		res, err := Build(ctx, cfg, req)
		if err != nil {
			t.Fatalf("%v\n%s", err, logs)
		}
		if !slices.Equal(res.Builders, []string{"local", "arm"}) || len(res.Platforms) != 2 {
			t.Fatalf("result: %+v", res)
		}
		ref, _ := name.ParseReference(res.Image, name.Insecure)
		idx, err := remote.Index(ref)
		if err != nil {
			t.Fatal(err)
		}
		im, _ := idx.IndexManifest()
		arches := map[string]string{}
		for _, d := range im.Manifests {
			if d.Annotations[annoRefType] == attestation {
				continue
			}
			img, _ := idx.Image(d.Digest)
			arches[d.Platform.String()] = strings.TrimSpace(string(layerFiles(t, img)["arch"]))
		}
		if arches["linux/amd64"] != "x86_64" || arches["linux/arm64"] != "aarch64" {
			t.Fatalf("arches: %v", arches)
		}
		if bp, _ := filepath.Glob(filepath.Join(req.WorkDir, OutDir, BuilderProvDir, "*.json")); len(bp) != 2 {
			t.Fatalf("builder provenance files: %v", bp)
		}
	})
}

func TestImageSignScanContainers(t *testing.T) {
	e := startEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	t.Run("signed with a cosign key: cosign verifies the signature, the provenance and the SBOM", func(t *testing.T) {
		keys := t.TempDir()
		_ = os.Chmod(keys, 0o777)
		password := "pw-" + fmt.Sprint(time.Now().UnixNano())
		t.Setenv("LAUNCHWAY_TEST_COSIGN_PASSWORD", password)
		if out, ok := imagetest.Cosign(t, imagetest.CosignImage, keys, "generate-key-pair", "--output-key-prefix", "/w/cosign"); !ok {
			t.Fatalf("cosign generate-key-pair: %s", out)
		}
		if out, ok := imagetest.Cosign(t, imagetest.CosignImage, keys, "generate-key-pair", "--output-key-prefix", "/w/other"); !ok {
			t.Fatalf("cosign generate-key-pair: %s", out)
		}
		key, err := os.ReadFile(filepath.Join(keys, "cosign.key"))
		if err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		e.secrets["t:cosign"], e.secrets["t:cosign-pw"] = string(key), password
		e.mu.Unlock()
		logs := &logLines{}
		req := e.request(t, workspace(t, "FROM docker.io/library/alpine:3.22\nRUN echo signed > /signed\n", map[string]string{}), logs)
		req.Tags = []Tag{{"plain", "team/signed", "v1"}}
		req.Push, req.Provenance, req.SBOM = true, true, true
		req.Sign = &Sign{Key: &pipeline.SecretRef{Store: "t", Key: "cosign"}, Password: &pipeline.SecretRef{Store: "t", Key: "cosign-pw"}}
		res, err := Build(ctx, e.config(), req)
		if err != nil {
			t.Fatalf("%v\n%s", err, logs)
		}
		if !res.Signed || res.Provenance != "attached" || !res.SBOM || res.KeyID == "" || len(res.Signatures) < 3 {
			t.Fatalf("result: %+v", res)
		}
		if sboms, _ := filepath.Glob(filepath.Join(req.WorkDir, OutDir, SBOMDir, "*.cdx.json")); len(sboms) != 1 {
			t.Fatalf("SBOM files: %v", sboms)
		}
		if strings.Contains(logs.String(), password) || strings.Contains(mustJSON(res), password) {
			t.Fatal("the key's password is in the log or the result")
		}
		img := res.Image
		for _, image := range []string{imagetest.CosignImage, imagetest.Cosign3Image} {
			base := []string{"--key", "/w/cosign.pub", "--insecure-ignore-tlog=true", "--allow-http-registry=true"}
			if out, ok := imagetest.Cosign(t, image, keys, append(append([]string{"verify"}, base...), img)...); !ok {
				t.Fatalf("%s verify: %s", image, out)
			}
			for _, typ := range []string{"slsaprovenance1", "cyclonedx"} {
				target := img
				if typ == "cyclonedx" {
					target = e.plain.Host + "/team/signed@" + res.Platforms[0].Digest
				}
				out, ok := imagetest.Cosign(t, image, keys, append(append([]string{"verify-attestation", "--type", typ}, base...), target)...)
				if !ok {
					t.Fatalf("%s verify-attestation --type %s: %s", image, typ, out)
				}
			}
			out, ok := imagetest.Cosign(t, image, keys, "verify", "--key", "/w/other.pub", "--insecure-ignore-tlog=true", "--allow-http-registry=true", img)
			if ok {
				t.Fatalf("%s verified the image with another key: %s", image, out)
			}
		}
	})

	t.Run("the scan finds vulnerabilities; enforce fails the step before anything is pushed", func(t *testing.T) {
		// A package manifest of a lodash version with published
		// vulnerabilities, so the scan has something to find.
		files := map[string]string{"app/node_modules/lodash/package.json": `{"name":"lodash","version":"4.17.15"}`,
			"app/package.json": `{"name":"app","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}`}
		df := "FROM docker.io/library/alpine:3.22\nCOPY app /app\n"
		for _, enforce := range []bool{false, true} {
			logs := &logLines{}
			req := e.request(t, workspace(t, df, files), logs)
			req.Tags = []Tag{{"plain", "team/scanned", fmt.Sprint(enforce)}}
			req.Push, req.SBOM = true, true
			req.Scan = &Scan{FailOn: "high", Enforce: enforce}
			res, err := Build(ctx, e.config(), req)
			sarifs, _ := filepath.Glob(filepath.Join(req.WorkDir, OutDir, ScanDir, "*.sarif"))
			if len(sarifs) != 1 || res.Scan == nil || res.Scan.High+res.Scan.Critical == 0 {
				t.Fatalf("enforce %v: %v; sarif %v; scan %+v\n%s", enforce, err, sarifs, res.Scan, logs)
			}
			var pe *PolicyError
			switch {
			case !enforce && (err != nil || !res.Pushed || res.Scan.Failed):
				t.Fatalf("report mode: %v %+v", err, res)
			case enforce && (!errors.As(err, &pe) || res.Pushed || !res.Scan.Failed):
				t.Fatalf("enforce mode: %v %+v", err, res)
			}
			if enforce {
				ref, _ := name.ParseReference(e.plain.Host+"/team/scanned:true", name.Insecure)
				if _, err := remote.Head(ref); err == nil {
					t.Fatal("pushed although the scan failed the step")
				}
				t.Logf("enforce: %v", err)
			}
		}
	})
}

// pruneBuilder empties the builder's cache, except the scanner's database.
func pruneBuilder(t *testing.T, ctx context.Context, e *env) {
	b, err := connect(ctx, e.config().Builders[0])
	if err != nil {
		t.Fatal(err)
	}
	defer b.c.Close()
	ch := make(chan client.UsageInfo)
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	err = b.c.Prune(ctx, ch, client.PruneAll, client.WithFilter([]string{"type!=exec.cachemount"}))
	close(ch)
	<-done
	if err != nil {
		t.Fatal(err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = gzip.NewReader
var _ = errors.New
