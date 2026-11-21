package argocd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func applyTag(t *testing.T, a *Agent, dep, tag string) *engine.DeployFacts {
	t.Helper()
	j := job("never")
	j.Deploy.Deployment = dep
	j.Deploy.Version = tag
	j.Deploy.Message = "Deploy " + tag
	j.Deploy.Changes = []engine.DeployChange{{File: "a.yaml", Image: "team/shop", Tag: tag}}
	f, err := a.Apply(context.Background(), j, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func bareFile(t *testing.T, bare string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "show", "main:a.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("%s", out)
	}
	return strings.TrimSpace(string(out))
}

func revertJob(version string, rv ...engine.DeployRevert) *engine.Job {
	j := job("never")
	j.Deploy.Action, j.Deploy.Deployment, j.Deploy.Version, j.Deploy.Reverts = engine.ActionRevert, "dep_rollback", version, rv
	return j
}

// The deploy runner reverts the deployments of a rollback run, newest
// first, in one change; it says what it reverted, and the application is
// asked to look at the last revert.
func TestRevertSeriesOfARollbackRun(t *testing.T) {
	bare, _ := bareRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	a := agentFor(t, bare, cl)
	fa := applyTag(t, a, "dep_a", "2")
	fb := applyTag(t, a, "dep_b", "3")
	if bareFile(t, bare) != "image: team/shop:3" {
		t.Fatalf("a.yaml = %q", bareFile(t, bare))
	}
	var lines []string
	facts, err := a.Revert(context.Background(), revertJob("1",
		engine.DeployRevert{Deployment: "dep_b", Version: "3", Commit: fb.Commit, Message: "Roll back: revert 3"},
		engine.DeployRevert{Deployment: "dep_a", Version: "2", Commit: fa.Commit, Message: "Roll back: revert 2"}),
		func(f string, args ...any) { lines = append(lines, f) })
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Reverted || facts.Revert == "" || facts.Existing || bareFile(t, bare) != "image: team/shop:1" {
		t.Fatalf("facts %+v, a.yaml %q", facts, bareFile(t, bare))
	}
	// Again (an earlier try made it): found, nothing pushed, same answer.
	again, err := a.Revert(context.Background(), revertJob("1",
		engine.DeployRevert{Deployment: "dep_b", Version: "3", Commit: fb.Commit, Message: "x"},
		engine.DeployRevert{Deployment: "dep_a", Version: "2", Commit: fa.Commit, Message: "x"}), func(string, ...any) {})
	if err != nil || !again.Existing || again.Revert != facts.Revert {
		t.Fatalf("%+v %v", again, err)
	}
	// Nothing to revert for deployments that made no commit.
	none, err := a.Revert(context.Background(), revertJob("1", engine.DeployRevert{Deployment: "dep_x", Version: "9"}), func(string, ...any) {})
	if err != nil || none.Reverted {
		t.Fatalf("%+v %v", none, err)
	}
}

// When one revert cannot be applied, nothing changes in Git and the
// message names the deployment, the files, and what to do.
func TestRevertSeriesRefusesPlainlyOnAConflict(t *testing.T) {
	bare, _ := bareRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	a := agentFor(t, bare, cl)
	fa := applyTag(t, a, "dep_a", "2")
	fb := applyTag(t, a, "dep_b", "3")
	// Someone edits the same line by hand.
	work := t.TempDir()
	for _, args := range [][]string{{"clone", "-q", bare, work}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	os.WriteFile(filepath.Join(work, "a.yaml"), []byte("image: team/shop:hotfix\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=h", "-c", "user.email=h@h", "commit", "-qm", "hotfix"}, {"push", "-q", "origin", "HEAD:main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	_, err := a.Revert(context.Background(), revertJob("1",
		engine.DeployRevert{Deployment: "dep_b", Version: "3", RunNumber: 12, Commit: fb.Commit, Message: "x"},
		engine.DeployRevert{Deployment: "dep_a", Version: "2", RunNumber: 11, Commit: fa.Commit, Message: "x"}), func(string, ...any) {})
	if err == nil {
		t.Fatal("a revert that does not apply succeeded")
	}
	for _, w := range []string{"rolling back to 1 cannot be done cleanly", "reverting 3 (run #12, deployment dep_b)", "a.yaml", "Nothing was changed in Git", "fix those files by hand", "normal run"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal lacks %q: %v", w, err)
		}
	}
	if bareFile(t, bare) != "image: team/shop:hotfix" {
		t.Fatalf("Git was changed: %q", bareFile(t, bare))
	}
}

// A runner says whether it can write the repository a deployment goes to.
func TestWriteSelfCheck(t *testing.T) {
	bare, _ := bareRepo(t)
	_, cl := fakeCluster(t, map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "shop", "namespace": "argocd"}})
	a := agentFor(t, bare, cl)
	if ok, why := a.canWrite("shop", "gitops"); !ok || why != "" {
		t.Fatalf("%v %q", ok, why)
	}
	if ok, why := a.canWrite("other", "gitops"); ok || !strings.Contains(why, "may not change repository gitops for project other") {
		t.Fatalf("%v %q", ok, why)
	}
	if ok, why := a.canWrite("shop", "elsewhere"); ok || !strings.Contains(why, "no GitOps repository elsewhere") {
		t.Fatalf("%v %q", ok, why)
	}
	r := a.Config.Repos["gitops"]
	r.URL = "https://git.example/gitops.git"
	a.Config.Repos["gitops"] = r
	if ok, why := a.canWrite("shop", "gitops"); ok || !strings.Contains(why, "no credentials for repository gitops") {
		t.Fatalf("%v %q", ok, why)
	}
	rights := a.withWriteRights(&deploy.Rights{}, "shop", "gitops")
	if rights.Write == nil || *rights.Write || rights.WriteWhy == "" {
		t.Fatalf("%+v", rights)
	}
}
