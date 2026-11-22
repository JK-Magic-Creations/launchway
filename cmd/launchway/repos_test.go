package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// hostedCLI is a server with Git hosting on (allowing local files as mirror
// sources) and a client for each of a few people.
type hostedCLI struct {
	t      *testing.T
	h      *hosting.Hosting
	srv    *httptest.Server
	client func(user, group string, out *bytes.Buffer) *client
}

func newHostedCLI(t *testing.T) *hostedCLI {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := auth.NewPolicy([]auth.Binding{{Group: "ops", Role: auth.Operator}, {Group: "root", Role: auth.Admin}})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := egress.New([]string{"127.0.0.1"})
	guard.AllowFiles = true
	h, err := hosting.New(t.Context(), hosting.Options{Engine: eng, Blobs: blobs, Data: t.TempDir(), Server: "cli-test", Log: quiet,
		Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r", Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour},
		Forge: &api.ForgeHosting{Provider: h.Provider, Mirrors: h.Mirrors, RunTokens: h.RunTokens, Pulls: h.Pulls, Trash: h, Aliases: h, Rules: h.Rules, Git: githttp.New(githttp.Options{Backend: h.Store, Log: quiet})}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	hc := &hostedCLI{t: t, h: h, srv: srv}
	hc.client = func(user, group string, out *bytes.Buffer) *client {
		if err := eng.SignIn(user, []string{group}, engine.Profile{}, "session-"+user, now, now+3600_000, 0); err != nil {
			t.Fatal(err)
		}
		tok := "lw_" + "clirp" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	return hc
}

func (hc *hostedCLI) repo(path string) {
	hc.t.Helper()
	owner, name, _ := strings.Cut(path, "/")
	if _, err := hc.h.Provider.Repositories().Create(hc.t.Context(), forge.NewRepository{Home: owner, Name: name}); err != nil {
		hc.t.Fatal(err)
	}
}

// bareWith makes a bare repository with one commit on main and a tag.
func bareWith(t *testing.T) (bare, work string) {
	t.Helper()
	root := t.TempDir()
	bare, work = filepath.Join(root, "up.git"), filepath.Join(root, "w")
	env := testlib.GitEnv(t, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.x")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "--bare", "-b", "main", bare)
	run(root, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "one")
	run(work, "tag", "v1")
	run(work, "push", "-q", bare, "main", "v1")
	return bare, work
}

func TestCLIReposMirror(t *testing.T) {
	hc := newHostedCLI(t)
	hc.repo("acme/app")
	bare, _ := bareWith(t)
	var oo, ao bytes.Buffer
	otto, ada := hc.client("otto", "ops", &oo), hc.client("ada", "root", &ao)

	// An operator may not set a mirror up; an admin may, and the secret is never an argument.
	if err := otto.reposMirror([]string{"add", "acme/app", "--url", "file://" + bare}); err == nil {
		t.Fatal("an operator set up a mirror")
	}
	if err := ada.reposMirror([]string{"add", "acme/app", "--url", "https://u:pw@git.example.com/x.git"}); err == nil || strings.Contains(err.Error(), "pw@") {
		t.Fatalf("credentials in the address: %v", err)
	}
	if err := ada.reposMirror([]string{"add", "acme/app", "--url", "file://" + bare, "--interval", "10m"}); err != nil || !strings.Contains(ao.String(), "now a pull mirror") {
		t.Fatalf("add: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.reposMirror([]string{"status", "acme/app"}); err != nil || !strings.Contains(ao.String(), "acme/app") || !strings.Contains(ao.String(), "pending") {
		t.Fatalf("status: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := otto.reposMirror([]string{"sync", "acme/app"}); err != nil {
		t.Fatalf("an operator runs the mirror: %v", err)
	}
	if err := otto.reposMirror([]string{"status", "--json"}); err != nil || !strings.Contains(oo.String(), `"state": "ok"`) || !strings.Contains(oo.String(), `"refs": 2`) {
		t.Fatalf("status after a sync: %v %q", err, oo.String())
	}
	// Stopping: an operator cannot; an admin does, with a final sync.
	if err := otto.reposMirror([]string{"stop", "acme/app"}); err == nil {
		t.Fatal("an operator stopped mirroring")
	}
	if err := ada.reposMirror([]string{"stop", "acme/app", "--final-sync"}); err != nil || !strings.Contains(ao.String(), "no longer a mirror") {
		t.Fatalf("stop: %v %q", err, ao.String())
	}
	if err := ada.reposMirror([]string{"stop", "acme/app"}); err == nil {
		t.Fatal("stopped twice")
	}
	if err := ada.reposMirror([]string{"add", "nonsense", "--url", "https://git.example.com/x"}); err == nil || !strings.Contains(err.Error(), "OWNER/NAME") {
		t.Fatalf("a bad repository name: %v", err)
	}
}

// otherServer serves the bare repositories under root with git's http-backend.
func otherServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	gp, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	h := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	t.Cleanup(srv.Close)
	return srv
}

// import --dry-run lists and changes nothing; import copies each repository
// and says so; verify proves equality and exits 1 when it cannot.
func TestCLIReposImportAndVerify(t *testing.T) {
	hc := newHostedCLI(t)
	root := t.TempDir()
	for _, n := range []string{"shop", "blog"} {
		bare, _ := bareWith(t)
		if err := os.MkdirAll(filepath.Join(root, "old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(bare, filepath.Join(root, "old", n+".git")); err != nil {
			t.Fatal(err)
		}
	}
	other := otherServer(t, root)
	var oo, ao bytes.Buffer
	otto, ada := hc.client("otto", "ops", &oo), hc.client("ada", "root", &ao)
	urls := []string{"--url", other.URL + "/old/shop.git", "--url", other.URL + "/old/blog.git"}
	if err := otto.reposImport(append([]string{"--dry-run"}, urls...)); err == nil {
		t.Fatal("an operator imported")
	}
	if err := ada.reposImport(append([]string{"--dry-run"}, urls...)); err != nil || !strings.Contains(ao.String(), "old/blog") || !strings.Contains(ao.String(), "Nothing was changed") {
		t.Fatalf("dry run: %v %q", err, ao.String())
	}
	if page, _ := hc.h.Provider.Repositories().List(t.Context(), ""); len(page.Items) != 0 {
		t.Fatal("a dry run made repositories")
	}
	ao.Reset()
	if err := ada.reposImport(urls); err != nil || !strings.Contains(ao.String(), "verified  old/shop: 2 refs equal") || !strings.Contains(ao.String(), "2 verified") {
		t.Fatalf("import: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.reposImport(urls); err != nil || !strings.Contains(ao.String(), "2 verified") {
		t.Fatalf("import again: %v %q", err, ao.String())
	}
	if err := ada.reposConnection([]string{"add", "old", "--kind", "gitea", "--base-url", other.URL, "--credential-env", "OLD_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	ao.Reset()
	if err := ada.reposConnection([]string{"list"}); err != nil || !strings.Contains(ao.String(), "env:OLD_TOKEN") {
		t.Fatalf("%v %q", err, ao.String())
	}
	if err := ada.reposConnection([]string{"add", "bad", "--kind", "gitea", "--credential-env", "ghp_abc123"}); err == nil {
		t.Fatal("a token as the credential was accepted")
	}
}
