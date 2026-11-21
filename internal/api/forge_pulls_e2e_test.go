package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A pull request through the API, with real git pushes: open it, comment on a
// line, push again (the comment moves, the head is a new revision), review,
// see what blocks the merge in words, merge, revert.

type prView struct {
	pulls.PullRequest
	Readiness pulls.Readiness `json:"readiness"`
	Actions   []struct {
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	} `json:"actions"`
	FetchRef string `json:"fetch_ref"`
}

func (v prView) action(name string) (avail bool, reason string) {
	for _, a := range v.Actions {
		if a.Name == name {
			return a.Available, a.Reason
		}
	}
	return false, "no such action"
}

// waitMergeable polls the pull request until its judgment is made.
func waitMergeable(t *testing.T, b *browser, url string, want pulls.MergeState) prView {
	t.Helper()
	var v prView
	hostedWait(t, "the judgment of mergeability to be "+string(want), func() bool {
		b.get(url).json(t, &v)
		return v.Mergeability.State == want
	})
	return v
}

func TestPullRequestThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"

	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitAt(t, src, date, "init", "-q", "-b", "main")
	write("code.txt", "l1\nl2\nl3\nl4\nl5\n")
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")
	gitAt(t, src, date, "checkout", "-q", "-b", "feature")
	write("code.txt", "l1\nl2\nl3 changed\nl4\nl5\n")
	write("new.txt", "hello\n")
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "change l3 and add new")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/feature")

	// A viewer cannot open one; an operator can.
	if r := vic.do("POST", api+"/pulls", `{"title":"x","source_branch":"feature"}`); r.status != 403 {
		t.Fatalf("a viewer opened a pull request: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/pulls", `{"title":"","source_branch":"feature"}`); r.status != 400 || !strings.Contains(string(r.body), "needs a title") || !strings.Contains(string(r.body), `"code"`) {
		t.Fatalf("no title: %d %s", r.status, r.body)
	}
	var pr prView
	r := otto.do("POST", api+"/pulls", `{"title":"Change l3","body":"Please **review**.\n\n<script>alert(1)</script>","source_branch":"feature","labels":["bug"],"requested_reviewers":["ada"]}`)
	r.json(t, &pr)
	if r.status != 201 || pr.Number != 1 || pr.TargetBranch != "main" || pr.Author != "otto" || pr.FetchRef != "refs/pull/1/head" || pr.Repository != "acme/shop" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if !strings.Contains(pr.Body, "<script>") {
		t.Error("the description was altered on the server: clients sanitize it when they render it")
	}
	prURL := api + "/pulls/1"
	pr = waitMergeable(t, otto, prURL, pulls.MergeClean)
	if ok, _ := pr.action("merge"); !ok {
		t.Fatalf("a clean pull request into an unprotected branch cannot merge: %+v", pr.Readiness)
	}
	// The head is a ref of the repository: it can be fetched.
	if out := mustGit(t, src, "ls-remote", url, "refs/pull/1/head"); !strings.HasPrefix(out, pr.HeadSHA) {
		t.Fatalf("ls-remote: %q (head %s)", out, pr.HeadSHA)
	}
	// Nobody can push to the server's own refs.
	if out, err := git(t, src, "push", url, "HEAD:refs/pull/9/head"); err == nil || !strings.Contains(out, "belongs to a pull request") {
		t.Fatalf("a push to refs/pull/: %v\n%s", err, out)
	}

	var list struct {
		Items []pulls.PullRequest `json:"items"`
		Total int                 `json:"total"`
	}
	vic.get(api+"/pulls?state=open&author=otto").json(t, &list)
	if list.Total != 1 || list.Items[0].Number != 1 {
		t.Fatalf("%+v", list)
	}
	vic.get(api+"/pulls?state=merged").json(t, &list)
	if list.Total != 0 {
		t.Fatalf("%+v", list)
	}

	// Files, a patch, commits.
	var files struct {
		Files []pulls.FileChange `json:"files"`
		Total int                `json:"total"`
	}
	vic.get(prURL+"/files").json(t, &files)
	if files.Total != 2 || files.Files[0].Path != "code.txt" || files.Files[0].Status != "modified" || files.Files[1].Status != "added" {
		t.Fatalf("%+v", files)
	}
	var patch pulls.Patch
	vic.get(prURL+"/patch?path=code.txt").json(t, &patch)
	if !strings.Contains(patch.Diff, "+l3 changed") {
		t.Fatalf("%+v", patch)
	}
	if r := vic.get(prURL + "/patch?from=--output=/x&to=feature"); r.status != 400 {
		t.Fatalf("an option as a revision: %d %s", r.status, r.body)
	}
	var commits struct{ Items []pulls.CommitInfo }
	vic.get(prURL+"/commits").json(t, &commits)
	if len(commits.Items) != 1 || commits.Items[0].Subject != "change l3 and add new" {
		t.Fatalf("%+v", commits)
	}

	// A comment on line 5, then a new push that adds a line above it.
	var c pulls.Comment
	if r := vic.do("POST", prURL+"/comments", `{"body":"is this needed?","path":"code.txt","line":5}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &c)
	}
	if r := vic.do("POST", prURL+"/comments", `{"body":"x","path":"code.txt","line":99}`); r.status != 400 || !strings.Contains(string(r.body), "has 5 lines") {
		t.Fatalf("a comment past the end: %d %s", r.status, r.body)
	}
	write("code.txt", "l0\nl1\nl2\nl3 changed\nl4\nl5\n")
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "add l0")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/feature")
	hostedWait(t, "the pull request to follow the push", func() bool {
		ada.get(prURL).json(t, &pr)
		return pr.Revisions == 2
	})
	var conv struct {
		Threads []struct {
			Line     int  `json:"line"`
			Outdated bool `json:"outdated"`
		} `json:"threads"`
		Revisions []pulls.Revision `json:"revisions"`
	}
	ada.get(prURL+"/conversation").json(t, &conv)
	if len(conv.Threads) != 1 || conv.Threads[0].Line != 6 || conv.Threads[0].Outdated || len(conv.Revisions) != 2 || conv.Revisions[1].Forced {
		t.Fatalf("%+v", conv)
	}
	var since struct {
		Total int `json:"total"`
	}
	ada.get(prURL+"/files?since=1").json(t, &since)
	if since.Total != 1 {
		t.Fatalf("changes since the first revision: %+v", since)
	}

	// Rules: an administrator asks for one approval on main.
	if r := otto.do("PUT", api+"/merge-rules", `{"rules":[{"pattern":"main","required_approvals":1}]}`); r.status != 403 {
		t.Fatalf("an operator set rules: %d", r.status)
	}
	if r := ada.do("PUT", api+"/merge-rules", `{"rules":[{"pattern":"main","required_approvals":1,"no_self_approval":true,"allow_admin_bypass":false}]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The rules answer says what each person may do with them, and never offers signed commits.
	for who, want := range map[*browser]bool{ada: true, otto: false} {
		var rules struct {
			Actions []struct {
				Name      string `json:"name"`
				Available bool   `json:"available"`
				Reason    string `json:"reason"`
			} `json:"actions"`
		}
		who.get(api+"/merge-rules").json(t, &rules)
		got := map[string]bool{}
		for _, a := range rules.Actions {
			got[a.Name] = a.Available
			if !a.Available && a.Reason == "" {
				t.Errorf("action %s is unavailable without a reason", a.Name)
			}
		}
		if got["edit_rules"] != want || got["edit_settings"] != want || got["require_signed_commits"] {
			t.Errorf("rule actions: %v, want edit %v", got, want)
		}
	}
	// Asking someone to review, then taking them off the list.
	if r := ada.do("POST", prURL+"/review-requests", `{"reviewers":["vic"]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", prURL+"/review-requests/nobody", ""); r.status != 404 {
		t.Fatalf("removing someone not asked: %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", prURL+"/review-requests/vic", ""); r.status != 200 || strings.Contains(string(r.body), `"vic"`) {
		t.Fatalf("removing a review request: %d %s", r.status, r.body)
	}
	pr = waitMergeable(t, otto, prURL, pulls.MergeClean)
	if ok, why := pr.action("merge"); ok || why != "needs 1 more approval" {
		t.Fatalf("merge action: %v %q", ok, why)
	}
	r = otto.do("POST", prURL+"/merge", `{}`)
	var refused struct{ Error, Code, Next string }
	r.json(t, &refused)
	if r.status != 409 || refused.Code != "not_ready" || !strings.Contains(refused.Error, "needs 1 more approval") || refused.Next == "" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The author's own approval does not count; the administrator's does.
	if r := otto.do("POST", prURL+"/reviews", `{"verdict":"approve"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	otto.get(prURL).json(t, &pr)
	if ok, _ := pr.action("merge"); ok || pr.Readiness.Approvals != 0 {
		t.Fatalf("a self approval counted: %+v", pr.Readiness)
	}
	if r := ada.do("POST", prURL+"/reviews", `{"verdict":"approve","body":"looks good"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	otto.get(prURL).json(t, &pr)
	if ok, why := pr.action("merge"); !ok {
		t.Fatalf("merge: %q %+v", why, pr.Readiness)
	}
	if r := vic.do("POST", prURL+"/merge", `{}`); r.status != 403 {
		t.Fatalf("a viewer merged: %d", r.status)
	}

	// Merge by squash, deleting the branch.
	r = otto.do("POST", prURL+"/merge", `{"method":"squash","delete_branch":true,"expect_head":"`+pr.HeadSHA+`"}`)
	var merged struct {
		Pull          prView `json:"pull"`
		Commit        string `json:"commit"`
		BranchDeleted bool   `json:"branch_deleted"`
	}
	r.json(t, &merged)
	if r.status != 200 || merged.Pull.State != pulls.Merged || !merged.BranchDeleted || merged.Pull.MergeMethod != "squash" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if ok, _ := merged.Pull.action("merge"); ok {
		t.Error("a merged pull request still offers merge")
	}
	if ok, _ := merged.Pull.action("revert"); !ok {
		t.Error("a merged pull request does not offer revert")
	}
	out := mustGit(t, src, "ls-remote", url)
	if !strings.Contains(out, merged.Commit+"\trefs/heads/main") || strings.Contains(out, "refs/heads/feature") || !strings.Contains(out, "refs/pull/1/head") {
		t.Fatalf("ls-remote after the merge:\n%s", out)
	}
	// Revert opens a new pull request that undoes it.
	var rev prView
	if r := otto.do("POST", prURL+"/revert", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &rev)
	}
	if rev.Number != 2 || rev.Reverts != 1 || rev.SourceBranch != "revert-pr-1" {
		t.Fatalf("%+v", rev.PullRequest)
	}
	// Close and reopen through PATCH; the audit trail has the changes.
	if r := otto.do("PATCH", api+"/pulls/2", `{"state":"closed"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.do("PATCH", api+"/pulls/2", `{"state":"open"}`); r.status != 403 {
		t.Fatalf("a viewer reopened: %d", r.status)
	}
	if r := otto.do("PATCH", api+"/pulls/2", `{"state":"open"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var audit struct {
		Entries []struct{ Action, Target, Detail string } `json:"entries"`
	}
	ada.get(e.srv.URL+"/api/v1/audit?limit=100").json(t, &audit)
	var seen []string
	for _, a := range audit.Entries {
		seen = append(seen, a.Action)
	}
	for _, want := range []string{"pull.create", "pull.merge", "pull.review", "pull.revert", "pull.update"} {
		if !strings.Contains(strings.Join(seen, " "), want) {
			t.Errorf("no audit entry %s in %v", want, seen)
		}
	}
}

const prPipeline = `
pipeline: app
on:
  push: { branches: [main] }
  pull_request: {}
steps:
  gate: {kind: approval, message: "Ship it?"}
`

// Opening a pull request starts the pipeline's pull_request run on its head;
// the run is a check of the pull request, and a required check that has not
// passed is named in the sentence that says why it cannot merge.
func TestPullRequestRunsAreChecks(t *testing.T) {
	h := sshEnv(t)
	e := h.e
	old := egress.Default.Policy()
	t.Cleanup(func() { _ = egress.Default.Set(old) })
	if err := egress.Default.Set(egress.Policy{AllowLocal: true, Allow: []string{"127.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.api.Forge.Provider.Repositories().Create(t.Context(), forge.NewRepository{Home: "acme", Name: "shop", Visibility: forge.Public}); err != nil {
		t.Fatal(err)
	}
	otto, ada := e.signIn(t, "otto"), e.signIn(t, "ada")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"
	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	gitAt(t, src, date, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(src, ".launchway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".launchway", "pipeline.yaml"), []byte(prPipeline), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")
	if err := e.api.Projects.Reconfigure([]source.Project{{Name: "shop", URL: e.srv.URL + "/git/acme/shop.git", Poll: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.Start(t.Context())
	hostedWait(t, "the project's first look", func() bool {
		seen, _ := e.eng.SeenRefs("shop")
		if st, err := e.api.Projects.Get("shop"); err == nil && st.Error != "" {
			t.Fatalf("the project: %s", st.Error)
		}
		return seen["refs/heads/main"] != ""
	})

	gitAt(t, src, date, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "add f")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/feature")
	if r := ada.do("PUT", api+"/merge-rules", `{"rules":[{"pattern":"main","required_checks":["app"]}]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var pr prView
	if r := otto.do("POST", api+"/pulls", `{"title":"Add f","source_branch":"feature"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &pr)
	}
	prURL := api + "/pulls/1"
	hostedWait(t, "the run of the pull request", func() bool {
		otto.get(prURL).json(t, &pr)
		return len(pr.LinkedRuns) == 1
	})
	var run struct {
		Event       string `json:"event"`
		SHA         string `json:"sha"`
		PullRequest int    `json:"pull_request"`
	}
	ada.get(e.srv.URL+"/api/v1/runs/"+pr.LinkedRuns[0]).json(t, &run)
	if run.Event != "pull_request" || run.PullRequest != 1 || run.SHA != pr.HeadSHA {
		t.Fatalf("the run: %+v (head %s)", run, pr.HeadSHA)
	}
	var found bool
	for _, c := range pr.Readiness.Checks {
		if c.Name == "app" && c.Required && c.RunID == pr.LinkedRuns[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("the run is not a check of the pull request: %+v", pr.Readiness.Checks)
	}
	waitMergeable(t, otto, prURL, pulls.MergeClean)
	otto.get(prURL).json(t, &pr)
	if ok, why := pr.action("merge"); ok || why != "`app` has not passed" {
		t.Fatalf("merge: %v %q", ok, why)
	}
}
