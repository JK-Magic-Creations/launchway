package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote makes a bare repository with one commit on main, as a Git host
// would hold it, and returns its path.
func remote(t *testing.T, dir, pipelineYAML string) string {
	t.Helper()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	run(t, work, "git", "init", "-q", "-b", "main")
	commit(t, work, pipelineYAML, "first")
	bare := filepath.Join(dir, "origin.git")
	run(t, dir, "git", "clone", "-q", "--bare", work, bare)
	return bare
}

func newTestEngine(t *testing.T, addr string) *engine.Engine {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng
}

func manager(t *testing.T, eng *engine.Engine, data, url string, push *PushSettings) *Manager {
	t.Helper()
	m, err := NewManager(eng, data, []Project{{Name: "app", URL: url, Push: push}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// noTempLeft fails if a save left anything in the data directory's tmp.
func noTempLeft(t *testing.T, data string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(data, "tmp"))
	if len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temporary files left behind: %v", names)
	}
}

const edited = `pipeline: app
on:
  push: { branches: [main] }
steps:
  test:
    description: Run the tests
    image: alpine
    run: echo edited
`

var saveTime = time.Date(2026, 11, 14, 9, 30, 5, 0, time.UTC)

// A draft runs edited text against the tip of a branch; the file in Git is
// not touched.
func TestDraftAgainstBranchTip(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	eng := newTestEngine(t, testdb.New(t))
	m := manager(t, eng, filepath.Join(dir, "data"), origin, nil)
	ctx := context.Background()
	tip := gitIn(t, origin, "rev-parse", "main")

	f, err := m.ReadPipelineFile(ctx, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Ref != "main" || f.Kind != "branch" || f.SHA != tip || !f.Exists || f.Text != pl || f.Path != ".launchway/pipeline.yaml" {
		t.Fatalf("file %+v", f)
	}
	r, err := m.StartDraft(ctx, DraftRequest{Project: "app", Ref: "main", Text: edited, Actor: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Draft || r.Event != "draft" || r.SHA != tip || r.Branch != "main" || r.Actor != "dana" || r.PipelineHash != engine.DraftHash(edited) {
		t.Fatalf("run %+v", r)
	}
	d, err := eng.GetRun(r.ID)
	if err != nil || d.Source.Kind != "git" || d.Source.URL != origin || d.Graph.Steps[0].Description != "Run the tests" {
		t.Fatalf("detail %+v %v", d, err)
	}
	text, _, _ := eng.DraftText(r.ID)
	if text.Text != edited {
		t.Fatalf("kept %q", text.Text)
	}
	if got, _ := m.ReadPipeline(ctx, "app", tip); got != pl {
		t.Fatalf("the file in Git changed: %q", got)
	}

	// Problems are reported with lines, and nothing is started.
	_, err = m.StartDraft(ctx, DraftRequest{Project: "app", Ref: "main", Text: "pipeline: app\nsteps:\n  a: {image: x}\n", Actor: "dana"})
	var ps pipeline.Problems
	if !errors.As(err, &ps) || ps[0].Line != 3 {
		t.Fatalf("problems %v", err)
	}
	if _, err := m.StartDraft(ctx, DraftRequest{Project: "app", Ref: "nope", Text: edited}); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("missing branch: %v", err)
	}
	if runs, _ := eng.ListRuns(engine.RunFilter{}); len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
}

// A branch without a pipeline file opens with a starter pipeline.
func TestReadWithoutPipelineGivesStarter(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	run(t, work, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "README"), []byte("hi"), 0o644)
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-q", "-m", "no pipeline")
	m := manager(t, newTestEngine(t, testdb.New(t)), filepath.Join(dir, "data"), work, nil)
	f, err := m.ReadPipelineFile(context.Background(), "app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if f.Exists || !strings.Contains(f.Text, "pipeline: app") || f.Path != ".launchway/pipeline.yaml" {
		t.Fatalf("file %+v", f)
	}
	if _, err := pipeline.Parse([]byte(f.Text)); err != nil {
		t.Fatalf("the starter has problems: %v", err)
	}
}

// Saving creates a branch under the prefix, authored by the person and
// committed by Launchway, with only the pipeline file changed.
func TestSaveCreatesPrefixedBranch(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	data := filepath.Join(dir, "data")
	m := manager(t, newTestEngine(t, testdb.New(t)), data, origin, &PushSettings{})
	ctx := context.Background()
	base := gitIn(t, origin, "rev-parse", "main")

	res, err := m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: base, Text: edited,
		Message: "Describe the test step\n\nSo people know what it does.", Now: saveTime,
		Author: Author{User: "Dana.Smith@Example.com", Name: "Dana <Smith>", Email: "dana@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "launchway/dana.smith-example.com/20261114-093005" || res.Direct || res.BaseSHA != base || res.PullRequestURL != "" {
		t.Fatalf("result %+v", res)
	}
	if got := gitIn(t, origin, "rev-parse", res.Branch); got != res.SHA {
		t.Fatalf("branch at %s, want %s", got, res.SHA)
	}
	if got := gitIn(t, origin, "rev-parse", "main"); got != base {
		t.Fatal("main moved")
	}
	show := gitIn(t, origin, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%P|%B", res.Branch)
	want := "Dana Smith <dana@example.com>|Launchway <launchway@users.noreply.launchway>|" + base + "|Describe the test step\n\nSo people know what it does."
	if show != want {
		t.Fatalf("commit:\n%s\nwant:\n%s", show, want)
	}
	if got := gitIn(t, origin, "show", res.Branch+":.launchway/pipeline.yaml"); got != strings.TrimSpace(edited) {
		t.Fatalf("file %q", got)
	}
	if got := gitIn(t, origin, "diff", "--name-only", base, res.Branch); got != ".launchway/pipeline.yaml" {
		t.Fatalf("changed files: %q", got)
	}
	noTempLeft(t, data)

	// The new branch is in the branch list at once.
	refs, _ := m.Refs(ctx, "app")
	found := false
	for _, r := range refs {
		found = found || r.Name == res.Branch
	}
	if !found {
		t.Fatalf("refs %+v", refs)
	}

	// Without a name or address from the provider: the username and a
	// no-reply address; and the default message.
	res, err = m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: base, Text: edited + "# again\n",
		Now: saveTime.Add(time.Second), Author: Author{User: "otto"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, origin, "log", "-1", "--format=%an <%ae>|%s", res.Branch); got != "otto <otto@users.noreply.launchway>|"+DefaultCommitMessage {
		t.Fatalf("commit %q", got)
	}
}

// A direct save commits to the branch itself, only where allow_direct says.
func TestDirectSaveOnlyWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	data := filepath.Join(dir, "data")
	eng := newTestEngine(t, testdb.New(t))
	ctx := context.Background()
	base := gitIn(t, origin, "rev-parse", "main")
	req := SaveRequest{Project: "app", BaseRef: "main", BaseSHA: base, Text: edited, Branch: "main", Now: saveTime, Author: Author{User: "dana"}}

	m := manager(t, eng, data, origin, &PushSettings{BranchPrefix: "edits/", AllowDirect: []string{"release/*"}})
	if _, err := m.Save(ctx, req); !errors.Is(err, engine.ErrForbidden) || !strings.Contains(err.Error(), "may not be committed to directly") {
		t.Fatalf("direct save to main: %v", err)
	}
	other := req
	other.Branch = "somewhere-else"
	if _, err := m.Save(ctx, other); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("save to another branch: %v", err)
	}
	if got := gitIn(t, origin, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main" {
		t.Fatalf("refs after refusals: %s", got)
	}

	m = manager(t, eng, data, origin, &PushSettings{AllowDirect: []string{"main"}})
	res, err := m.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Direct || res.Branch != "main" || gitIn(t, origin, "rev-parse", "main") != res.SHA ||
		gitIn(t, origin, "rev-parse", "main~1") != base {
		t.Fatalf("result %+v", res)
	}
	noTempLeft(t, data)

	// A project without push settings cannot be saved to at all.
	m = manager(t, eng, data, origin, nil)
	if _, err := m.Save(ctx, req); !errors.Is(err, ErrNotSavable) {
		t.Fatalf("no push settings: %v", err)
	}
}

// Text opened at a commit that is no longer the branch's tip is refused,
// and nothing is pushed. So is text with problems.
func TestStaleAndInvalidSavesPushNothing(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	data := filepath.Join(dir, "data")
	m := manager(t, newTestEngine(t, testdb.New(t)), data, origin, &PushSettings{AllowDirect: []string{"main"}})
	ctx := context.Background()
	old := gitIn(t, origin, "rev-parse", "main")

	// Someone else pushes to main.
	work := filepath.Join(dir, "work")
	commit(t, work, "", "someone else")
	run(t, work, "git", "push", "-q", origin, "main")
	before := gitIn(t, origin, "for-each-ref", "--format=%(refname) %(objectname)")

	for _, branch := range []string{"", "main"} {
		_, err := m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: old, Text: edited, Branch: branch, Now: saveTime, Author: Author{User: "dana"}})
		if !errors.Is(err, engine.ErrConflict) || !strings.Contains(err.Error(), "has moved on") || !strings.Contains(err.Error(), "someone else") {
			t.Fatalf("stale save to %q: %v", branch, err)
		}
	}
	_, err := m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: gitIn(t, origin, "rev-parse", "main"),
		Text: "pipeline: app\nsteps:\n  a: {image: x}\n", Now: saveTime, Author: Author{User: "dana"}})
	var ps pipeline.Problems
	if !errors.As(err, &ps) {
		t.Fatalf("invalid save: %v", err)
	}
	for _, bad := range []string{"", "main", "abc", strings.Repeat("g", 40)} {
		if _, err := m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: bad, Text: edited, Author: Author{User: "dana"}}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("base_sha %q: %v", bad, err)
		}
	}
	if after := gitIn(t, origin, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Fatalf("refs changed:\n%s\n%s", before, after)
	}
	noTempLeft(t, data)

	// Forced, text with problems is saved.
	res, err := m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: gitIn(t, origin, "rev-parse", "main"),
		Text: "pipeline: app\nsteps:\n  a: {image: x}\n", Force: true, Now: saveTime, Author: Author{User: "ada"}})
	if err != nil || !strings.HasPrefix(res.Branch, "launchway/ada/") {
		t.Fatalf("forced save: %+v %v", res, err)
	}
}

