package source

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"github.com/jk-magic-creations/launchway/internal/verify"
)

// The window the fixtures are recorded in.
var (
	t0 = time.Unix(1795132800, 0).UTC()
	t1 = t0.Add(time.Hour)
)

// secret is the test's credential, made at run time.
var secret = "pw-" + strings.Repeat("ab", 6) + time.Now().Format("150405")

func lookupSecret(_ context.Context, _ string, ref pipeline.SecretRef) (string, error) {
	if ref.Key == "missing" {
		return "", errors.New("there is no secret at missing")
	}
	return secret, nil
}

func mustOpen(t *testing.T, cfg map[string]Config) *Sources {
	t.Helper()
	s, err := Open(cfg, lookupSecret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve answers every request with status and body, and records the last
// request.
type recorder struct {
	srv   *httptest.Server
	hits  atomic.Int32
	req   atomic.Pointer[http.Request]
	body  atomic.Pointer[string]
	reply func(w http.ResponseWriter, r *http.Request)
}

func serve(t *testing.T, status int, contentType string, body []byte) *recorder {
	return serveFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

func serveFunc(t *testing.T, f func(w http.ResponseWriter, r *http.Request)) *recorder {
	t.Helper()
	rec := &recorder{reply: f}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		rec.body.Store(&s)
		r2 := r.Clone(context.Background())
		rec.req.Store(r2)
		rec.reply(w, r)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func promCfg(u string) Config {
	return Config{Type: "prometheus", URL: u, Projects: []string{"shop", "team-*"}}
}

func TestOpenRefuses(t *testing.T) {
	ok := func(c Config) Config { return c }
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"mysql", Config{Type: "mysql", URL: "http://x"}, "no MySQL driver"},
		{"no type", Config{URL: "http://x"}, "type is missing"},
		{"unknown", Config{Type: "graphite", URL: "http://x"}, `unknown type "graphite"`},
		{"long timeout", ok(Config{Type: "prometheus", URL: "http://x", Timeout: "2m"}), "longer than the most allowed, 1m0s"},
		{"bad timeout", Config{Type: "prometheus", URL: "http://x", Timeout: "soon"}, "not a duration"},
		{"authorization header", Config{Type: "loki", URL: "http://x", Headers: map[string]string{"Authorization": "Bearer x"}}, "may not be set in headers"},
		{"cookie header", Config{Type: "loki", URL: "http://x", Headers: map[string]string{"cookie": "a=b"}}, "may not be set in headers"},
		{"ftp", Config{Type: "loki", URL: "ftp://x"}, "http:// or https://"},
		{"user in url", Config{Type: "loki", URL: "http://u:p@x"}, "user or password"},
		{"postgres password", Config{Type: "postgres", URL: "postgres://u:p@x/db", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT 1"}}}, "has a password"},
		{"max series", Config{Type: "prometheus", URL: "http://x", Limits: Limits{MaxSeries: 3}}, "max_series can only be 1"},
		{"statements on prometheus", Config{Type: "prometheus", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric}}}, "statements are for"},
		{"clickhouse without statements", Config{Type: "clickhouse", URL: "http://x"}, "needs statements"},
		{"clickhouse insert", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "INSERT INTO t VALUES (1)"}}}, "single SELECT"},
		{"clickhouse two statements", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT 1; DROP TABLE t"}}}, "more than one statement"},
		{"clickhouse format", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT 1 FORMAT TSV"}}}, "uses FORMAT"},
		{"clickhouse settings", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT 1 SETTINGS readonly=0"}}}, "uses SETTINGS"},
		{"clickhouse undeclared", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT count() FROM t WHERE s = {svc:String}"}}}, "does not declare svc"},
		{"clickhouse type mismatch", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT {n:UInt64}", Params: map[string]string{"n": "String"}}}}, "reads n as UInt64"},
		{"clickhouse param in string", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT {n:String}, '{n:String}'", Params: map[string]string{"n": "String"}}}}, "inside a quoted string"},
		{"clickhouse reserved", Config{Type: "clickhouse", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, SQL: "SELECT {start:String}", Params: map[string]string{"start": "String"}}}}, "always passed"},
		{"postgres placeholder in quotes", Config{Type: "postgres", URL: "postgres://u@x/db", Statements: map[string]Statement{"a": {Kind: Logs, SQL: "SELECT message FROM l WHERE s = '{{svc}}'", Params: map[string]string{"svc": "text"}}}}, "inside a quoted string"},
		{"postgres type", Config{Type: "postgres", URL: "postgres://u@x/db", Statements: map[string]Statement{"a": {Kind: Logs, SQL: "SELECT {{n}}", Params: map[string]string{"n": "bigint"}}}}, "text, int, float or timestamptz"},
		{"postgres unused", Config{Type: "postgres", URL: "postgres://u@x/db", Statements: map[string]Statement{"a": {Kind: Logs, SQL: "SELECT 1", Params: map[string]string{"n": "int"}}}}, "does not use {{n}}"},
		{"postgres with", Config{Type: "postgres", URL: "postgres://u@x/db", Statements: map[string]Statement{"a": {Kind: Logs, SQL: "WITH d AS (DELETE FROM t RETURNING *) SELECT 1"}}}, "uses DELETE"},
		{"es no indices", Config{Type: "opensearch", URL: "http://x"}, "indices is empty"},
		{"http path", Config{Type: "http", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, Path: "/../admin", Value: "v"}}}, "path must be a path"},
		{"http other host", Config{Type: "http", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, Path: "//evil/x", Value: "v"}}}, "path must be a path"},
		{"http body not json", Config{Type: "http", URL: "http://x", Statements: map[string]Statement{"a": {Kind: Metric, Path: "/q", Method: "POST", Body: `{"a": {{x}}}`, Value: "v", Params: map[string]string{"x": "string"}}}}, "body is not JSON"},
		{"auth without secret", Config{Type: "loki", URL: "http://x", Auth: &Auth{Type: "bearer"}}, "needs a store and a key"},
		{"auth type", Config{Type: "loki", URL: "http://x", Auth: &Auth{Type: "magic", Secret: pipeline.SecretRef{Store: "v", Key: "k"}}}, `auth.type "magic"`},
		{"bad name", Config{Type: "loki", URL: "http://x"}, "the name must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := "src"
			if c.name == "bad name" {
				name = "Bad Name"
			}
			_, err := Open(map[string]Config{name: c.cfg}, lookupSecret)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "health source "+name+":") {
				t.Fatalf("the error does not name the source: %v", err)
			}
		})
	}
}

