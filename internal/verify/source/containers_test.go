package source

// Tests against real backends in containers. They run when
// LAUNCHWAY_TEST_CONTAINERS is set; see package sourcetest.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"github.com/jk-magic-creations/launchway/internal/verify/source/sourcetest"
)

// lookupFixed resolves every secret to v, a password or token made at run
// time.
func lookupFixed(v string) Lookup {
	return func(context.Context, string, pipeline.SecretRef) (string, error) { return v, nil }
}

var vaultRef = pipeline.SecretRef{Store: "vault", Key: "health/source", Field: "password"}

// bearerProxy forwards to target only requests that carry the token.
func bearerProxy(t *testing.T, target, token string) string {
	t.Helper()
	u, _ := url.Parse(target)
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// slowProxy forwards to target after a delay.
func slowProxy(t *testing.T, target string, delay time.Duration) string {
	t.Helper()
	u, _ := url.Parse(target)
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
			rp.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// garbage answers every request with an HTML login page, as a misrouted
// ingress might.
func garbage(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<!doctype html><html><body><form>Sign in</form></body></html>")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func query(t *testing.T, s *Sources, name string, q Query) (Result, error) {
	t.Helper()
	if q.Project == "" {
		q.Project = "shop"
	}
	return s.Query(context.Background(), name, q)
}

func mustQuery(t *testing.T, s *Sources, name string, q Query) Result {
	t.Helper()
	r, err := query(t, s, name, q)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want an error containing %q", err, want)
	}
}

// downQuickly stops the backend behind u and checks the source says so,
// quickly.
func downQuickly(t *testing.T, s *Sources, name, u string, q Query) {
	t.Helper()
	sourcetest.Stop(t, u)
	began := time.Now()
	_, err := query(t, s, name, q)
	if err == nil || !IsUnavailable(err) || !strings.Contains(err.Error(), "could not connect to the source") {
		t.Fatalf("source down: %v", err)
	}
	if d := time.Since(began); d > testlib.Slow(5*time.Second) {
		t.Fatalf("source down took %s to notice", d)
	}
	t.Logf("source down: %v", err)
}

func TestContainersPrometheus(t *testing.T) {
	sourcetest.Skip(t)
	t.Parallel()
	exp := sourcetest.NewExporter(t)
	exp.SetErrorRate("aaa", 10, 1)
	exp.SetErrorRate("bbb", 10, 0)
	u := sourcetest.Prometheus(t, exp.Addr())
	token := "tok-" + sourcetest.RandHex(12)
	cfg := map[string]Config{
		"prom":       {Type: "prometheus", URL: u, Projects: []string{"shop"}},
		"guarded":    {Type: "prometheus", URL: bearerProxy(t, u, token), Projects: []string{"shop"}, Auth: &Auth{Type: "bearer", Secret: vaultRef}},
		"slow":       {Type: "prometheus", URL: slowProxy(t, u, 3*time.Second), Projects: []string{"shop"}, Timeout: "500ms"},
		"garbage":    {Type: "prometheus", URL: garbage(t), Projects: []string{"shop"}},
		"wrongtoken": {Type: "prometheus", URL: bearerProxy(t, u, token+"x"), Projects: []string{"shop"}, Auth: &Auth{Type: "bearer", Secret: vaultRef}},
	}
	s, err := Open(cfg, lookupFixed(token))
	if err != nil {
		t.Fatal(err)
	}
	ratio := `sum(rate(http_requests_total{code="500",pod_template_hash="{{hash}}"}[5s])) / sum(rate(http_requests_total{pod_template_hash="{{hash}}"}[5s]))`
	window := func() (time.Time, time.Time) { now := time.Now(); return now.Add(-20 * time.Second), now }
	// Wait for enough scrapes for a rate.
	var res Result
	deadline := time.Now().Add(sourcetest.ReadyTimeout)
	for {
		start, end := window()
		res = mustQuery(t, s, "prom", Query{Kind: Metric, Text: ratio, Params: map[string]string{"hash": "aaa"}, Start: start, End: end, Step: 5 * time.Second})
		if len(res.Points) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no error-rate points after %s", sourcetest.ReadyTimeout)
		}
		time.Sleep(time.Second)
	}
	for _, p := range res.Points {
		if p.V < 0.05 || p.V > 0.15 {
			t.Fatalf("error rate of aaa: %v, want about 0.1", res.Points)
		}
	}
	t.Logf("aaa error rate: %v", res.Points)
	start, end := window()

	// A changed rate shows.
	exp.SetErrorRate("bbb", 10, 5)
	time.Sleep(8 * time.Second)
	start, end = window()
	res = mustQuery(t, s, "prom", Query{Kind: Metric, Text: ratio, Params: map[string]string{"hash": "bbb"}, Start: end.Add(-2 * time.Second), End: end, Step: 5 * time.Second})
	if len(res.Points) == 0 || res.Points[len(res.Points)-1].V < 0.3 {
		t.Fatalf("bbb after its error rate rose: %v", res.Points)
	}

	// Injection attempts select nothing: each value is only ever compared
	// as itself.
	count := `count(http_requests_total{pod_template_hash="{{v}}"})`
	countRE := `count(http_requests_total{pod_template_hash=~"{{v}}"})`
	for _, c := range []struct{ text, v string }{
		{count, `aaa"} or vector(1) #`},
		{count, `x", pod_template_hash=~".*`},
		{count, "aaa\"}\nor vector(1)"},
		{count, `aaa\`},
		{countRE, `.*`},
		{countRE, `aaa|bbb`},
		{countRE, `aa.`},
	} {
		res, err := query(t, s, "prom", Query{Kind: Metric, Text: c.text, Params: map[string]string{"v": c.v}, Start: start, End: end, Step: 5 * time.Second})
		if err != nil || len(res.Points) != 0 {
			t.Errorf("%s with %q: %v %v; want nothing selected", c.text, c.v, res.Points, err)
		}
	}
	// The same queries do select what they name.
	if res := mustQuery(t, s, "prom", Query{Kind: Metric, Text: countRE, Params: map[string]string{"v": "aaa"}, Start: start, End: end, Step: 5 * time.Second}); len(res.Points) == 0 || res.Points[0].V != 2 {
		t.Fatalf("count of aaa: %v", res.Points)
	}

	_, err = query(t, s, "prom", Query{Kind: Metric, Text: `http_requests_total`, Start: start, End: end, Step: 5 * time.Second})
	wantErr(t, err, "the query returned more than one series")
	_, err = query(t, s, "prom", Query{Kind: Metric, Text: `sum(rate(nope[1m])`, Start: start, End: end})
	wantErr(t, err, "answered 400 Bad Request: bad_data")
	_, err = query(t, s, "prom", Query{Kind: Metric, Text: `up{job={{j}}}`, Params: map[string]string{"j": "app"}, Start: start, End: end})
	wantErr(t, err, `{{j}} at line 1 column 8 must be inside quotes`)

	if res := mustQuery(t, s, "guarded", Query{Kind: Metric, Text: ratio, Params: map[string]string{"hash": "aaa"}, Start: start, End: end, Step: 5 * time.Second}); len(res.Points) == 0 {
		t.Fatal("no points through the guarded proxy")
	}
	_, err = query(t, s, "wrongtoken", Query{Kind: Metric, Text: "up", Start: start, End: end})
	wantErr(t, err, "refused the request (401 Unauthorized): check the deploy runner's credentials")
	if strings.Contains(err.Error(), token) {
		t.Fatal("the token is in the error")
	}
	began := time.Now()
	_, err = query(t, s, "slow", Query{Kind: Metric, Text: "up", Start: start, End: end})
	wantErr(t, err, "slow: the source did not answer within 500ms")
	if d := time.Since(began); d > testlib.Slow(2*time.Second) {
		t.Fatalf("the slow source took %s", d)
	}
	_, err = query(t, s, "garbage", Query{Kind: Metric, Text: "up", Start: start, End: end})
	wantErr(t, err, "returned something that is not JSON (200, text/html)")
	downQuickly(t, s, "prom", u, Query{Kind: Metric, Text: "up", Start: start, End: end})
}

func TestContainersLoki(t *testing.T) {
	sourcetest.Skip(t)
	t.Parallel()
	u := sourcetest.Loki(t)
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	sourcetest.PushLoki(t, u, map[string]string{"app": "shop", "pod_template_hash": "aaa"},
		[]string{`level=error msg="payment failed" id=1`, `level=info msg="ok"`, `level=error msg="payment failed" id=2`, `level=info msg=say "hi"`, `level=info msg=a.*b`}, at)
	sourcetest.PushLoki(t, u, map[string]string{"app": "other", "pod_template_hash": "bbb"}, []string{`level=error msg="secret thing"`}, at)
	token := "tok-" + sourcetest.RandHex(12)
	s, err := Open(map[string]Config{
		"loki":    {Type: "loki", URL: u, Projects: []string{"shop"}, Headers: map[string]string{"X-Scope-OrgID": "fake"}},
		"guarded": {Type: "loki", URL: bearerProxy(t, u, token), Projects: []string{"shop"}, Auth: &Auth{Type: "bearer", Secret: vaultRef}},
		"wrong":   {Type: "loki", URL: bearerProxy(t, u, token+"x"), Projects: []string{"shop"}, Auth: &Auth{Type: "bearer", Secret: vaultRef}},
	}, lookupFixed(token))
	if err != nil {
		t.Fatal(err)
	}
	start, end := at.Add(-time.Minute), time.Now().Add(time.Second)
	sel := `{app="{{app}}"} |= "{{needle}}"`
	var res Result
	deadline := time.Now().Add(time.Minute)
	for {
		res = mustQuery(t, s, "loki", Query{Kind: Logs, Text: sel, Params: map[string]string{"app": "shop", "needle": "level=error"}, Start: start, End: end})
		if len(res.Lines) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if strings.Join(res.Lines, "\n") != "level=error msg=\"payment failed\" id=1\nlevel=error msg=\"payment failed\" id=2" || res.Truncated {
		t.Fatalf("error lines: %q truncated %v", res.Lines, res.Truncated)
	}
	res = mustQuery(t, s, "loki", Query{Kind: Logs, Text: `{app="{{app}}"}`, Params: map[string]string{"app": "shop"}, Start: start, End: end, MaxLines: 3})
	if len(res.Lines) != 3 || !res.Truncated || !strings.Contains(res.Lines[0], "id=1") {
		t.Fatalf("three of five lines: %q truncated %v", res.Lines, res.Truncated)
	}
	// A value with quotes is matched as written.
	res = mustQuery(t, s, "loki", Query{Kind: Logs, Text: sel, Params: map[string]string{"app": "shop", "needle": `say "hi"`}, Start: start, End: end})
	if len(res.Lines) != 1 || res.Lines[0] != `level=info msg=say "hi"` {
		t.Fatalf("quoted needle: %q", res.Lines)
	}
	// Injection attempts select nothing more than their literal value.
	for _, c := range []struct{ text, app, needle string }{
		{sel, `shop"} or {app="other`, ""},
		{sel, `x", app=~".+`, ""},
		{sel, "shop", "\"\n|= \""},
		{`{app="{{app}}"} |~ "{{needle}}"`, "shop", `.+`},
		{`{app="{{app}}"} |~ "{{needle}}"`, "shop", `secret|payment`},
		{`{app=~"{{app}}"}`, `.+`, ""},
	} {
		res, err := query(t, s, "loki", Query{Kind: Logs, Text: c.text, Params: map[string]string{"app": c.app, "needle": c.needle}, Start: start, End: end})
		if err != nil {
			// Loki may refuse a selector that matches nothing; that is fine
			// as long as nothing is read.
			if !strings.Contains(err.Error(), "answered 400") {
				t.Errorf("%s with %q/%q: %v", c.text, c.app, c.needle, err)
			}
			continue
		}
		if len(res.Lines) != 0 {
			t.Errorf("%s with %q/%q selected %q", c.text, c.app, c.needle, res.Lines)
		}
	}
	// A regular expression value matches only itself.
	res = mustQuery(t, s, "loki", Query{Kind: Logs, Text: `{app="{{app}}"} |~ "{{needle}}"`, Params: map[string]string{"app": "shop", "needle": "a.*b"}, Start: start, End: end})
	if len(res.Lines) != 1 || res.Lines[0] != "level=info msg=a.*b" {
		t.Fatalf("regexp needle: %q", res.Lines)
	}
	res = mustQuery(t, s, "loki", Query{Kind: Metric, Text: `sum(count_over_time({app="{{app}}"} |= "level=error" [5m]))`, Params: map[string]string{"app": "shop"}, Start: start, End: end, Step: 15 * time.Second})
	if len(res.Points) == 0 || res.Points[len(res.Points)-1].V != 2 {
		t.Fatalf("error count: %v", res.Points)
	}
	_, err = query(t, s, "loki", Query{Kind: Logs, Text: `sum(count_over_time({app="shop"}[1m]))`, Start: start, End: end})
	wantErr(t, err, "the query is a metric query")
	_, err = query(t, s, "loki", Query{Kind: Logs, Text: `{app="shop"} |= `, Start: start, End: end})
	wantErr(t, err, "answered 400 Bad Request: parse error")
	if res := mustQuery(t, s, "guarded", Query{Kind: Logs, Text: `{app="shop"}`, Start: start, End: end}); len(res.Lines) != 5 {
		t.Fatalf("through the guarded proxy: %q", res.Lines)
	}
	_, err = query(t, s, "wrong", Query{Kind: Logs, Text: `{app="shop"}`, Start: start, End: end})
	wantErr(t, err, "refused the request (401 Unauthorized)")
	downQuickly(t, s, "loki", u, Query{Kind: Logs, Text: `{app="shop"}`, Start: start, End: end})
}

func TestContainersClickHouse(t *testing.T) {
	sourcetest.Skip(t)
	t.Parallel()
	u, user, pw := sourcetest.ClickHouse(t)
	exec := func(sql string) string { return sourcetest.ClickHouseExec(t, u, user, pw, sql) }
	exec(`CREATE TABLE logs (ts DateTime64(3, 'UTC'), service String, level String, msg String) ENGINE = MergeTree ORDER BY ts`)
	exec(`CREATE TABLE secrets (v String) ENGINE = MergeTree ORDER BY v`)
	exec(`INSERT INTO secrets VALUES ('the secret')`)
	base := time.Now().Add(-5 * time.Minute).Truncate(time.Minute).UTC()
	var rows []string
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		for j := 0; j <= i; j++ {
			rows = append(rows, fmt.Sprintf("(%f, 'shop', 'error', 'boom %d.%d')", float64(ts.Add(time.Duration(j)*time.Second).UnixMilli())/1000, i, j))
		}
		rows = append(rows, fmt.Sprintf("(%f, 'shop', 'info', 'fine')", float64(ts.UnixMilli())/1000))
		rows = append(rows, fmt.Sprintf("(%f, 'other', 'error', 'not yours')", float64(ts.UnixMilli())/1000))
	}
	exec("INSERT INTO logs VALUES " + strings.Join(rows, ", "))
	stmts := map[string]Statement{
		"errors_per_minute": {Kind: Metric, Params: map[string]string{"service": "String"},
			SQL: `SELECT toStartOfMinute(ts) AS t, count() AS value FROM logs
			      WHERE service = {service:String} AND level = 'error' AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)}
			      GROUP BY t ORDER BY t`},
		"error_lines": {Kind: Logs, Params: map[string]string{"service": "String"},
			SQL: `SELECT ts AS t, msg AS message FROM logs WHERE service = {service:String} AND level = 'error'
			      AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)} ORDER BY ts`},
		"by_level": {Kind: Metric, Params: map[string]string{"limit": "UInt64"},
			SQL: `SELECT count() AS value FROM (SELECT * FROM logs LIMIT {limit:UInt64})`},
		"slow":       {Kind: Metric, SQL: `SELECT sum(sleepEachRow(0.5)) AS value FROM numbers(5)`},
		"restricted": {Kind: Metric, SQL: `SELECT 1 AS value`, Projects: []string{"payments"}},
	}
	s, err := Open(map[string]Config{
		"ch":    {Type: "clickhouse", URL: u, Projects: []string{"shop"}, Statements: stmts, Timeout: "1s", Limits: Limits{MaxLines: 4}, Auth: &Auth{User: user, Secret: vaultRef}},
		"wrong": {Type: "clickhouse", URL: u, Projects: []string{"shop"}, Statements: stmts, Auth: &Auth{User: user, Secret: pipeline.SecretRef{Store: "vault", Key: "wrong"}}},
	}, func(_ context.Context, _ string, ref pipeline.SecretRef) (string, error) {
		if ref.Key == "wrong" {
			return pw + "x", nil
		}
		return pw, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	start, end := base.Add(-time.Minute), time.Now()
	res := mustQuery(t, s, "ch", Query{Kind: Metric, Statement: "errors_per_minute", Params: map[string]string{"service": "shop"}, Start: start, End: end})
	if len(res.Points) != 3 || res.Points[0].T != base.UnixMilli() || res.Points[0].V != 1 || res.Points[2].V != 3 {
		t.Fatalf("errors per minute: %v", res.Points)
	}
	res = mustQuery(t, s, "ch", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"service": "shop"}, Start: start, End: end})
	if strings.Join(res.Lines, ",") != "boom 0.0,boom 1.0,boom 1.1,boom 2.0" || !res.Truncated {
		t.Fatalf("error lines (at most 4): %q truncated %v", res.Lines, res.Truncated)
	}
	// The window is applied: only the last minute's lines.
	res = mustQuery(t, s, "ch", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"service": "shop"}, Start: base.Add(2 * time.Minute), End: end})
	if strings.Join(res.Lines, ",") != "boom 2.0,boom 2.1,boom 2.2" || res.Truncated {
		t.Fatalf("lines of the last minute: %q", res.Lines)
	}
	// A value with SQL in it is only a value.
	for _, v := range []string{"'; DROP TABLE logs; --", "shop' OR '1'='1", `shop\' OR 1=1 --`} {
		res := mustQuery(t, s, "ch", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"service": v}, Start: start, End: end})
		if len(res.Lines) != 0 {
			t.Fatalf("service %q selected %q", v, res.Lines)
		}
	}
	if n := strings.TrimSpace(exec("SELECT count() FROM logs")); n != "12" {
		t.Fatalf("logs has %s rows after the injection attempts", n)
	}
	_, err = query(t, s, "ch", Query{Kind: Metric, Statement: "by_level", Params: map[string]string{"limit": "1; DROP TABLE logs"}, Start: start, End: end})
	wantErr(t, err, "a parameter's value does not fit its type")
	_, err = query(t, s, "ch", Query{Kind: Metric, Statement: "restricted", Start: start, End: end})
	wantErr(t, err, "statement restricted may not be used by project shop")
	began := time.Now()
	_, err = query(t, s, "ch", Query{Kind: Metric, Statement: "slow", Start: start, End: end})
	if err == nil || !IsUnavailable(err) || !(strings.Contains(err.Error(), "did not finish the query within 1s") || strings.Contains(err.Error(), "did not answer within 1s")) {
		t.Fatalf("slow statement: %v", err)
	}
	if d := time.Since(began); d > testlib.Slow(3*time.Second) {
		t.Fatalf("the slow statement took %s", d)
	}
	_, err = query(t, s, "wrong", Query{Kind: Metric, Statement: "by_level", Params: map[string]string{"limit": "1"}, Start: start, End: end})
	wantErr(t, err, "refused the request (403 Forbidden)")
	if strings.Contains(err.Error(), pw) {
		t.Fatal("the password is in the error")
	}

	// Statements an operator wrote maliciously, slipped past Open's check:
	// the server refuses them, because every query is read-only.
	ch := s.byName["ch"].backend.(*chBackend)
	for sql, want := range map[string]string{
		"INSERT INTO logs VALUES (now64(3), 'x', 'error', 'injected')": "read-only",
		"DROP TABLE secrets":                                                             "read-only",
		"CREATE TEMPORARY TABLE tmp (a Int8)":                                            "read-only",
		"INSERT INTO FUNCTION file('x.csv') SELECT 1":                                    "read-only",
		"SELECT 1 AS value SETTINGS readonly=0":                                          "read-only",
		"SELECT 1 AS value SETTINGS max_result_rows=1000000":                             "read-only",
		"SELECT count() AS value FROM url('http://127.0.0.1:8123/ping', 'LineAsString')": "read-only",
	} {
		ch.stmts["evil"] = Statement{Kind: Metric, SQL: sql}
		s.byName["ch"].cfg.Statements["evil"] = ch.stmts["evil"]
		_, err := query(t, s, "ch", Query{Kind: Metric, Statement: "evil", Start: start, End: end})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", sql, err, want)
		}
	}
	if n := strings.TrimSpace(exec("SELECT count() FROM logs")); n != "12" {
		t.Fatalf("logs has %s rows after the write attempts", n)
	}
	if n := strings.TrimSpace(exec("SELECT count() FROM secrets")); n != "1" {
		t.Fatalf("secrets has %s rows after the write attempts", n)
	}
	// The row limit holds even for a statement that returns many rows.
	ch.stmts["many"] = Statement{Kind: Logs, SQL: "SELECT toString(number) AS message FROM numbers(1000000)"}
	s.byName["ch"].cfg.Statements["many"] = ch.stmts["many"]
	res = mustQuery(t, s, "ch", Query{Kind: Logs, Statement: "many", Start: start, End: end})
	if len(res.Lines) != 4 || !res.Truncated {
		t.Fatalf("many rows: %d lines truncated %v", len(res.Lines), res.Truncated)
	}
	downQuickly(t, s, "ch", u, Query{Kind: Metric, Statement: "by_level", Params: map[string]string{"limit": "1"}, Start: start, End: end})
}

