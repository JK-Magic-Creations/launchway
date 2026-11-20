package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// setupStore is setup with artifacts kept in a directory.
func setupStore(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db := testdb.New(t)
	store, err := blobstore.NewFilesystem(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 5 * time.Second, Logger: quiet(),
		Artifacts: engine.ArtifactOptions{Store: store}})
	if err != nil {
		t.Fatal(err)
	}
	eng.SetDefaultBranch(func(string) string { return "main" }) // build caches write the default scope only for a known default branch
	s := &Server{Engine: eng, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	return &env{eng: eng, srv: srv, dir: dir, db: db}
}

// The fake key is AWS's documented example, so nothing real leaks.
const fakeKey = "AKIAIOSFODNN7EXAMPLE"

const secretsSARIF = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gitleaks","rules":[{"id":"aws-access-token",
"shortDescription":{"text":"AWS access key"}}]}},"results":[{"ruleId":"aws-access-token",
"message":{"text":"aws-access-token has detected secret ` + fakeKey + `"},
"locations":[{"physicalLocation":{"artifactLocation":{"uri":"config/settings.py"},
"region":{"startLine":12,"snippet":{"text":"KEY = \"` + fakeKey + `\""}}}}]}]}]}`

// Two runners, one step each: the first keeps an artifact larger than a
// chunk, SARIF files and an SBOM; the second, on the other runner, fetches
// the artifact into a directory and checks it. Then everything is read
// back through the UI API.
func TestArtifactsAndFindingsEndToEnd(t *testing.T) {
	e := setupStore(t)
	// The run comes from a Git repository, so each runner checks it out
	// into a workspace of its own: the second never sees the first's.
	repo := filepath.Join(e.dir, "repo")
	os.MkdirAll(filepath.Join(repo, "reports"), 0o755)
	os.WriteFile(filepath.Join(repo, "reports", "secrets.sarif"), []byte(secretsSARIF), 0o644)
	os.WriteFile(filepath.Join(repo, "reports", "sbom.cdx.json"), []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[
{"type":"library","name":"left-pad","version":"1.3.0","licenses":[{"license":{"id":"WTFPL"}}]},
{"type":"library","name":"requests","version":"2.25.1","licenses":[{"license":{"id":"Apache-2.0"}}]}]}`), 0o644)
	src := `
pipeline: demo
steps:
  build:
    image: none
    run: |
      mkdir -p dist
      head -c 9000000 /dev/urandom > dist/big.bin
      printf '#!/bin/sh\necho hello from the artifact\n' > dist/app
      chmod +x dist/app
      sha256sum dist/big.bin | cut -d' ' -f1 > dist/big.sum
    artifacts:
      - {name: binary, paths: [dist]}
    reports:
      findings: reports/*.sarif
      sbom: reports/sbom.cdx.json
  check:
    image: none
    needs: build
    fetch:
      - {from: build, name: binary, into: got}
    run: |
      if [ -e dist ]; then echo "workspace shared"; else echo "fresh workspace"; fi
      test "$(sha256sum got/dist/big.bin | cut -d' ' -f1)" = "$(cat got/dist/big.sum)"
      ./got/dist/app
`
	gitRepo(t, repo, src)
	sha, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	e.runner(t, "r1")
	r, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p,
		Trigger: engine.Trigger{Event: "manual", SHA: strings.TrimSpace(string(sha)), Ref: "refs/heads/main", Branch: "main"},
		Source:  engine.Source{Kind: "git", URL: repo}})
	if err != nil {
		t.Fatal(err)
	}
	id := r.ID
	if d := e.wait(t, id, 60*time.Second); d.Status != "succeeded" {
		t.Fatalf("run %s", d.Status)
	}
	// A re-run from check reuses build: check runs in the new run's own,
	// fresh workspace, and fetches the artifact the first run kept.
	re, err := e.eng.Rerun(engine.RerunRequest{Run: id, From: "check", Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.wait(t, re.ID, 60*time.Second); d.Status != "succeeded" {
		t.Fatalf("re-run %s", d.Status)
	}
	var logs struct{ Lines []engine.LogLine }
	getJSON(t, e.srv.URL+"/api/v1/runs/"+re.ID+"/steps/check/logs", &logs)
	var text strings.Builder
	for _, l := range logs.Lines {
		text.WriteString(l.Text + "\n")
	}
	if !strings.Contains(text.String(), "fresh workspace") || !strings.Contains(text.String(), "hello from the artifact") ||
		!strings.Contains(text.String(), "fetched 3 files") {
		t.Fatalf("the re-run's check:\n%s", text.String())
	}
	var reArts engine.RunArtifacts
	getJSON(t, e.srv.URL+"/api/v1/runs/"+re.ID+"/artifacts", &reArts)
	if len(reArts.Artifacts) != 2 || !reArts.Artifacts[0].Reused || reArts.Artifacts[0].RunID != id {
		t.Fatalf("the re-run's artifacts %+v", reArts.Artifacts)
	}

	// Listed, with checksums.
	var arts engine.RunArtifacts
	getJSON(t, e.srv.URL+"/api/v1/runs/"+id+"/artifacts", &arts)
	var bin, sbomArt *engine.ArtifactView
	for _, a := range arts.Artifacts {
		switch a.Name {
		case "binary":
			bin = a
		case "sbom":
			sbomArt = a
		}
	}
	if bin == nil || bin.Status != "kept" || bin.FileCount != 3 || sbomArt == nil || sbomArt.Kind != "sbom" {
		t.Fatalf("artifacts %+v", arts.Artifacts)
	}
	// One file, as a download that is never shown as a page.
	resp, err := http.Get(e.srv.URL + "/api/v1/runs/" + id + "/artifacts/" + bin.ID + "/file?path=dist/app")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from the artifact") {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	for k, want := range map[string]string{
		"Content-Type":           "application/octet-stream",
		"Content-Disposition":    `attachment; filename="app"; filename*=UTF-8''app`,
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("Content-Security-Policy: %q", csp)
	}
	// A range of a large file.
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/runs/"+id+"/artifacts/"+bin.ID+"/file?path=dist/big.bin", nil)
	req.Header.Set("Range", "bytes=8388600-8388615")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(part) != 16 {
		t.Fatalf("range: %d, %d bytes", resp.StatusCode, len(part))
	}
	// Everything, as a zip.
	resp, err = http.Get(e.srv.URL + "/api/v1/runs/" + id + "/artifacts/" + bin.ID + "/archive")
	if err != nil {
		t.Fatal(err)
	}
	zipped, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/zip" || !strings.Contains(resp.Header.Get("Content-Disposition"), "build-binary.zip") {
		t.Fatalf("archive headers %v", resp.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil || len(zr.File) != 3 {
		t.Fatalf("archive: %v", err)
	}
	// A file not in the artifact, and an artifact through another run.
	for _, bad := range []string{"/artifacts/" + bin.ID + "/file?path=../../etc/passwd", "/artifacts/art_nope/file?path=dist/app"} {
		if resp, _ := http.Get(e.srv.URL + "/api/v1/runs/" + id + bad); resp.StatusCode != 404 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}

	// Findings: the secret is located and never shown, here or anywhere.
	resp, err = http.Get(e.srv.URL + "/api/v1/runs/" + id + "/findings")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(raw, []byte(fakeKey)) {
		t.Fatal("the findings API shows the secret")
	}
	var rf engine.RunFindings
	json.Unmarshal(raw, &rf)
	if rf.Counts == nil || rf.Counts.Total != 1 || rf.Counts.High != 1 || len(rf.Findings) != 1 || !rf.Findings[0].Secret ||
		rf.Findings[0].File != "config/settings.py" || rf.Findings[0].StartLine != 12 {
		t.Fatalf("findings %s", raw)
	}
	if len(rf.SBOMs) != 1 || rf.SBOMs[0].Components != 2 || rf.SBOMs[0].Licences["WTFPL"] != 1 {
		t.Fatalf("sboms %+v", rf.SBOMs)
	}
	assertNotInDatabase(t, e.db, fakeKey)
	evs, _ := e.eng.Events(id)
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		if bytes.Contains(b, []byte(fakeKey)) {
			t.Fatalf("the event log holds the secret: %s", b)
		}
	}
	// Components can be searched; run lists carry the counts.
	var cs engine.ComponentSearch
	getJSON(t, e.srv.URL+"/api/v1/components?name=LEFT-PAD&version=1.3.0", &cs)
	if len(cs.Hits) != 1 || cs.Hits[0].RunID != id {
		t.Fatalf("component search %+v", cs)
	}
	var list []engine.RunSummary
	getJSON(t, e.srv.URL+"/api/v1/runs", &list)
	for _, l := range list {
		want := 2 // the re-run reused the step that kept them: they are the first run's
		if l.ID != id {
			want = 0
		}
		if l.Findings == nil || l.Findings.High != 1 || l.Artifacts != want {
			t.Fatalf("the list: run %d: %+v %d", l.Number, l.Findings, l.Artifacts)
		}
	}
}

