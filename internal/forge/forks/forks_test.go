package forks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	fstore "github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	gitexec.Reexec()
	os.Exit(m.Run())
}

type world struct {
	t    *testing.T
	ctx  context.Context
	svc  *Service
	prov *launchway.Provider
	st   *storage.Store
	n    int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := storage.New(ctx, storage.Options{DB: eng.Database(), Blobs: blobs, CacheDir: filepath.Join(t.TempDir(), "repos"), Server: "t"})
	if err != nil {
		t.Fatal(err)
	}
	fs := fstore.New(eng.Database())
	conn, err := fs.CreateConnection(ctx, forge.Connection{Name: "launchway", Kind: forge.KindLaunchway, Events: forge.EventsInProcess, Enabled: true}, "system")
	if err != nil {
		t.Fatal(err)
	}
	prov := launchway.New(conn, fs, st, launchway.Options{BaseURL: "https://example.test", CreatesPerMinute: 1000})
	svc := &Service{Repos: prov.Repositories(), ByID: fs.Repository, Git: st, DB: eng.Database()}
	return &world{t: t, ctx: ctx, svc: svc, prov: prov, st: st}
}

func (w *world) repo(owner, name string, vis forge.Visibility) forge.Repository {
	w.t.Helper()
	r, err := w.prov.Repositories().Create(w.ctx, forge.NewRepository{Home: owner, Name: name, Visibility: vis})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

var testIdent = []string{"GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com"}

// commit writes files (an empty map keeps the tree) on a branch of a
// repository, from the branch's tip or, when it is new, from main's.
func (w *world) commit(repo forge.Repository, branch string, files map[string]string, msg string) string {
	w.t.Helper()
	var made string
	run := func(scratch, stdin string, env []string, args ...string) string {
		sp := gitexec.Spec{GitDir: scratch, Args: args, Timeout: time.Minute, AllowAlternates: true, Env: append(append([]string{}, testIdent...), env...)}
		if stdin != "" {
			sp.Stdin = strings.NewReader(stdin)
		}
		res, err := gitexec.Run(w.ctx, sp)
		if err != nil {
			w.t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(res.Stdout)
	}
	_, err := w.st.Mutate(w.ctx, repo.ID, func(_ context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		w.n++
		par := refs["refs/heads/"+branch]
		if par == "" {
			par = refs["refs/heads/main"]
		}
		env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "idx")}
		if par != "" {
			run(scratch, "", env, "read-tree", par)
		}
		for p, content := range files {
			blob := run(scratch, content, nil, "hash-object", "-w", "--stdin")
			run(scratch, "100644 "+blob+"\t"+p+"\n", env, "update-index", "--add", "--index-info")
		}
		args := []string{"commit-tree", run(scratch, "", env, "write-tree"), "-m", msg}
		if par != "" {
			args = append(args, "-p", par)
		}
		d := time.Unix(1_700_000_000+int64(w.n)*60, 0).UTC().Format(time.RFC3339)
		made = run(scratch, "", []string{"GIT_AUTHOR_DATE=" + d, "GIT_COMMITTER_DATE=" + d}, args...)
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, Old: refs["refs/heads/"+branch], New: made}}, nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return made
}