func TestContainersPostgres(t *testing.T) {
	sourcetest.Skip(t)
	t.Parallel()
	u, user, pw := sourcetest.Postgres(t)
	exec := func(sql string) { sourcetest.PostgresExec(t, u, pw, sql) }
	exec(`CREATE TABLE events (ts timestamptz NOT NULL, service text NOT NULL, level text NOT NULL, msg text NOT NULL)`)
	exec(`CREATE SEQUENCE seq`)
	base := time.Now().Add(-5 * time.Minute).Truncate(time.Minute).UTC()
	var rows []string
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		for j := 0; j <= i; j++ {
			rows = append(rows, fmt.Sprintf("('%s', 'shop', 'error', 'boom %d.%d')", ts.Add(time.Duration(j)*time.Second).Format(time.RFC3339), i, j))
		}
		rows = append(rows, fmt.Sprintf("('%s', 'other', 'error', 'not yours')", ts.Format(time.RFC3339)))
	}
	exec("INSERT INTO events VALUES " + strings.Join(rows, ", "))
	stmts := map[string]Statement{
		"errors_per_minute": {Kind: Metric, Params: map[string]string{"svc": "text"},
			SQL: `SELECT date_trunc('minute', ts) AS t, count(*) AS value FROM events
			      WHERE service = {{svc}} AND level = 'error' AND ts >= {{start}} AND ts < {{end}} GROUP BY 1 ORDER BY 1`},
		"error_lines": {Kind: Logs, Params: map[string]string{"svc": "text"},
			SQL: `SELECT ts AS t, msg AS message FROM events WHERE service = {{svc}} AND level = 'error' AND ts >= {{start}} AND ts < {{end}} ORDER BY ts`},
		"above": {Kind: Metric, Params: map[string]string{"n": "int", "f": "float", "since": "timestamptz"},
			SQL: `SELECT count(*) AS value FROM events WHERE length(msg) > {{n}} AND {{f}} > 0 AND ts >= {{since}}`},
		"nextval":   {Kind: Metric, SQL: `SELECT nextval('seq') AS value`},
		"readwrite": {Kind: Metric, SQL: `SELECT set_config('transaction_read_only', 'off', true) AS value`},
		"slow":      {Kind: Metric, SQL: `SELECT 1 AS value FROM pg_sleep(3)`},
	}
	s, err := Open(map[string]Config{
		"pg":    {Type: "postgres", URL: u, Projects: []string{"shop"}, Statements: stmts, Timeout: "1s", Limits: Limits{MaxLines: 4}, Auth: &Auth{Secret: vaultRef}},
		"wrong": {Type: "postgres", URL: u, Projects: []string{"shop"}, Statements: stmts, Auth: &Auth{User: user, Secret: pipeline.SecretRef{Store: "vault", Key: "wrong"}}},
	}, func(_ context.Context, _ string, ref pipeline.SecretRef) (string, error) {
		if ref.Key == "wrong" {
			return pw + "x", nil
		}
		return pw, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	start, end := base.Add(-time.Minute), time.Now()
	res := mustQuery(t, s, "pg", Query{Kind: Metric, Statement: "errors_per_minute", Params: map[string]string{"svc": "shop"}, Start: start, End: end})
	if len(res.Points) != 3 || res.Points[0].T != base.UnixMilli() || res.Points[0].V != 1 || res.Points[2].V != 3 {
		t.Fatalf("errors per minute: %v", res.Points)
	}
	res = mustQuery(t, s, "pg", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"svc": "shop"}, Start: start, End: end})
	if strings.Join(res.Lines, ",") != "boom 0.0,boom 1.0,boom 1.1,boom 2.0" || !res.Truncated {
		t.Fatalf("error lines: %q truncated %v", res.Lines, res.Truncated)
	}
	res = mustQuery(t, s, "pg", Query{Kind: Metric, Statement: "above", Params: map[string]string{"n": "8", "f": "0.5", "since": base.Format(time.RFC3339)}, Start: start, End: end})
	if len(res.Points) != 1 || res.Points[0].V != 3 {
		t.Fatalf("typed parameters: %v", res.Points)
	}
	for _, v := range []string{"'; DROP TABLE events; --", "shop' OR '1'='1"} {
		res := mustQuery(t, s, "pg", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"svc": v}, Start: start, End: end})
		if len(res.Lines) != 0 {
			t.Fatalf("svc %q selected %q", v, res.Lines)
		}
	}
	_, err = query(t, s, "pg", Query{Kind: Metric, Statement: "above", Params: map[string]string{"n": "1; DROP TABLE events", "f": "1", "since": base.Format(time.RFC3339)}, Start: start, End: end})
	wantErr(t, err, `parameter n: "1; DROP TABLE events" is not an int`)
	_, err = query(t, s, "pg", Query{Kind: Metric, Statement: "nextval", Start: start, End: end})
	wantErr(t, err, "the server refused the statement because health queries are read-only: cannot execute nextval() in a read-only transaction")
	// Turning the transaction read-write from inside is refused too.
	_, err = query(t, s, "pg", Query{Kind: Metric, Statement: "readwrite", Start: start, End: end})
	wantErr(t, err, "transaction read-write mode must be set before any query")
	began := time.Now()
	_, err = query(t, s, "pg", Query{Kind: Metric, Statement: "slow", Start: start, End: end})
	if err == nil || !IsUnavailable(err) || !strings.Contains(err.Error(), "within 1s") {
		t.Fatalf("slow: %v", err)
	}
	if d := time.Since(began); d > testlib.Slow(3*time.Second) {
		t.Fatalf("the slow statement took %s", d)
	}
	_, err = query(t, s, "wrong", Query{Kind: Metric, Statement: "nextval", Start: start, End: end})
	wantErr(t, err, "refused the login")
	if strings.Contains(err.Error(), pw) {
		t.Fatal("the password is in the error")
	}
	// A malicious statement that slipped past Open's check is refused by
	// the read-only transaction.
	pg := s.byName["pg"].backend.(*pgBackend)
	for _, sql := range []string{"INSERT INTO events VALUES (now(), 'x', 'error', 'injected')", "DROP TABLE events", "CREATE TABLE x (a int)"} {
		pg.stmts["evil"] = pgStatement{Statement: Statement{Kind: Metric}, sql: sql}
		s.byName["pg"].cfg.Statements["evil"] = pg.stmts["evil"].Statement
		_, err := query(t, s, "pg", Query{Kind: Metric, Statement: "evil", Start: start, End: end})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	pg.stmts["evil"] = pgStatement{Statement: Statement{Kind: Metric}, sql: "SELECT 1 AS value; DROP TABLE events"}
	_, err = query(t, s, "pg", Query{Kind: Metric, Statement: "evil", Start: start, End: end})
	if err == nil {
		t.Error("two statements ran")
	}
	if res := mustQuery(t, s, "pg", Query{Kind: Logs, Statement: "error_lines", Params: map[string]string{"svc": "shop"}, Start: start, End: end}); len(res.Lines) != 4 {
		t.Fatalf("events changed: %q", res.Lines)
	}
	downQuickly(t, s, "pg", u, Query{Kind: Metric, Statement: "nextval", Start: start, End: end})
}

