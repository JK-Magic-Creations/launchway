package imagebuild

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/buildkit/session/auth"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"golang.org/x/crypto/nacl/sign"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func TestConfigCheck(t *testing.T) {
	pw := pipeline.SecretRef{Store: "vault", Key: "ci/harbor", Field: "password"}
	good := func() *Config {
		return &Config{
			Builders: []BuilderConfig{{Name: "shared", Address: "tcp://buildkitd:1234", CAFile: "ca", CertFile: "c", KeyFile: "k"}},
			Registries: map[string]RegistryConfig{"harbor": {Host: "harbor.example.com",
				Credentials: &Credentials{Username: "robot$ci", Password: pw},
				Push:        []PushRule{{Projects: []string{"shop"}, Repositories: []string{"team/**"}}}}},
		}
	}
	if err := good().Check(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(c *Config){
		"tcp without mutual TLS": func(c *Config) { c.Builders[0].CAFile = "" },
		"bad address":            func(c *Config) { c.Builders[0].Address = "buildkitd:1234" },
		"twice":                  func(c *Config) { c.Builders = append(c.Builders, c.Builders[0]) },
		"bad platform":           func(c *Config) { c.Builders[0].Platforms = []string{"arm64"} },
		"bad host": func(c *Config) {
			r := c.Registries["harbor"]
			r.Host = "https://harbor.example.com"
			c.Registries["harbor"] = r
		},
		"no password": func(c *Config) {
			r := c.Registries["harbor"]
			r.Credentials = &Credentials{Username: "x"}
			c.Registries["harbor"] = r
		},
		"no user": func(c *Config) {
			r := c.Registries["harbor"]
			r.Credentials = &Credentials{Password: pw}
			c.Registries["harbor"] = r
		},
		"empty rule": func(c *Config) {
			r := c.Registries["harbor"]
			r.Push = []PushRule{{Projects: []string{"shop"}}}
			c.Registries["harbor"] = r
		},
		"scanner not pinned": func(c *Config) { c.Scanner.Image = "aquasec/trivy:latest" },
		"pull_projects without pull credentials": func(c *Config) {
			r := c.Registries["harbor"]
			r.PullProjects = []string{"shop"}
			c.Registries["harbor"] = r
		},
		"signing without key": func(c *Config) { c.Signing = &SigningConfig{} },
	}
	for name, edit := range cases {
		c := good()
		edit(c)
		if err := c.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMayPush(t *testing.T) {
	r := RegistryConfig{Push: []PushRule{
		{Projects: []string{"shop", "web-*"}, Repositories: []string{"team/shop", "team/cache/*"}},
		{Projects: []string{"platform"}, Repositories: []string{"platform/**"}},
	}}
	for _, c := range []struct {
		project, repo string
		want          bool
	}{
		{"shop", "team/shop", true},
		{"web-a", "team/cache/web-a", true},
		{"shop", "team/cache/a/b", false}, // * is one level
		{"shop", "team/other", false},
		{"other", "team/shop", false},
		{"platform", "platform/a/b/c", true},
		{"platform", "platform", true},
		{"platform", "platformx/a", false},
		{"shop", "", false},
	} {
		if got := r.MayPush(c.project, c.repo); got != c.want {
			t.Errorf("%s → %s: %v, want %v", c.project, c.repo, got, c.want)
		}
	}
	if (RegistryConfig{}).MayPush("shop", "team/shop") {
		t.Error("a registry without rules lets a project push")
	}
}

func TestPick(t *testing.T) {
	amd := &builder{cfg: BuilderConfig{Name: "amd"}, platforms: []string{"linux/amd64", "linux/arm64"}}
	arm := &builder{cfg: BuilderConfig{Name: "arm", Platforms: []string{"linux/arm64"}}, platforms: []string{"linux/arm64"}}
	only := &builder{cfg: BuilderConfig{Name: "only", Projects: []string{"secret-*"}}, platforms: []string{"linux/amd64"}}
	g, err := pick([]*builder{amd, arm}, "shop", []string{"linux/amd64", "linux/arm64"})
	if err != nil || len(g) != 1 || len(g[amd]) != 2 {
		t.Fatalf("one builder for both: %v %v", g, err)
	}
	amd.platforms = []string{"linux/amd64"}
	g, err = pick([]*builder{amd, arm}, "shop", []string{"linux/amd64", "linux/arm64"})
	if err != nil || len(g) != 2 || g[arm][0] != "linux/arm64" {
		t.Fatalf("one per platform: %v %v", g, err)
	}
	if _, err := pick([]*builder{amd, arm}, "shop", []string{"linux/s390x"}); err == nil || !strings.Contains(err.Error(), "linux/s390x") {
		t.Fatalf("missing platform: %v", err)
	}
	if _, err := pick([]*builder{only}, "shop", nil); err == nil || !strings.Contains(err.Error(), "serves project shop") {
		t.Fatalf("project not served: %v", err)
	}
}

// The builder checks a token authority's signature as NaCl's sign.Open
// does.
func TestTokenAuthority(t *testing.T) {
	a := newSessionAuth(map[string]hostAuth{"harbor.example.com": {username: "u", secret: "p"}})
	salt := []byte("salt")
	pub, err := a.GetTokenAuthority(context.Background(), &auth.GetTokenAuthorityRequest{Host: "harbor.example.com", Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := a.VerifyTokenAuthority(context.Background(), &auth.VerifyTokenAuthorityRequest{Host: "harbor.example.com", Salt: salt, Payload: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	copy(key[:], pub.PublicKey)
	msg, ok := sign.Open(nil, signed.Signed, &key)
	if !ok || string(msg) != "hello" {
		t.Fatal("the signature does not verify")
	}
	// Other credentials, another key; no credentials, a key of the salt only.
	b := newSessionAuth(map[string]hostAuth{"harbor.example.com": {username: "u", secret: "q"}})
	pub2, _ := b.GetTokenAuthority(context.Background(), &auth.GetTokenAuthorityRequest{Host: "harbor.example.com", Salt: salt})
	if bytes.Equal(pub.PublicKey, pub2.PublicKey) {
		t.Fatal("two sets of credentials share a token authority")
	}
	if len(pub.PublicKey) != ed25519.PublicKeySize {
		t.Fatal("not an ed25519 key")
	}
	c, _ := a.Credentials(context.Background(), &auth.CredentialsRequest{Host: "other.example.com"})
	if c.Username != "" || c.Secret != "" {
		t.Fatal("credentials given for a registry the build may not use")
	}
}

func TestUnpackLayoutRefusesOddEntries(t *testing.T) {
	for _, entry := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "blobs/sha256/x", Typeflag: tar.TypeReg},
		{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "other", Typeflag: tar.TypeReg},
	} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		h := entry
		h.Mode = 0o644
		_ = tw.WriteHeader(&h)
		_ = tw.Close()
		f := filepath.Join(t.TempDir(), "x.tar")
		_ = os.WriteFile(f, buf.Bytes(), 0o600)
		if err := unpackLayout(f, t.TempDir()); err == nil {
			t.Errorf("%s (%c) was accepted", entry.Name, entry.Typeflag)
		}
	}
}

func TestLoadSigner(t *testing.T) {
	pass := []byte("pw-" + "123456")
	priv, _, err := cryptoutils.GeneratePEMEncodedECDSAKeyPair(elliptic.P256(), cryptoutils.StaticPasswordFunc(pass))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(priv, []byte("ENCRYPTED SIGSTORE PRIVATE KEY")) {
		t.Fatalf("not a cosign key: %s", priv[:40])
	}
	s, err := loadSigner(priv, pass)
	if err != nil || len(s.KeyID) != 16 || !strings.Contains(s.PublicKey, "PUBLIC KEY") {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err := loadSigner(priv, []byte("wrong")); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("wrong password: %v", err)
	}
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(edKey)
	if _, err := loadSigner(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil); err == nil || !strings.Contains(err.Error(), "ECDSA") {
		t.Fatalf("ed25519 key: %v", err)
	}
	if _, err := loadSigner([]byte("not a key"), nil); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestScanSummaryAtLeast(t *testing.T) {
	s := ScanSummary{Critical: 1, High: 2, Medium: 3, Low: 4, Unknown: 5}
	for sev, want := range map[string]int{"critical": 1, "high": 3, "medium": 6, "low": 10, "": 0} {
		if got := s.atLeast(sev); got != want {
			t.Errorf("%s: %d, want %d", sev, got, want)
		}
	}
}

func TestHTTPSOnly(t *testing.T) {
	r, _ := newRemote("x", RegistryConfig{Host: "10.0.0.5:5000"}, "u", "p")
	tr := r.transport()
	req, _ := http.NewRequest(http.MethodGet, "http://10.0.0.5:5000/v2/", nil)
	if _, err := tr.RoundTrip(req); err != errPlainHTTP {
		t.Fatalf("plain HTTP to a registry not marked insecure: %v", err)
	}
}

// Pull credentials go to the builder only for the projects a registry
// names, and for pull-request and draft runs only when they may read
// secrets; otherwise no secret store is read for them at all.
func TestPullCredentialsAreScoped(t *testing.T) {
	cfg := &Config{Registries: map[string]RegistryConfig{
		"harbor": {Host: "harbor.example.com",
			Credentials:     &Credentials{Username: "push", Password: pipeline.SecretRef{Store: "s", Key: "push"}},
			PullCredentials: &Credentials{Username: "pull", Password: pipeline.SecretRef{Store: "s", Key: "pull"}},
			PullProjects:    []string{"shop", "web-*"},
			Push:            []PushRule{{Projects: []string{"shop"}, Repositories: []string{"team/*"}}}},
	}}
	for _, c := range []struct {
		name, project, event string
		pull, push           bool
		wantUser             string
		wantReads            []string
		wantNote             string
	}{
		{"in scope", "shop", "push", true, false, "pull", []string{"s:pull"}, ""},
		{"pattern", "web-a", "manual", true, false, "pull", []string{"s:pull"}, ""},
		{"out of scope", "other", "push", true, false, "", nil, "not for project other"},
		{"pull request without secrets", "shop", "pull_request", false, false, "", nil, "pull-request run may not read secrets"},
		{"draft without secrets", "shop", "draft", false, false, "", nil, "draft run may not read secrets"},
		{"pull request with secrets", "shop", "pull_request", true, false, "pull", []string{"s:pull"}, ""},
		{"push run pushes with push credentials", "shop", "push", true, true, "push", []string{"s:push"}, ""},
	} {
		var reads []string
		req := Request{Project: c.project, Event: c.event, PullCredentials: c.pull, Push: c.push,
			Lookup: func(_ context.Context, ref pipeline.SecretRef) (string, error) {
				reads = append(reads, ref.String())
				return "pw-" + ref.Key, nil
			}}
		if c.push {
			req.Tags = []Tag{{"harbor", "team/shop", "v1"}}
		}
		rs := &registrySet{cfg: cfg, req: req, regs: map[string]*remoteRegistry{}}
		hosts, err := rs.builderHosts(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		h := hosts["harbor.example.com"]
		if h.username != c.wantUser || !slices.Equal(reads, c.wantReads) {
			t.Errorf("%s: user %q reads %v; want %q %v", c.name, h.username, reads, c.wantUser, c.wantReads)
		}
		if c.wantNote != "" && (len(rs.notes) != 1 || !strings.Contains(rs.notes[0], c.wantNote)) {
			t.Errorf("%s: notes %v", c.name, rs.notes)
		}
		// What the builder is told when it asks.
		a := newSessionAuth(hosts)
		cr, _ := a.Credentials(context.Background(), &auth.CredentialsRequest{Host: "harbor.example.com"})
		if (c.wantUser == "") != (cr.Username == "" && cr.Secret == "") {
			t.Errorf("%s: the builder is told %q", c.name, cr.Username)
		}
	}
}

// Credentials are never sent to a token service over plain HTTP, unless
// the registry is marked insecure.
func TestTokenRealmMustBeHTTPS(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"t0k","access_token":"t0k","expires_in":60}`))
	}))
	defer srv.Close()
	req := &auth.FetchTokenRequest{Host: "reg.example.com", Realm: srv.URL + "/token", Service: "reg", Scopes: []string{"repository:a:pull"}}
	a := newSessionAuth(map[string]hostAuth{"reg.example.com": {username: "u", secret: "p"}})
	if _, err := a.FetchToken(context.Background(), req); err == nil || !strings.Contains(err.Error(), "not https") || got != "" {
		t.Fatalf("plain HTTP realm: %v (request made: %q)", err, got)
	}
	a = newSessionAuth(map[string]hostAuth{"reg.example.com": {username: "u", secret: "p", insecure: true}})
	if tok, err := a.FetchToken(context.Background(), req); err != nil || tok.Token != "t0k" {
		t.Fatalf("insecure registry: %v %+v", err, tok)
	}
	// Anonymous requests carry nothing to protect.
	a = newSessionAuth(nil)
	if _, err := a.FetchToken(context.Background(), req); err != nil {
		t.Fatalf("anonymous: %v", err)
	}
}
