// Package sourcetest starts real health-source backends in Podman or
// Docker for tests: Prometheus, Loki, ClickHouse, OpenSearch and
// PostgreSQL, and an exporter whose request counters a test drives. It is
// for tests only. Tests that use it should call Skip first: they run when
// LAUNCHWAY_TEST_CONTAINERS is set.
//
// Every container gets a unique name, is started with --rm and is removed
// when the test ends. Each backend listens on 127.0.0.1 on a free port.
// Prometheus runs on the host's network (--network host), so that it
// scrapes an Exporter, which is an ordinary listener on the host's
// 127.0.0.1, at the same address the test uses.
package sourcetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pinned images (multi-platform index digests).
const (
	PrometheusImage = "docker.io/prom/prometheus:v3.16.0@sha256:e04987641b57704f408e31f6373ba4138ecf27cb4a165d24aabb6c38fddd0f06"
	LokiImage       = "docker.io/grafana/loki:3.7.10@sha256:b43c257ccfc925d4a50b8717cac61a3b51df8c48d01847936057e5c4075a24a7"
	ClickHouseImage = "docker.io/clickhouse/clickhouse-server:26.10.7.24-alpine@sha256:ca552861ea45858b80178afdabe5efc638f4525a0eb5ce0ad1ede77b7b8c8567"
	OpenSearchImage = "docker.io/opensearchproject/opensearch:3.10.0@sha256:02d6d6e40c820ae08233b60540aa5d3d9364d64e2dc724e38d6c8c794879b2a0"
	PostgresImage   = "docker.io/library/postgres:17.12-alpine@sha256:526f86d93c6ea08fd3f1fe5605650cacc781b2c6013169a0ad8f42c7d3d84f32"
)

// ReadyTimeout is how long a backend may take to become ready. Machines
// that run tests are often busy.
var ReadyTimeout = 3 * time.Minute

// Skip skips the test unless container tests are asked for.
func Skip(t testing.TB) {
	t.Helper()
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
}

// Engine is podman or docker.
func Engine(t testing.TB) string {
	t.Helper()
	for _, e := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(e); err == nil {
			return p
		}
	}
	t.Fatal("neither podman nor docker is installed")
	return ""
}

func run(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.Command(Engine(t), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(Engine(t)), strings.Join(args, " "), err, out)
	}
	return string(out)
}

// RandHex returns n random bytes in hex, for names and passwords.
func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// FreePort returns a TCP port free on 127.0.0.1 a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var (
	mu         sync.Mutex
	containers = map[string]string{} // URL → container name
)

// start runs a container, removed when the test ends, and records it
// under the URL a test reaches it at.
func start(t testing.TB, kind, u string, args ...string) string {
	t.Helper()
	name := "launchway-test-" + kind + "-" + RandHex(4)
	run(t, append([]string{"run", "-d", "--rm", "--name", name}, args...)...)
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command(Engine(t), "logs", "--tail", "30", name).CombinedOutput()
			t.Logf("%s log:\n%s", name, out)
		}
		remove(t, name)
	})
	mu.Lock()
	containers[u] = name
	mu.Unlock()
	return name
}

// Stop stops the container serving u, to test a source that is down.
func Stop(t testing.TB, u string) {
	t.Helper()
	mu.Lock()
	name := containers[u]
	mu.Unlock()
	if name == "" {
		t.Fatalf("no container serves %s", u)
	}
	remove(t, name)
}

// remove removes a container by its name, without waiting long for it to
// stop.
func remove(t testing.TB, name string) {
	eng := Engine(t)
	args := []string{"rm", "-f", name}
	if filepath.Base(eng) == "podman" {
		args = []string{"rm", "-f", "-t", "1", name}
	}
	_ = exec.Command(eng, args...).Run()
}

// waitHTTP polls u until it answers with a status ok accepts.
func waitHTTP(t testing.TB, u string, header http.Header, ok func(int, []byte) bool) {
	t.Helper()
	deadline := time.Now().Add(ReadyTimeout)
	var last string
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		for k, v := range header {
			req.Header[k] = v
		}
		c := &http.Client{Timeout: 5 * time.Second}
		resp, err := c.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if ok(resp.StatusCode, body) {
				return
			}
			last = fmt.Sprintf("%d %s", resp.StatusCode, bytes.TrimSpace(body))
		} else {
			last = err.Error()
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s was not ready within %s: %s", u, ReadyTimeout, last)
}

func status200(code int, _ []byte) bool { return code == http.StatusOK }

