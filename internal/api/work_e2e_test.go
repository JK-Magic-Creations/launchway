package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/work"
)

func TestWorkItemsThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "otto")
	hostedRepo(t, e, "acme/shop")
	base := e.srv.URL + "/api/v1/work"
	scope := "?scope=project:acme/shop"

	// A viewer reads and cannot write; an operator creates.
	if r := vic.do("POST", base+"/items"+scope, `{"title":"nope"}`); r.status != 403 {
		t.Fatalf("a viewer created: %d %s", r.status, r.body)
	}
	var it work.Item
	r := otto.do("POST", base+"/items"+scope, `{"title":"Crash on login","type":"bug","labels":["ui"],"criteria":["no crash"],"estimate":2}`)
	r.json(t, &it)
	if r.status != 201 || it.Ref != "SHOP-1" || it.Reporter != "otto" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var det workDetail
	vic.get(base+"/items/SHOP-1").json(t, &det)
	if det.Title != "Crash on login" || len(det.Criteria) != 1 || len(det.Actions) != 0 {
		t.Fatalf("%+v", det)
	}
	if r := vic.do("PATCH", base+"/items/SHOP-1", `{"title":"x"}`); r.status != 403 {
		t.Fatalf("viewer patch: %d", r.status)
	}
	// A stale version is refused.
	if r := otto.do("PATCH", base+"/items/SHOP-1", `{"expected_version":99,"title":"x"}`); r.status != 409 {
		t.Fatalf("stale: %d %s", r.status, r.body)
	}
	// Only a person finishes work, and finishing is audited.
	if r := otto.do("POST", base+"/items/SHOP-1/move", `{"state":"in_progress"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Confidential: the reporter and maintainers only.
	r = otto.do("POST", base+"/items"+scope, `{"title":"Vulnerability","confidential":true}`)
	r.json(t, &it)
	if r.status != 201 || !it.Confidential {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := vic.get(base + "/items/" + it.Ref); r.status != 404 {
		t.Fatalf("a viewer saw a confidential item: %d", r.status)
	}
	var pg work.Page
	vic.get(base+"/items"+scope).json(t, &pg)
	if pg.Total != 1 {
		t.Fatalf("viewer's list: %+v", pg)
	}
	ada.get(base+"/items"+scope).json(t, &pg)
	if pg.Total != 2 {
		t.Fatalf("admin's list: %+v", pg)
	}
	// An unreadable project answers as if it did not exist.
	if r := vic.get(base + "/items?scope=project:other/secret"); r.status != 200 {
		t.Fatalf("%d", r.status)
	}
	// Query, comment, link, board, sprint.
	ada.get(base+"/items"+scope+"&q=label:ui+is:open").json(t, &pg)
	if pg.Total != 1 {
		t.Fatalf("%+v", pg)
	}
	if r := otto.do("POST", base+"/items/SHOP-1/comments", `{"body":"on it, @ada"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var inbox struct {
		Entries []work.InboxEntry
		Unread  int
	}
	ada.get(base+"/inbox").json(t, &inbox)
	if inbox.Unread < 1 || inbox.Entries[0].Kind != work.InboxMentioned {
		t.Fatalf("%+v", inbox)
	}
	var b work.Board
	vic.get(base+"/board"+scope).json(t, &b)
	if len(b.Columns) != 6 || b.Columns[2].Count != 1 {
		t.Fatalf("%+v", b)
	}
	sp := ada.do("POST", base+"/sprints", `{"scope":"project:acme/shop","name":"S1","start_at":"2026-03-02","end_at":"2026-03-13"}`)
	if sp.status != 201 {
		t.Fatalf("%d %s", sp.status, sp.body)
	}
	if r := otto.do("POST", base+"/sprints", `{"scope":"project:acme/shop","name":"S2","start_at":"2026-03-02","end_at":"2026-03-13"}`); r.status != 201 {
		t.Fatalf("an operator may plan: %d", r.status)
	}
	// Setting up work is for those who maintain the project.
	put := `{"scope":"project:acme/shop","settings":{"estimate_unit":"hours"}}`
	if r := otto.do("PUT", base+"/settings", put); r.status != 403 {
		t.Fatalf("operator settings: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", base+"/settings", put); r.status != 200 {
		t.Fatalf("admin settings: %d %s", r.status, r.body)
	}
	// Loosening what agents may do needs a reason.
	loose := `{"scope":"project:acme/shop","settings":{"agents":{"autonomy":{"level":2,"paths":["docs/**"]}}}}`
	if r := ada.do("PUT", base+"/settings", loose); r.status != 400 || !strings.Contains(string(r.body), "reason") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Every change is in the audit trail.
	found := false
	for _, a := range e.auditEntries(t) {
		found = found || (a.Action == "work.create" && a.Target == "SHOP-1" && a.User == "otto")
	}
	if !found {
		t.Error("creating work is not audited")
	}
	// Attachments are served as downloads, never as pages.
	ar := otto.do("POST", base+"/items/SHOP-1/attachments?name=x.html", "<script>alert(1)</script>", "Content-Type", "text/html")
	var att work.Attachment
	ar.json(t, &att)
	got := vic.get(base + "/attachments/" + att.ID)
	if ar.status != 201 || got.status != 200 || !strings.HasPrefix(got.header.Get("Content-Disposition"), "attachment") || got.header.Get("X-Content-Type-Options") != "nosniff" || got.header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("%d %v", got.status, got.header)
	}
}

// The whole agent loop, scripted without a model: an agent is registered and
// given work, claims it, pushes a branch with its own token, opens a pull
// request that names the item, hands over for review; the merge gate refuses
// until a person approves; the person merges and the item is done.
func TestAgentLoopThroughTheAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, otto, vic := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic")
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	repo := e.srv.URL + "/api/v1/repositories/acme/shop"
	base := e.srv.URL + "/api/v1/work"

	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	gitAt(t, src, date, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "add", "-A")
	gitAt(t, src, date, "commit", "-q", "-m", "first")
	mustGit(t, src, "push", "-q", url, "HEAD:refs/heads/main")

	// Only a person who maintains registers an agent.
	body := `{"name":"docs","scope":{"kind":"installation"},"projects":["acme/shop"],"labels":["agent:docs"],"types":["task","issue"]}`
	if r := otto.do("POST", base+"/agents", body); r.status != 403 {
		t.Fatalf("an operator registered an agent: %d %s", r.status, r.body)
	}
	var reg struct {
		Agent work.Agent
		Token string
	}
	r := ada.do("POST", base+"/agents", body)
	r.json(t, &reg)
	if r.status != 201 || !strings.HasPrefix(reg.Token, "lwa_") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	agent := func(method, path, b string) response {
		return vic.do(method, base+path, b, "Authorization", "Bearer "+reg.Token)
	}

	// The item is for the agent through its label; a person writes it.
	var it work.Item
	otto.do("POST", base+"/items?scope=project:acme/shop", `{"title":"Fix the typo in the guide","type":"task","labels":["agent:docs"]}`).json(t, &it)
	if r := agent("POST", "/agent/claim", `{"item":"`+it.Ref+`"}`); r.status != 200 {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	// The agent works in a clone with its own token.
	agentURL := withToken(e.srv.URL, "/git/acme/shop.git", reg.Token)
	mustGit(t, src, "checkout", "-q", "-b", "work/docs-1")
	if err := os.WriteFile(filepath.Join(src, "docs", "guide.md"), []byte("# Guide\n\nFixed.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, date, "commit", "-q", "-am", "Fix typo")
	mustGit(t, src, "push", "-q", agentURL, "HEAD:refs/heads/work/docs-1")
	// Its token cannot write where it was not registered.
	hostedRepo(t, e, "acme/blog")
	if out, err := git(t, src, "push", withToken(e.srv.URL, "/git/acme/blog.git", reg.Token), "HEAD:refs/heads/x"); err == nil {
		t.Fatalf("an agent pushed outside its projects:\n%s", out)
	}
	pr := agentOpen(t, vic, repo, reg.Token, `{"title":"Fix typo","body":"fixes `+it.Ref+`","source_branch":"work/docs-1"}`)
	if pr.Author != "agent:docs" {
		t.Fatalf("author %q", pr.Author)
	}
	// Opening it moved the item to review and linked it.
	var det workDetail
	ada.get(base+"/items/"+it.Ref).json(t, &det)
	if det.State != "in_review" || len(det.Code) != 1 || det.Code[0].Kind != "pull" {
		t.Fatalf("%+v", det)
	}
	// An agent never approves through the ordinary review, nor merges, nor sets auto-merge.
	prURL := repo + "/pulls/" + fmt.Sprint(pr.Number)
	waitMergeable(t, ada, prURL, pulls.MergeClean)
	for _, c := range []struct{ method, path, body string }{{"POST", "/reviews", `{"verdict":"approve"}`}, {"POST", "/merge", `{}`}, {"PATCH", "", `{"auto_merge":"merge"}`}} {
		if r := vic.do(c.method, prURL+c.path, c.body, "Authorization", "Bearer "+reg.Token); r.status < 400 {
			t.Errorf("an agent did %s %s: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// The agent hands it over; a person is told.
	if r := agent("POST", "/agent/items/"+it.Ref+"/request-review", `{"summary":"typo fixed"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var home struct {
		Agents work.AgentWork
	}
	otto.get(base+"/home").json(t, &home)
	if len(home.Agents.Review) != 1 {
		t.Fatalf("home: %+v", home)
	}
	// Merging the agent's change without a person's approval is refused, whoever asks.
	if r := otto.do("POST", prURL+"/merge", `{}`); r.status != 409 || !strings.Contains(string(r.body), "needs a person's approval") {
		t.Fatalf("merge without approval: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", prURL+"/reviews", `{"verdict":"approve"}`); r.status >= 400 {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", prURL+"/merge", `{}`); r.status != 200 {
		t.Fatalf("merge after approval: %d %s", r.status, r.body)
	}
	// Merged by a person: the item is done, and its history marks the agent's actions.
	ada.get(base+"/items/"+it.Ref).json(t, &det)
	if det.State != "done" {
		t.Fatalf("state %s", det.State)
	}
	var hist struct{ Events []work.Event }
	ada.get(base+"/items/"+it.Ref+"/history").json(t, &hist)
	agentMade := 0
	for _, ev := range hist.Events {
		if ev.ActorKind == "agent" {
			agentMade++
		}
	}
	if agentMade < 2 {
		t.Fatalf("the history does not mark agent-made changes: %+v", hist.Events)
	}
	// The kill switch: the token stops working at once.
	if r := ada.do("PATCH", base+"/agents/docs", `{"disabled":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := agent("GET", "/agent/queue", ""); r.status != 401 {
		t.Fatalf("a switched-off agent: %d %s", r.status, r.body)
	}
	if r := agent("GET", "/agent/me", ""); r.status != 401 {
		t.Fatalf("%d", r.status)
	}
}

func agentOpen(t *testing.T, b *browser, repo, token, body string) pulls.PullRequest {
	t.Helper()
	var pr pulls.PullRequest
	r := b.do("POST", repo+"/pulls", body, "Authorization", "Bearer "+token)
	r.json(t, &pr)
	if r.status != 201 {
		t.Fatalf("an agent opening a pull request: %d %s", r.status, r.body)
	}
	return pr
}
