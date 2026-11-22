package pulls

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

const forkID = "frk1"

// commitIn is world.commit for any repository of the world.
func (w *world) commitIn(repo, branch string, files map[string]string, msg string) string {
	w.t.Helper()
	var made string
	_, err := w.st.Mutate(w.ctx, repo, func(_ context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		w.n++
		par := refs["refs/heads/"+branch]
		if par == "" {
			par = refs["refs/heads/main"] // a new branch starts from main
		}
		idx := filepath.Join(scratch, "idx")
		env := []string{"GIT_INDEX_FILE=" + idx}
		if par != "" {
			w.git(scratch, "", env, "read-tree", par)
		}
		for p, content := range files {
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

func (w *world) makeFork() {
	w.t.Helper()
	if err := w.st.Create(w.ctx, forkID); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.st.Fork(w.ctx, repoID, forkID); err != nil {
		w.t.Fatal(err)
	}
	w.svc.SameNetwork = func(_ context.Context, a, b string) (bool, error) {
		return (a == forkID && b == repoID) || (a == repoID && b == forkID), nil
	}
}

func (w *world) hasObject(repo, sha string) bool {
	w.t.Helper()
	found := false
	err := w.st.WithCache(w.ctx, repo, func(dir string) error {
		_, err := gitexec.Run(w.ctx, gitexec.Spec{GitDir: dir, Args: []string{"cat-file", "-e", sha}, Timeout: time.Minute})
		found = err == nil
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return found
}

// A pull request from a fork keeps its head under the target's pull ref, takes
// only that branch's objects, follows the fork's branch, judges and merges like
// any other, and leaves the fork's branch alone.
func TestPullRequestFromAFork(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"a.txt": "one\n"}, "start")
	w.makeFork()
	topic := w.commitIn(forkID, "topic", map[string]string{"b.txt": "from the fork\n"}, "fork work")
	secret := w.commitIn(forkID, "private-work", map[string]string{"c.txt": "not for the parent\n"}, "other branch")
	// The fork's branches start from the parent's main: "topic" and the other are new branches on top of it.
	_ = secret

	p, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "From my fork", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceRepo != forkID || p.HeadSHA != topic || p.Number != 1 {
		t.Fatalf("%+v", p)
	}
	if got := w.refs()[PullRef(1)]; got != topic {
		t.Fatalf("the pull ref is %s, want %s", got, topic)
	}
	if _, ok := w.refs()["refs/heads/topic"]; ok {
		t.Error("the fork's branch became a branch of the target")
	}
	if w.hasObject(repoID, secret) {
		t.Fatal("A COMMIT OF THE FORK'S OTHER BRANCH IS IN THE TARGET")
	}
	p = w.get(1)
	if p.Mergeability.State != MergeClean || p.Mergeability.Ahead != 1 {
		t.Fatalf("judgment: %+v", p.Mergeability)
	}
	if _, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "again", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"}); !errors.Is(err, ErrConflict) {
		t.Errorf("a second pull request for the same branch: %v", err)
	}

	// The fork's branch moves: the pull request follows.
	topic2 := w.commitIn(forkID, "topic", map[string]string{"b.txt": "changed\n"}, "more")
	w.svc.Pushed(w.ctx, forkID, "ada", []RefUpdate{{Name: "refs/heads/topic", Old: topic, New: topic2}})
	p = w.get(1)
	if p.HeadSHA != topic2 || p.Revisions != 2 || w.refs()[PullRef(1)] != topic2 || w.refs()[RevisionRef(1, 2)] != topic2 {
		t.Fatalf("after the fork's push: %+v refs %v", p, w.refs())
	}

	// Merge: the target gets the change, the fork's branch is not deleted.
	del := true
	res, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: "merge", Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo", DeleteBranch: &del})
	if err != nil {
		t.Fatal(err)
	}
	if res.BranchDeleted {
		t.Error("merging deleted something")
	}
	if !w.hasObject(repoID, topic2) {
		t.Fatal("the merge lost the head")
	}
	frefs, _ := w.st.Refs(w.ctx, forkID)
	if frefs["refs/heads/topic"] != topic2 {
		t.Errorf("the fork's branch moved: %v", frefs)
	}
}

// A pull request can come only from repositories of one network; nothing
// says otherwise, and the service refuses the rest in plain words.
func TestPullRequestFromARepositoryOfAnotherNetworkIsRefused(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"a.txt": "one\n"}, "start")
	w.makeFork()
	w.commitIn(forkID, "topic", map[string]string{"b.txt": "x\n"}, "fork work")
	w.svc.SameNetwork = func(context.Context, string, string) (bool, error) { return false, nil }
	_, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "t", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "fork of this repository") {
		t.Fatalf("%v", err)
	}
	w.svc.SameNetwork = nil
	if _, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "t", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("with no network check at all: %v", err)
	}
}

// The run of a first-time contributor's pull request from a fork waits for
// approval; approving it lets that head run, and a new head waits again.
func TestForkRunNeedsApprovalUntilApprovedOrTrusted(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"a.txt": "one\n"}, "start")
	w.makeFork()
	topic := w.commitIn(forkID, "topic", map[string]string{"b.txt": "x\n"}, "fork work")
	p, err := w.svc.Open(w.ctx, repoID, "newcomer", New{Title: "t", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	needs, why, err := w.svc.ForkRunNeedsApproval(w.ctx, repoID, p.Number)
	if err != nil || !needs || !strings.Contains(why, "must approve") {
		t.Fatalf("a first-timer: %v %q %v", needs, why, err)
	}
	if err := w.svc.ApproveRun(w.ctx, w.get(1)); err != nil {
		t.Fatal(err)
	}
	if needs, _, _ := w.svc.ForkRunNeedsApproval(w.ctx, repoID, p.Number); needs {
		t.Error("an approved head still waits")
	}
	topic2 := w.commitIn(forkID, "topic", map[string]string{"b.txt": "y\n"}, "more")
	w.svc.Pushed(w.ctx, forkID, "newcomer", []RefUpdate{{Name: "refs/heads/topic", Old: topic, New: topic2}})
	w.get(1)
	if needs, _, _ := w.svc.ForkRunNeedsApproval(w.ctx, repoID, p.Number); !needs {
		t.Error("a new head ran without approval")
	}
	// Once a pull request of theirs is merged, they are no first-timer.
	if _, err := w.svc.Merge(w.ctx, repoID, w.get(1), MergeInput{Method: "merge", Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"}); err != nil {
		t.Fatal(err)
	}
	topic3 := w.commitIn(forkID, "next", map[string]string{"c.txt": "z\n"}, "next work")
	_ = topic3
	p2, err := w.svc.Open(w.ctx, repoID, "newcomer", New{Title: "t2", SourceRepo: forkID, SourceBranch: "next", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if needs, _, _ := w.svc.ForkRunNeedsApproval(w.ctx, repoID, p2.Number); needs {
		t.Error("a contributor with a merged pull request still waits for approval")
	}
}