// Prometheus starts Prometheus on the host's network, scraping the targets
// (host:port, such as an Exporter's Addr) every second as job "app", and
// returns its URL.
func Prometheus(t testing.TB, targets ...string) string {
	t.Helper()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	quoted := make([]string, len(targets))
	for i, tg := range targets {
		quoted[i] = strconv.Quote(tg)
	}
	cfg := fmt.Sprintf("global:\n  scrape_interval: 1s\n  evaluation_interval: 1s\nscrape_configs:\n  - job_name: app\n    scrape_interval: 1s\n    scrape_timeout: 900ms\n    static_configs:\n      - targets: [%s]\n",
		strings.Join(quoted, ", "))
	if err := os.WriteFile(filepath.Join(dir, "prometheus.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	port := FreePort(t)
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	start(t, "prometheus", u, "--network", "host", "-v", dir+":/cfg:ro,Z", PrometheusImage,
		"--config.file=/cfg/prometheus.yml", fmt.Sprintf("--web.listen-address=127.0.0.1:%d", port),
		"--storage.tsdb.path=/prometheus")
	waitHTTP(t, u+"/-/ready", nil, status200)
	return u
}

// Loki starts a single-process Loki without authentication and returns its
// URL.
func Loki(t testing.TB) string {
	t.Helper()
	port := FreePort(t)
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	start(t, "loki", u, "-p", fmt.Sprintf("127.0.0.1:%d:3100", port), LokiImage, "-config.file=/etc/loki/local-config.yaml")
	waitHTTP(t, u+"/ready", nil, status200)
	// Ready can come before the ingester accepts pushes; wait for one.
	deadline := time.Now().Add(ReadyTimeout)
	for {
		err := pushLoki(u, map[string]string{"launchway_probe": "1"}, []string{"probe"}, time.Now())
		if err == nil {
			return u
		}
		if time.Now().After(deadline) {
			t.Fatalf("loki at %s accepts no pushes: %v", u, err)
		}
		time.Sleep(time.Second)
	}
}

// PushLoki pushes lines to Loki as one stream with labels, the first at
// at and each next one a millisecond later.
func PushLoki(t testing.TB, u string, labels map[string]string, lines []string, at time.Time) {
	t.Helper()
	if err := pushLoki(u, labels, lines, at); err != nil {
		t.Fatal(err)
	}
}

func pushLoki(u string, labels map[string]string, lines []string, at time.Time) error {
	values := make([][2]string, len(lines))
	for i, l := range lines {
		values[i] = [2]string{strconv.FormatInt(at.Add(time.Duration(i)*time.Millisecond).UnixNano(), 10), l}
	}
	body, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": labels, "values": values}}})
	resp, err := http.Post(u+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("loki push: %s: %s", resp.Status, b)
	}
	return nil
}

// ClickHouse starts ClickHouse with a user that has a random password, and
// returns its HTTP URL, the user and the password.
func ClickHouse(t testing.TB) (u, user, password string) {
	t.Helper()
	port := FreePort(t)
	u = fmt.Sprintf("http://127.0.0.1:%d", port)
	user, password = "verify", "pw-"+RandHex(12)
	start(t, "clickhouse", u, "-p", fmt.Sprintf("127.0.0.1:%d:8123", port),
		"-e", "CLICKHOUSE_USER="+user, "-e", "CLICKHOUSE_PASSWORD="+password,
		"-e", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1", "--ulimit", "nofile=262144:262144", ClickHouseImage)
	waitHTTP(t, u+"/ping", nil, status200)
	h := http.Header{"X-Clickhouse-User": {user}, "X-Clickhouse-Key": {password}}
	waitHTTP(t, u+"/?query=SELECT%201", h, status200)
	return u, user, password
}

// ClickHouseExec runs one SQL statement on ClickHouse, failing the test if
// it fails.
func ClickHouseExec(t testing.TB, u, user, password, sql string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, u+"/", strings.NewReader(sql))
	req.Header.Set("X-ClickHouse-User", user)
	req.Header.Set("X-ClickHouse-Key", password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clickhouse: %s: %s", resp.Status, b)
	}
	return string(b)
}

// OpenSearch starts a single-node OpenSearch with its security plugin
// disabled (plain HTTP, no login) and returns its URL. It needs about a
// gigabyte of memory and can take a minute or two to start.
func OpenSearch(t testing.TB) string {
	t.Helper()
	port := FreePort(t)
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	start(t, "opensearch", u, "-p", fmt.Sprintf("127.0.0.1:%d:9200", port),
		"-e", "discovery.type=single-node", "-e", "DISABLE_SECURITY_PLUGIN=true", "-e", "DISABLE_INSTALL_DEMO_CONFIG=true",
		"-e", "OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m", "-e", "bootstrap.memory_lock=false",
		"-e", "cluster.routing.allocation.disk.threshold_enabled=false", OpenSearchImage)
	waitHTTP(t, u+"/_cluster/health?wait_for_status=yellow&timeout=5s", nil, status200)
	return u
}

// OpenSearchIndex indexes documents into index and refreshes it, so they
// are searchable at once.
func OpenSearchIndex(t testing.TB, u, index string, docs []map[string]any) {
	t.Helper()
	var b bytes.Buffer
	for _, d := range docs {
		b.WriteString(`{"index":{}}` + "\n")
		j, _ := json.Marshal(d)
		b.Write(j)
		b.WriteByte('\n')
	}
	resp, err := http.Post(u+"/"+url.PathEscape(index)+"/_bulk?refresh=true", "application/x-ndjson", &b)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Errors bool `json:"errors"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &r) != nil || r.Errors {
		t.Fatalf("opensearch bulk: %s: %s", resp.Status, body)
	}
}

// Postgres starts PostgreSQL and returns a postgres:// URL without the
// password, the user and the password.
func Postgres(t testing.TB) (u, user, password string) {
	t.Helper()
	port := FreePort(t)
	user, password = "verify", "pw-"+RandHex(12)
	u = fmt.Sprintf("postgres://%s@127.0.0.1:%d/verify?sslmode=disable", user, port)
	start(t, "postgres", u, "-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"-e", "POSTGRES_USER="+user, "-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB=verify", PostgresImage)
	deadline := time.Now().Add(ReadyTimeout)
	for {
		err := postgresExec(u, password, "SELECT 1")
		if err == nil {
			return u, user, password
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres at 127.0.0.1:%d was not ready within %s: %v", port, ReadyTimeout, err)
		}
		time.Sleep(time.Second)
	}
}

// PostgresExec runs SQL on PostgreSQL, failing the test if it fails.
func PostgresExec(t testing.TB, u, password, sql string) {
	t.Helper()
	if err := postgresExec(u, password, sql); err != nil {
		t.Fatal(err)
	}
}

func postgresExec(u, password, sql string) error {
	cfg, err := pgx.ParseConfig(u)
	if err != nil {
		return err
	}
	cfg.Password = password
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, sql)
	return err
}

// Exporter is an HTTP server with Prometheus metrics, on the host's
// 127.0.0.1: http_requests_total counters, by code and pod_template_hash,
// growing at the rates a test sets.
type Exporter struct {
	srv *http.Server
	ln  net.Listener
	mu  sync.Mutex
	pod map[string]*podRates
	now func() time.Time
}

type podRates struct {
	ok, errs   float64 // counter values at since
	okR, errsR float64 // per second
	since      time.Time
}

// NewExporter starts an exporter, stopped when the test ends.
func NewExporter(t testing.TB) *Exporter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &Exporter{ln: ln, pod: map[string]*podRates{}, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", e.metrics)
	e.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = e.srv.Serve(ln) }()
	t.Cleanup(func() { _ = e.srv.Close() })
	return e
}

// Addr is host:port, for Prometheus's targets.
func (e *Exporter) Addr() string { return e.ln.Addr().String() }

// SetErrorRate sets how fast a pod's requests grow from now on: in total,
// and how many of those are errors (code 500); the rest are code 200.
func (e *Exporter) SetErrorRate(hash string, perSecondTotal, perSecondErrors float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	p := e.pod[hash]
	if p == nil {
		p = &podRates{since: now}
		e.pod[hash] = p
	}
	p.ok, p.errs = p.values(now)
	p.okR, p.errsR, p.since = math.Max(perSecondTotal-perSecondErrors, 0), perSecondErrors, now
}

func (p *podRates) values(now time.Time) (ok, errs float64) {
	d := now.Sub(p.since).Seconds()
	return p.ok + p.okR*d, p.errs + p.errsR*d
}

func (e *Exporter) metrics(w http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	now := e.now()
	hashes := make([]string, 0, len(e.pod))
	for h := range e.pod {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	var b strings.Builder
	b.WriteString("# HELP http_requests_total Requests served.\n# TYPE http_requests_total counter\n")
	for _, h := range hashes {
		ok, errs := e.pod[h].values(now)
		fmt.Fprintf(&b, "http_requests_total{code=\"200\",pod_template_hash=%q} %g\n", h, ok)
		fmt.Fprintf(&b, "http_requests_total{code=\"500\",pod_template_hash=%q} %g\n", h, errs)
	}
	e.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, b.String())
}