func TestNamesHideURLsAndCredentials(t *testing.T) {
	s := mustOpen(t, map[string]Config{
		"prom": {Type: "prometheus", URL: "https://prom.internal.example:9090", Projects: []string{"shop"},
			Auth: &Auth{Type: "bearer", Secret: pipeline.SecretRef{Store: "vault", Key: "apps/prom", Field: "token"}}},
		"pg": {Type: "postgresql", URL: "postgres://reader@db.internal.example/app", Projects: []string{"*"},
			Statements: map[string]Statement{"errors": {Kind: Metric, SQL: "SELECT count(*) AS value FROM e WHERE t >= {{start}} AND svc = {{svc}}", Params: map[string]string{"svc": "text"}}}},
	})
	names := s.Names()
	b, _ := json.Marshal(names)
	for _, bad := range []string{"internal.example", "apps/prom", "vault", "reader"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("Names shows %q: %s", bad, b)
		}
	}
	if len(names) != 2 || names[0].Name != "pg" || names[0].Type != Postgres || names[0].Statements[0].Name != "errors" || names[1].Projects[0] != "shop" {
		t.Errorf("Names = %s", b)
	}
}

func TestProjectAllowLists(t *testing.T) {
	rec := serve(t, 200, "application/json", fixture(t, "prom_matrix.json"))
	none := promCfg(rec.srv.URL)
	none.Projects = nil
	ch := Config{Type: "clickhouse", URL: rec.srv.URL, Projects: []string{"*"},
		Statements: map[string]Statement{"errors": {Kind: Metric, SQL: "SELECT 1 AS value", Projects: []string{"payments"}}}}
	s := mustOpen(t, map[string]Config{"prom": promCfg(rec.srv.URL), "closed": none, "ch": ch})
	q := Query{Kind: Metric, Text: "up", Start: t0, End: t1}
	for _, c := range []struct {
		source, project, want string
	}{
		{"prom", "other", "project other may not use this source (it allows: shop, team-*)"},
		{"closed", "shop", "allows no project"},
		{"ch", "shop", "statement errors may not be used by project shop"},
		{"nope", "shop", "no health source of that name"},
	} {
		q.Project = c.project
		q.Statement = ""
		if c.source == "ch" {
			q.Statement = "errors"
		}
		_, err := s.Query(context.Background(), c.source, q)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), c.source+": ") {
			t.Errorf("%s as %s: got %v, want %q", c.source, c.project, err, c.want)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("%d requests were sent for refused queries", n)
	}
	q.Project, q.Statement = "team-a", ""
	if _, err := s.Query(context.Background(), "prom", q); err != nil {
		t.Fatal(err)
	}
}

