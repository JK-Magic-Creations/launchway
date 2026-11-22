package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// libEnv is a server with a library and a project whose pipeline uses it,
// and a runner with a real executor.
type libEnv struct {
	*env
	lib, app *testlib.Repo
	api      *Server
}

func setupLibrary(t *testing.T, ex executor.Executor, appPipeline string, stores *secrets.Resolver) *libEnv {
	t.Helper()
	dir := t.TempDir()
	return setupLibraryIn(t, ex, dir, testlib.NewLibrary(t, filepath.Join(dir, "lib")), map[string]string{".launchway/pipeline.yaml": appPipeline}, stores)
}

// setupLibraryWith is setupLibrary with a library the test made and the files of the application's repository.
func setupLibraryWith(t *testing.T, ex executor.Executor, dir string, lib *testlib.Repo, appFiles map[string]string) *libEnv {
	t.Helper()
	return setupLibraryIn(t, ex, dir, lib, appFiles, nil)
}

func setupLibraryIn(t *testing.T, ex executor.Executor, dir string, lib *testlib.Repo, appFiles map[string]string, stores *secrets.Resolver) *libEnv {
	t.Helper()
	db := testdb.New(t)
	eng, err := engine.Open(engine.Options{Database: db, LeaseTTL: 10 * time.Second, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	le := &libEnv{}
	le.lib = lib
	le.app = testlib.New(t, filepath.Join(dir, "app"), appFiles)
	mgr, err := source.NewManagerWithLibraries(eng, filepath.Join(dir, "data"), []source.Project{{Name: "app", URL: le.app.Bare}},
		[]source.Library{{Name: "ci-templates", URL: le.lib.Bare}}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	le.api = &Server{Engine: eng, Projects: mgr, RunnerToken: "secret", Actor: "tester", Log: quiet()}
	srv := httptest.NewServer(le.api.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	le.env = &env{eng: eng, srv: srv, dir: dir, db: db}
	r := le.newRunner("r1")
	r.Exec, r.Secrets = ex, stores
	le.startRunner(t, r)
	return le
}

// libApp is the application's pipeline: the library's check template, and
// a step after it that reports what the template exposed.
func libApp(version, image string) string {
	return `pipeline: app
libraries:
  platform: { repo: ci-templates, version: ` + version + ` }
steps:
  ci:
    uses: platform/check
    with: { greeting: hi, image: "` + image + `" }
  after:
    image: ` + image + `
    needs: ci
    run: |
      echo "seen=$LAUNCHWAY_NEEDS_CI_VERSION" >> "$LAUNCHWAY_OUTPUTS"
      echo "lib=$LAUNCHWAY_LIB_PLATFORM" >> "$LAUNCHWAY_OUTPUTS"
      echo "own=${LAUNCHWAY_LIB:-none}" >> "$LAUNCHWAY_OUTPUTS"
`
}

// startRun starts a run through the API, as a person would.
func (le *libEnv) startRun(t *testing.T, libs map[string]string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ref": "main", "libraries": libs})
	req, _ := http.NewRequest("POST", le.srv.URL+"/api/v1/projects/app/runs", bytes.NewReader(body))
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

func outputsOf(t *testing.T, d *engine.RunDetail, step string) map[string]string {
	t.Helper()
	for _, s := range d.StepList {
		if s.ID == step {
			if s.Status != engine.StepSucceeded {
				t.Fatalf("%s %s: %s\n%s", step, s.Status, s.Error, s.Excerpt)
			}
			return s.Outputs
		}
	}
	t.Fatalf("no step %s", step)
	return nil
}

// testLibraryRuns runs the check template from the library at a pinned tag,
// then after the script changed on main, then with an override to main.
func testLibraryRuns(t *testing.T, ex executor.Executor, image, libDir string, timeout time.Duration) {
	le := setupLibrary(t, ex, libApp("v1", image), nil)
	v1 := le.lib.Head()
	check := func(id, version string) {
		t.Helper()
		d := le.wait(t, id, timeout)
		if d.Status != engine.RunSucceeded {
			for _, s := range d.StepList {
				lines, _, _ := le.eng.Logs(id, s.ID, 0, 0, 0)
				for _, l := range lines {
					t.Logf("%s: %s", s.ID, l.Text)
				}
			}
			t.Fatalf("run %s", d.Status)
		}
		out := outputsOf(t, d, "ci/run")
		if out["version"] != version {
			t.Fatalf("the template ran check.sh version %q, want %q", out["version"], version)
		}
		if out["clean"] != "0" {
			t.Fatalf("git status in the step showed changes: %q", out["clean"])
		}
		wantDir := libDir
		if wantDir == "" {
			wantDir = filepath.Join(le.dir, "r1", "workspaces", id, ".launchway", "lib", "platform")
		}
		if out["lib"] != wantDir {
			t.Fatalf("LAUNCHWAY_LIB is %q, want %q", out["lib"], wantDir)
		}
		after := outputsOf(t, d, "after")
		if after["seen"] != version || after["lib"] != wantDir || after["own"] != "none" {
			t.Fatalf("after: %v", after)
		}
	}

	id := le.startRun(t, nil)
	check(id, "1")
	if l := mustRun(t, le.eng, id).Graph.Libraries[0]; l.Commit != v1 || l.Override != "" {
		t.Fatalf("library %+v", l)
	}

	// The script changes on main. A run pinned to v1 still uses the old one.
	tip := le.lib.Commit(map[string]string{"scripts/check.sh": testlib.CheckScript("2")}, "check.sh version 2")
	id = le.startRun(t, nil)
	check(id, "1")

	// A run that overrides the library's version uses the new one.
	id = le.startRun(t, map[string]string{"platform": "main"})
	check(id, "2")
	if l := mustRun(t, le.eng, id).Graph.Libraries[0]; l.Commit != tip || l.Override != "main" || l.Version != "v1" {
		t.Fatalf("library %+v", l)
	}
}

func mustRun(t *testing.T, eng *engine.Engine, id string) *engine.RunDetail {
	t.Helper()
	d, err := eng.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLibraryEndToEndOnTheHost(t *testing.T) {
	testLibraryRuns(t, &executor.Host{}, "none", "", 30*time.Second)
}

func TestLibraryEndToEndInContainers(t *testing.T) {
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	ex, err := executor.ByName("container", executor.Options{RunnerID: "test-libraries"})
	if err != nil {
		t.Skip(err)
	}
	c := ex.(*executor.Container)
	if err := exec.Command(c.Bin, "pull", "-q", gitImage).Run(); err != nil {
		t.Skipf("cannot pull %s: %v", gitImage, err)
	}
	testLibraryRuns(t, ex, gitImage, "/workspace/.launchway/lib/platform", 2*time.Minute)
}

func TestLibraryEndToEndOnKubernetes(t *testing.T) {
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	ex, err := executor.NewKubernetes(executor.KubernetesOptions{Context: ctxName, Namespace: kubeTestNamespace(), StartTimeout: "5m"},
		fmt.Sprintf("test-libraries-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	testLibraryRuns(t, ex, gitImage, "/workspace/.launchway/lib/platform", 6*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Every run succeeded, so the runner may remove their pods.
	_ = ex.Cleanup(ctx, func(context.Context, []string) (map[string]executor.RunInfo, error) { return nil, nil })
}

// gitImage has sh and git, so a step can show what git status says.
const gitImage = "docker.io/alpine/git:v2.52.0"

// A template's step that asks for a secret is judged by the project of the
// pipeline that uses the template: expansion does not change whose job it
// is, so a store that does not allow the project refuses it.
func TestTemplateSecretsBelongToTheUsingProject(t *testing.T) {
	t.Setenv("LIB_TEST_TOKEN", "s3cret-value-for-the-template")
	const secretTemplate = `template: deploy
steps:
  push:
    image: none
    secrets: { TOKEN: { store: env, key: LIB_TEST_TOKEN } }
    run: test -n "$TOKEN" && echo "the token has ${#TOKEN} characters"
`
	app := "pipeline: app\nlibraries:\n  platform: { repo: ci-templates, version: main }\nsteps:\n  d: { uses: platform/deploy }\n"
	for _, c := range []struct {
		projects []string
		want     engine.RunStatus
	}{{[]string{"elsewhere"}, engine.RunFailed}, {[]string{"app"}, engine.RunSucceeded}} {
		stores := secrets.NewResolver()
		stores.Add("env", secrets.Env{}, c.projects, nil)
		le := setupLibrary(t, &executor.Host{}, app, stores)
		le.lib.Commit(map[string]string{"templates/deploy.yaml": secretTemplate}, "a template with a secret")
		d := le.wait(t, le.startRun(t, nil), 30*time.Second)
		if d.Status != c.want {
			t.Fatalf("store for %v: run %s", c.projects, d.Status)
		}
		if c.want == engine.RunFailed && !strings.Contains(d.StepList[0].Error, `store "env" does not allow project "app"`) {
			t.Fatalf("error %q", d.StepList[0].Error)
		}
	}
}
