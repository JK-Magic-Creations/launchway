package pulls

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

func TestMain(m *testing.M) {
	testlib.NoPrompt()
	gitexec.Reexec()
	os.Exit(m.Run())
}

const repoID = "rep1"

var testIdent = []string{"GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com"}

// world is a database, storage and the service over them, with one repository.
type world struct {
	t   *testing.T
	eng *engine.Engine
	st  *storage.Store
	svc *Service
	ctx context.Context
	n   int
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
	st, err := storage.New(context.Background(), storage.Options{DB: eng.Database(), Blobs: blobs, CacheDir: filepath.Join(t.TempDir(), "repos"), Server: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), repoID); err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewStore(eng.Database()), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(svc.Stop)
	return &world{t: t, eng: eng, st: st, svc: svc, ctx: context.Background()}
}

func (w *world) git(scratch, stdin string, env []string, args ...string) string {
	w.t.Helper()
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

// commit makes a commit on a branch: its parent is the branch's tip (or
// parent, a branch or sha, when the branch is new), files are written (an
// empty value deletes the file), and the branch moves to it. It returns the
// commit's name. The commit is made on the server's side through Mutate, as
// a merge is, so these tests need no push.
func (w *world) commit(branch, parent string, files map[string]string, msg string) string {
	w.t.Helper()
	var made string
	_, err := w.st.Mutate(w.ctx, repoID, func(_ context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		w.n++
		par := refs["refs/heads/"+branch]
		if par == "" && parent != "" {
			par = refs["refs/heads/"+parent]
			if par == "" {
				par = parent
			}
		}
		idx := filepath.Join(scratch, "idx")
		env := []string{"GIT_INDEX_FILE=" + idx}
		if par != "" {
			w.git(scratch, "", env, "read-tree", par)
		}
		for p, content := range files {
			if content == "" {
				w.git(scratch, "0 "+strings.Repeat("0", 40)+"\t"+p+"\n", env, "update-index", "--index-info")
				continue
			}
			blob := w.git(scratch, content, nil, "hash-object", "-w", "--stdin")
			w.git(scratch, "100644 "+blob+"\t"+p+"\n", env, "update-index", "--index-info")
		}
		tree := w.git(scratch, "", env, "write-tree")
		args := []string{"commit-tree", tree, "-m", msg}
		if par != "" {
			args = append(args, "-p", par)
		}
		made = w.git(scratch, "", []string{"GIT_AUTHOR_DATE=" + time.Unix(1_700_000_000+int64(w.n)*60, 0).UTC().Format(time.RFC3339), "GIT_COMMITTER_DATE=" + time.Unix(1_700_000_000+int64(w.n)*60, 0).UTC().Format(time.RFC3339)}, args...)
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, Old: refs["refs/heads/"+branch], New: made}}, nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return made
}

// force moves a branch to any commit, as a force-push would.
func (w *world) force(branch, sha string) {
	w.t.Helper()
	_, err := w.st.Mutate(w.ctx, repoID, func(_ context.Context, refs map[string]string, _ string) ([]storage.RefUpdate, error) {
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, Old: refs["refs/heads/"+branch], New: sha}}, nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) push(actor string, branch string, old, new string) {
	w.svc.Pushed(w.ctx, repoID, actor, []RefUpdate{{Name: "refs/heads/" + branch, Old: old, New: new}})
}

func (w *world) open(author string, n New) PullRequest {
	w.t.Helper()
	p, err := w.svc.Open(w.ctx, repoID, author, n)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) get(number int) PullRequest {
	w.t.Helper()
	w.svc.Settle()
	p, err := w.svc.Store.Get(w.ctx, repoID, number)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) refs() map[string]string {
	w.t.Helper()
	r, err := w.st.Refs(w.ctx, repoID)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}
