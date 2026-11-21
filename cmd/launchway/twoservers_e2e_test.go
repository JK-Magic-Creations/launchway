package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Two servers, as real processes, sharing a PostgreSQL database and
// nothing else: no shared directory, no object store. A step uploads a
// large artifact through server A (behind a proxy, as behind a load
// balancer); A is killed with SIGKILL in the middle of the upload; the
// proxy sends everything to B from then on. The runner carries the upload
// on at B, the next step fetches the artifact through B and checks it, and
// a browser download through B gives the same bytes.
//
// It needs PostgreSQL (LAUNCHWAY_TEST_POSTGRES) and git, and builds the
// launchway binary.
func TestTwoServersShareArtifactsThroughTheDatabase(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set LAUNCHWAY_TEST_POSTGRES")
	}
	if testing.Short() {
		t.Skip("builds the binary and moves a few hundred MB")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "launchway")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dbURL := testdb.New(t)
	// The servers share one database, so they share its root key: a new
	// installation's first server would make one in its own data directory,
	// and the second, with another data directory, would refuse to start.
	rootKey := filepath.Join(dir, "root.key")
	if err := keyring.WriteKeyFile(rootKey); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAUNCHWAY_ROOT_KEY_FILE", rootKey)

	const size = 320 << 20 // big enough that the upload takes a while
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".launchway"), 0o755)
	os.WriteFile(filepath.Join(repo, ".launchway", "pipeline.yaml"), []byte(fmt.Sprintf(`
pipeline: twoservers
steps:
  build:
    image: none
    run: |
      head -c %d /dev/urandom > big.bin
      sha256sum big.bin | cut -d' ' -f1 > big.sum
    artifacts:
      - {name: out, paths: [big.bin, big.sum]}
  check:
    image: none
    needs: build
    fetch:
      - {from: build, name: out, into: got}
    run: |
      test "$(sha256sum got/big.bin | cut -d' ' -f1)" = "$(cat got/big.sum)"
      echo "fetched through the other server, and it matches"
`, size)), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com",
			"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	type server struct {
		url  string
		cmd  *exec.Cmd
		log  *os.File
		data string
	}
	start := func(name string) *server {
		port := freePort(t)
		data := filepath.Join(dir, name)
		os.MkdirAll(data, 0o755)
		cfg := filepath.Join(dir, name+".yaml")
		os.WriteFile(cfg, []byte(fmt.Sprintf(`listen: 127.0.0.1:%d
data: %s
database: %q
runner: { enabled: false }
egress: { allow_local: true } # the project's repository is a path on this machine
projects:
  - { name: demo, repo: %s, branch: main, poll: 1h }
`, port, data, dbURL, repo)), 0o644)
		logf, _ := os.Create(filepath.Join(dir, name+".log"))
		cmd := exec.Command(bin, "server", "--config", cfg)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		s := &server{url: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd, log: logf, data: data}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); logf.Close() })
		waitReady(t, s.url, logf.Name())
		return s
	}
	a := start("a")
	b := start("b")
	for _, s := range []*server{a, b} {
		var st struct {
			Store     string `json:"store"`
			Available bool   `json:"available"`
		}
		getJSONFrom(t, s.url+"/api/v1/artifact-store", &st)
		if st.Store != "database" || !st.Available {
			t.Fatalf("%s keeps artifacts in %q (available %v); want the database\n%s", s.url, st.Store, st.Available, readFile(s.log.Name()))
		}
	}

	// The load balancer: A until it dies, then B.
	var target atomic.Pointer[url.URL]
	au, _ := url.Parse(a.url)
	bu, _ := url.Parse(b.url)
	target.Store(au)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target.Load())
			r.Out.Host = r.In.Host
		},
		FlushInterval: -1,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lb := &http.Server{Handler: proxy}
	go lb.Serve(ln)
	t.Cleanup(func() { lb.Close() })
	lbURL := "http://" + ln.Addr().String()

	token, err := os.ReadFile(filepath.Join(a.data, "runner-token"))
	if err != nil {
		t.Fatal(err)
	}
	runLog, _ := os.Create(filepath.Join(dir, "runner.log"))
	runner := exec.Command(bin, "runner", "--server", lbURL, "--token", strings.TrimSpace(string(token)), "--name", "r1",
		"--data", filepath.Join(dir, "runner"), "--executor", "host")
	runner.Stdout, runner.Stderr = runLog, runLog
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runner.Process.Kill(); runner.Wait(); runLog.Close() })

	var run struct{ ID string }
	postJSON(t, a.url+"/api/v1/projects/demo/runs", `{"ref":"main"}`, &run)
	if run.ID == "" {
		t.Fatal("no run started")
	}

	// Wait until the upload is part-way, then kill A.
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(3 * time.Minute)
	var received int64
	for {
		db.QueryRow(`SELECT COALESCE(MAX(received), 0) FROM artifact_blobs WHERE size = $1`, int64(size)).Scan(&received)
		if received >= 3*8<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upload did not get going (received %d)\nrunner:\n%s\nA:\n%s", received, readFile(runLog.Name()), readFile(a.log.Name()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if received >= size {
		t.Fatalf("the upload finished before server A could be killed; make the artifact bigger")
	}
	uploadStart := time.Now()
	a.cmd.Process.Signal(syscall.SIGKILL)
	a.cmd.Wait()
	target.Store(bu)
	t.Logf("killed server A with %d of %d bytes received", received, size)

	// The run finishes through B.
	var detail struct {
		Status   string `json:"status"`
		StepList []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"step_list"`
	}
	deadline = time.Now().Add(5 * time.Minute)
	for {
		getJSONFrom(t, b.url+"/api/v1/runs/"+run.ID, &detail)
		if detail.Status == "succeeded" || detail.Status == "failed" || detail.Status == "canceled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run still %s\nrunner:\n%s\nB:\n%s", detail.Status, readFile(runLog.Name()), readFile(b.log.Name()))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if detail.Status != "succeeded" {
		t.Fatalf("run %s: %+v\nrunner:\n%s\nB:\n%s", detail.Status, detail.StepList, readFile(runLog.Name()), readFile(b.log.Name()))
	}
	t.Logf("the run finished through server B %s after A was killed", time.Since(uploadStart).Round(time.Millisecond))
	logs := readFile(runLog.Name())
	if !strings.Contains(logs, "matches") {
		// The step's own output goes to the server, not the runner's log;
		// read it from B.
		resp, err := http.Get(b.url + "/api/v1/runs/" + run.ID + "/steps/check/logs")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			logs = string(body)
		}
	}
	if !strings.Contains(logs, "fetched through the other server, and it matches") {
		t.Fatalf("the check step's log does not say it matched:\n%s", logs)
	}

	// The browser's download, through B.
	var arts struct {
		Artifacts []struct {
			ID    string `json:"id"`
			Files []struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"files"`
		} `json:"artifacts"`
	}
	getJSONFrom(t, b.url+"/api/v1/runs/"+run.ID+"/artifacts", &arts)
	if len(arts.Artifacts) != 1 {
		t.Fatalf("artifacts: %+v", arts)
	}
	for _, f := range arts.Artifacts[0].Files {
		if f.Path != "big.bin" {
			continue
		}
		downloadStart := time.Now()
		resp, err := http.Get(b.url + "/api/v1/runs/" + run.ID + "/artifacts/" + arts.Artifacts[0].ID + "/file?path=" + url.QueryEscape(f.Path))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		n, err := io.Copy(h, resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || n != size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			t.Fatalf("download through B: status %d, %d bytes, %v", resp.StatusCode, n, err)
		}
		took := time.Since(downloadStart)
		t.Logf("downloaded %d MB through server B at %.0f MB/s, SHA-256 matches", n>>20, float64(n)/1e6/took.Seconds())
	}

	// Nothing of the interrupted part is visible, and the store's figures
	// add up: one big object (and the marker, and big.sum).
	var objects int
	db.QueryRow(`SELECT COUNT(*) FROM blob_objects WHERE key LIKE 'artifacts/%'`).Scan(&objects)
	if objects != 2 {
		t.Fatalf("%d artifact objects in the database store, want 2", objects)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitReady(t *testing.T, base, logName string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not become ready:\n%s", base, readFile(logName))
}

func readFile(name string) string {
	b, _ := os.ReadFile(name)
	if len(b) > 8000 {
		b = b[len(b)-8000:]
	}
	return string(b)
}

func getJSONFrom(t *testing.T, u string, v any) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", u, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
}

func postJSON(t *testing.T, u, body string, v any) {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Launchway-CSRF", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s: %d %s", u, resp.StatusCode, out)
	}
	if err := json.Unmarshal(out, v); err != nil {
		t.Fatalf("POST %s: %v: %s", u, err, out)
	}
}