func TestPrometheusQuery(t *testing.T) {
	rec := serve(t, 200, "application/json", fixture(t, "prom_matrix.json"))
	s := mustOpen(t, map[string]Config{"prom": promCfg(rec.srv.URL)})
	res, err := s.Query(context.Background(), "prom", Query{Project: "shop", Kind: Metric,
		Text:   `sum(rate(http_requests_total{pod_template_hash="{{hash}}",code=~"{{code}}"}[1m]))`,
		Params: map[string]string{"hash": `x"} or vector(1)`, "code": "5.."}, Start: t0, End: t1})
	if err != nil {
		t.Fatal(err)
	}
	// NaN and +Inf are dropped; the point after the window is cut.
	want := []verify.Point{{T: t0.UnixMilli(), V: 0.5}, {T: t0.UnixMilli() + 90500, V: 0.25}}
	if len(res.Points) != len(want) || res.Points[0] != want[0] || res.Points[1] != want[1] {
		t.Fatalf("points = %v, want %v", res.Points, want)
	}
	r := rec.req.Load()
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/query_range" || r.Header.Get("User-Agent") != "launchway-verify" {
		t.Fatalf("request: %s %s %v", r.Method, r.URL, r.Header)
	}
	form, _ := url.ParseQuery(*rec.body.Load())
	if got := form.Get("query"); got != `sum(rate(http_requests_total{pod_template_hash="x\"} or vector(1)",code=~"5\\.\\."}[1m]))` {
		t.Errorf("query sent: %s", got)
	}
	if form.Get("step") != "30" || form.Get("start") != "1795132800.000" || form.Get("end") != "1795136400.000" || form.Get("limit") != "2" || form.Get("timeout") != "10s" {
		t.Errorf("form: %v", form)
	}
}

func TestPrometheusStepFitsMaxPoints(t *testing.T) {
	rec := serve(t, 200, "application/json", fixture(t, "prom_matrix.json"))
	c := promCfg(rec.srv.URL)
	c.Limits.MaxPoints = 100
	s := mustOpen(t, map[string]Config{"prom": c})
	if _, err := s.Query(context.Background(), "prom", Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t0.Add(24 * time.Hour), Step: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(*rec.body.Load())
	step, _ := time.ParseDuration(form.Get("step") + "s")
	if n := int(24*time.Hour/step) + 1; n > 100 {
		t.Fatalf("step %s gives %d points, more than 100", step, n)
	}
	if _, err := s.Query(context.Background(), "prom", Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t1, Step: time.Second}); err == nil || !strings.Contains(err.Error(), "shorter than the least allowed, 5s") {
		t.Fatalf("a 1s step: %v", err)
	}
}

func TestPrometheusErrors(t *testing.T) {
	for _, c := range []struct {
		name        string
		status      int
		ctype, body string
		want        string
		unavailable bool
	}{
		{"several series", 200, "application/json", string(fixture(t, "prom_two_series.json")), `the query returned 3 series ({pod="a"}, {pod="b"}); a metric query must return exactly one (aggregate with sum() or avg())`, false},
		{"bad query", 400, "application/json", string(fixture(t, "prom_bad_query.json")), `answered 400 Bad Request: bad_data: invalid parameter "query"`, false},
		{"unauthorized", 401, "text/plain", "no", "refused the request (401 Unauthorized): check the deploy runner's credentials for this source", false},
		{"forbidden", 403, "text/plain", "no", "refused the request (403 Forbidden)", false},
		{"html", 200, "text/html", "<html><body>Sign in</body></html>", `returned something that is not JSON (200, text/html): "<html><body>Sign in</body></html>"`, false},
		{"bad gateway", 502, "text/html", "<html>bad gateway</html>", "answered 502 Bad Gateway: <html>bad gateway</html>", true},
		{"not found", 404, "text/plain", "404 page not found", "answered 404 Not Found: 404 page not found (check the source's url and type)", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(t, c.status, c.ctype, []byte(c.body))
			s := mustOpen(t, map[string]Config{"prom": promCfg(rec.srv.URL)})
			_, err := s.Query(context.Background(), "prom", Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t1})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v\nwant %q", err, c.want)
			}
			if IsUnavailable(err) != c.unavailable {
				t.Fatalf("IsUnavailable = %v", IsUnavailable(err))
			}
		})
	}
}

func TestTimeoutDownRedirectAndSize(t *testing.T) {
	slow := serveFunc(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	redirect := serveFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.example/api?token=x", http.StatusFound)
	})
	big := serve(t, 200, "application/json", []byte(`{"status":"success","data":{"resultType":"matrix","result":[]},"pad":"`+strings.Repeat("x", 4096)+`"}`))
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	down := "http://" + l.Addr().String()
	l.Close()
	cfg := map[string]Config{}
	for name, u := range map[string]string{"slow": slow.srv.URL, "redirect": redirect.srv.URL, "big": big.srv.URL, "down": down} {
		c := promCfg(u)
		c.Timeout = "300ms"
		c.Limits.MaxBytes = 2048
		cfg[name] = c
	}
	s := mustOpen(t, cfg)
	for name, want := range map[string]string{
		"slow":     "slow: the source did not answer within 300ms",
		"redirect": "answered with a redirect (302 Found) to http://elsewhere.example/api; health sources do not follow redirects",
		"big":      "the response was larger than 2 KiB",
		"down":     "down: could not connect to the source",
	} {
		began := time.Now()
		_, err := s.Query(context.Background(), name, Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t1})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", name, err, want)
		}
		if d := time.Since(began); d > testlib.Slow(3*time.Second) {
			t.Errorf("%s took %s", name, d)
		}
	}
}