func (w *world) refs(repo forge.Repository) map[string]string {
	w.t.Helper()
	r, err := w.st.Refs(w.ctx, repo.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// gitIn runs git in a repository's cache.
func (w *world) gitIn(repo forge.Repository, args ...string) string {
	w.t.Helper()
	var out string
	err := w.st.WithCache(w.ctx, repo.ID, func(dir string) error {
		res, err := gitexec.Run(w.ctx, gitexec.Spec{GitDir: dir, Args: args, Timeout: time.Minute})
		out = strings.TrimSpace(res.Stdout)
		return err
	})
	if err != nil {
		w.t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func TestAllowForksFollowsTheSwitchesThenTheDefault(t *testing.T) {
	cases := []struct {
		repo     int
		owner    bool
		ownerSet bool
		vis      forge.Visibility
		want     bool
	}{
		{-1, false, false, forge.Public, true},
		{-1, false, false, forge.Private, false},
		{-1, false, false, forge.Internal, false},
		{1, false, false, forge.Private, true},
		{0, true, true, forge.Public, false},
		{-1, false, true, forge.Public, false},
		{-1, true, true, forge.Private, true},
		{1, false, true, forge.Private, true},
	}
	for i, c := range cases {
		if got, why := AllowForks(c.repo, c.owner, c.ownerSet, c.vis); got != c.want || why == "" {
			t.Errorf("case %d: %v %q, want %v", i, got, why, c.want)
		}
	}
	if _, err := ForkVisibility(forge.Private, forge.Public); !errors.Is(err, ErrVisibility) {
		t.Errorf("a public fork of a private repository: %v", err)
	}
	if v, err := ForkVisibility(forge.Private, ""); err != nil || v != forge.Private {
		t.Errorf("%v %v", v, err)
	}
	if v, err := ForkVisibility(forge.Public, forge.Private); err != nil || v != forge.Private {
		t.Errorf("%v %v", v, err)
	}
}

func TestForkRecordsItsPlaceAndFollowsTheRules(t *testing.T) {
	w := newWorld(t)
	parent := w.repo("acme", "app", forge.Public)
	tip := w.commit(parent, "main", map[string]string{"a.txt": "one\n"}, "start")
	f1, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if f1.Path != "ada/app" || f1.Visibility != forge.Public {
		t.Fatalf("%+v", f1)
	}
	info, _ := w.svc.Info(w.ctx, f1.ID)
	if info.ParentID != parent.ID || info.RootID != parent.ID || info.ForkPoint != tip || info.ForkedBy != "ada" || info.ForkedAt == 0 {
		t.Fatalf("%+v", info)
	}
	if w.refs(f1)["refs/heads/main"] != tip {
		t.Fatal("the fork has no main")
	}
	// A fork of the fork has the same root.
	f2, err := w.svc.Fork(w.ctx, f1, NewFork{Owner: "bo", By: "bo"})
	if err != nil {
		t.Fatal(err)
	}
	if i2, _ := w.svc.Info(w.ctx, f2.ID); i2.ParentID != f1.ID || i2.RootID != parent.ID {
		t.Fatalf("%+v", i2)
	}
	if ok, err := w.svc.SameNetwork(w.ctx, f2.ID, f1.ID); err != nil || !ok {
		t.Errorf("forks of one root: %v %v", ok, err)
	}
	other := w.repo("acme", "other", forge.Public)
	if ok, _ := w.svc.SameNetwork(w.ctx, f2.ID, other.ID); ok {
		t.Error("repositories of two networks are in one")
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"}); !errors.Is(err, forge.ErrExists) {
		t.Errorf("a name in use: %v", err)
	}
	if list, _ := w.svc.Forks(w.ctx, parent.ID, 10); len(list) != 1 || list[0].ID != f1.ID {
		t.Errorf("forks of the parent: %+v", list)
	}
}

func TestForksOfPrivateRepositoriesAreOffUntilTurnedOnAndStayPrivate(t *testing.T) {
	w := newWorld(t)
	parent := w.repo("acme", "secret", forge.Private)
	w.commit(parent, "main", map[string]string{"a.txt": "one\n"}, "start")
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"}); !errors.Is(err, ErrForksOff) {
		t.Fatalf("a private repository by default: %v", err)
	}
	// The owner's switch turns them on for all of its repositories.
	if err := w.svc.SetOwnerAllowForks(w.ctx, "acme", true, false, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada", Visibility: forge.Public}); !errors.Is(err, ErrVisibility) {
		t.Fatalf("a public fork of a private repository: %v", err)
	}
	f, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"})
	if err != nil || f.Visibility != forge.Private {
		t.Fatalf("%+v %v", f, err)
	}
	// The repository's own switch wins over the owner's.
	if err := w.svc.SetAllowForks(w.ctx, parent.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "bo", By: "bo"}); !errors.Is(err, ErrForksOff) {
		t.Fatalf("a repository that turned forks off: %v", err)
	}
	// A failed fork leaves nothing behind: no repository under the name.
	if _, err := w.prov.Repositories().Get(w.ctx, "bo/secret"); err == nil {
		t.Error("a refused fork left a repository")
	}
}

func TestForkCreationIsRateLimited(t *testing.T) {
	w := newWorld(t)
	w.svc.PerPersonPerHour = 2
	parent := w.repo("acme", "app", forge.Public)
	w.commit(parent, "main", map[string]string{"a.txt": "one\n"}, "start")
	for _, name := range []string{"a1", "a2"} {
		if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", Name: name, By: "ada"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", Name: "a3", By: "ada"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("a third fork in an hour: %v", err)
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "bo", Name: "b1", By: "bo"}); err != nil {
		t.Fatalf("another person is not held back by the first: %v", err)
	}
	w.svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", Name: "a3", By: "ada"}); err != nil {
		t.Fatalf("after the hour: %v", err)
	}
}

func TestDeletingTheParentLeavesTheForkWhole(t *testing.T) {
	w := newWorld(t)
	parent := w.repo("acme", "app", forge.Public)
	w.commit(parent, "main", map[string]string{"a.txt": "one\n"}, "start")
	f, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.prov.Repositories().Delete(w.ctx, parent.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Purge(w.ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.st.Wipe(f.ID); err != nil {
		t.Fatal(err)
	}
	if out := w.gitIn(f, "fsck", "--full", "--no-dangling"); out != "" && strings.Contains(out, "error") {
		t.Fatalf("fsck: %s", out)
	}
	if got := w.gitIn(f, "show", "refs/heads/main:a.txt"); got != "one" {
		t.Fatalf("the fork's file: %q", got)
	}
	if _, err := w.svc.Sync(w.ctx, f, "", "", "", "ada"); !errors.Is(err, ErrRelation) {
		t.Fatalf("syncing from a deleted parent: %v", err)
	}
	if _, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "bo", By: "bo"}); err == nil {
		t.Error("a deleted repository was forked")
	}
}

