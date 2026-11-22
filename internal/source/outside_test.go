package source

import (
	"context"
	"encoding/base64"
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

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type fakeOutside struct {
	cred OutsideCredential
	on   bool
	err  error
	pr   func(n int) (PullRequestInfo, bool, error)
}

func (f fakeOutside) PullRequest(_ context.Context, _ string, n int) (PullRequestInfo, bool, error) {
	if f.pr == nil {
		return PullRequestInfo{}, false, nil
	}
	return f.pr(n)
}

func (f fakeOutside) Credential(context.Context, string) (OutsideCredential, bool, error) {
	return f.cred, f.on, f.err
}

// privateHost serves a bare repository over smart HTTP, but only to a caller
// that sends the right Authorization header: a private repository.
func privateHost(t *testing.T, repo, user, secret string) (*httptest.Server, *int) {
	t.Helper()
	backend, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	out, _ := exec.Command(backend, "--exec-path").Output()
	bin := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no git-http-backend")
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
	denied := 0
	h := &cgi.Handler{Path: bin, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(repo), "GIT_HTTP_EXPORT_ALL=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			denied++
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &denied
}

func TestPrivateOutsideRepositoryPollsWithTheConnectionsCredential(t *testing.T) {
	root := t.TempDir()
	work, bare := filepath.Join(root, "work"), filepath.Join(root, "srv", "app.git")
	os.MkdirAll(work, 0o755)
	run(t, work, "git", "init", "-q", "-b", "main")
	commit(t, work, pl, "private change")
	run(t, root, "git", "clone", "-q", "--bare", work, bare)
	srv, denied := privateHost(t, bare, "x-access-token", "s3cret-token")

	eng, _ := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer eng.Close()
	data := filepath.Join(root, "data")
	m, _ := NewManager(eng, data, []Project{{Name: "app", URL: "https://elsewhere.invalid/acme/app"}}, nil)

	// Without a credential the mirror cannot be made (the address is not even reachable).
	m.SetOutsideSource(fakeOutside{on: true, cred: OutsideCredential{URL: srv.URL + "/app.git", Username: "x-access-token", Secret: "wrong"}})
	if _, err := m.Refs(context.Background(), "app"); err == nil {
		t.Fatal("a wrong credential fetched a private repository")
	} else if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("the credential is in the error: %v", err)
	}
	if *denied == 0 {
		t.Fatal("the host was never asked")
	}

	m.SetOutsideSource(fakeOutside{on: true, cred: OutsideCredential{URL: srv.URL + "/app.git", Username: "x-access-token", Secret: "s3cret-token"}})
	refs, err := m.Refs(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 || refs[0].Subject != "private change" {
		t.Fatalf("refs = %+v", refs)
	}
	mirror := filepath.Join(data, "repos", "app.git")
	// The pipeline file is read from the mirror.
	if out, err := gitOut(context.Background(), mirror, "show", "main:.launchway/pipeline.yaml"); err != nil || !strings.Contains(out, "pipeline: app") {
		t.Fatalf("pipeline file: %q %v", out, err)
	}
	// Neither the mirror's configuration nor its address holds the credential.
	cfg, _ := os.ReadFile(filepath.Join(mirror, "config"))
	if strings.Contains(string(cfg), "s3cret") || strings.Contains(string(cfg), "extraheader") {
		t.Fatalf("the credential reached the mirror's configuration:\n%s", cfg)
	}
	// A new push is seen by the next fetch.
	commit(t, work, pl, "second")
	run(t, work, "git", "push", "-q", bare, "main")
	if err := m.cat.Load().projects["app"].fetch(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	refs, _ = m.Refs(context.Background(), "app")
	if refs[0].Subject != "second" {
		t.Fatalf("not followed: %+v", refs)
	}
}

func TestOutsideEnvRefusesOddAddressesAndKeepsTheHeaderToTheOrigin(t *testing.T) {
	for _, bad := range []string{"ssh://git@h/x.git", "https://u:p@h/x.git", "file:///tmp/x", "/tmp/x"} {
		if _, err := outsideEnv(OutsideCredential{URL: bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	env, err := outsideEnv(OutsideCredential{URL: "https://git.example.com/a/b.git", Username: "u", Secret: "p"})
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(env, "\n")
	for _, w := range []string{"GIT_ALLOW_PROTOCOL=http:https", "http.followRedirects", "http.https://git.example.com/.extraHeader"} {
		if !strings.Contains(j, w) {
			t.Errorf("missing %s in %s", w, j)
		}
	}
}

func TestAnOutsideSourceThatFailsStopsTheFetchInsteadOfFallingBack(t *testing.T) {
	eng, _ := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer eng.Close()
	m, _ := NewManager(eng, t.TempDir(), []Project{{Name: "app", URL: "https://elsewhere.invalid/a/b"}}, nil)
	m.SetOutsideSource(fakeOutside{err: io.ErrUnexpectedEOF})
	if _, err := m.Refs(context.Background(), "app"); err == nil {
		t.Fatal("fetched with no credential")
	}
}
