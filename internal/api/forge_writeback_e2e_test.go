package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
)

// A deploy step writes to a hosted repository with a credential of its own:
// nothing stored, valid for one deployment, and admitted by a rule of the
// repository for exactly the files it may write.
func TestDeployCredentialWritesOnlyWhatARuleAdmits(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	repo := hostedRepo(t, e, "acme/gitops")
	adaURL := withToken(e.srv.URL, "/git/acme/gitops.git", e.tokenFor(t, "ada", "t"))
	api := e.srv.URL + "/api/v1/repositories/acme/gitops"

	src := t.TempDir()
	mustGit(t, src, "init", "-q", "-b", "main")
	commit(t, src, "apps/a.yaml", "image: a:1\n", "seed")
	commit(t, src, "apps/b.yaml", "image: b:1\n", "seed b")
	mustGit(t, src, "push", "-q", adaURL, "HEAD:refs/heads/main")

	// main is protected; only mcp-* pipelines may change apps/a.yaml.
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main"],"pipeline_writers":[{"branch":"main","projects":["mcp-*"],"files":["apps/a.yaml"]}]}`); r.status != 200 {
		t.Fatalf("set rules: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main"],"pipeline_writers":[{"branch":"main","projects":["mcp-*"],"files":["**"]}]}`); r.status != 400 {
		t.Fatalf("a rule admitting every file was accepted: %d %s", r.status, r.body)
	}
	var got struct {
		Writers []struct {
			Files []string `json:"files"`
		} `json:"pipeline_writers"`
	}
	ada.get(api+"/protected-branches").json(t, &got)
	if len(got.Writers) != 1 || got.Writers[0].Files[0] != "apps/a.yaml" {
		t.Fatalf("rules read back: %+v", got)
	}
	// A person other than an administrator still cannot push to main.
	commit(t, src, "apps/c.yaml", "x: 1\n", "by a person")
	if out, err := git(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", e.tokenFor(t, "otto", "t")), "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("a person pushed to the protected branch: %s", out)
	}

	token := func(project, dep, mode string) string {
		tok, err := h.RunTokens.MintForDeploy(githttp.DeployClaim{Repo: repo.ID, Project: project, Deployment: dep, Branch: "main", Mode: mode, Merge: "auto"}, time.Hour, 2*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	clone := func(tok string) string {
		d := filepath.Join(t.TempDir(), "w")
		mustGit(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", tok), d)
		return d
	}
	push := func(dir, tok, ref string) (string, error) {
		return git(t, dir, "push", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", tok), "HEAD:"+ref)
	}

	// Direct: the admitted file goes through.
	tok := token("mcp-weather", "dep_1", "direct")
	w := clone(tok)
	commit(t, w, "apps/a.yaml", "image: a:2\n", "deploy a")
	if out, err := push(w, tok, "refs/heads/main"); err != nil {
		t.Fatalf("the admitted write was refused: %v\n%s", err, out)
	}
	// Another file, another branch, another project and no rule at all are refused, in words.
	commit(t, w, "apps/b.yaml", "image: b:2\n", "sneaky")
	for name, c := range map[string]struct{ ref, tok, want string }{
		"another file":    {"refs/heads/main", tok, "may not change apps/b.yaml on main"},
		"another branch":  {"refs/heads/other", tok, "may push only main, not other"},
		"another project": {"refs/heads/main", token("shop", "dep_9", "direct"), "no rule of this repository admits a pipeline of project shop"},
		"tags":            {"refs/tags/v1", tok, "may push only main"},
	} {
		out, err := push(w, c.tok, c.ref)
		if err == nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: want a refusal saying %q, got %v\n%s", name, c.want, err, out)
		}
	}
	// A protected-tag rule that names the Flight lets its credential make a matching tag (and no other, and never
	// move or delete one); a Flight it does not name is told so.
	if r := ada.do("PUT", api+"/protected-tags", `{"rules":[{"pattern":"v*","pipelines":["mcp-*"]}]}`); r.status != 200 {
		t.Fatalf("tag rules: %d %s", r.status, r.body)
	}
	if out, err := push(w, token("shop", "dep_9", "direct"), "refs/tags/v2"); err == nil || !strings.Contains(out, "the pipeline of shop is not named by the rule") {
		t.Errorf("a Flight the rule does not name made a protected tag: %v\n%s", err, out)
	}
	if out, err := push(w, tok, "refs/tags/other"); err == nil || !strings.Contains(out, "may push only main") {
		t.Errorf("a tag no rule covers: %v\n%s", err, out)
	}
	if out, err := push(w, tok, "refs/tags/v2"); err != nil {
		t.Errorf("a named Flight's tag was refused: %v\n%s", err, out)
	}
	if out, err := git(t, w, "push", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", tok), ":refs/tags/v2"); err == nil {
		t.Errorf("a credential deleted a protected tag: %s", out)
	}
	if out, err := git(t, w, "push", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", tok), ":refs/heads/main"); err == nil || !strings.Contains(out, "cannot delete") {
		t.Errorf("a deletion: %v\n%s", err, out)
	}
	// An expired credential is refused.
	old, _ := githttp.RunTokens{Key: h.RunTokens.Key, Now: func() time.Time { return time.Now().Add(-90 * time.Minute) }}.
		MintForDeploy(githttp.DeployClaim{Repo: repo.ID, Project: "mcp-weather", Deployment: "dep_2", Branch: "main", Mode: "direct"}, time.Hour, 2*time.Hour)
	if out, err := push(w, old, "refs/heads/main"); err == nil || !strings.Contains(out, "expired") {
		t.Errorf("an expired credential: %v\n%s", err, out)
	}
	// The credential reads no other repository.
	hostedRepo(t, e, "acme/other")
	if out, err := git(t, t.TempDir(), "ls-remote", withToken(e.srv.URL, "/git/acme/other.git", tok)); err == nil {
		t.Errorf("a deploy credential read another repository: %s", out)
	}
}

// pull_request mode: the credential pushes only the deployment's own branch;
// the server opens the pull request and merges it when its rules are met.
func TestDeployWritesBackByPullRequest(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	repo := hostedRepo(t, e, "acme/gitops")
	adaURL := withToken(e.srv.URL, "/git/acme/gitops.git", e.tokenFor(t, "ada", "t"))
	api := e.srv.URL + "/api/v1/repositories/acme/gitops"
	src := t.TempDir()
	mustGit(t, src, "init", "-q", "-b", "main")
	commit(t, src, "apps/a.yaml", "image: a:1\n", "seed")
	mustGit(t, src, "push", "-q", adaURL, "HEAD:refs/heads/main")
	ada.do("PUT", api+"/protected-branches", `{"patterns":["main"],"pipeline_writers":[{"branch":"main","projects":["mcp-*"],"files":["apps/a.yaml"]}]}`)

	claim := githttp.DeployClaim{Repo: repo.ID, Project: "mcp-weather", Deployment: "dep_pr", Branch: "main", Mode: "pull_request", Merge: "auto", Method: "merge"}
	tok, _ := h.RunTokens.MintForDeploy(claim, time.Hour, 2*time.Hour)
	url := withToken(e.srv.URL, "/git/acme/gitops.git", tok)
	w := filepath.Join(t.TempDir(), "w")
	mustGit(t, t.TempDir(), "clone", "-q", url, w)
	commit(t, w, "apps/a.yaml", "image: a:2\n", "Deploy a\n\nLaunchway-Deployment: dep_pr")
	if out, err := git(t, w, "push", "-q", url, "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "may push only launchway/deploy/dep_pr") {
		t.Fatalf("pull_request mode pushed to the branch itself: %v\n%s", err, out)
	}
	mustGit(t, w, "push", "-q", url, "HEAD:refs/heads/launchway/deploy/dep_pr")

	call := func(tok, body string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/runner/v1/deploy/pull-request", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runner-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	body := func(op string, n int) string {
		b, _ := json.Marshal(map[string]any{"token": tok, "op": op, "number": n, "title": "Deploy a", "body": "x\n\nLaunchway-Deployment: dep_pr"})
		return string(b)
	}
	code, pr := call(tok, body("open", 0))
	if code != 200 || pr["number"] == nil {
		t.Fatalf("open: %d %v", code, pr)
	}
	n := int(pr["number"].(float64))
	// The same call again answers with the same pull request.
	if _, again := call(tok, body("open", 0)); again["number"] != float64(n) {
		t.Fatalf("a second open made another pull request: %v", again)
	}
	var state map[string]any
	hostedWait(t, "the pull request to merge itself", func() bool {
		_, state = call(tok, body("status", n))
		return state["state"] == "merged"
	})
	if c, _ := state["commit"].(string); len(c) < 40 {
		t.Fatalf("merged without a commit: %v", state)
	}
	// A retried open after the merge answers with the merged pull request, not an error.
	if code, again := call(tok, body("open", 0)); code != 200 || again["number"] != float64(n) || again["state"] != "merged" {
		t.Fatalf("an open after the merge: %d %v", code, again)
	}
	// A credential of another deployment learns nothing about this pull request.
	other, _ := h.RunTokens.MintForDeploy(githttp.DeployClaim{Repo: repo.ID, Project: "mcp-weather", Deployment: "dep_other", Branch: "main", Mode: "pull_request"}, time.Hour, 2*time.Hour)
	if code, _ := call(other, strings.Replace(body("status", n), tok, other, 1)); code != 404 {
		t.Fatalf("another deployment read this pull request: %d", code)
	}
	if code, _ := call(tok, `{"token":"lwd_nope.x","op":"open"}`); code != http.StatusForbidden {
		t.Fatalf("a forged credential: %d", code)
	}
}