func TestSyncFastForwardsMergesAndSaysWhenItConflicts(t *testing.T) {
	w := newWorld(t)
	parent := w.repo("acme", "app", forge.Public)
	w.commit(parent, "main", map[string]string{"a.txt": "one\n", "b.txt": "b\n"}, "start")
	f, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Sync(w.ctx, f, "", "", "", "ada")
	if err != nil || res.Status != StatusUpToDate {
		t.Fatalf("%+v %v", res, err)
	}
	up1 := w.commit(parent, "main", map[string]string{"c.txt": "c\n"}, "parent moves")
	st, err := w.svc.Standing(w.ctx, f, "", "")
	if err != nil || st.Behind != 1 || st.Ahead != 0 {
		t.Fatalf("standing %+v %v", st, err)
	}
	res, err = w.svc.Sync(w.ctx, f, "", "", "", "ada")
	if err != nil || res.Status != StatusFastForwarded || !res.Moved() || w.refs(f)["refs/heads/main"] != up1 {
		t.Fatalf("%+v %v", res, err)
	}
	// The fork has its own commit, and the parent another one.
	w.commit(f, "main", map[string]string{"mine.txt": "m\n"}, "fork work")
	w.commit(parent, "main", map[string]string{"d.txt": "d\n"}, "parent again")
	st, _ = w.svc.Standing(w.ctx, f, "", "")
	if st.Ahead != 1 || st.Behind != 1 {
		t.Fatalf("diverged standing %+v", st)
	}
	res, err = w.svc.Sync(w.ctx, f, "", "", "", "ada")
	if err != nil || res.Status != StatusDiverged || res.Moved() {
		t.Fatalf("fast-forward only: %+v %v", res, err)
	}
	res, err = w.svc.Sync(w.ctx, f, "", "", "merge", "ada")
	if err != nil || res.Status != StatusMerged {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if out := w.gitIn(f, "rev-list", "--parents", "-n", "1", "refs/heads/main"); len(strings.Fields(out)) != 3 {
		t.Fatalf("not a merge commit: %s", out)
	}
	if got := w.gitIn(f, "log", "-1", "--format=%an", "refs/heads/main"); got != "ada" {
		t.Errorf("the merge is by %q", got)
	}
	// Both sides change a.txt: a conflict, and nothing moves.
	w.commit(parent, "main", map[string]string{"a.txt": "parent's\n"}, "parent edits a")
	w.commit(f, "main", map[string]string{"a.txt": "fork's\n"}, "fork edits a")
	before := w.refs(f)["refs/heads/main"]
	res, err = w.svc.Sync(w.ctx, f, "", "", "merge", "ada")
	if err != nil || res.Status != StatusConflicts || len(res.Conflicts) != 1 || res.Conflicts[0] != "a.txt" {
		t.Fatalf("conflict: %+v %v", res, err)
	}
	if w.refs(f)["refs/heads/main"] != before {
		t.Error("a conflicting sync moved the branch")
	}
	// Only the parent's synced branch came over, none of its others.
	secret := w.commit(parent, "secret-branch", map[string]string{"s.txt": "s\n"}, "private work")
	if _, err := w.svc.Sync(w.ctx, f, "", "", "merge", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(w.ctx, gitexec.Spec{GitDir: func() string { var d string; _ = w.st.WithCache(w.ctx, f.ID, func(x string) error { d = x; return nil }); return d }(), Args: []string{"cat-file", "-e", secret}, Timeout: time.Minute}); err == nil {
		t.Error("another branch of the parent came over with the sync")
	}
}

func TestAForkWiderThanItsParentCannotSyncFromIt(t *testing.T) {
	w := newWorld(t)
	parent := w.repo("acme", "app", forge.Public)
	w.commit(parent, "main", map[string]string{"a.txt": "one\n"}, "start")
	f, err := w.svc.Fork(w.ctx, parent, NewFork{Owner: "ada", By: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.prov.SetVisibility(w.ctx, parent.Path, forge.Private); err != nil {
		t.Fatal(err)
	}
	w.commit(parent, "main", map[string]string{"new-secret.txt": "x\n"}, "private now")
	if _, err := w.svc.Sync(w.ctx, f, "", "", "", "ada"); !errors.Is(err, ErrVisibility) {
		t.Fatalf("a public fork syncing from a repository that went private: %v", err)
	}
	// Once the fork is no wider, it can.
	if err := w.prov.SetVisibility(w.ctx, f.Path, forge.Private); err != nil {
		t.Fatal(err)
	}
	f, _ = w.prov.Repositories().Get(w.ctx, f.Path)
	if res, err := w.svc.Sync(w.ctx, f, "", "", "", "ada"); err != nil || res.Status != StatusFastForwarded {
		t.Fatalf("%+v %v", res, err)
	}
}