func TestAuthAndSecretsNeverInErrors(t *testing.T) {
	// The server echoes what it was sent, as a careless proxy might.
	rec := serveFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad request with "+r.Header.Get("Authorization")+" and "+r.Header.Get("X-Api-Key"))
	})
	s := mustOpen(t, map[string]Config{
		"bearer": {Type: "loki", URL: rec.srv.URL, Projects: []string{"shop"}, Headers: map[string]string{"X-Scope-OrgID": "tenant-a"},
			Auth: &Auth{Type: "bearer", Secret: pipeline.SecretRef{Store: "vault", Key: "loki"}}},
		"header": {Type: "prometheus", URL: rec.srv.URL, Projects: []string{"shop"},
			Auth: &Auth{Type: "header", Header: "X-Api-Key", Secret: pipeline.SecretRef{Store: "vault", Key: "k"}}},
		"missing": {Type: "prometheus", URL: rec.srv.URL, Projects: []string{"shop"},
			Auth: &Auth{Type: "basic", User: "u", Secret: pipeline.SecretRef{Store: "vault", Key: "missing"}}},
	})
	_, err := s.Query(context.Background(), "bearer", Query{Project: "shop", Kind: Logs, Text: `{app="a"}`, Start: t0, End: t1})
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "Bearer ***") {
		t.Fatalf("error: %v", err)
	}
	r := rec.req.Load()
	if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Scope-OrgID") != "tenant-a" {
		t.Fatalf("headers sent: %v", r.Header)
	}
	_, err = s.Query(context.Background(), "header", Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t1})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error: %v", err)
	}
	if rec.req.Load().Header.Get("X-Api-Key") != secret {
		t.Fatal("the header secret was not sent")
	}
	n := rec.hits.Load()
	_, err = s.Query(context.Background(), "missing", Query{Project: "shop", Kind: Metric, Text: "up", Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "could not read its credentials (vault:missing)") || rec.hits.Load() != n {
		t.Fatalf("missing secret: %v", err)
	}
}

