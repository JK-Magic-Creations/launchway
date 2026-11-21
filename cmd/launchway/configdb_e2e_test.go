package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Configuration in the database, as real processes on one PostgreSQL
// database: the first start imports the file once and then ignores it; a
// change made through server A is used by server B within seconds; a
// restart keeps what the database says over what the file says; and rolling
// back (an export, a server of the schema before configuration was kept in
// the database, the exported text as its file) gives the same projects and
// environments.
//
// It needs PostgreSQL (LAUNCHWAY_TEST_POSTGRES) and git, and builds two
// binaries: this one, and one limited to schema 22, as the release before
// would be.
func TestConfigurationInTheDatabaseTwoServersAndRollback(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set LAUNCHWAY_TEST_POSTGRES")
	}
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	dir := t.TempDir()
	bin, old := filepath.Join(dir, "launchway"), filepath.Join(dir, "launchway-22")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if out, err := exec.Command("go", "build", "-ldflags", "-X github.com/jk-magic-creations/launchway/internal/engine.schemaLimit=22",
		"-o", old, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build (schema 22): %v\n%s", err, out)
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

	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".launchway"), 0o755)
	os.WriteFile(filepath.Join(repo, ".launchway", "pipeline.yaml"), []byte("pipeline: demo\nsteps:\n  hi:\n    image: none\n    run: echo hi\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// The file as an installation of the release before would have it:
	// projects, environments and limits in it.
	fileBody := func(port int, data string) string {
		return fmt.Sprintf(`listen: 127.0.0.1:%d
data: %s
database: %q
runner: { enabled: false }
approvals: { allow_self: true }
egress: { allow_local: true }
projects:
  - { name: demo, repo: %s, branch: main, poll: 1h }
environments:
  - { name: staging, target: home, projects: ["*"] }
`, port, data, dbURL, repo)
	}
	type server struct {
		url, log, cfg string
		cmd           *exec.Cmd
	}
	start := func(name, binary string, body func(port int, data string) string) *server {
		port := freePort(t)
		data := filepath.Join(dir, name)
		os.MkdirAll(data, 0o755)
		cfg := filepath.Join(dir, name+".yaml")
		os.WriteFile(cfg, []byte(body(port, data)), 0o644)
		logf, _ := os.Create(filepath.Join(dir, name+".log"))
		cmd := exec.Command(binary, "server", "--config", cfg)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		s := &server{url: fmt.Sprintf("http://127.0.0.1:%d", port), log: logf.Name(), cfg: cfg, cmd: cmd}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); logf.Close() })
		waitReady(t, s.url, s.log)
		return s
	}
	stop := func(s *server) { s.cmd.Process.Kill(); s.cmd.Wait() }
	// names lists the names in a list the API answers with, bare or under key.
	names := func(s *server, path, key string) []string {
		resp, err := http.Get(s.url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		type item struct {
			Name string `json:"name"`
		}
		var list []item
		if json.Unmarshal(b, &list) != nil {
			var wrapped map[string][]item
			_ = json.Unmarshal(b, &wrapped)
			list = wrapped[key]
		}
		var out []string
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}
	has := func(list []string, n string) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	envsOf := func(s *server) []string { return names(s, "/api/v1/environments", "environments") }
	waitFor := func(what string, f func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !f() {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen within 15 seconds", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	put := func(s *server, path, body string) {
		t.Helper()
		req, _ := http.NewRequest("PUT", s.url+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Launchway-CSRF", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("PUT %s: %d %s", path, resp.StatusCode, out)
		}
	}

	// 1. The first start imports the file, once, and says what it ignores.
	a := start("a", bin, fileBody)
	var imports struct {
		Imports []struct {
			ID, Origin string
		} `json:"imports"`
	}
	getJSONFrom(t, a.url+"/api/v1/config/imports", &imports)
	if len(imports.Imports) != 1 || imports.Imports[0].Origin != "bootstrap-file" {
		t.Fatalf("the first start's import: %+v\n%s", imports, readFile(a.log))
	}
	logA := readFile(a.log)
	if !strings.Contains(logA, "projects") || !strings.Contains(logA, "environments") || !strings.Contains(logA, imports.Imports[0].ID) {
		t.Errorf("the startup warning does not name the ignored keys and the import:\n%s", logA)
	}
	b := start("b", bin, fileBody)
	getJSONFrom(t, b.url+"/api/v1/config/imports", &imports)
	if len(imports.Imports) != 1 {
		t.Errorf("a second server imported again: %+v", imports)
	}
	if p := names(b, "/api/v1/projects", "projects"); !has(p, "demo") {
		t.Errorf("server B lacks the imported project: %v", p)
	}

	// 2. A change through A is used by B within seconds, no restart.
	put(a, "/api/v1/config/projects/shop", fmt.Sprintf(`{"spec":{"repo":%q,"branch":"main","poll":"1h"},"comment":"second project"}`, repo))
	put(a, "/api/v1/config/environments/prod", `{"spec":{"target":"home","projects":["*"],"protected":true}}`)
	waitFor("server B listing the new project", func() bool { return has(names(b, "/api/v1/projects", "projects"), "shop") })
	waitFor("server B listing the new environment", func() bool { return has(envsOf(b), "prod") })

	// 3. A restart keeps what the database says, not what the file says.
	stop(b)
	b = start("b2", bin, func(port int, data string) string {
		return strings.Replace(fileBody(port, data), "  - { name: demo,", "  - { name: renamed-in-file,", 1)
	})
	p := names(b, "/api/v1/projects", "projects")
	if !has(p, "demo") || !has(p, "shop") || has(p, "renamed-in-file") {
		t.Errorf("after a restart with a different file, projects are %v", p)
	}

	// 4. Rollback: export, stop the servers, run the release before with the
	// exported text as its file.
	resp, err := http.Get(a.url + "/api/v1/config/export")
	if err != nil {
		t.Fatal(err)
	}
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(exported), "shop") || !strings.Contains(string(exported), "prod") {
		t.Fatalf("export: %d\n%s", resp.StatusCode, exported)
	}
	stop(a)
	stop(b)
	var doc map[string]any
	if err := yaml.Unmarshal(exported, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "objects") // what the release before cannot read: names and levels the file has no place for
	rolled, _ := yaml.Marshal(doc)
	older := start("older", old, func(port int, data string) string {
		return fmt.Sprintf("listen: 127.0.0.1:%d\ndata: %s\ndatabase: %q\nrunner: { enabled: false }\n", port, data, dbURL) + string(rolled)
	})
	if p := names(older, "/api/v1/projects", "projects"); !has(p, "demo") || !has(p, "shop") {
		t.Errorf("the release before, with the exported file, has projects %v\n%s", p, readFile(older.log))
	}
	if e := envsOf(older); !has(e, "staging") || !has(e, "prod") {
		t.Errorf("the release before, with the exported file, has environments %v", e)
	}
	if !strings.Contains(readFile(older.log), "newer version") {
		t.Logf("the older server did not say the database is newer:\n%s", readFile(older.log))
	}
}
