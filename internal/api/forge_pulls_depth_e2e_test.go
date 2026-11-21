package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Review depth through the API with real pushes: an owners file on the target
// branch, rules that name owners and a queue, group review requests, the
// inbox, files marked viewed, and who may do what.
func TestReviewDepthThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	api := e.srv.URL + "/api/v1/repositories/acme/shop"

	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	write := func(name, content string) {
		p := filepath.Join(src, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitAt(t, src, date, "init", "-q", "-b", "main")
	write("code.txt", "l1\nl2\nl3\n")
	write(".launchway/owners", "# owners\n/docs/ @vic @group:writers\n*.txt @ada\n")
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")
	gitAt(t, src, date, "checkout", "-q", "-b", "feature")
	write("code.txt", "l1\nl2 changed\nl3\n")
	write("docs/a.md", "doc\n")
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "change")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/feature")

	var pr prView
	if r := otto.do("POST", api+"/pulls", `{"title":"Change","source_branch":"feature","requested_reviewers":["ada"]}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &pr)
	}
	prURL := api + "/pulls/1"
	waitMergeable(t, otto, prURL, pulls.MergeClean)

	// Who owns what, from the target branch's file; a reader may see it.
	var owners struct {
		File   string `json:"file"`
		Owners struct {
			Present bool `json:"present"`
			Files   []struct {
				Path   string   `json:"path"`
				Owners []string `json:"owners"`
			} `json:"files"`
		} `json:"owners"`
	}
	vic.get(prURL+"/owners").json(t, &owners)
	got := map[string]string{}
	for _, f := range owners.Owners.Files {
		got[f.Path] = strings.Join(f.Owners, " ")
	}
	if !owners.Owners.Present || owners.File != ".launchway/owners" || got["code.txt"] != "ada" || got["docs/a.md"] != "vic group:writers" {
		t.Fatalf("%+v", owners)
	}
	// Checking a file says what is wrong, line by line, in plain words.
	var chk struct {
		Valid    bool `json:"valid"`
		Problems []struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	if r := vic.do("POST", api+"/owners/check", `{"text":"a.txt @x\nb.txt x\n"}`); r.status != 200 {
		t.Fatalf("%d", r.status)
	} else {
		r.json(t, &chk)
	}
	if chk.Valid || len(chk.Problems) != 1 || chk.Problems[0].Line != 2 || !strings.Contains(chk.Problems[0].Message, "@name") {
		t.Fatalf("%+v", chk)
	}

	// Rules: only an administrator sets them; the new ones are stored and read back.
	rules := `{"rules":[{"pattern":"main","require_owner_approval":true,"require_latest_push_approved_by_other":true,"merge_queue":true,"dismiss_stale_approvals":true,` +
		`"required_checks_by_path":[{"paths":["/docs/"],"check":"docs"}]}]}`
	if r := otto.do("PUT", api+"/merge-rules", rules); r.status != 403 {
		t.Fatalf("an operator set rules: %d", r.status)
	}
	if r := ada.do("PUT", api+"/merge-rules", `{"rules":[{"pattern":"main","required_checks_by_path":[{"paths":[],"check":"docs"}]}]}`); r.status != 400 || !strings.Contains(string(r.body), "paths") {
		t.Fatalf("a path check with no paths: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/merge-rules", rules); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var back struct{ Rules []pulls.Rule }
	vic.get(api+"/merge-rules").json(t, &back)
	if len(back.Rules) != 1 || !back.Rules[0].RequireOwnerApproval || !back.Rules[0].MergeQueue || !back.Rules[0].RequireFreshApproval || len(back.Rules[0].PathChecks) != 1 {
		t.Fatalf("%+v", back.Rules)
	}

	// The merge sentence names what is missing; a direct merge is refused server-side.
	otto.get(prURL).json(t, &pr)
	if pr.Readiness.CanMerge || !strings.Contains(pr.Readiness.Why, "owner") || !strings.Contains(pr.Readiness.Why, "`docs`") {
		t.Fatalf("%s", pr.Readiness.Why)
	}
	if r := otto.do("POST", prURL+"/merge", `{}`); r.status != 409 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A viewer cannot queue; an operator is told what is missing.
	if r := vic.do("POST", prURL+"/queue", `{}`); r.status != 403 {
		t.Fatalf("a viewer queued: %d", r.status)
	}
	if r := otto.do("POST", prURL+"/queue", `{}`); r.status != 409 || !strings.Contains(string(r.body), "owner") {
		t.Fatalf("queued without the owners: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", prURL+"/queue", `{"priority":5}`); r.status != 403 {
		t.Fatalf("an operator gave priority: %d", r.status)
	}
	var q struct {
		Enabled bool   `json:"enabled"`
		Branch  string `json:"branch"`
		Entry   any    `json:"entry"`
	}
	vic.get(prURL+"/queue").json(t, &q)
	if !q.Enabled || q.Branch != "main" || q.Entry != nil {
		t.Fatalf("%+v", q)
	}
	if r := otto.do("DELETE", prURL+"/queue", ""); r.status != 404 {
		t.Fatalf("dequeue of one not queued: %d", r.status)
	}

	// A group is asked to review; a viewer cannot ask.
	if r := vic.do("POST", prURL+"/review-requests", `{"groups":["writers"]}`); r.status != 403 {
		t.Fatalf("a viewer asked a group: %d", r.status)
	}
	var asked struct {
		Groups []string `json:"requested_reviewer_groups"`
	}
	if r := ada.do("POST", prURL+"/review-requests", `{"groups":["writers"]}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &asked)
	}
	if len(asked.Groups) != 1 || asked.Groups[0] != "writers" {
		t.Fatalf("%+v", asked)
	}
	if r := ada.do("DELETE", prURL+"/review-requests/group:writers", ""); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", prURL+"/review-requests/group:writers", ""); r.status != 404 {
		t.Fatalf("%d", r.status)
	}

	// ada was asked to review at opening: it is in her inbox, and not in vic's.
	var inbox struct {
		Items []struct {
			Repository string `json:"repository"`
			Number     int    `json:"number"`
			Kind       string `json:"kind"`
			Why        string `json:"why"`
		} `json:"items"`
	}
	ada.get(e.srv.URL+"/api/v1/pulls/inbox").json(t, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Repository != "acme/shop" || inbox.Items[0].Kind != "review_requested" || !strings.Contains(inbox.Items[0].Why, "asked you") {
		t.Fatalf("%+v", inbox)
	}
	vic.get(e.srv.URL+"/api/v1/pulls/inbox").json(t, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("someone else's inbox: %+v", inbox)
	}
	// Changes asked on otto's pull request reach otto's inbox.
	if r := ada.do("POST", prURL+"/reviews", `{"verdict":"request_changes","body":"please"}`); r.status != 201 {
		t.Fatalf("%d", r.status)
	}
	otto.get(e.srv.URL+"/api/v1/pulls/inbox").json(t, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Kind != "changes_requested" {
		t.Fatalf("%+v", inbox)
	}

	// Viewed marks are the caller's own.
	var viewed struct {
		Viewed []string `json:"viewed"`
	}
	if r := vic.do("PUT", prURL+"/viewed", `{"paths":["code.txt"],"viewed":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	} else {
		r.json(t, &viewed)
	}
	if len(viewed.Viewed) != 1 {
		t.Fatalf("%+v", viewed)
	}
	ada.get(prURL+"/viewed").json(t, &viewed)
	if len(viewed.Viewed) != 0 {
		t.Fatalf("a mark leaked: %+v", viewed)
	}
	if r := vic.do("PUT", prURL+"/viewed", `{"paths":["../etc"],"viewed":true}`); r.status != 400 {
		t.Fatalf("%d", r.status)
	}

	// Updating the branch: a viewer cannot; with nothing behind it says so.
	if r := vic.do("POST", prURL+"/update-branch", `{}`); r.status != 403 {
		t.Fatalf("%d", r.status)
	}
	if r := otto.do("POST", prURL+"/update-branch", `{}`); r.status != 409 || !strings.Contains(string(r.body), "already contains") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
