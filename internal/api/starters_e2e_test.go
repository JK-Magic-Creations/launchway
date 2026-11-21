package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
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
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// Each flight plan of the built-in library, run for real on the host against
// a tiny repository of its language: the run is green, the Tests tab has the
// tests, and a second run restores the dependency cache the first one saved.
// A language whose toolchain this machine lacks is skipped, with the reason.

func fixtureFiles(t *testing.T, name string) map[string]string {
	t.Helper()
	root := filepath.Join("..", "starters", "testdata", "fixtures", name)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed on this machine, so this flight plan cannot run here", tool)
		}
	}
}

// startersEnv is a server whose project's pipeline uses the built-in library,
// and a host runner.
func startersEnv(t *testing.T, files map[string]string, pipelineText string) (*env, *Server) {
	t.Helper()
	return startersEnvWith(t, files, pipelineText, nil)
}

// startersEnvWith is startersEnv for a project that also has a configured
// pipeline; with no pipelineText the repository has no pipeline file.
func startersEnvWith(t *testing.T, files map[string]string, pipelineText string, configured *source.ProjectPipeline) (*env, *Server) {
	t.Helper()
	dir := t.TempDir()
	db := testdb.New(t)
	store, err := blobstore.NewFilesystem(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 10 * time.Second, Logger: quiet(),
		Artifacts: engine.ArtifactOptions{Store: store}})
	if err != nil {
		t.Fatal(err)
	}
	eng.SetDefaultBranch(func(string) string { return "main" }) // build caches write the default scope only for a known default branch
	if pipelineText != "" {
		files[".launchway/pipeline.yaml"] = pipelineText
	}
	app := testlib.New(t, filepath.Join(dir, "app"), files)
	mgr, err := source.NewManagerWithLibraries(eng, filepath.Join(dir, "data"), []source.Project{{Name: "app", URL: app.Bare, Pipeline: configured}}, nil, quiet())
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{Engine: eng, Projects: mgr, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	e := &env{eng: eng, srv: srv, dir: dir, db: db}
	r := e.newRunner("r1")
	r.Exec = &executor.Host{}
	e.startRunner(t, r)
	return e, api
}

func startAppRun(t *testing.T, e *env) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ref": "main"})
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/projects/app/runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("starting a run: %d %s", resp.StatusCode, out.Error)
	}
	return out.ID
}

func logsOf(t *testing.T, e *env, d *engine.RunDetail) string {
	var b strings.Builder
	for _, s := range d.StepList {
		lines, _, _ := e.eng.Logs(d.ID, s.ID, 0, 0, 0)
		for _, l := range lines {
			b.WriteString(s.ID + ": " + l.Text + "\n")
		}
	}
	return b.String()
}

// runStarter runs the flight plan twice and returns the second run's detail.
func runStarter(t *testing.T, fixture, flight, with string, wantTests int, wantCaches bool) {
	t.Helper()
	text := "pipeline: app\nsteps:\n  ci:\n    uses: launchway/" + flight + "\n    with: { " + with + " }\n"
	e, _ := startersEnv(t, fixtureFiles(t, fixture), text)
	var first *engine.RunDetail
	for i := 1; i <= 2; i++ {
		id := startAppRun(t, e)
		d := e.wait(t, id, 4*time.Minute)
		if d.Status != engine.RunSucceeded {
			t.Fatalf("run %d is %s\n%s", i, d.Status, logsOf(t, e, d))
		}
		tests, err := e.eng.RunTests(id)
		if err != nil {
			t.Fatal(err)
		}
		if wantTests > 0 && (tests.Totals == nil || tests.Totals.Passed < wantTests) {
			t.Fatalf("run %d: the Tests tab has %+v, want at least %d passed\n%s", i, tests.Totals, wantTests, logsOf(t, e, d))
		}
		if tests.Totals != nil && tests.Totals.Failed+tests.Totals.Errored != 0 {
			t.Fatalf("failed tests: %+v", tests.Totals)
		}
		if i == 1 {
			first = d
			continue
		}
		if wantCaches {
			hits := 0
			for _, s := range d.StepList {
				for _, c := range s.Caches {
					if c.Result == "hit" || c.Restored == "hit" {
						hits++
					}
				}
			}
			saved := 0
			for _, s := range first.StepList {
				for _, c := range s.Caches {
					if c.Result == "saved" {
						saved++
					}
				}
			}
			if saved == 0 || hits == 0 {
				t.Fatalf("the first run saved %d caches and the second restored %d (want both above zero)\n%s", saved, hits, logsOf(t, e, d))
			}
		}
	}
}