func TestLoki(t *testing.T) {
	streams := serve(t, 200, "application/json", fixture(t, "loki_streams.json"))
	matrix := serve(t, 200, "application/json", fixture(t, "loki_matrix.json"))
	text := serve(t, 400, "text/plain; charset=utf-8", []byte("parse error at line 1, col 6: syntax error\n"))
	cfg := map[string]Config{}
	for n, r := range map[string]*recorder{"streams": streams, "matrix": matrix, "text": text} {
		cfg[n] = Config{Type: "loki", URL: r.srv.URL, Projects: []string{"shop"}}
	}
	s := mustOpen(t, cfg)
	res, err := s.Query(context.Background(), "streams", Query{Project: "shop", Kind: Logs, Text: `{app="{{app}}"} |= "{{v}}"`,
		Params: map[string]string{"app": "shop", "v": `"} |= "`}, Start: t0, End: t1, MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Lines, ",") != "a first,b first,a second" || !res.Truncated {
		t.Fatalf("lines %q truncated %v", res.Lines, res.Truncated)
	}
	q := streams.req.Load().URL.Query()
	if q.Get("query") != `{app="shop"} |= "\"} |= \""` || q.Get("direction") != "forward" || q.Get("limit") != "4" ||
		q.Get("start") != "1795132800000000000" || q.Get("end") != "1795136400000000000" {
		t.Fatalf("query sent: %v", q)
	}
	res, err = s.Query(context.Background(), "streams", Query{Project: "shop", Kind: Logs, Text: `{app="shop"}`, Start: t0, End: t1})
	if err != nil || len(res.Lines) != 4 || res.Truncated {
		t.Fatalf("all lines: %v %v %v", res.Lines, res.Truncated, err)
	}
	if _, err := s.Query(context.Background(), "streams", Query{Project: "shop", Kind: Metric, Text: `{app="shop"}`, Start: t0, End: t1}); err == nil || !strings.Contains(err.Error(), "returned log lines") {
		t.Fatalf("metric kind on streams: %v", err)
	}
	res, err = s.Query(context.Background(), "matrix", Query{Project: "shop", Kind: Metric, Text: `sum(count_over_time({app="shop"}[1m]))`, Start: t0, End: t1})
	if err != nil || len(res.Points) != 2 || res.Points[1].V != 4 {
		t.Fatalf("matrix: %v %v", res.Points, err)
	}
	if matrix.req.Load().URL.Query().Get("step") != "30" {
		t.Fatalf("step: %v", matrix.req.Load().URL.Query())
	}
	_, err = s.Query(context.Background(), "text", Query{Project: "shop", Kind: Logs, Text: `{app="shop"}`, Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "answered 400 Bad Request: parse error at line 1, col 6") {
		t.Fatalf("text error: %v", err)
	}
	_, err = s.Query(context.Background(), "text", Query{Project: "shop", Kind: Logs, Text: `{app={{app}}}`, Params: map[string]string{"app": "x"}, Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "must be inside quotes") {
		t.Fatalf("unquoted placeholder: %v", err)
	}
	_, err = s.Query(context.Background(), "text", Query{Project: "shop", Kind: Logs, Text: `{app="{{app}}"}`, Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "parameter app is used by the query but has no value") {
		t.Fatalf("missing value: %v", err)
	}
}

func TestClickHouseRequest(t *testing.T) {
	rec := serve(t, 200, "application/x-ndjson", fixture(t, "ch_metric.ndjson"))
	logs := serve(t, 200, "application/x-ndjson", fixture(t, "ch_logs.ndjson"))
	failing := serveFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-ClickHouse-Exception-Code", "164")
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "Code: 164. DB::Exception: verify: Cannot execute query in readonly mode. (READONLY) (version 26.10.7.24 (official build))\n")
	})
	stmts := map[string]Statement{
		"errors": {Kind: Metric, SQL: "SELECT toStartOfMinute(ts) AS t, count() AS value FROM logs WHERE service = {service:String} AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)} GROUP BY t ORDER BY t",
			Params: map[string]string{"service": "String"}},
		"lines": {Kind: Logs, SQL: "SELECT ts AS t, msg AS message FROM logs WHERE ts >= {start:DateTime64(3)} ORDER BY t"},
	}
	cfg := map[string]Config{}
	for n, r := range map[string]*recorder{"ch": rec, "logs": logs, "failing": failing} {
		cfg[n] = Config{Type: "clickhouse", URL: r.srv.URL, Projects: []string{"shop"}, Statements: stmts,
			Auth: &Auth{User: "verify", Secret: pipeline.SecretRef{Store: "vault", Key: "ch"}}}
	}
	s := mustOpen(t, cfg)
	res, err := s.Query(context.Background(), "ch", Query{Project: "shop", Kind: Metric, Statement: "errors",
		Params: map[string]string{"service": "'; DROP TABLE logs; --"}, Start: t0, End: t1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Points) != 2 || res.Points[0] != (verify.Point{T: t0.UnixMilli(), V: 1.5}) || res.Points[1].V != 2 {
		t.Fatalf("points %v", res.Points)
	}
	r := rec.req.Load()
	q := r.URL.Query()
	for k, v := range map[string]string{"readonly": "1", "max_execution_time": "10", "max_result_rows": "501", "result_overflow_mode": "break",
		"max_result_bytes": "8388608", "param_service": "'; DROP TABLE logs; --", "param_start": "1795132800.000", "param_end": "1795136400.000",
		"default_format": "JSONCompactEachRowWithNames"} {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if r.Header.Get("X-ClickHouse-User") != "verify" || r.Header.Get("X-ClickHouse-Key") != secret || r.Header.Get("Authorization") != "" {
		t.Errorf("headers: %v", r.Header)
	}
	if *rec.body.Load() != stmts["errors"].SQL {
		t.Errorf("body: %s", *rec.body.Load())
	}
	// Lines come oldest first by t; one more row than asked for means more.
	res, err = s.Query(context.Background(), "logs", Query{Project: "shop", Kind: Logs, Statement: "lines", Start: t0, End: t1, MaxLines: 2})
	if err != nil || strings.Join(res.Lines, ",") != "first,second" || res.Truncated {
		t.Fatalf("logs: %v %v %v", res.Lines, res.Truncated, err)
	}
	if logs.req.Load().URL.Query().Get("max_result_rows") != "3" {
		t.Errorf("max_result_rows for two lines: %v", logs.req.Load().URL.Query())
	}
	res, err = s.Query(context.Background(), "logs", Query{Project: "shop", Kind: Logs, Statement: "lines", Start: t0, End: t1, MaxLines: 1})
	if err != nil || len(res.Lines) != 1 || !res.Truncated {
		t.Fatalf("logs: %v %v %v", res.Lines, res.Truncated, err)
	}
	if logs.req.Load().URL.Query().Get("max_result_rows") != "2" {
		t.Errorf("max_result_rows for one line: %v", logs.req.Load().URL.Query())
	}
	_, err = s.Query(context.Background(), "failing", Query{Project: "shop", Kind: Logs, Statement: "lines", Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "failing: the server refused the statement because health queries are read-only: Code: 164.") || strings.Contains(err.Error(), "version") {
		t.Fatalf("readonly error: %v", err)
	}
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{Kind: Metric, Statement: "errors"}, "needs parameter service"},
		{Query{Kind: Logs, Statement: "errors", Params: map[string]string{"service": "x"}}, "reads metric, but the query asks for logs"},
		{Query{Kind: Metric, Statement: "nope"}, "there is no statement nope (it has: errors, lines)"},
		{Query{Kind: Metric}, "runs only statements its operator wrote"},
	} {
		c.q.Project, c.q.Start, c.q.End = "shop", t0, t1
		if _, err := s.Query(context.Background(), "ch", c.q); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.q, err, c.want)
		}
	}
}

