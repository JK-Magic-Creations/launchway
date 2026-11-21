package storage

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// The tests run real servers: this test binary, started again with
// STORAGE_TEST_SERVER set, serves a minimal smart-HTTP front on a Store, in a
// process of its own, so that "kill -9 the server" is a real thing a test can
// do. The git client is the real git over HTTP.
const envServer = "STORAGE_TEST_SERVER"

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	gitexec.Reexec() // answers git's hooks, which are symlinks to this binary
	if os.Getenv(envServer) != "" {
		runTestServer()
		return
	}
	if testing.CoverMode() != "" && os.Getenv("ST_HOOKBIN") == "" {
		// Git's hooks are this binary, started by git. A coverage-instrumented
		// binary hangs when it is started that way (seen: every push stalls),
		// so under -cover the hooks are the real, uninstrumented launchway.
		dir, err := os.MkdirTemp("", "lw-hookbin-")
		if err == nil {
			bin := filepath.Join(dir, "launchway")
			if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "github.com/jk-magic-creations/launchway/cmd/launchway").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "building the hook binary: %v\n%s", err, out)
				os.Exit(1)
			}
			os.Setenv("ST_HOOKBIN", bin)
			defer os.RemoveAll(dir)
		}
	}
	code := m.Run()
	os.Exit(code)
}

type serverEnv struct {
	DB       string
	Blobs    string // "fs:<dir>" or "db"
	Cache    string
	Name     string
	NoVerify bool
	MaxRepo  int64
	MaxPack  int64
}

func (e serverEnv) environ() []string {
	return append(os.Environ(), envServer+"=1", "ST_DB="+e.DB, "ST_BLOBS="+e.Blobs, "ST_CACHE="+e.Cache, "ST_NAME="+e.Name,
		fmt.Sprintf("ST_NOVERIFY=%v", e.NoVerify), "ST_MAXREPO="+strconv.FormatInt(e.MaxRepo, 10), "ST_MAXPACK="+strconv.FormatInt(e.MaxPack, 10))
}

