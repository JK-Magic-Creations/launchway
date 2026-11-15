package gitops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// server is a bare repository standing in for the GitOps repository.
type server struct {
	t    *testing.T
	path string
}

// newServer makes a bare repository whose main branch holds app.yaml and
// other.yaml.
func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{t: t, path: filepath.Join(t.TempDir(), "gitops.git")}
	run(t, "", "init", "--quiet", "--bare", "--initial-branch=main", s.path)
	s.commit(map[string]string{"app.yaml": "image: beacon:1.0\n", "other.yaml": "x: 1\n"}, "Initial")
	return s
}

// run runs git outside the code under test, with an author of its own.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Someone", "-c", "user.email=someone@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit pushes a commit from another clone, as someone else would.
func (s *server) commit(files map[string]string, msg string) string {
	s.t.Helper()
	dir := filepath.Join(s.t.TempDir(), "other")
	run(s.t, "", "clone", "--quiet", s.path, dir)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	run(s.t, dir, "add", "-A")
	run(s.t, dir, "commit", "--quiet", "--allow-empty", "-m", msg)
	run(s.t, dir, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	return run(s.t, dir, "rev-parse", "HEAD")
}

func (s *server) ref() string {
	s.t.Helper()
	return run(s.t, s.path, "rev-parse", "refs/heads/main")
}

func (s *server) file(name string) string {
	s.t.Helper()
	return run(s.t, s.path, "show", "refs/heads/main:"+name)
}

func (s *server) message(commit string) string {
	s.t.Helper()
	return run(s.t, s.path, "log", "-1", "--format=%B", commit)
}

func (s *server) isAncestor(commit string) bool {
	s.t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, "refs/heads/main")
	cmd.Dir = s.path
	return cmd.Run() == nil
}

// logs collects a Repo's progress lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (s *server) repo(t *testing.T) (*Repo, *logs) {
	l := &logs{}
	return &Repo{
		URL:    s.path,
		Branch: "main",
		Dir:    filepath.Join(t.TempDir(), "clone"),
		Author: Author{Name: "Launchway", Email: "launchway@example.com"},
		Log:    l.add,
	}, l
}

// set is an edit that replaces a file's contents.
func set(content string) Edit {
	return func(path string, data []byte) ([]byte, error) {
		if data == nil {
			return nil, fmt.Errorf("%s does not exist", path)
		}
		return []byte(content), nil
	}
}

func deploy(dep, image string) ChangeSet {
	return ChangeSet{
		Deployment: dep,
		Message:    "Deploy beacon " + image + " to prod\n\nRequested by the release run.",
		Files:      []FileEdit{{Path: "app.yaml", Edit: set("image: beacon:" + image + "\n")}},
	}
}