func TestStarterGoLibraryRunsGreen(t *testing.T) {
	need(t, "go", "gofmt")
	runStarter(t, "go", "go-library", "vuln_scan: false", 2, true)
}

func TestStarterNodeLibraryRunsGreen(t *testing.T) {
	need(t, "node", "npm")
	runStarter(t, "node", "node-library", "", 2, true)
}

func TestStarterPythonLibraryRunsGreen(t *testing.T) {
	need(t, "python3")
	if out, err := exec.Command("python3", "-m", "venv", "--help").CombinedOutput(); err != nil {
		t.Skipf("python3 has no venv module: %s", out)
	}
	if err := exec.Command("timeout", "20", "python3", "-m", "pip", "download", "--no-deps", "-q", "-d", t.TempDir(), "pytest").Run(); err != nil {
		t.Skip("the Python flight plan installs pytest and ruff from PyPI, which cannot be reached from here")
	}
	runStarter(t, "python", "python-library", "", 2, true)
}

func TestStarterStaticSiteRunsGreen(t *testing.T) {
	runStarter(t, "static", "static-site", "", 0, false)
}

func TestStarterHelmChartRunsGreen(t *testing.T) {
	need(t, "helm")
	runStarter(t, "helm", "helm-chart", "chart: chart", 0, false)
}

func TestStarterGoServiceRunsGreenWithoutImage(t *testing.T) {
	need(t, "go", "gofmt")
	runStarter(t, "go", "go-service", "vuln_scan: false, image: false, license_scan: false", 2, false)
}

func TestStarterNodeAppRunsGreenWithoutImage(t *testing.T) {
	need(t, "node", "npm")
	runStarter(t, "node", "node-app", "image: false, audit: false, license_scan: false", 2, false)
}

func TestStarterPythonServiceRunsGreenWithoutImage(t *testing.T) {
	need(t, "python3")
	if out, err := exec.Command("python3", "-m", "venv", "--help").CombinedOutput(); err != nil {
		t.Skipf("python3 has no venv module: %s", out)
	}
	if err := exec.Command("timeout", "20", "python3", "-m", "pip", "download", "--no-deps", "-q", "-d", t.TempDir(), "pytest").Run(); err != nil {
		t.Skip("the Python flight plan installs pytest and ruff from PyPI, which cannot be reached from here")
	}
	runStarter(t, "python", "python-service", "image: false, audit: false, analysis: false, license_scan: false", 2, false)
}

// A repository with no pipeline file runs the flight plan its files call for,
// where its level allows it, and the run says so and why.
func TestZeroFileModeRunsTheFlightPlanThatFits(t *testing.T) {
	need(t, "go", "gofmt")
	files := fixtureFiles(t, "go")
	files["Dockerfile"] = "FROM scratch\n" // makes it a service: go-service, whose image step is not run here
	delete(files, "Dockerfile")
	e, api := startersEnvWith(t, files, "", &source.ProjectPipeline{Detect: true,
		With: map[string]string{"vuln_scan": "false"}})
	id := startAppRun(t, e)
	d := e.wait(t, id, 4*time.Minute)
	if d.Status != engine.RunSucceeded {
		t.Fatalf("run is %s\n%s", d.Status, logsOf(t, e, d))
	}
	o := d.Graph.Origin
	if o == nil || o.Detected == nil || o.Detected.Flight != "launchway/go-library" || o.Detected.Because != "go.mod was found" {
		t.Fatalf("origin %+v", o)
	}
	if !strings.Contains(o.Text, "No pipeline file: using launchway/go-library because go.mod was found.") || !strings.Contains(o.Text, "uses: launchway/go-library") {
		t.Fatalf("the kept text:\n%s", o.Text)
	}
	tests, err := e.eng.RunTests(id)
	if err != nil || tests.Totals == nil || tests.Totals.Passed < 2 {
		t.Fatalf("tests %+v %v", tests, err)
	}
	_ = api

	// The editor opens the same text, so saving it makes it the repository's own.
	f, err := api.Projects.ReadPipelineFile(context.Background(), "app", "")
	if err != nil || f.Exists || !f.Configured || !strings.Contains(f.Text, "uses: launchway/go-library") {
		t.Fatalf("editor file %+v %v", f, err)
	}
}

// A repository that shows nothing to detect is left alone, as before.
func TestZeroFileModeLeavesAnUnknownRepositoryAlone(t *testing.T) {
	e, _ := startersEnvWith(t, map[string]string{"notes.txt": "hello\n"}, "", &source.ProjectPipeline{Detect: true})
	body, _ := json.Marshal(map[string]any{"ref": "main"})
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/projects/app/runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