func openStore(ctx context.Context, e serverEnv) (*Store, *engine.Engine, error) {
	eng, err := engine.Open(engine.Options{Database: e.DB, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return nil, nil, err
	}
	var blobs blobstore.Store
	if strings.HasPrefix(e.Blobs, "fs:") {
		fs, err := blobstore.NewFilesystem(strings.TrimPrefix(e.Blobs, "fs:"))
		if err != nil {
			return nil, nil, err
		}
		blobs = fs
	} else {
		db, err := blobstore.NewDatabase(eng.Database().SQL(), eng.Database().Postgres(), blobstore.DatabaseOptions{WriteLock: eng.Database().WriteLock})
		if err != nil {
			return nil, nil, err
		}
		blobs = db
	}
	st, err := New(ctx, Options{DB: eng.Database(), Blobs: &faultyBlobs{Store: blobs}, CacheDir: e.Cache, Server: e.Name,
		Binary: os.Getenv("ST_HOOKBIN"), NoVerifyUpload: e.NoVerify, MaxRepoBytes: e.MaxRepo, MaxPackBytes: e.MaxPack})
	return st, eng, err
}

// ---------------------------------------------------------------- fault injection

type testFault struct {
	Point, Action, Arg string
}

type testFaultKey struct{}

func parseTestFault(s string) testFault {
	if s == "" {
		return testFault{}
	}
	pt, act, _ := strings.Cut(s, "=")
	name, arg, _ := strings.Cut(act, ":")
	return testFault{pt, name, arg}
}

func (f testFault) String() string {
	if f.Point == "" {
		return ""
	}
	s := f.Point + "=" + f.Action
	if f.Arg != "" {
		s += ":" + f.Arg
	}
	return s
}

// Hit implements Faults: it fails, or ends the server process, at the point.
func (f testFault) Hit(point string) error {
	if f.Point != point {
		return nil
	}
	switch f.Action {
	case "kill": // kill -9 the server: nothing of it runs on
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	case "killgit": // kill -9 git, as an OOM killer would, leaving its lock files
		killChildren()
		time.Sleep(200 * time.Millisecond)
	case "error":
		return injected(point)
	}
	return nil
}

// Drop implements Faults: the database connection is lost.
func (f testFault) Drop(when string) error {
	if f.Point == "db" && f.Action == "drop"+when {
		return fmt.Errorf("%w: the database connection was lost (%s the commit)", ErrInjected, when)
	}
	return nil
}

func killChildren() {
	ents, _ := os.ReadDir("/proc")
	me := os.Getpid()
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(data)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+2:])
		if len(f) > 1 {
			if ppid, _ := strconv.Atoi(f[1]); ppid == me {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
}

// faultyBlobs fails the blob store as the request's fault says.
type faultyBlobs struct{ blobstore.Store }

func fault(ctx context.Context) testFault { f, _ := ctx.Value(testFaultKey{}).(testFault); return f }

func (b *faultyBlobs) gate(ctx context.Context) error {
	f := fault(ctx)
	if f.Point == "blob" && f.Action == "down" {
		return fmt.Errorf("%w: the blob store is unavailable", ErrInjected)
	}
	if f.Point == "blob" && f.Action == "slow" {
		d, _ := time.ParseDuration(f.Arg)
		time.Sleep(d)
	}
	return nil
}

func (b *faultyBlobs) CreateUpload(ctx context.Context, key string) (string, error) {
	if err := b.gate(ctx); err != nil {
		return "", err
	}
	return b.Store.CreateUpload(ctx, key)
}

func (b *faultyBlobs) UploadPart(ctx context.Context, key, id string, n int, data []byte) (string, error) {
	if err := b.gate(ctx); err != nil {
		return "", err
	}
	f := fault(ctx)
	if f.Point == "upload:part" && f.Action == "error" {
		return "", fmt.Errorf("%w: part %d", ErrInjected, n)
	}
	if f.Point == "upload:data" && f.Action == "flip" && n == 1 && len(data) > 0 && strings.Contains(key, "/packs/") {
		data = append([]byte(nil), data...)
		data[len(data)/2] ^= 0xff
	}
	if f.Point == "upload:data" && f.Action == "truncate" && strings.Contains(key, "/packs/") {
		data = data[:len(data)/2]
	}
	return b.Store.UploadPart(ctx, key, id, n, data)
}

func (b *faultyBlobs) Open(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := b.gate(ctx); err != nil {
		return nil, err
	}
	return b.Store.Open(ctx, key, off, n)
}

func (b *faultyBlobs) Stat(ctx context.Context, key string) (int64, error) {
	if err := b.gate(ctx); err != nil {
		return 0, err
	}
	return b.Store.Stat(ctx, key)
}

// ---------------------------------------------------------------- the server process

func runTestServer() {
	ctx := context.Background()
	e := serverEnv{DB: os.Getenv("ST_DB"), Blobs: os.Getenv("ST_BLOBS"), Cache: os.Getenv("ST_CACHE"), Name: os.Getenv("ST_NAME"),
		NoVerify: os.Getenv("ST_NOVERIFY") == "true"}
	e.MaxRepo, _ = strconv.ParseInt(os.Getenv("ST_MAXREPO"), 10, 64)
	e.MaxPack, _ = strconv.ParseInt(os.Getenv("ST_MAXPACK"), 10, 64)
	st, _, err := openStore(ctx, e)
	if err != nil {
		fmt.Fprintln(os.Stdout, "ERROR", err)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "ADDR http://"+ln.Addr().String())
	_ = http.Serve(ln, smartHTTP(st))
}

// smartHTTP is the smallest smart-HTTP front the tests need: the real one is
// story S34-12. Paths are /<repo>.git/info/refs and /<repo>.git/git-<service>.
func smartHTTP(st *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		f := parseTestFault(r.Header.Get("X-Test-Fault"))
		ctx = context.WithValue(WithFaults(ctx, f), testFaultKey{}, f)
		path := strings.TrimPrefix(r.URL.Path, "/")
		repo, rest, ok := strings.Cut(path, ".git/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		var in io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			in = gz
		}
		rpc := RPC{In: in, Out: w, GitProtocol: r.Header.Get("Git-Protocol")}
		var svc string
		switch {
		case rest == "info/refs":
			svc = r.URL.Query().Get("service")
			rpc.Advertise = true
			w.Header().Set("Content-Type", "application/x-"+svc+"-advertisement")
			if !strings.Contains(rpc.GitProtocol, "version=2") {
				pkt := "# service=" + svc + "\n"
				fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)
			}
		case strings.HasPrefix(rest, "git-"):
			svc = rest
			w.Header().Set("Content-Type", "application/x-"+svc+"-result")
		default:
			http.NotFound(w, r)
			return
		}
		var err error
		switch svc {
		case "git-receive-pack":
			_, err = st.Receive(ctx, repo, rpc)
		case "git-upload-pack":
			err = st.Upload(ctx, repo, rpc)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "server %s: %v\n", repo, err)
		}
	})
}

// ---------------------------------------------------------------- the test side

// proc is a server process.
type proc struct {
	t    testing.TB
	cmd  *exec.Cmd
	URL  string
	env  serverEnv
	done chan struct{}
}

func startServer(t testing.TB, e serverEnv) *proc {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = e.environ()
	if testing.CoverMode() != "" {
		// A coverage-instrumented server writes its counters when it exits.
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+t.TempDir())
	}
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, env: e, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "ADDR ") {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatalf("the server did not start: %q %v", line, err)
	}
	p.URL = strings.TrimSpace(strings.TrimPrefix(line, "ADDR "))
	t.Cleanup(p.Kill)
	return p
}

// Kill is kill -9 of the server and everything it started.
func (p *proc) Kill() {
	killTree(p.cmd.Process.Pid)
	<-p.done
}

// killTree kills a process, its group, and every descendant (git processes of
// their own groups included), so nothing a test started outlives it.
func killTree(pid int) {
	kids := map[int][]int{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		n, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(data)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			if f := strings.Fields(s[i+2:]); len(f) > 1 {
				pp, _ := strconv.Atoi(f[1])
				kids[pp] = append(kids[pp], n)
			}
		}
	}
	var walk func(int)
	walk = func(p int) {
		for _, k := range kids[p] {
			walk(k)
		}
		_ = syscall.Kill(-p, syscall.SIGKILL)
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	walk(pid)
}

// Restart starts the same server again on the same cache.
func (p *proc) Restart() *proc { p.Kill(); return startServer(p.t, p.env) }

func (p *proc) repoURL(repo string) string { return p.URL + "/" + repo + ".git" }

var _ = filepath.Join
