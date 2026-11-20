package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const reportsPipeline = `pipeline: app
steps:
  e2e:
    image: none
    run: t
    reports:
      tests: { paths: results/junit.xml, attachments: test-results }
      coverage: cover.out
      html: { name: site, path: site }
`

// hostileReport is an HTML report whose script tries to use the viewer's
// session.
const hostileReport = `<!doctype html><html><body><script>
fetch('/api/v1/runs', {credentials: 'include'}).then(r => r.text()).then(t => document.title = t);
fetch('/api/v1/tokens', {method: 'POST', credentials: 'include', headers: {'X-Launchway-CSRF': '1'}, body: '{"name":"x"}'});
</script></body></html>`

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 64)...)

// runWithReports makes a run of the project whose step keeps attachments
// (a PNG, an SVG with script and an HTML page) and an HTML report, and
// returns the run and its failing result.
func runWithReports(t *testing.T, eng *engine.Engine, project string) (string, int64) {
	t.Helper()
	p, err := pipeline.Parse([]byte(reportsPipeline))
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: engine.Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main"},
		Source: engine.Source{Kind: "dir", Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	j, err := eng.Lease(context.Background(), engine.RunnerInfo{ID: "r", Features: engine.FeatureArtifacts + " " + engine.FeatureCoverage}, 0)
	if err != nil || j == nil {
		t.Fatalf("lease: %v", err)
	}
	eng.Start(j.LeaseID)
	keepFiles(t, eng, j.LeaseID, "test-attachments", map[string][]byte{
		"test-results/shot.png":   tinyPNG,
		"test-results/icon.svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(document.cookie)"/>`),
		"test-results/page.html":  []byte(`<script>alert(document.cookie)</script>`),
		"test-results/notes.txt":  []byte("plain words\n"),
		"test-results/trace.zip":  []byte("PK\x03\x04 trace"),
		"test-results/video.webm": append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 32)...),
	})
	keepFiles(t, eng, j.LeaseID, "report-site", map[string][]byte{
		"site/index.html": []byte(hostileReport),
		"site/app.js":     []byte("console.log('report')"),
	})
	junit := `<testsuite name="s"><testcase classname="c" name="n"><failure message="m">trace</failure><system-out>
[[ATTACHMENT|test-results/shot.png]]
[[ATTACHMENT|test-results/icon.svg]]
[[ATTACHMENT|test-results/page.html]]
[[ATTACHMENT|test-results/notes.txt]]
[[ATTACHMENT|test-results/trace.zip]]
[[ATTACHMENT|test-results/video.webm]]
</system-out></testcase></testsuite>`
	cover := "mode: set\nexample.com/app/a.go:1.1,3.2 2 1\nexample.com/app/a.go:4.1,6.2 2 0\n"
	if err := eng.Finish(j.LeaseID, engine.Result{Outcome: "failed",
		Tests:    &engine.CollectedReports{Files: []engine.ReportFile{{Path: "results/junit.xml", Data: []byte(junit)}}},
		Coverage: &engine.CollectedReports{Files: []engine.ReportFile{{Path: "cover.out", Data: []byte(cover)}}}}); err != nil {
		t.Fatal(err)
	}
	page, err := eng.RunTestCases(r.ID, engine.CaseQuery{})
	if err != nil || len(page.Cases) != 1 {
		t.Fatalf("cases: %+v %v", page, err)
	}
	return r.ID, page.Cases[0].ID
}

func keepFiles(t *testing.T, eng *engine.Engine, lease, name string, files map[string][]byte) {
	t.Helper()
	var infos []engine.ArtifactFileInfo
	for p, data := range files {
		sum := sha256.Sum256(data)
		infos = append(infos, engine.ArtifactFileInfo{Path: p, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	b, err := eng.BeginArtifact(lease, engine.BeginArtifact{Name: name, Files: infos})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range b.Files {
		data := files[f.Path]
		sum := sha256.Sum256(data)
		if _, err := eng.PutArtifactChunk(context.Background(), lease, b.ID, f.Path, 0, hex.EncodeToString(sum[:]), data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.CommitArtifact(context.Background(), lease, b.ID); err != nil {
		t.Fatal(err)
	}
}

type reportList struct {
	Reports []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Base string `json:"base"`
	} `json:"reports"`
	Origin string `json:"origin"`
}

// Coverage, attachments and HTML reports follow the run's access;
// attachments are served by what their content is, never as pages; and an
// HTML report is served sandboxed under a signed link, so its script cannot
// act as the viewer.
func TestReportAccess(t *testing.T) {
	e := setupAuth(t, envOpts{artifacts: &engine.ArtifactOptions{Store: mustFS(t)}})
	pay, payCase := runWithReports(t, e.eng, "pay-api")
	web, webCase := runWithReports(t, e.eng, "web")

	vic := e.signIn(t, "vic") // viewer of pay-*
	for _, path := range []string{
		"/api/v1/runs/" + pay + "/coverage",
		"/api/v1/runs/" + pay + "/coverage/files?sort=missed",
		"/api/v1/projects/pay-api/coverage",
		"/api/v1/runs/" + pay + "/tests/compare",
		"/api/v1/runs/" + pay + "/reports",
	} {
		if r := vic.get(e.srv.URL + path); r.status != http.StatusOK {
			t.Errorf("vic reading %s: %d %s", path, r.status, r.body)
		}
	}
	var cov engine.RunCoverage
	vic.get(e.srv.URL+"/api/v1/runs/"+pay+"/coverage").json(t, &cov)
	if cov.Totals == nil || cov.Totals.Lines != (engine.CoverageCounter{Covered: 2, Total: 4}) || cov.Totals.Unit != "statements" {
		t.Fatalf("coverage %+v", cov.Totals)
	}
	for _, path := range []string{
		"/api/v1/runs/" + web + "/coverage",
		"/api/v1/runs/" + web + "/reports",
		"/api/v1/projects/web/coverage",
		fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d/attachments/0", web, webCase),
		fmt.Sprintf("/api/v1/runs/%s/tests/cases/%d/attachments/0", pay, webCase),
	} {
		if r := vic.get(e.srv.URL + path); r.status != http.StatusNotFound {
			t.Errorf("vic reading %s: %d, want 404", path, r.status)
		}
	}

	// Attachments: by content, never a page.
	att := func(n int, q string) response {
		return vic.get(fmt.Sprintf("%s/api/v1/runs/%s/tests/cases/%d/attachments/%d%s", e.srv.URL, pay, payCase, n, q))
	}
	for n, want := range map[int]struct{ ctype, disp string }{
		0: {"image/png", "inline"},
		1: {"application/octet-stream", "attachment"}, // SVG
		2: {"application/octet-stream", "attachment"}, // HTML
		3: {"text/plain; charset=utf-8", "inline"},
		4: {"application/octet-stream", "attachment"}, // a trace
		5: {"video/webm", "inline"},
	} {
		r := att(n, "")
		if r.status != http.StatusOK || r.header.Get("Content-Type") != want.ctype || !strings.HasPrefix(r.header.Get("Content-Disposition"), want.disp) ||
			r.header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("attachment %d: %d %v", n, r.status, r.header)
		}
	}
	if r := att(0, "?download=1"); r.header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("download: %v", r.header)
	}
	if r := vic.get(fmt.Sprintf("%s/api/v1/runs/%s/tests/cases/%d/attachments/5", e.srv.URL, pay, payCase), "Range", "bytes=4-7"); r.status != http.StatusPartialContent || len(r.body) != 4 {
		t.Errorf("a range of the video: %d %d bytes", r.status, len(r.body))
	}

	// The HTML report: a signed link that works without a session.
	var list reportList
	vic.get(e.srv.URL+"/api/v1/runs/"+pay+"/reports").json(t, &list)
	if len(list.Reports) != 1 || list.Reports[0].URL == "" || list.Origin != "sandboxed" {
		t.Fatalf("reports %+v", list)
	}
	link := list.Reports[0].URL
	anon := newBrowser(t)
	r := anon.get(e.srv.URL + link)
	if r.status != http.StatusOK || string(r.body) != hostileReport {
		t.Fatalf("hosted page: %d %s", r.status, r.body)
	}
	csp := r.header.Get("Content-Security-Policy")
	base := e.srv.URL + list.Reports[0].Base
	for _, want := range []string{"sandbox allow-scripts", "connect-src " + base + " ", "default-src " + base + ";", "form-action 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the policy %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-top-navigation") || strings.Contains(csp, "'self' 'unsafe") {
		t.Errorf("the policy gives the report too much: %q", csp)
	}
	if r.header.Get("X-Frame-Options") != "" || r.header.Get("Referrer-Policy") != "no-referrer" || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Errorf("hosted headers %v", r.header)
	}
	if r := anon.get(e.srv.URL + list.Reports[0].Base + "app.js"); r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/javascript") {
		t.Errorf("app.js: %d %v", r.status, r.header.Get("Content-Type"))
	}
	for _, bad := range []string{
		strings.Replace(link, "/hosted/art_", "/hosted/art_x", 1), // another artifact under the same MAC
		link[:len(link)-len("index.html")] + "..%2f..%2fapi%2fv1%2fruns",
		link[:len(link)-len("index.html")] + "nope.html",
		"/hosted/" + "x/index.html",
	} {
		if r := anon.get(e.srv.URL + bad); r.status != http.StatusNotFound {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
	// What the report's script would send: the session does not work from
	// a sandboxed page (Origin: null) or another site, for reading or
	// changing anything; links followed from elsewhere still work.
	for _, h := range [][]string{
		{"Origin", "null"},
		{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"},
	} {
		if r := vic.get(e.srv.URL+"/api/v1/runs/"+pay+"/tests", h...); r.status != http.StatusForbidden {
			t.Errorf("a read from %v: %d", h, r.status)
		}
		if r := vic.do(http.MethodPost, e.srv.URL+"/api/v1/tokens", `{"name":"x"}`, append(h, csrfHeader, "1")...); r.status != http.StatusForbidden {
			t.Errorf("a write from %v: %d", h, r.status)
		}
	}
	if r := vic.get(e.srv.URL+"/api/v1/runs/"+pay+"/tests", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate"); r.status != http.StatusOK {
		t.Errorf("a link followed from another site: %d", r.status)
	}
	if r := vic.get(e.srv.URL+"/api/v1/runs/"+pay+"/tests", "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors"); r.status != http.StatusOK {
		t.Errorf("Launchway's own page: %d", r.status)
	}
	// Links expire.
	e.clk.Add(hostedLinkTTL + time.Minute)
	if r := anon.get(e.srv.URL + link); r.status != http.StatusNotFound || !strings.Contains(string(r.body), "expired") {
		t.Errorf("an expired link: %d %s", r.status, r.body)
	}
}

// With a separate origin for reports, reports are served only there, and
// nothing else is.
func TestHostedReportsOnTheirOwnOrigin(t *testing.T) {
	e := setupStore(t)
	run, _ := runWithReports(t, e.eng, "app")
	s := &Server{Engine: e.eng, RunnerToken: "secret", Actor: "tester", Log: quiet(), ReportsOrigin: "http://reports.example.test"}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	get := func(host, path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	resp, err := http.Get(srv.URL + "/api/v1/runs/" + run + "/reports")
	if err != nil {
		t.Fatal(err)
	}
	var list reportList
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Reports) != 1 || list.Origin != "separate" || !strings.HasPrefix(list.Reports[0].URL, "http://reports.example.test/hosted/") {
		t.Fatalf("reports %+v", list)
	}
	path := strings.TrimPrefix(list.Reports[0].URL, "http://reports.example.test")
	if r := get("reports.example.test", path); r.StatusCode != http.StatusOK || !strings.Contains(r.Header.Get("Content-Security-Policy"), "frame-ancestors *") {
		t.Fatalf("on the reports origin: %d %v", r.StatusCode, r.Header)
	}
	if r := get(strings.TrimPrefix(srv.URL, "http://"), path); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a report on Launchway's own origin: %d", r.StatusCode)
	}
	if r := get("reports.example.test", "/api/v1/runs"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("the API on the reports origin: %d", r.StatusCode)
	}
	if r := get("reports.example.test", "/"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("the UI on the reports origin: %d", r.StatusCode)
	}
}