func TestClickHouseStreamError(t *testing.T) {
	_, _, err := chRows([]byte("[\"t\",\"value\"]\n[1,2]\nCode: 395. DB::Exception: boom (version 26.1)\n"))
	if err == nil || !strings.Contains(err.Error(), "the query failed while the answer was being sent: Code: 395. DB::Exception: boom") {
		t.Fatal(err)
	}
	_, _, err = chRows([]byte("<html>"))
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatal(err)
	}
}

func TestHTTPJSON(t *testing.T) {
	rec := serveFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"data":{"result":[[1795132800,"1.5"],[1795132860000,2],[1795132900,null]]}}`)
			return
		}
		if r.URL.Path == "/base/lines" {
			_, _ = io.WriteString(w, `{"logs":["a",{"message":"b"},3]}`)
			return
		}
		_, _ = io.WriteString(w, `{"value": 0.75}`)
	})
	html := serve(t, 200, "text/html", []byte("<html>login</html>"))
	stmts := map[string]Statement{
		"ratio": {Kind: Metric, Path: "/api/ratio?fixed=1", Value: "value", Params: map[string]string{"service": "string"}},
		"series": {Kind: Metric, Method: "post", Path: "/api/series", Value: "data.result", Params: map[string]string{"service": "string"},
			Body: `{"filter": {"service": "{{service}}", "from": "{{start}}"}, "n": 5}`},
		"lines": {Kind: Logs, Path: "/lines", Value: "logs"},
	}
	s := mustOpen(t, map[string]Config{
		"api":  {Type: "http", URL: rec.srv.URL + "/base", Projects: []string{"shop"}, Statements: stmts},
		"html": {Type: "http", URL: html.srv.URL, Projects: []string{"shop"}, Statements: stmts},
	})
	inj := `x&service=y", "n": 99, "z": "`
	res, err := s.Query(context.Background(), "api", Query{Project: "shop", Kind: Metric, Statement: "ratio", Params: map[string]string{"service": inj}, Start: t0, End: t1})
	if err != nil || len(res.Points) != 1 || res.Points[0] != (verify.Point{T: t1.UnixMilli(), V: 0.75}) {
		t.Fatalf("ratio: %v %v", res.Points, err)
	}
	u := rec.req.Load().URL
	if u.Path != "/base/api/ratio" || u.Query().Get("service") != inj || u.Query().Get("fixed") != "1" || u.Query().Get("start") != "1795132800" || u.Query().Get("end") != "1795136400" || len(u.Query()["service"]) != 1 {
		t.Fatalf("url: %s", u)
	}
	res, err = s.Query(context.Background(), "api", Query{Project: "shop", Kind: Metric, Statement: "series", Params: map[string]string{"service": inj}, Start: t0, End: t1})
	if err != nil || len(res.Points) != 2 || res.Points[1] != (verify.Point{T: 1795132860000, V: 2}) {
		t.Fatalf("series: %v %v", res.Points, err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(*rec.body.Load()), &body); err != nil {
		t.Fatal(err)
	}
	if f := body["filter"].(map[string]any); f["service"] != inj || f["from"] != "1795132800" || body["n"] != 5.0 || len(body) != 2 {
		t.Fatalf("body: %s", *rec.body.Load())
	}
	res, err = s.Query(context.Background(), "api", Query{Project: "shop", Kind: Logs, Statement: "lines", Start: t0, End: t1})
	if err != nil || strings.Join(res.Lines, ",") != "a,b" {
		t.Fatalf("lines: %v %v", res.Lines, err)
	}
	_, err = s.Query(context.Background(), "html", Query{Project: "shop", Kind: Logs, Statement: "lines", Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "returned something that is not JSON (text/html)") {
		t.Fatalf("html: %v", err)
	}
}

func TestElasticRequest(t *testing.T) {
	rec := serve(t, 200, "application/json", fixture(t, "es_hits.json"))
	bad := serve(t, 400, "application/json", fixture(t, "es_error.json"))
	cfg := map[string]Config{}
	for n, r := range map[string]*recorder{"es": rec, "bad": bad} {
		cfg[n] = Config{Type: "opensearch", URL: r.srv.URL, Projects: []string{"shop"}, Indices: []string{"logs-shop-*"}, MessageField: "log.message"}
	}
	cfg["es"] = Config{Type: "elasticsearch", URL: rec.srv.URL, Projects: []string{"shop"}, Indices: []string{"logs-shop-*"}}
	s := mustOpen(t, cfg)
	inj := `x"}}, {"match_all": {}}]}} #`
	res, err := s.Query(context.Background(), "es", Query{Project: "shop", Kind: Logs, Index: "logs-shop-*",
		Text: `{"bool": {"filter": [{"term": {"service": "{{svc}}"}}]}}`, Params: map[string]string{"svc": inj}, Start: t0, End: t1, MaxLines: 50})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Lines, "|") != "first line" || !res.Truncated {
		t.Fatalf("lines %q truncated %v", res.Lines, res.Truncated)
	}
	if p := rec.req.Load().URL; p.Path != "/logs-shop-*/_search" || p.Query().Get("allow_partial_search_results") != "false" {
		t.Fatalf("url: %s", p)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(*rec.body.Load()), &body); err != nil {
		t.Fatal(err)
	}
	want := `{"_source":["message"],"query":{"bool":{"filter":[{"range":{"@timestamp":{"format":"epoch_millis","gte":1795132800000,"lt":1795136400000}}}],"must":[{"bool":{"filter":[{"term":{"service":"x\"}}, {\"match_all\": {}}]}} #"}}]}}]}},"size":50,"sort":[{"@timestamp":"asc"}],"timeout":"10000ms","track_total_hits":true}`
	if got, _ := json.Marshal(body); string(got) != want {
		t.Fatalf("body:\n%s\nwant\n%s", got, want)
	}
	res, err = s.Query(context.Background(), "es", Query{Project: "shop", Kind: Metric, Index: "logs-shop-1", Text: `{"match_all": {}}`, Start: t0, End: t1})
	if err != nil || len(res.Points) != 1 || res.Points[0] != (verify.Point{T: t1.UnixMilli(), V: 3.0 / 60}) {
		t.Fatalf("metric: %v %v", res.Points, err)
	}
	if !strings.Contains(*rec.body.Load(), `"size":0`) {
		t.Fatalf("metric body: %s", *rec.body.Load())
	}
	_, err = s.Query(context.Background(), "bad", Query{Project: "shop", Kind: Logs, Index: "logs-shop-1", Text: `{"match_all": {}}`, Start: t0, End: t1})
	if err == nil || !strings.Contains(err.Error(), "answered 400 Bad Request: parsing_exception: unknown query [nope]") {
		t.Fatalf("error: %v", err)
	}
	// A source can read a nested message field.
	hits := mustOpen(t, map[string]Config{"n": {Type: "opensearch", URL: rec.srv.URL, Projects: []string{"shop"}, Indices: []string{"logs-*"}, MessageField: "log.message"}})
	res, err = hits.Query(context.Background(), "n", Query{Project: "shop", Kind: Logs, Index: "logs-shop-1", Text: `{"match_all": {}}`, Start: t0, End: t1})
	if err != nil || strings.Join(res.Lines, "|") != "nested" {
		t.Fatalf("nested: %v %v", res.Lines, err)
	}
}