// assertNotInDatabase checks that no text column of any table holds s.
func assertNotInDatabase(t *testing.T, addr, s string) {
	t.Helper()
	tables := []string{"events", "findings", "finding_rules", "finding_reports", "logs", "steps", "runs", "sbom_components", "sbom_reports"}
	driver := "sqlite"
	if strings.HasPrefix(addr, "postgres") {
		driver = "pgx"
	}
	db, err := sql.Open(driver, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			rows.Scan(ptrs...)
			for i, v := range vals {
				if strings.Contains(fmt.Sprint(v), s) || (fmt.Sprintf("%T", v) == "[]uint8" && bytes.Contains(v.([]byte), []byte(s))) {
					rows.Close()
					t.Fatalf("%s.%s holds the secret", table, cols[i])
				}
			}
		}
		rows.Close()
	}
}

// keepOne runs a one-step run of project that keeps an artifact, through
// the engine as a runner would, and returns the run and the artifact.
func keepOne(t *testing.T, eng *engine.Engine, project string) (string, string) {
	t.Helper()
	p, err := pipeline.Parse([]byte("pipeline: app\nsteps:\n  build: {image: none, run: b, artifacts: [{name: out, paths: [out.html]}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: engine.Trigger{Event: "manual"},
		Source: engine.Source{Kind: "dir", Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	j, err := eng.Lease(context.Background(), engine.RunnerInfo{ID: "r", Features: engine.FeatureArtifacts}, 0)
	if err != nil || j == nil {
		t.Fatalf("lease: %v", err)
	}
	eng.Start(j.LeaseID)
	data := []byte("<script>alert(document.cookie)</script>")
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	b, err := eng.BeginArtifact(j.LeaseID, engine.BeginArtifact{Name: "out", Files: []engine.ArtifactFileInfo{{Path: "out.html", Size: int64(len(data)), SHA256: hexSum}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PutArtifactChunk(context.Background(), j.LeaseID, b.ID, "out.html", 0, hexSum, data); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CommitArtifact(context.Background(), j.LeaseID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := eng.Finish(j.LeaseID, engine.Result{Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	return r.ID, b.ID
}

// Artifacts follow the run's access: a viewer of the project lists and
// downloads them, an operator deletes them (recorded in the audit trail),
// and another team's look like ones that do not exist. An HTML file is
// still only ever a download.
func TestArtifactAccess(t *testing.T) {
	e := setupAuth(t, envOpts{artifacts: &engine.ArtifactOptions{Store: mustFS(t)}})
	pay, payArt := keepOne(t, e.eng, "pay-api")
	web, webArt := keepOne(t, e.eng, "web")

	vic := e.signIn(t, "vic") // viewer of pay-*
	if r := vic.get(e.srv.URL + "/api/v1/runs/" + pay + "/artifacts"); r.status != 200 || !strings.Contains(string(r.body), payArt) {
		t.Fatalf("vic lists: %d %s", r.status, r.body)
	}
	r := vic.get(e.srv.URL + "/api/v1/runs/" + pay + "/artifacts/" + payArt + "/file?path=out.html")
	if r.status != 200 || r.header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment;") ||
		r.header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("vic downloads: %d %v", r.status, r.header)
	}
	for _, path := range []string{
		"/api/v1/runs/" + web + "/artifacts",
		"/api/v1/runs/" + web + "/artifacts/" + webArt + "/file?path=out.html",
		"/api/v1/runs/" + pay + "/artifacts/" + webArt + "/file?path=out.html", // another team's, through a run vic may see
		"/api/v1/runs/" + pay + "/artifacts/" + webArt + "/archive",
	} {
		if r := vic.get(e.srv.URL + path); r.status != 404 {
			t.Errorf("vic reading %s: %d", path, r.status)
		}
	}
	if r := vic.do(http.MethodDelete, e.srv.URL+"/api/v1/runs/"+pay+"/artifacts/"+payArt, "", csrfHeader, "1"); r.status != 403 {
		t.Fatalf("vic deletes: %d %s", r.status, r.body)
	}
	otto := e.signIn(t, "otto") // operator of pay-*
	if r := otto.do(http.MethodDelete, e.srv.URL+"/api/v1/runs/"+pay+"/artifacts/"+payArt, "", csrfHeader, "1"); r.status != 200 {
		t.Fatalf("otto deletes: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodDelete, e.srv.URL+"/api/v1/runs/"+web+"/artifacts/"+webArt, "", csrfHeader, "1"); r.status != 404 {
		t.Fatalf("otto deletes another team's: %d", r.status)
	}
	if r := vic.get(e.srv.URL + "/api/v1/runs/" + pay + "/artifacts/" + payArt + "/file?path=out.html"); r.status != 404 {
		t.Fatalf("a deleted artifact is still served: %d", r.status)
	}
	var found bool
	for _, a := range e.auditEntries(t) {
		if a.Action == "artifact.delete" && a.User == "otto" && a.Outcome == "ok" && a.Project == "pay-api" && strings.Contains(a.Detail, "artifact out") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry for the deletion: %+v", e.auditEntries(t))
	}
	// Component search only looks in projects the caller may see.
	if r := vic.get(e.srv.URL + "/api/v1/components?name=x"); r.status != 200 {
		t.Fatalf("component search: %d", r.status)
	}
}

func mustFS(t *testing.T) blobstore.Store {
	t.Helper()
	s, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
