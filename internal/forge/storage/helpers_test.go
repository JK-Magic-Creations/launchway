package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

var ident = []string{"GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com"}

// world is a database, a blob store and any number of server processes.
type world struct {
	t     testing.TB
	root  string
	db    string
	blobs string // "fs:<dir>" or "db"
	repo  string
	eng   *engine.Engine
	st    *Store // an in-process store on the same database and blobs, for the refs of record and maintenance
	envs  []serverEnv
}

func newWorld(t testing.TB, blobs string, pg bool) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, root: root, repo: "rep1"}
	w.db = filepath.Join(root, "launchway.db")
	if pg {
		w.db = testdb.New(t)
	}
	w.blobs = blobs
	if blobs == "fs" {
		w.blobs = "fs:" + filepath.Join(root, "blobs")
	}
	e := serverEnv{DB: w.db, Blobs: w.blobs, Cache: filepath.Join(root, "inproc"), Name: "inproc"}
	st, eng, err := openStore(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	w.eng, w.st = eng, st
	if err := st.Create(context.Background(), w.repo); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) server(name string, mutate ...func(*serverEnv)) *proc {
	e := serverEnv{DB: w.db, Blobs: w.blobs, Cache: filepath.Join(w.root, "servers", name), Name: name}
	for _, m := range mutate {
		m(&e)
	}
	w.envs = append(w.envs, e)
	return startServer(w.t, e)
}

// client is a working repository.
type client struct {
	t    testing.TB
	dir  string
	seed int64
}

func newClient(t testing.TB) *client {
	t.Helper()
	c := &client{t: t, dir: filepath.Join(t.TempDir(), "w")}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c.git("init", "-q", "-b", "main")
	return c
}

func (c *client) run(env []string, args ...string) (string, error) {
	res, err := gitexec.Run(context.Background(), gitexec.Spec{Dir: c.dir, Args: args, Env: append(append([]string{}, ident...), env...), Timeout: 90 * time.Second, IdleTimeout: 45 * time.Second})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return ee.Output(), err
		}
		return err.Error(), err
	}
	return strings.TrimSpace(res.Stdout + res.Stderr), nil
}

func (c *client) git(args ...string) string {
	c.t.Helper()
	out, err := c.run(nil, args...)
	if err != nil {
		c.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// commit adds a file of random (incompressible) content and commits it.
func (c *client) commit(size int) string {
	c.t.Helper()
	c.seed++
	buf := make([]byte, size)
	rand.New(rand.NewSource(c.seed)).Read(buf)
	if err := os.WriteFile(filepath.Join(c.dir, fmt.Sprintf("f%d.bin", c.seed)), buf, 0o644); err != nil {
		c.t.Fatal(err)
	}
	c.git("add", "-A")
	c.git("commit", "-q", "-m", fmt.Sprintf("change %d", c.seed))
	return c.git("rev-parse", "HEAD")
}

func (c *client) head() string { return c.git("rev-parse", "HEAD") }

// pushResult is what a push came to as the client saw it.
type pushResult struct {
	Acked  bool
	Output string
}

// push pushes refspecs to a server, with a fault named in a header.
func (c *client) push(p *proc, repo string, f testFault, refspecs ...string) pushResult {
	env := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=X-Test-Fault: " + f.String()}
	args := append([]string{"push", "--atomic", p.repoURL(repo)}, refspecs...)
	out, err := c.run(env, args...)
	return pushResult{Acked: err == nil, Output: out}
}

// cloneMirror makes a mirror clone from a server into a new directory.
func cloneMirror(t testing.TB, p *proc, repo string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "clone.git")
	res, err := gitexec.Run(context.Background(), gitexec.Spec{Args: []string{"clone", "-q", "--mirror", p.repoURL(repo), dest}, Timeout: 10 * time.Minute})
	if err != nil {
		t.Fatalf("clone from %s: %v %s", p.URL, err, res.Stderr)
	}
	return dest
}

func gitOut(t testing.TB, dir string, args ...string) string {
	t.Helper()
	res, err := gitexec.Run(context.Background(), gitexec.Spec{GitDir: dir, Args: args, Timeout: 10 * time.Minute})
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(res.Stdout)
}

func refsOf(t testing.TB, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, line := range strings.Split(gitOut(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if n, v, ok := strings.Cut(line, " "); ok {
			m[n] = v
		}
	}
	return m
}

// fingerprint identifies the refs and every reachable object of a repository.
func fingerprint(t testing.TB, dir string) string {
	t.Helper()
	refs := gitOut(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
	objs := strings.Split(gitOut(t, dir, "rev-list", "--all", "--objects"), "\n")
	sort.Strings(objs)
	sum := sha256.Sum256([]byte(refs + "\n--\n" + strings.Join(objs, "\n")))
	return hex.EncodeToString(sum[:])
}

type ackedPush struct{ ref, sha string }

// verify checks what the design promises, from a server that holds nothing:
// no acknowledged push lost, no ref at missing objects, the refs a fresh
// server serves are the refs of record.
func (w *world) verify(t testing.TB, ack []ackedPush) {
	t.Helper()
	ctx := context.Background()
	auth, err := w.st.Refs(ctx, w.repo)
	if err != nil {
		t.Fatalf("refs of record: %v", err)
	}
	cold := w.server(fmt.Sprintf("cold-%d", time.Now().UnixNano()))
	dest := cloneMirror(t, cold, w.repo)
	if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"fsck", "--full", "--strict", "--no-dangling"}, Timeout: 10 * time.Minute}); err != nil {
		t.Fatalf("NO REF MAY POINT AT MISSING OBJECTS: fsck of a fresh clone: %v", err)
	}
	got := refsOf(t, dest)
	for n, sha := range auth {
		if got[n] != sha {
			t.Errorf("a cold server serves %s at %s, the refs of record say %s", n, short(got[n]), short(sha))
		}
	}
	for n := range got {
		if _, ok := auth[n]; !ok {
			t.Errorf("a cold server serves %s, which the refs of record lack", n)
		}
	}
	for _, a := range ack {
		cur, ok := auth[a.ref]
		if !ok {
			t.Errorf("ACKNOWLEDGED PUSH LOST: %s (pushed %s) does not exist", a.ref, short(a.sha))
			continue
		}
		if cur != a.sha {
			if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"merge-base", "--is-ancestor", a.sha, cur}}); err != nil {
				t.Errorf("ACKNOWLEDGED PUSH LOST: %s is at %s, which does not contain the acknowledged %s", a.ref, short(cur), short(a.sha))
			}
		}
	}
	cold.Kill()
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func newRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }
