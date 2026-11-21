package launchway

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const rid = "rep1"

func commit(t *testing.T, st *storage.Store, branch, parent, file, content string) {
	t.Helper()
	ident := []string{"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com"}
	run := func(scratch, stdin string, env []string, args ...string) string {
		sp := gitexec.Spec{GitDir: scratch, Args: args, Timeout: time.Minute, AllowAlternates: true, Env: append(append([]string{}, ident...), env...)}
		if stdin != "" {
			sp.Stdin = strings.NewReader(stdin)
		}
		res, err := gitexec.Run(context.Background(), sp)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(res.Stdout)
	}
	_, err := st.Mutate(context.Background(), rid, func(_ context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		par := refs["refs/heads/"+branch]
		if par == "" && parent != "" {
			par = refs["refs/heads/"+parent]
		}
		env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "idx")}
		if par != "" {
			run(scratch, "", env, "read-tree", par)
		}
		blob := run(scratch, content, nil, "hash-object", "-w", "--stdin")
		run(scratch, "100644 "+blob+"\t"+file+"\n", env, "update-index", "--index-info")
		args := []string{"commit-tree", run(scratch, "", env, "write-tree"), "-m", "c"}
		if par != "" {
			args = append(args, "-p", par)
		}
		made := run(scratch, "", nil, args...)
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, Old: refs["refs/heads/"+branch], New: made}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProposalsOverPullRequests(t *testing.T) {
	ctx := context.Background()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	blobs, _ := blobstore.NewFilesystem(t.TempDir())
	st, err := storage.New(ctx, storage.Options{DB: eng.Database(), Blobs: blobs, CacheDir: filepath.Join(t.TempDir(), "repos"), Server: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, rid); err != nil {
		t.Fatal(err)
	}
	svc := pulls.NewService(pulls.NewStore(eng.Database()), st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(svc.Stop)
	commit(t, st, "main", "", "go.mod", "module a\n")
	commit(t, st, "launchway/update/go-x", "main", "go.mod", "module a\n// bumped\n")

	p := New(forge.Connection{ID: "c"}, nil, nil, Options{Pulls: svc, ProposalAuthor: "launchway-updates"})
	if !p.Capabilities().Has(forge.CapProposals) {
		t.Fatal("proposals must be declared when the pull-request service is there")
	}
	if New(forge.Connection{}, nil, nil, Options{}).Capabilities().Has(forge.CapProposals) {
		t.Fatal("no service, no proposals")
	}
	repo := forge.Repository{ID: rid, Path: "o/r", DefaultBranch: "main"}
	pr := p.Proposals()
	created, err := pr.Create(ctx, repo, forge.NewProposal{Title: "Update a", Body: "why", Source: refOf("launchway/update/go-x"), Target: refOf("main")})
	if err != nil || created.Number != 1 || created.State != forge.ProposalOpen || created.Author.Login != "launchway-updates" {
		t.Fatalf("%+v %v", created, err)
	}
	if err := pr.(interface {
		SetLabels(context.Context, forge.Repository, int, []string) error
	}).SetLabels(ctx, repo, 1, []string{"dependencies", "go"}); err != nil {
		t.Fatal(err)
	}
	title := "Update a, again"
	body := "new body"
	up, err := pr.Update(ctx, repo, 1, forge.ProposalPatch{Title: &title, Body: &body})
	if err != nil || up.Title != title || up.Body != body {
		t.Fatalf("%+v %v", up, err)
	}
	open, err := pr.List(ctx, repo, forge.ProposalOpen, "")
	if err != nil || len(open.Items) != 1 || open.Items[0].Ext["labels"].([]string)[0] != "dependencies" {
		t.Fatalf("%+v %v", open, err)
	}
	if revs, err := pr.Revisions(ctx, repo, 1); err != nil || len(revs) != 1 {
		t.Fatalf("%v %v", revs, err)
	}
	if err := pr.Close(ctx, repo, 1); err != nil {
		t.Fatal(err)
	}
	closed, _ := pr.List(ctx, repo, forge.ProposalClosed, "")
	if len(closed.Items) != 1 || closed.Items[0].State != forge.ProposalClosed {
		t.Fatalf("%+v", closed)
	}
	if err := pr.Reopen(ctx, repo, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := pr.Get(ctx, repo, 1); got.State != forge.ProposalOpen {
		t.Errorf("%+v", got)
	}
	if _, err := pr.Get(ctx, repo, 9); err == nil {
		t.Error("an unknown number must fail")
	}
}
