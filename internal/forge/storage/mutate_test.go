package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

func scratchGitT(t *testing.T, scratch, stdin string, args ...string) string {
	t.Helper()
	spec := gitexec.Spec{GitDir: scratch, Args: args, Timeout: time.Minute, AllowAlternates: true, Env: ident}
	if stdin != "" {
		spec.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(res.Stdout)
}

// A mutation writes a commit on top of a pushed branch; it is stored as a pack
// in the blob store, the ref moves, and a fresh clone through another server
// sees it. Moving a ref to an existing commit (a pull request head) stores no
// pack at all.
func TestMutateWritesObjectsAndMovesRefs(t *testing.T) {
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	base := pushOK(t, c, a, w.repo, &ack, 3000)
	ctx := context.Background()
	var made string
	res, err := w.st.Mutate(ctx, w.repo, func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error) {
		tree := scratchGitT(t, scratch, "", "rev-parse", base+"^{tree}")
		made = scratchGitT(t, scratch, "", "commit-tree", tree, "-p", base, "-m", "made by the server")
		return []RefUpdate{{Name: "refs/heads/main", Old: refs["refs/heads/main"], New: made}, {Name: "refs/pull/1/head", Old: "", New: base}}, nil
	})
	if err != nil || res.Updated != 1 || res.Created != 1 || res.PackBytes == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	refs, _ := w.st.Refs(ctx, w.repo)
	if refs["refs/heads/main"] != made || refs["refs/pull/1/head"] != base {
		t.Fatalf("refs: %v", refs)
	}
	dest := cloneMirror(t, a, w.repo)
	if got := refsOf(t, dest); got["refs/heads/main"] != made || got["refs/pull/1/head"] != base {
		t.Fatalf("a clone sees %v", got)
	}
	// A cache of another server is rebuilt from the manifest and has the commit.
	if err := w.st.Wipe(w.repo); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Mutate(ctx, w.repo, func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error) {
		if scratchGitT(t, scratch, "", "cat-file", "-t", made) != "commit" {
			t.Error("the commit the server made is not in the rebuilt cache")
		}
		return []RefUpdate{{Name: "refs/pull/2/head", New: made}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A push to the target while a mutation is deciding makes the mutation fail
// as stale and change nothing; run again, it sees the new tip and succeeds.
func TestMutateLosesToAConcurrentPush(t *testing.T) {
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	c1 := pushOK(t, c, a, w.repo, &ack, 3000)
	c2 := pushOK(t, c, a, w.repo, &ack, 3000)
	ctx := context.Background()
	moved := false
	try := func() (string, error) {
		var made string
		_, err := w.st.Mutate(ctx, w.repo, func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error) {
			tip := refs["refs/heads/main"]
			if !moved { // the push lands after the mutation read the refs and before it commits
				moved = true
				if _, err := w.st.Ledger().Commit(ctx, w.repo, []RefUpdate{{Name: "refs/heads/main", Old: tip, New: c1}}, "", noFaults{}); err != nil {
					t.Fatal(err)
				}
			}
			tree := scratchGitT(t, scratch, "", "rev-parse", tip+"^{tree}")
			made = scratchGitT(t, scratch, "", "commit-tree", tree, "-p", tip, "-m", "merge")
			return []RefUpdate{{Name: "refs/heads/main", Old: tip, New: made}}, nil
		})
		return made, err
	}
	_, err := try()
	var stale *ErrStale
	if !errors.As(err, &stale) {
		t.Fatalf("want a stale error, got %v", err)
	}
	if refs, _ := w.st.Refs(ctx, w.repo); refs["refs/heads/main"] != c1 {
		t.Fatalf("a failed mutation changed the branch: %v (c2 was %s)", refs, c2)
	}
	made, err := try()
	if err != nil {
		t.Fatal(err)
	}
	if refs, _ := w.st.Refs(ctx, w.repo); refs["refs/heads/main"] != made {
		t.Fatalf("the retry did not land: %v", refs)
	}
}