func TestContainersOpenSearch(t *testing.T) {
	sourcetest.Skip(t)
	t.Parallel()
	u := sourcetest.OpenSearch(t)
	base := time.Now().Add(-5 * time.Minute).Truncate(time.Minute)
	var docs []map[string]any
	for i := 0; i < 6; i++ {
		svc := "shop"
		if i%3 == 2 {
			svc = "other"
		}
		docs = append(docs, map[string]any{"@timestamp": base.Add(time.Duration(i) * 10 * time.Second).UnixMilli(), "service": svc, "level": "error", "message": fmt.Sprintf("boom %d %s", i, svc)})
	}
	sourcetest.OpenSearchIndex(t, u, "logs-shop-1", docs)
	sourcetest.OpenSearchIndex(t, u, "secrets", []map[string]any{{"@timestamp": base.UnixMilli(), "service": "shop", "message": "the secret"}})
	s, err := Open(map[string]Config{"os": {Type: "opensearch", URL: u, Projects: []string{"shop"}, Indices: []string{"logs-shop-*"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start, end := base.Add(-time.Minute), time.Now()
	q := `{"bool": {"filter": [{"term": {"service.keyword": "{{svc}}"}}, {"match": {"level": "error"}}]}}`
	res := mustQuery(t, s, "os", Query{Kind: Logs, Index: "logs-shop-*", Text: q, Params: map[string]string{"svc": "shop"}, Start: start, End: end, MaxLines: 3})
	if strings.Join(res.Lines, ",") != "boom 0 shop,boom 1 shop,boom 3 shop" || !res.Truncated {
		t.Fatalf("lines: %q truncated %v", res.Lines, res.Truncated)
	}
	res = mustQuery(t, s, "os", Query{Kind: Metric, Index: "logs-shop-1", Text: q, Params: map[string]string{"svc": "shop"}, Start: start, End: end})
	minutes := end.Sub(start).Minutes()
	if len(res.Points) != 1 || res.Points[0].V != 4/minutes {
		t.Fatalf("per minute: %v, want %v", res.Points, 4/minutes)
	}
	for _, v := range []string{`shop"}}, {"match_all": {}}]}}`, `*`, `shop OR other`} {
		res := mustQuery(t, s, "os", Query{Kind: Logs, Index: "logs-shop-1", Text: q, Params: map[string]string{"svc": v}, Start: start, End: end})
		if len(res.Lines) != 0 {
			t.Fatalf("svc %q selected %q", v, res.Lines)
		}
	}
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "secrets", Text: `{"match_all": {}}`, Start: start, End: end})
	wantErr(t, err, `index "secrets" is not allowed by this source (it allows logs-shop-*)`)
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "logs-shop-1,secrets", Text: `{"match_all": {}}`, Start: start, End: end})
	wantErr(t, err, "not one index name")
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "logs-shop-1", Text: `{"script": {"script": {"source": "true"}}}`, Start: start, End: end})
	wantErr(t, err, "script is not allowed in a health query")
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "logs-shop-1", Text: `{"terms": {"service.keyword": {"index": "secrets", "id": "1", "path": "service"}}}`, Start: start, End: end})
	wantErr(t, err, "terms lookup")
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "logs-shop-1", Text: `{"nope": {}}`, Start: start, End: end})
	wantErr(t, err, "answered 400 Bad Request")
	_, err = query(t, s, "os", Query{Kind: Logs, Index: "logs-shop-9", Text: `{"match_all": {}}`, Start: start, End: end})
	wantErr(t, err, "index_not_found")
	downQuickly(t, s, "os", u, Query{Kind: Logs, Index: "logs-shop-1", Text: `{"match_all": {}}`, Start: start, End: end})
}