func TestElasticIndexAllowList(t *testing.T) {
	b := &esBackend{indices: []string{"logs-shop-*", "audit"}}
	for _, ok := range []string{"logs-shop-2026.11.19", "logs-shop-*", "audit"} {
		if err := b.checkIndex(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for idx, want := range map[string]string{
		"":                    "names no index",
		"*":                   "not allowed by this source",
		"_all":                "not one index name",
		"logs-shop-1,secrets": "not one index name",
		"-logs-shop-1":        "not one index name",
		"logs-other":          "not allowed by this source (it allows logs-shop-*, audit)",
		"remote:logs-shop-1":  "not one index name",
		"<logs-{now/d}>":      "not one index name",
		"LOGS-SHOP-1":         "not one index name",
	} {
		if err := b.checkIndex(idx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", idx, err, want)
		}
	}
	all := &esBackend{indices: []string{"*"}}
	if err := all.checkIndex("*"); err != nil {
		t.Errorf("an operator who lists * allows it: %v", err)
	}
}

func TestElasticWalkerRefuses(t *testing.T) {
	decl := map[string]bool{"v": true}
	for text, want := range map[string]string{
		`{"script": {"script": "doc['a'].value > 1"}}`:                                      "script is not allowed in a health query: it runs a script",
		`{"bool": {"filter": [{"script": {"script": {"source": "1"}}}]}}`:                   "bool.filter[0].script is not allowed",
		`{"script_score": {"query": {"match_all": {}}, "script": {"source": "1"}}}`:         "script_score is not allowed",
		`{"function_score": {"query": {"match_all": {}}}}`:                                  "function_score is not allowed",
		`{"wrapper": {"query": "eyJtYXRjaF9hbGwiOnt9fQ=="}}`:                                "wrapper is not allowed",
		`{"terms": {"user": {"index": "secrets", "id": "1", "path": "x"}}}`:                 "terms lookup",
		`{"percolate": {"field": "q", "index": "other", "id": "1"}}`:                        "percolate is not allowed",
		`{"match_all": {}, "aggs": {"x": {"terms": {"field": "f"}}}}`:                       "must be one query clause",
		`{"query": {"match_all": {}}, "size": 10000}`:                                       "must be one query clause",
		`{"query_string": {"query": "service:{{v}}"}}`:                                      "{{v}} is in a query_string clause",
		`{"wildcard": {"service": {"value": "{{v}}"}}}`:                                     "{{v}} is in a wildcard clause",
		`{"term": {"{{v}}": "x"}}`:                                                          "placeholders may only be in string values",
		`["match_all"]`:                                                                     "must be a JSON object",
		`{"match_all": {}} {"match_all": {}}`:                                               "more than one JSON value",
		`{"bool": {"must": [{"runtime_mappings": {}}]}}`:                                    "runtime_mappings is not allowed",
		`{"bool": {"should": [{"terms_set": {"f": {"minimum_should_match_script": {}}}}]}}`: "terms_set is not allowed",
	} {
		if _, err := rewriteDSL(text, nil, decl); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", text, err, want)
		}
	}
	got, err := rewriteDSL(`{"query": {"bool": {"must": [{"match": {"msg": {"query": "{{v}} failed"}}}], "must_not": [{"term": {"level": "debug"}}]}}}`,
		map[string]string{"v": `"}}]`}, decl)
	if err != nil || string(got) != `{"bool":{"must":[{"match":{"msg":{"query":"\"}}] failed"}}}],"must_not":[{"term":{"level":"debug"}}]}}` {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestJSONPoints(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`[1, 2, 3, 4]`), &doc)
	// Plain numbers are spread over the window, the last at its end.
	pts, err := jsonPoints([]any{json.Number("1"), json.Number("2"), json.Number("3"), json.Number("4")}, t0, t1)
	if err != nil || len(pts) != 4 || pts[3].T != t1.UnixMilli() || pts[0].T != t0.Add(15*time.Minute).UnixMilli() {
		t.Fatalf("%v %v", pts, err)
	}
	if _, err := jsonPoints("abc", t0, t1); err == nil {
		t.Fatal("a string that is not a number was accepted")
	}
	if _, err := jsonPath(map[string]any{"a": []any{map[string]any{"b": 1}}}, "a.0.b"); err != nil {
		t.Fatal(err)
	}
	if _, err := jsonPath(map[string]any{"a": 1}, "a.b"); err == nil || !strings.Contains(err.Error(), "has no b") {
		t.Fatal(err)
	}
}

func TestQueryValidation(t *testing.T) {
	rec := serve(t, 200, "application/json", fixture(t, "prom_matrix.json"))
	s := mustOpen(t, map[string]Config{"prom": promCfg(rec.srv.URL)})
	for _, c := range []struct {
		q    Query
		want string
	}{
		{Query{Kind: Metric, Text: "up", Start: t0, End: t1}, "has no project"},
		{Query{Project: "shop", Kind: "trace", Text: "up", Start: t0, End: t1}, "kind must be metric or logs"},
		{Query{Project: "shop", Kind: Metric, Text: "up", Start: t1, End: t0}, "start before its end"},
		{Query{Project: "shop", Kind: Metric, Text: `up{a="{{v}}"}`, Params: map[string]string{"v": strings.Repeat("x", MaxValueBytes+1)}, Start: t0, End: t1}, "longer than 4096 bytes"},
		{Query{Project: "shop", Kind: Logs, Text: "up", Start: t0, End: t1}, "reads metrics only"},
	} {
		if _, err := s.Query(context.Background(), "prom", c.q); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.q, err, c.want)
		}
	}
	if rec.hits.Load() != 0 {
		t.Fatal("a refused query was sent")
	}
}