func TestApplyCommitsPushesAndIsFoundFromAFreshClone(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, l := s.repo(t)
	res, err := r.Apply(ctx, deploy("dep_abc", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Existing || res.Commit == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Files) != 1 || res.Files[0] != "app.yaml" {
		t.Fatalf("files = %v", res.Files)
	}
	if s.ref() != res.Commit {
		t.Fatalf("server is at %s, want %s", s.ref(), res.Commit)
	}
	if got := s.file("app.yaml"); got != "image: beacon:2.0" {
		t.Fatalf("app.yaml = %q", got)
	}
	want := "Deploy beacon 2.0 to prod\n\nRequested by the release run.\n\nLaunchway-Deployment: dep_abc"
	if got := s.message(res.Commit); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if !strings.Contains(l.all(), "Committed "+res.Commit[:7]+" to main: Deploy beacon 2.0 to prod") {
		t.Fatalf("log = %q", l.all())
	}

	// Another machine retries the same action with a clone of its own.
	before := s.ref()
	r2, _ := s.repo(t)
	again, err := r2.Apply(ctx, deploy("dep_abc", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Existing || !again.Changed || again.Commit != res.Commit || len(again.Files) != 0 {
		t.Fatalf("second result = %+v, want existing %s", again, res.Commit)
	}
	if s.ref() != before {
		t.Fatal("the second Apply pushed")
	}

	// The same clone used again is reused.
	if third, err := r.Apply(ctx, deploy("dep_abc", "2.0")); err != nil || !third.Existing {
		t.Fatalf("third = %+v, %v", third, err)
	}
}

func TestApplyWhenFilesAlreadyHoldTheValues(t *testing.T) {
	s := newServer(t)
	r, _ := s.repo(t)
	before := s.ref()
	res, err := r.Apply(context.Background(), deploy("dep_same", "1.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Existing || res.Commit != before || len(res.Files) != 0 {
		t.Fatalf("result = %+v, want unchanged at %s", res, before)
	}
	if s.ref() != before {
		t.Fatal("pushed although nothing changed")
	}
}

func TestApplySamePathTwiceAndOnlyChangedFilesListed(t *testing.T) {
	s := newServer(t)
	r, _ := s.repo(t)
	cs := ChangeSet{Deployment: "dep_two", Message: "Two edits", Files: []FileEdit{
		{Path: "app.yaml", Edit: set("image: beacon:9.9\n")},
		{Path: "./app.yaml", Edit: func(_ string, data []byte) ([]byte, error) { return append(data, "replicas: 2\n"...), nil }},
		{Path: "other.yaml", Edit: set("x: 1\n")}, // unchanged
	}}
	res, err := r.Apply(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0] != "app.yaml" {
		t.Fatalf("files = %v", res.Files)
	}
	if got := s.file("app.yaml"); got != "image: beacon:9.9\nreplicas: 2" {
		t.Fatalf("app.yaml = %q", got)
	}
}

func TestApplyRetriesWhenTheBranchMoves(t *testing.T) {
	s := newServer(t)
	r, l := s.repo(t)
	var foreign string
	r.beforePush = func() {
		if foreign == "" {
			foreign = s.commit(map[string]string{"other.yaml": "x: 2\n"}, "Someone else's change")
		}
	}
	res, err := r.Apply(context.Background(), deploy("dep_race", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if s.ref() != res.Commit {
		t.Fatal("the server is not at the commit made")
	}
	if !s.isAncestor(foreign) {
		t.Fatal("the foreign commit was lost")
	}
	if s.file("other.yaml") != "x: 2" || s.file("app.yaml") != "image: beacon:2.0" {
		t.Fatal("files are not both changed")
	}
	if parent := run(t, s.path, "rev-parse", res.Commit+"^"); parent != foreign {
		t.Fatalf("the change is on %s, want on the foreign commit %s", parent, foreign)
	}
	if !strings.Contains(l.all(), "moved on the server") {
		t.Fatalf("log = %q", l.all())
	}
}

func TestApplyGivesUpWhenTheBranchKeepsMoving(t *testing.T) {
	s := newServer(t)
	r, _ := s.repo(t)
	r.MaxPushTries = 3
	n := 0
	r.beforePush = func() {
		n++
		s.commit(map[string]string{"other.yaml": fmt.Sprintf("x: %d\n", n+10)}, "Busy")
	}
	_, err := r.Apply(context.Background(), deploy("dep_busy", "2.0"))
	if err == nil || !strings.Contains(err.Error(), "gave up after 3 tries") {
		t.Fatalf("err = %v", err)
	}
	if n != 3 {
		t.Fatalf("pushed %d times, want 3", n)
	}
	if s.file("app.yaml") != "image: beacon:1.0" {
		t.Fatal("the change reached the server")
	}
}

func TestApplyEditErrorLeavesTheServerUntouched(t *testing.T) {
	s := newServer(t)
	r, _ := s.repo(t)
	before := s.ref()
	cs := ChangeSet{Deployment: "dep_bad", Message: "Bad", Files: []FileEdit{
		{Path: "app.yaml", Edit: set("image: beacon:2.0\n")},
		{Path: "missing.yaml", Edit: set("y: 1\n")},
	}}
	_, err := r.Apply(context.Background(), cs)
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
	if s.ref() != before {
		t.Fatal("the server moved")
	}
	if out := run(t, r.Dir, "status", "--porcelain"); out != "" {
		t.Fatalf("clone left dirty: %q", out)
	}
	// A path outside the repository is refused.
	for _, p := range []string{"../escape.yaml", "/etc/passwd", ".git/config"} {
		cs := ChangeSet{Deployment: "dep_bad", Message: "Bad", Files: []FileEdit{{Path: p, Edit: set("x")}}}
		if _, err := r.Apply(context.Background(), cs); err == nil {
			t.Fatalf("%s: no error", p)
		}
	}
	if s.ref() != before {
		t.Fatal("the server moved")
	}
}

func TestTrailerMatchesWholeLinesOnly(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	abc := s.commit(map[string]string{"app.yaml": "image: beacon:5.0\n"}, "Deploy\n\nLaunchway-Deployment: dep_abc")
	r, _ := s.repo(t)
	if got, err := r.Find(ctx, "dep_ab"); err != nil || got != "" {
		t.Fatalf("Find(dep_ab) = %q, %v", got, err)
	}
	if got, err := r.Find(ctx, "dep_abc"); err != nil || got != abc {
		t.Fatalf("Find(dep_abc) = %q, %v; want %s", got, err, abc)
	}
	res, err := r.Apply(ctx, deploy("dep_ab", "6.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Existing || !res.Changed || res.Commit == abc {
		t.Fatalf("dep_ab matched dep_abc: %+v", res)
	}
	// A deployment whose ID is a longer form of another is not matched either.
	if got, err := r.Find(ctx, "dep_abcd"); err != nil || got != "" {
		t.Fatalf("Find(dep_abcd) = %q, %v", got, err)
	}
}

func TestFindAndApplyIgnoreTheRevertCommit(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_x", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.Find(ctx, "dep_x"); err != nil || got != applied.Commit {
		t.Fatalf("Find = %q, %v; want %s", got, err, applied.Commit)
	}
	rev, err := r.Revert(ctx, "dep_x", applied.Commit, "Roll back beacon 2.0")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := s.repo(t)
	if got, err := r2.Find(ctx, "dep_x"); err != nil || got != applied.Commit {
		t.Fatalf("Find after revert = %q, %v; want the original %s, not the revert %s", got, err, applied.Commit, rev.Commit)
	}
	before := s.ref()
	again, err := r2.Apply(ctx, deploy("dep_x", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Existing || again.Commit != applied.Commit || s.ref() != before {
		t.Fatalf("Apply after revert = %+v", again)
	}
	if got, err := r2.Find(ctx, "dep_none"); err != nil || got != "" {
		t.Fatalf("Find(dep_none) = %q, %v", got, err)
	}
}

func TestHistoryDepthBoundsTheSearch(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_old", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	s.commit(map[string]string{"other.yaml": "x: 5\n"}, "Later")
	s.commit(map[string]string{"other.yaml": "x: 6\n"}, "Later still")
	r.HistoryDepth = 2
	if got, err := r.Find(ctx, "dep_old"); err != nil || got != "" {
		t.Fatalf("Find beyond the depth = %q, %v", got, err)
	}
	r.HistoryDepth = 3
	if got, err := r.Find(ctx, "dep_old"); err != nil || got != applied.Commit {
		t.Fatalf("Find within the depth = %q, %v", got, err)
	}
}

func TestRevert(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, l := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_r", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Revert(ctx, "dep_r", applied.Commit, "Roll back beacon 2.0\n\nThe health gate failed.")
	if err != nil {
		t.Fatal(err)
	}
	if res.Existing || s.ref() != res.Commit {
		t.Fatalf("result = %+v", res)
	}
	if s.file("app.yaml") != "image: beacon:1.0" {
		t.Fatalf("app.yaml = %q", s.file("app.yaml"))
	}
	want := "Roll back beacon 2.0\n\nThe health gate failed.\n\nThis reverts commit " + applied.Commit +
		".\n\nLaunchway-Deployment: dep_r\nLaunchway-Revert: dep_r"
	if got := s.message(res.Commit); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if !strings.Contains(l.all(), "Committed "+res.Commit[:7]+" to main: Roll back beacon 2.0") {
		t.Fatalf("log = %q", l.all())
	}

	// Retried from another machine: found, nothing pushed.
	before := s.ref()
	r2, _ := s.repo(t)
	again, err := r2.Revert(ctx, "dep_r", applied.Commit, "Roll back beacon 2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Existing || again.Commit != res.Commit || s.ref() != before {
		t.Fatalf("second revert = %+v", again)
	}
}

func TestRevertAfterAnUnrelatedChange(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_u", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	foreign := s.commit(map[string]string{"other.yaml": "x: 3\n"}, "Unrelated")
	res, err := r.Revert(ctx, "dep_u", applied.Commit, "Roll back")
	if err != nil {
		t.Fatal(err)
	}
	if s.ref() != res.Commit || !s.isAncestor(foreign) {
		t.Fatal("revert not on top of the foreign commit")
	}
	if s.file("app.yaml") != "image: beacon:1.0" || s.file("other.yaml") != "x: 3" {
		t.Fatal("files wrong after the revert")
	}
}

func TestRevertRetriesWhenTheBranchMoves(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_rr", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	var foreign string
	r.beforePush = func() {
		if foreign == "" {
			foreign = s.commit(map[string]string{"other.yaml": "x: 4\n"}, "Meanwhile")
		}
	}
	res, err := r.Revert(ctx, "dep_rr", applied.Commit, "Roll back")
	if err != nil {
		t.Fatal(err)
	}
	if s.ref() != res.Commit || !s.isAncestor(foreign) || s.file("app.yaml") != "image: beacon:1.0" {
		t.Fatal("revert did not land on the new tip")
	}
}

func TestRevertConflict(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	r, _ := s.repo(t)
	applied, err := r.Apply(ctx, deploy("dep_c", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	s.commit(map[string]string{"app.yaml": "image: beacon:3.0\n"}, "Someone deployed over it")
	before := s.ref()
	_, err = r.Revert(ctx, "dep_c", applied.Commit, "Roll back")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "app.yaml" || !strings.Contains(err.Error(), "app.yaml") {
		t.Fatalf("conflict = %+v (%v)", conflict.Files, err)
	}
	if s.ref() != before {
		t.Fatal("the server moved")
	}
	if out := run(t, r.Dir, "status", "--porcelain", "--ignored"); out != "" {
		t.Fatalf("clone left dirty: %q", out)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".git", "REVERT_HEAD")); err == nil {
		t.Fatal("a revert is still in progress in the clone")
	}
	// The clone is usable again at once.
	if _, err := r.Apply(ctx, deploy("dep_after", "4.0")); err != nil {
		t.Fatal(err)
	}
}

func TestRevertRefusesACommitNotOnTheBranch(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	// A commit on another branch of the same server.
	dir := filepath.Join(t.TempDir(), "side")
	run(t, "", "clone", "--quiet", s.path, dir)
	os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("image: side\n"), 0o644)
	run(t, dir, "commit", "--quiet", "-am", "Side")
	run(t, dir, "push", "--quiet", "origin", "HEAD:refs/heads/side")
	side := run(t, dir, "rev-parse", "HEAD")

	r, _ := s.repo(t)
	before := s.ref()
	_, err := r.Revert(ctx, "dep_side", side, "Roll back")
	if err == nil || !strings.Contains(err.Error(), "not on main") {
		t.Fatalf("err = %v", err)
	}
	// Also when the clone has the commit, but the branch does not.
	run(t, r.Dir, "fetch", "--quiet", "origin", "refs/heads/side")
	_, err = r.Revert(ctx, "dep_side", side, "Roll back")
	if err == nil || !strings.Contains(err.Error(), "not on main") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Revert(ctx, "dep_side", "--help", "Roll back"); err == nil {
		t.Fatal("an option was taken as a commit")
	}
	if s.ref() != before {
		t.Fatal("the server moved")
	}
}

func TestContains(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	first := s.ref()
	r, _ := s.repo(t)
	tip, err := r.Tip(ctx)
	if err != nil || tip != first {
		t.Fatalf("Tip = %q, %v", tip, err)
	}
	// A commit the clone has not fetched yet.
	second := s.commit(map[string]string{"other.yaml": "x: 9\n"}, "Second")
	if ok, err := r.Contains(ctx, first, second); err != nil || !ok {
		t.Fatalf("Contains(first, second) = %v, %v", ok, err)
	}
	if ok, err := r.Contains(ctx, second, first); err != nil || ok {
		t.Fatalf("Contains(second, first) = %v, %v", ok, err)
	}
	if ok, err := r.Contains(ctx, second, second); err != nil || !ok {
		t.Fatalf("Contains(second, second) = %v, %v", ok, err)
	}
	if ok, err := r.Contains(ctx, second, "main"); err != nil || !ok {
		t.Fatalf("Contains(second, main) = %v, %v", ok, err)
	}
	if ok, err := r.Contains(ctx, strings.Repeat("ab", 20), "main"); err != nil || ok {
		t.Fatalf("Contains(unknown, main) = %v, %v", ok, err)
	}
	if _, err := r.Contains(ctx, first, strings.Repeat("cd", 20)); err == nil || !strings.Contains(err.Error(), "not in the repository") {
		t.Fatalf("Contains(first, unknown) err = %v", err)
	}
	if tip, err := r.Tip(ctx); err != nil || tip != second {
		t.Fatalf("Tip = %q, %v", tip, err)
	}
}

func TestBrokenDirIsRepaired(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	other := newServer(t)

	cases := map[string]func(dir string){
		"not a repository": func(dir string) {
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "junk"), []byte("junk"), 0o644)
		},
		"a file": func(dir string) {
			os.WriteFile(dir, []byte("junk"), 0o644)
		},
		"a clone of another repository": func(dir string) {
			run(t, "", "clone", "--quiet", other.path, dir)
		},
		"a corrupt clone": func(dir string) {
			run(t, "", "clone", "--quiet", s.path, dir)
			// The remote is right, but the objects are gone.
			os.RemoveAll(filepath.Join(dir, ".git", "objects"))
			os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755)
		},
		"a crashed attempt": func(dir string) {
			run(t, "", "clone", "--quiet", s.path, dir)
			os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("half written"), 0o644)
			os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("left over"), 0o644)
			os.WriteFile(filepath.Join(dir, ".git", "index.lock"), nil, 0o644)
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := s.repo(t)
			breakIt(r.Dir)
			res, err := r.Apply(ctx, deploy("dep_"+strings.ReplaceAll(name, " ", "_"), "7.0"))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Changed || s.ref() != res.Commit {
				t.Fatalf("result = %+v", res)
			}
			if got := run(t, r.Dir, "config", "--get", "remote.origin.url"); got != s.path {
				t.Fatalf("remote = %q", got)
			}
			if _, err := os.Stat(filepath.Join(r.Dir, "stray.txt")); err == nil {
				t.Fatal("a stray file survived")
			}
			// Back to the old value for the next case.
			if _, err := r.Apply(ctx, deploy("dep_back_"+strings.ReplaceAll(name, " ", "_"), "1.0")); err != nil {
				t.Fatal(err)
			}
		})
	}
	if other.ref() == "" {
		t.Fatal("the other server is gone")
	}
}

func TestRelativeLocalURL(t *testing.T) {
	s := newServer(t)
	wd, _ := os.Getwd()
	rel, err := filepath.Rel(wd, s.path)
	if err != nil {
		t.Skip("no relative path to the test repository")
	}
	r, _ := s.repo(t)
	r.URL = rel
	if _, err := r.Apply(context.Background(), deploy("dep_rel", "2.0")); err != nil {
		t.Fatal(err)
	}
	if s.file("app.yaml") != "image: beacon:2.0" {
		t.Fatal("not pushed")
	}
}

func TestRefusesCredentialsInTheURL(t *testing.T) {
	r := &Repo{URL: "https://bot:hunter2secret@git.example/a/b.git", Branch: "main", Dir: t.TempDir(),
		Author: Author{Name: "L", Email: "l@example.com"}}
	_, err := r.Tip(context.Background())
	if err == nil || strings.Contains(err.Error(), "hunter2secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestPasswordNeverLeaks(t *testing.T) {
	const password = "pa55-Never-Leak-7f3e"
	ctx := context.Background()
	s := newServer(t)
	r, l := s.repo(t)
	r.Auth = &Auth{Username: "deploy-bot", Password: password}
	var errs []string
	note := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	applied, err := r.Apply(ctx, deploy("dep_auth", "2.0"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Tip(ctx)
	note(err)
	_, err = r.Contains(ctx, applied.Commit, "main")
	note(err)
	_, err = r.Find(ctx, "dep_auth")
	note(err)
	// Failures, too.
	cs := deploy("dep_auth_fail", "3.0")
	cs.Files = append(cs.Files, FileEdit{Path: "missing.yaml", Edit: set("x")})
	_, err = r.Apply(ctx, cs)
	note(err)
	_, err = r.Contains(ctx, applied.Commit, "no-such-revision")
	note(err)
	s.commit(map[string]string{"app.yaml": "image: beacon:3.0\n"}, "Over it")
	_, err = r.Revert(ctx, "dep_auth", applied.Commit, "Roll back")
	note(err)
	// A server that does not exist.
	bad := *r
	bad.URL = filepath.Join(t.TempDir(), "missing.git")
	bad.Dir = filepath.Join(t.TempDir(), "bad")
	_, err = bad.Tip(ctx)
	note(err)
	// An unreachable HTTP server, where git does use the header.
	web := *r
	web.URL = "http://127.0.0.1:1/repo.git"
	web.Dir = filepath.Join(t.TempDir(), "web")
	_, err = web.Tip(ctx)
	note(err)

	if len(errs) < 4 {
		t.Fatalf("expected the failure cases to fail, got %q", errs)
	}
	for _, e := range errs {
		if strings.Contains(e, password) {
			t.Fatalf("error leaks the password: %q", e)
		}
	}
	if strings.Contains(l.all(), password) {
		t.Fatalf("log leaks the password: %q", l.all())
	}
	for _, dir := range []string{r.Dir, bad.Dir, web.Dir, s.path} {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(p)
			if err == nil && strings.Contains(string(data), password) {
				t.Errorf("%s holds the password", p)
			}
			return nil
		})
	}
	// The header is given to git, though: the environment carries it.
	found := false
	for _, kv := range r.env() {
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_0=Authorization: Basic ") {
			found = true
		}
	}
	if !found {
		t.Fatal("no Authorization header in git's environment")
	}
}

func TestCanceledContext(t *testing.T) {
	s := newServer(t)
	r, _ := s.repo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := s.ref()
	if _, err := r.Apply(ctx, deploy("dep_cancel", "2.0")); err == nil {
		t.Fatal("no error with a canceled context")
	}
	if s.ref() != before {
		t.Fatal("the server moved")
	}
}