// A repository that may be read but not written: the push fails with Git's
// own explanation, credentials removed, and nothing is left behind.
func TestPushFailureIsExplained(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	gitIn(t, origin, "update-server-info")
	// Served read-only over "dumb" HTTP, which cannot be pushed to.
	srv := httptest.NewServer(http.StripPrefix("/git/", http.FileServer(http.Dir(dir))))
	defer srv.Close()
	url := strings.Replace(srv.URL, "http://", "http://robot:s3cret-t0ken@", 1) + "/git/origin.git"
	data := filepath.Join(dir, "data")
	m := manager(t, newTestEngine(t, testdb.New(t)), data, url, &PushSettings{})
	ctx := context.Background()
	f, err := m.ReadPipelineFile(ctx, "app", "main")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: f.SHA, Text: edited, Now: saveTime, Author: Author{User: "dana"}})
	var pe *PushError
	if !errors.As(err, &pe) {
		t.Fatalf("err %T %v", err, err)
	}
	if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "robot") {
		t.Fatalf("credentials in the error: %v", err)
	}
	if !strings.Contains(err.Error(), "***@127.0.0.1") {
		t.Fatalf("the error does not say where: %v", err)
	}
	t.Logf("push error: %v", err)
	noTempLeft(t, data)
	if got := gitIn(t, origin, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main" {
		t.Fatalf("refs: %s", got)
	}

	// A local repository that cannot be written to.
	ro := remote(t, filepath.Join(dir, "ro"), pl)
	if err := filepath.Walk(filepath.Join(ro, "objects"), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			return os.Chmod(p, 0o555)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.Walk(filepath.Join(ro, "objects"), func(p string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	data2 := filepath.Join(dir, "data2")
	m = manager(t, newTestEngine(t, testdb.New(t)), data2, ro, &PushSettings{})
	_, err = m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: gitIn(t, ro, "rev-parse", "main"), Text: edited, Now: saveTime, Author: Author{User: "dana"}})
	if !errors.As(err, &pe) {
		t.Fatalf("read-only repository: %T %v", err, err)
	}
	t.Logf("read-only error: %v", err)
	noTempLeft(t, data2)
}