func TestSQLChecks(t *testing.T) {
	b, err := newPostgres(Config{Statements: map[string]Statement{"a": {Kind: Logs,
		SQL:    "SELECT msg AS message -- {{ignored}} in a comment\nFROM l WHERE svc = {{svc}} AND n > {{n}} AND svc <> {{svc}} AND t >= {{start}} AND s = $$ {{x}} $$ AND u = 'it''s'",
		Params: map[string]string{"svc": "text", "n": "int"}}}})
	if err == nil {
		t.Fatal("a placeholder in a dollar-quoted string was accepted")
	}
	b, err = newPostgres(Config{Statements: map[string]Statement{"a": {Kind: Logs,
		SQL:    "SELECT msg AS message -- {{ignored}}\nFROM l WHERE svc = {{svc}} AND n > {{n}} AND svc <> {{svc}} AND t >= {{start}} AND u = 'it''s;'",
		Params: map[string]string{"svc": "text", "n": "int"}}}})
	if err != nil {
		t.Fatal(err)
	}
	st := b.stmts["a"]
	if st.sql != "SELECT msg AS message -- {{ignored}}\nFROM l WHERE svc = $1::text AND n > $2::bigint AND svc <> $1::text AND t >= $3::timestamptz AND u = 'it''s;'" || strings.Join(st.order, ",") != "svc,n,start" {
		t.Fatalf("sql %q order %v", st.sql, st.order)
	}
	for v, want := range map[string]any{"42": int64(42)} {
		got, err := pgValue("int", v)
		if err != nil || got != want {
			t.Errorf("%s: %v %v", v, got, err)
		}
	}
	if _, err := pgValue("int", "1; DROP TABLE x"); err == nil {
		t.Error("a bad int was accepted")
	}
	if _, err := pgValue("timestamptz", "yesterday"); err == nil {
		t.Error("a bad time was accepted")
	}
}
