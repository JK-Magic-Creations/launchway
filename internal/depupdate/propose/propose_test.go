package propose

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/ecosystems"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "SSH_ASKPASS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// remote makes a bare repository holding the files on main.
func remote(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	git(t, root, "init", "--bare", "--quiet", bare)
	work := filepath.Join(root, "work")
	git(t, root, "clone", "--quiet", bare, work)
	for p, c := range files {
		full := filepath.Join(work, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "--quiet", "-m", "start")
	git(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return bare
}

// goProxy serves module versions in the proxy protocol, zips included.
func goProxy(t *testing.T, module string, versions ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/"+module+"/@v/")
		if p == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		switch {
		case p == "list":
			fmt.Fprintln(w, strings.Join(versions, "\n"))
		case strings.HasSuffix(p, ".info"):
			fmt.Fprintf(w, `{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, strings.TrimSuffix(p, ".info"))
		case strings.HasSuffix(p, ".mod"):
			fmt.Fprintf(w, "module %s\n\ngo 1.21\n", module)
		case strings.HasSuffix(p, ".zip"):
			v := strings.TrimSuffix(p, ".zip")
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			f, _ := zw.Create(module + "@" + v + "/go.mod")
			fmt.Fprintf(f, "module %s\n\ngo 1.21\n", module)
			f, _ = zw.Create(module + "@" + v + "/lib.go")
			fmt.Fprintf(f, "package lib\n\nconst Version = %q\n", v)
			zw.Close()
			w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeHost records what the proposer asks of a host.
type fakeHost struct {
	open, closed []PR
	created      []NewPR
	updated      map[int]string
	closedNums   []int
	next         int
}

func (h *fakeHost) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (h *fakeHost) Open(context.Context, string) ([]PR, error)    { return h.open, nil }
func (h *fakeHost) Closed(context.Context, string) ([]PR, error)  { return h.closed, nil }
func (h *fakeHost) Create(_ context.Context, n NewPR) (PR, error) {
	h.next++
	h.created = append(h.created, n)
	return PR{Number: h.next, Branch: n.Branch, Title: n.Title, Body: n.Body}, nil
}
func (h *fakeHost) Update(_ context.Context, num int, title, body string) error {
	if h.updated == nil {
		h.updated = map[int]string{}
	}
	h.updated[num] = body
	return nil
}
func (h *fakeHost) Close(_ context.Context, num int, _ string) error {
	h.closedNums = append(h.closedNums, num)
	return nil
}

func rd(t *testing.T, bare, branch, file string) string {
	t.Helper()
	return git(t, bare, "show", branch+":"+file)
}

const goMod = "module example.com/app\n\ngo 1.21\n\nrequire example.com/lib v1.4.0\n"

// The lock-file regeneration is tried where it runs for real: in a container, with the fake module
// proxy on the host's loopback made reachable by host networking (the container has no other way to
// it, and the test must not widen anything else). That is the default assertion; the host executor,
// which is only for a machine that is itself the sandbox, is the fallback where no container engine
// or no Go toolchain image is there.
func TestGoUpdateEndToEnd(t *testing.T) {
	if ex, ok := containerForTest(t); ok {
		goUpdateEndToEnd(t, ex, map[string]string{"GOPROXY": "", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod", "GONOSUMDB": "*", "GOPRIVATE": ""}, true)
		return
	}
	t.Log("no container engine or no Go toolchain image here: trying with the host executor")
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	goUpdateEndToEnd(t, &executor.Host{}, map[string]string{"GOPROXY": "", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod -modcacherw", "GONOSUMDB": "*", "GOPRIVATE": ""}, false)
}

// TestGoUpdateEndToEndOnTheHost keeps the host executor honest too, where there is a Go toolchain.
func TestGoUpdateEndToEndOnTheHost(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	goUpdateEndToEnd(t, &executor.Host{}, map[string]string{"GOPROXY": "", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod -modcacherw", "GONOSUMDB": "*", "GOPRIVATE": ""}, false)
}

// containerForTest is a container executor with host networking, when podman or docker and the
// default Go toolchain image are here (it never pulls an image).
func containerForTest(t *testing.T) (*executor.Container, bool) {
	t.Helper()
	for _, b := range []string{"podman", "docker"} {
		bin, err := exec.LookPath(b)
		if err != nil {
			continue
		}
		img := Toolchains{}.Defaults().Go
		if err := exec.Command(bin, "image", "exists", img).Run(); err != nil && b == "podman" {
			continue
		}
		if b == "docker" {
			if err := exec.Command(bin, "image", "inspect", img).Run(); err != nil {
				continue
			}
		}
		return &executor.Container{Bin: bin, Network: "host"}, true
	}
	return nil, false
}

func goUpdateEndToEnd(t *testing.T, ex executor.Executor, env map[string]string, isolated bool) {
	proxy := goProxy(t, "example.com/lib", "v1.4.0", "v1.4.2")
	env["GOPROXY"] = proxy.URL
	bare := remote(t, map[string]string{"go.mod": goMod, "main.go": "package main\n\nimport _ \"example.com/lib\"\n\nfunc main() {}\n", "go.sum": ""})
	scratch := t.TempDir()
	policy, _ := plan.Merge(plan.Layer{Level: "defaults", Partial: plan.Defaults()})
	ws := &Workspace{URL: "file://" + bare, Dir: filepath.Join(scratch, "ws"), Author: Author{"Launchway", "updates@launchway.invalid"}}
	runner := &ExecRunner{Exec: ex, Meta: scratch, Env: env}
	if runner.Isolated() != isolated {
		t.Fatalf("isolated is %v, want %v", runner.Isolated(), isolated)
	}
	host := &fakeHost{}

	d := plan.Update{Dep: depupdate.Dep{Ecosystem: "go", Name: "example.com/lib", Current: "v1.4.0", Constraint: "v1.4.0", Direct: true, File: "go.mod", Line: 5},
		Status: plan.StatusSecurity, Target: "v1.4.2", Kind: version.KindPatch, Security: true}
	decisions := plan.Plan(plan.Input{Policy: policy, Now: now, Immediate: true, Updates: []plan.Update{d}})
	if len(decisions) != 1 || decisions[0].Action != plan.ActionOpen {
		t.Fatalf("%+v", decisions)
	}
	p := &Proposer{Host: host, WS: ws, Runner: runner, Policy: policy, Scratch: scratch, Now: func() time.Time { return now }}
	var events []Event
	p.Audit = func(e Event) { events = append(events, e) }
	out := p.Execute(context.Background(), decisions)
	if out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	b := decisions[0].Branch
	if !strings.HasPrefix(b, "launchway/update/") {
		t.Fatal(b)
	}
	if gm := rd(t, bare, b, "go.mod"); !strings.Contains(gm, "example.com/lib v1.4.2") {
		t.Errorf("go.mod:\n%s", gm)
	}
	if gs := rd(t, bare, b, "go.sum"); !strings.Contains(gs, "example.com/lib v1.4.2 h1:") {
		t.Errorf("go.sum was not written by the Go tool:\n%s", gs)
	}
	if len(host.created) != 1 || host.created[0].Base != "main" || host.created[0].Branch != b {
		t.Fatalf("%+v", host.created)
	}
	body := host.created[0].Body
	isolation := "isolated container"
	if !isolated {
		isolation = "host executor"
	}
	for _, want := range []string{"fixes a known security problem", "example.com/lib", "`v1.4.0` to `v1.4.2`", "go get example.com/lib@v1.4.2", "go.sum", "ignore this major", "not edited by hand"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if want := map[bool]string{true: "isolated container with no secrets", false: "this run used the host executor"}[isolated]; !strings.Contains(body, want) {
		t.Errorf("the body does not say how the lock files were made (%s): want %q\n%s", isolation, want, body)
	}
	if len(events) != 1 || events[0].Action != "opened" || events[0].Number != 1 {
		t.Errorf("audit: %+v", events)
	}
	if !strings.Contains(strings.Join(host.created[0].Labels, ","), "security") {
		t.Errorf("labels %v", host.created[0].Labels)
	}
	// main is untouched
	if mm := rd(t, bare, "main", "go.mod"); !strings.Contains(mm, "v1.4.0") {
		t.Error("the base branch was changed")
	}

	// a newer version appears: the same branch is replaced and the pull request updated in place
	proxy2 := goProxy(t, "example.com/lib", "v1.4.0", "v1.4.2", "v1.4.3")
	runner.Env["GOPROXY"] = proxy2.URL
	_ = proxy2
	d.Target = "v1.4.3"
	host.open = []PR{{Number: 1, Branch: b, Body: body}}
	decisions = plan.Plan(plan.Input{Policy: policy, Now: now, Immediate: true, Updates: []plan.Update{d}, Open: OpenFrom(host.open)})
	if decisions[0].Action != plan.ActionUpdate || decisions[0].Number != 1 {
		t.Fatalf("%+v", decisions[0])
	}
	if out = p.Execute(context.Background(), decisions); out[0].Err != nil {
		t.Fatal(out[0].Err)
	}
	if gm := rd(t, bare, b, "go.mod"); !strings.Contains(gm, "v1.4.3") {
		t.Errorf("branch not replaced:\n%s", gm)
	}
	if !strings.Contains(host.updated[1], "`v1.4.3`") || len(host.created) != 1 {
		t.Errorf("updated in place expected: %v created %d", host.updated, len(host.created))
	}
	if c := strings.TrimSpace(git(t, bare, "rev-list", "--count", b)); c != "2" {
		t.Errorf("the replaced branch must be base plus one commit, has %s", c)
	}
}

func TestRefusesFilesOutsideTheUpdate(t *testing.T) {
	bare := remote(t, map[string]string{"web/package.json": `{"devDependencies":{"vite":"~5.1.0"}}`, "README.md": "x"})
	scratch := t.TempDir()
	ws := &Workspace{URL: "file://" + bare, Base: "main", Dir: filepath.Join(scratch, "ws"), Author: Author{"L", "l@example.com"}}
	if _, err := ws.Clone(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := plan.Update{Dep: depupdate.Dep{Ecosystem: "npm", Name: "vite", Current: "5.1.4", Constraint: "~5.1.0", Direct: true, File: "web/package.json", Group: "dev"}, Status: plan.StatusUpdate, Target: "5.2.0", Kind: version.KindMinor}
	policy, _ := plan.Merge(plan.Layer{Level: "defaults", Partial: plan.Defaults()})
	pr := plan.Plan(plan.Input{Policy: policy, Now: now, Immediate: true, Updates: []plan.Update{u}})[0].Proposal
	work, err := Build(pr, ws.Read, Toolchains{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(work.Edits["web/package.json"], `"vite":"~5.2.0"`) && !strings.Contains(work.Edits["web/package.json"], "~5.2.0") {
		t.Errorf("manifest edit: %v", work.Edits)
	}
	evil := stepFunc(func(_ context.Context, st Step, tree string, _ func(string)) error {
		os.WriteFile(filepath.Join(tree, "web", "package-lock.json"), []byte("{}"), 0o644)
		return os.WriteFile(filepath.Join(tree, "README.md"), []byte("pwned"), 0o644)
	})
	_, err = Apply(context.Background(), ws, pr, work, evil, "launchway/update", scratch)
	if err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("a step that changes other files must be refused: %v", err)
	}
	if out := git(t, bare, "branch", "--list"); strings.Contains(out, "launchway") {
		t.Errorf("nothing may be pushed: %s", out)
	}
	good := stepFunc(func(_ context.Context, st Step, tree string, _ func(string)) error {
		return os.WriteFile(filepath.Join(tree, "web", "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o644)
	})
	a, err := Apply(context.Background(), ws, pr, work, good, "launchway/update", scratch)
	if err != nil || strings.Join(a.Changed, ",") != "web/package-lock.json,web/package.json" {
		t.Fatalf("%v %+v", err, a)
	}
}

type stepFunc func(ctx context.Context, st Step, tree string, log func(string)) error

func (f stepFunc) Run(ctx context.Context, st Step, tree string, log func(string)) error {
	return f(ctx, st, tree, log)
}

func TestNeverPushesOutsideThePrefix(t *testing.T) {
	bare := remote(t, map[string]string{"a": "1"})
	ws := &Workspace{URL: "file://" + bare, Base: "main", Dir: filepath.Join(t.TempDir(), "ws"), Author: Author{"L", "l@example.com"}}
	if _, err := ws.Clone(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"main", "release/1", "launchwayx/update"} {
		if err := ws.Push(context.Background(), b, "launchway/update"); err == nil {
			t.Errorf("pushing %s must be refused", b)
		}
	}
}

func TestPathsMustStayInside(t *testing.T) {
	ws := &Workspace{Dir: t.TempDir()}
	for _, p := range []string{"../x", "/etc/passwd", ".git/config", ".", "a/../../b"} {
		if _, err := ws.safe(p); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

func TestBuildPerEcosystem(t *testing.T) {
	files := map[string]string{
		"Dockerfile":               "ARG NODE_IMAGE=docker.io/library/node:24.22.3-alpine3.23\nFROM ${NODE_IMAGE} AS web\n",
		"requirements.txt":         "requests==2.28.1 \\\n    --hash=sha256:aaaa\n",
		"py/pyproject.toml":        "[project]\ndependencies = [\"httpx>=0.27\"]\n",
		"py/uv.lock":               "version = 1\n[[package]]\nname = \"httpx\"\nversion = \"0.27.2\"\n",
		".github/workflows/ci.yml": "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n",
	}
	read := func(p string) (string, bool) { s, ok := files[p]; return s, ok }
	res, err := depupdate.Discover(mapFS(files), ecosystems.Parsers(nil), depupdate.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var ups []plan.Update
	for _, d := range res.Deps {
		u := plan.Update{Dep: d, Status: plan.StatusUpdate, Kind: version.KindMinor}
		switch d.Name {
		case "docker.io/library/node":
			u.Target = "24.23.0-alpine3.23"
		case "requests":
			u.Target = "2.31.0"
		case "httpx":
			u.Target = "0.28.0"
		case "actions/checkout":
			u.Target, u.Digest = "v7.0.2", strings.Repeat("f", 40)
		}
		ups = append(ups, u)
	}
	policy, _ := plan.Merge(plan.Layer{Level: "defaults", Partial: plan.Defaults()})
	pr := plan.Plan(plan.Input{Policy: policy, Now: now, Immediate: true, Updates: ups})
	got := map[string]Work{}
	for _, d := range pr {
		w, err := Build(d.Proposal, read, Toolchains{})
		if err != nil {
			t.Fatalf("%s: %v", d.Key, err)
		}
		k := d.Ecosystem
		if d.Directory != "" {
			k += ":" + d.Directory
		}
		got[k] = w
	}
	if e := got["container"].Edits["Dockerfile"]; !strings.Contains(e, "ARG NODE_IMAGE=docker.io/library/node:24.23.0-alpine3.23\n") {
		t.Errorf("dockerfile: %q", e)
	}
	if w := got["python"]; !strings.Contains(strings.Join(w.Notes, " "), "regenerate") || !strings.Contains(w.Edits["requirements.txt"], "requests==2.31.0") || strings.Contains(w.Edits["requirements.txt"], "aaaa") {
		t.Errorf("python: %+v", w)
	}
	py := got["python:py"]
	hasUV := false
	for _, s := range py.Steps {
		if strings.Contains(strings.Join(s.Commands[0], " "), "uv lock --upgrade-package httpx") && s.Allowed[0] == "py/uv.lock" {
			hasUV = true
		}
	}
	if !hasUV || !strings.Contains(py.Edits["py/pyproject.toml"], ">=0.28.0") {
		t.Errorf("uv step: %+v", py)
	}
	if e := got["workflow:.github/workflows"].Edits[".github/workflows/ci.yml"]; !strings.Contains(e, "checkout@"+strings.Repeat("f", 40)+" # v7.0.2") {
		t.Errorf("workflow: %q", e)
	}
	body := Body(pr[0].Proposal, Work{Files: []string{"x"}}, now, policy)
	if k, tg, ok := ParseMarker(body); !ok || k != pr[0].Key || len(tg) != len(pr[0].Updates) {
		t.Errorf("marker: %v %v %v", k, tg, ok)
	}
}

func TestIgnoreFromClosedPullRequests(t *testing.T) {
	mk := func(reason string) PR {
		body := Marker + `{"key":"npm-vite","targets":{"vite":"6.0.0"}}` + "`"
		return PR{Number: 3, Body: body, Reason: reason}
	}
	eco := func(k string) string { return strings.SplitN(k, "-", 2)[0] }
	if k := IgnoreKeys([]PR{mk("")}, eco); !k["npm|vite|6.0.0"] || len(k) != 1 {
		t.Errorf("default is this version: %v", k)
	}
	if k := IgnoreKeys([]PR{mk("Ignore this major, please")}, eco); !k["npm|vite|major-6"] {
		t.Errorf("%v", k)
	}
	if k := IgnoreKeys([]PR{mk("ignore this dependency")}, eco); !k["npm|vite|*"] {
		t.Errorf("%v", k)
	}
	merged := mk("")
	merged.Merged = true
	if k := IgnoreKeys([]PR{merged}, eco); len(k) != 0 {
		t.Errorf("a merged one is not an ignore: %v", k)
	}
}

func TestCloseDecision(t *testing.T) {
	policy, _ := plan.Merge(plan.Layer{Level: "defaults", Partial: plan.Defaults()})
	host := &fakeHost{}
	p := &Proposer{Host: host, Policy: policy}
	out := p.Execute(context.Background(), plan.Plan(plan.Input{Policy: policy, Now: now, Open: []plan.Open{{Key: "go-gone", Number: 9}}}))
	if out[0].Err != nil || len(host.closedNums) != 1 || host.closedNums[0] != 9 {
		t.Errorf("%+v %v", out, host.closedNums)
	}
	host.closedNums = nil
	p.DryRun = true
	p.Execute(context.Background(), plan.Plan(plan.Input{Policy: policy, Now: now, Open: []plan.Open{{Key: "go-gone", Number: 9}}}))
	if len(host.closedNums) != 0 {
		t.Error("a dry run changes nothing")
	}
}

type mapFS map[string]string

func (m mapFS) Open(name string) (fs.File, error) {
	return fstest.MapFS(toMapFS(m)).Open(name)
}

func toMapFS(m mapFS) fstest.MapFS {
	out := fstest.MapFS{}
	for k, v := range m {
		out[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return out
}