// Two servers saving at once, each with its own mirror: new branches never
// collide, and of two direct saves from the same base, one wins and the
// other gets a clean conflict.
func TestConcurrentSavesFromTwoServers(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	addr := testdb.New(t)
	push := &PushSettings{AllowDirect: []string{"main"}}
	ms := []*Manager{
		manager(t, newTestEngine(t, addr), filepath.Join(dir, "a"), origin, push),
		manager(t, newTestEngine(t, addr), filepath.Join(dir, "b"), origin, push),
	}
	ctx := context.Background()
	base := gitIn(t, origin, "rev-parse", "main")

	race := func(branch string) ([]*SaveResult, []error) {
		results := make([]*SaveResult, len(ms))
		errs := make([]error, len(ms))
		var wg sync.WaitGroup
		for i, m := range ms {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Same person, same second, different text.
				results[i], errs[i] = m.Save(ctx, SaveRequest{Project: "app", BaseRef: "main", BaseSHA: base,
					Text: edited + fmt.Sprintf("# from server %d\n", i), Branch: branch, Now: saveTime, Author: Author{User: "dana"}})
			}()
		}
		wg.Wait()
		return results, errs
	}

	results, errs := race("")
	for i, err := range errs {
		if err != nil {
			t.Fatalf("new branch from server %d: %v", i, err)
		}
	}
	if results[0].Branch == results[1].Branch || results[0].SHA == results[1].SHA {
		t.Fatalf("both saves went to %s", results[0].Branch)
	}
	for i, r := range results {
		if !strings.HasPrefix(r.Branch, "launchway/dana/20261114-093005") || gitIn(t, origin, "rev-parse", r.Branch) != r.SHA {
			t.Fatalf("server %d: %+v", i, r)
		}
		if !strings.Contains(gitIn(t, origin, "show", r.Branch+":.launchway/pipeline.yaml"), fmt.Sprintf("from server %d", i)) {
			t.Fatalf("server %d's branch holds the other's text", i)
		}
	}

	results, errs = race("main")
	won, lost := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
			if gitIn(t, origin, "rev-parse", "main") != results[i].SHA {
				t.Fatalf("server %d won, but main is elsewhere", i)
			}
		case errors.Is(err, engine.ErrConflict):
			lost++
		default:
			t.Fatalf("server %d: %v", i, err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("%d saves won and %d lost: %v", won, lost, errs)
	}
	if gitIn(t, origin, "rev-parse", "main~1") != base {
		t.Fatal("main is not one commit on from its base")
	}
	noTempLeft(t, filepath.Join(dir, "a"))
	noTempLeft(t, filepath.Join(dir, "b"))
}

