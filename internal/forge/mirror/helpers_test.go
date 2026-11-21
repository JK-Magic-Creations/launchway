package mirror_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

func TestMain(m *testing.M) {
	gitexec.Reexec()
	os.Exit(m.Run())
}

// gitEnv is a clean environment for the test's own git commands.
func gitEnv(t testing.TB) []string {
	return testlib.GitEnv(t, "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com")
}

func runGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// source is an external repository: a bare one that the test pushes to
// through a working clone, as someone using another Git server would.
type source struct {
	t    testing.TB
	bare string
	work string
	n    int
}

func newSource(t testing.TB) *source {
	t.Helper()
	root := t.TempDir()
	s := &source{t: t, bare: filepath.Join(root, "up.git"), work: filepath.Join(root, "work")}
	runGit(t, root, "init", "-q", "--bare", "-b", "main", s.bare)
	runGit(t, root, "init", "-q", "-b", "main", s.work)
	runGit(t, s.work, "remote", "add", "origin", s.bare)
	return s
}

func (s *source) url() string { return "file://" + s.bare }

func (s *source) git(args ...string) string { return runGit(s.t, s.work, args...) }

// commit adds a file and commits it on the current branch, returning the commit.
func (s *source) commit(msg string) string {
	s.n++
	if err := os.WriteFile(filepath.Join(s.work, fmt.Sprintf("f%d.txt", s.n)), []byte(strings.Repeat(msg+"\n", 20)), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", msg)
	return s.git("rev-parse", "HEAD")
}

func (s *source) pushAll() {
	s.git("push", "-q", "--force", "origin", "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*", "refs/notes/*:refs/notes/*")
}

// refs lists the branches, tags and notes of a repository, as "name sha".
func refsOf(t testing.TB, gitDir string) map[string]string {
	t.Helper()
	out := runGit(t, gitDir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/notes")
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if n, sha, ok := strings.Cut(l, " "); ok {
			m[n] = sha
		}
	}
	return m
}

func describe(m map[string]string) string {
	var ks []string
	for k, v := range m {
		ks = append(ks, k+" "+v[:8])
	}
	sort.Strings(ks)
	return strings.Join(ks, "; ")
}

// world is a hosting on a database and a filesystem blob store.
type world struct {
	t   testing.TB
	h   *hosting.Hosting
	eng *engine.Engine
}

type worldOpts struct {
	guard *egress.Guard
	mo    mirror.Options
	repo  int64 // MaxRepoBytes
}

func newWorld(t testing.TB, o worldOpts) *world {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if o.guard == nil {
		o.guard = &egress.Guard{AllowFiles: true}
	}
	h, err := hosting.New(context.Background(), hosting.Options{Engine: eng, Blobs: blobs, Data: t.TempDir(), Server: "t", BaseURL: "https://lw.example",
		Guard: o.guard, Mirror: o.mo, MaxRepoBytes: o.repo,
		Provider: launchway.Options{DefaultHome: "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, h: h, eng: eng}
}

func (w *world) newRepo(path string) forge.Repository {
	w.t.Helper()
	home, name, _ := strings.Cut(path, "/")
	r, err := w.h.Provider.Repositories().Create(context.Background(), forge.NewRepository{Home: home, Name: name})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// hostedRefs are the managed refs of a hosted repository, from the refs of record.
func (w *world) hostedRefs(r forge.Repository) map[string]string {
	w.t.Helper()
	all, err := w.h.Store.Refs(context.Background(), r.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	m := map[string]string{}
	for n, v := range all {
		if strings.HasPrefix(n, "refs/heads/") || strings.HasPrefix(n, "refs/tags/") || strings.HasPrefix(n, "refs/notes/") {
			m[n] = v
		}
	}
	return m
}

// fsck runs git's own check over the whole hosted repository, from the cache
// a server would serve it from.
func (w *world) fsck(r forge.Repository) {
	w.t.Helper()
	err := w.h.Store.WithCache(context.Background(), r.ID, func(dir string) error {
		out := runGit(w.t, dir, "fsck", "--strict", "--no-dangling")
		_ = out
		runGit(w.t, dir, "rev-list", "--objects", "--all")
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func equal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (w *world) mustSync(id string) mirror.Mirror {
	w.t.Helper()
	// A running scheduler may hold the mirror at this moment (that is the product's rule, a mirror
	// runs once at a time): wait until it lets go, then run it here.
	deadline := time.Now().Add(30 * time.Second)
	for {
		m, err := w.h.Mirrors.SyncNow(context.Background(), id)
		if errors.Is(err, mirror.ErrBusy) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if err != nil {
			w.t.Fatalf("sync: %v", err)
		}
		return m
	}
}

// gitServer serves bare repositories under root over HTTP with git's own
// http-backend, requiring Basic credentials when user is set: another Git
// server, on this machine.
func gitServer(t testing.TB, root, user, token string) *httptest.Server {
	t.Helper()
	exec, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(exec)), "git-http-backend")
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != user || p != token {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func eventually(t testing.TB, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