func TestPushSettingsAreChecked(t *testing.T) {
	eng := newTestEngine(t, testdb.New(t))
	for _, bad := range []PushSettings{
		{BranchPrefix: "-x/"}, {BranchPrefix: "a..b/"}, {BranchPrefix: "a b/"}, {BranchPrefix: "x.lock/"},
		{AllowDirect: []string{"[main"}},
	} {
		if _, err := NewManager(eng, t.TempDir(), []Project{{Name: "app", URL: "/r", Push: &bad}}, nil); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	m, err := NewManager(eng, t.TempDir(), []Project{{Name: "app", URL: "/r", Push: &PushSettings{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := m.Push("app"); p == nil || p.BranchPrefix != DefaultBranchPrefix {
		t.Fatalf("push %+v", p)
	}
}

// A save interrupted by a crash leaves a directory that the next start
// removes once it is old.
func TestStaleSaveDirectoriesAreRemoved(t *testing.T) {
	data := t.TempDir()
	old := filepath.Join(data, "tmp", "save-123")
	fresh := filepath.Join(data, "tmp", "save-456")
	os.MkdirAll(filepath.Join(old, "repo.git"), 0o755)
	os.MkdirAll(fresh, 0o755)
	long := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, long, long)
	if _, err := NewManager(newTestEngine(t, testdb.New(t)), data, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the old directory is still there")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a directory of a save that may be in progress was removed")
	}
}

func TestPullRequestURL(t *testing.T) {
	for _, c := range []struct{ remote, want string }{
		{"https://github.com/acme/web.git", "https://github.com/acme/web/compare/main...launchway/dana/1?expand=1"},
		{"https://bot:tok@github.com/acme/web", "https://github.com/acme/web/compare/main...launchway/dana/1?expand=1"},
		{"git@github.com:acme/web.git", "https://github.com/acme/web/compare/main...launchway/dana/1?expand=1"},
		{"ssh://git@gitlab.example.com:2222/team/sub/web.git",
			"https://gitlab.example.com/team/sub/web/-/merge_requests/new?merge_request%5Bsource_branch%5D=launchway%2Fdana%2F1&merge_request%5Btarget_branch%5D=main"},
		{"https://gitea.example.com:3000/acme/web.git", "https://gitea.example.com:3000/acme/web/compare/main...launchway/dana/1"},
		{"https://codeberg.org/acme/web", "https://codeberg.org/acme/web/compare/main...launchway/dana/1"},
		{"https://git.example.com/acme/web.git", ""},
		{"/srv/git/web.git", ""},
		{"file:///srv/git/web.git", ""},
		{"https://github.com/justone", ""},
	} {
		if got := PullRequestURL(c.remote, "main", "launchway/dana/1"); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.remote, got, c.want)
		}
	}
}
