package githostaccept

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// anonGet requests a URL with no credential and returns the status and body.
func anonGet(url string) (int, string, error) {
	resp, err := httpc.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b), nil
}

func TestAT_HG_07_PrivateAndTokens(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-07")
	name, _ := c.pushed(t)
	url := c.repoURL(name)
	if out, err := c.git("", false, "ls-remote", url); err == nil {
		t.Fatalf("anonymous clone of a private repository worked: %s", out)
	}
	r.Saw(t, "anonymous git access to the new (private) repository refused")
	bad := &Config{User: c.User, Token: "not-a-token-" + randHex(4)}
	if out, err := c.gitWith("", append(c.gitEnv(false), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: "+bad.basic()), "ls-remote", url); err == nil {
		t.Fatalf("a wrong token was accepted: %s", out)
	}
	r.Saw(t, "a wrong token is refused")
	s1, b1, _ := anonGet(url + "/info/refs?service=git-upload-pack")
	s2, b2, _ := anonGet(c.repoURL(repoPrefix+"nosuch-"+randHex(4)) + "/info/refs?service=git-upload-pack")
	if s1 != s2 || b1 != b2 {
		t.Fatalf("an existing private repository answers (%d) differently from an unknown name (%d): it reveals which names exist", s1, s2)
	}
	r.Saw(t, "anonymous: a private repository and an unknown name get the same answer (%d)", s1)
	if st, _, _ := c.do("GET", "/api/v1"+c.repoPath(name), nil, true); st != 404 && st != 401 {
		t.Fatalf("anonymous API read of a private repository: status %d", st)
	}
	r.Saw(t, "anonymous API read of the private repository refused")
	c.readOnlyToken(t, r, url)
}

// readOnlyToken runs the per-repository read-only token checks when a token
// and an existing repository are supplied.
func (c *Config) readOnlyToken(t *testing.T, r *Result, own string) {
	t.Helper()
	tok, repo := os.Getenv("LW_ACCEPT_RO_TOKEN"), os.Getenv("LW_ACCEPT_RO_REPO")
	if tok == "" || repo == "" {
		r.NotAutomated(t, "a token that reads one repository only: set LW_ACCEPT_RO_TOKEN and LW_ACCEPT_RO_REPO (the existing host's tokens are not repository-scoped; making a restricted user on the live host is not allowed)")
		return
	}
	ro := &Config{User: c.User, Token: tok}
	env := append(c.gitEnv(false), "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: "+ro.basic())
	roURL := c.URL + "/" + repo + ".git"
	if out, err := c.gitWith("", env, "ls-remote", roURL); err != nil {
		t.Fatalf("the read-only token cannot read %s: %s", repo, ro.scrub(out))
	}
	r.Saw(t, "read-only token reads %s", repo)
	if out, err := c.gitWith("", env, "ls-remote", own); err == nil {
		t.Fatalf("the read-only token read another repository: %s", ro.scrub(out))
	}
	r.Saw(t, "read-only token refused on a repository it was not given")
}

func TestAT_HG_09_PullRequests(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-09")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	for _, b := range []string{"pr-a", "pr-b"} {
		c.must(t, seed, "checkout", "-q", "-b", b, "main")
		c.commit(t, seed, b+".txt", b+"\n", "change "+b)
		c.must(t, seed, "push", "-q", url, b)
		c.waitBranch(t, name, b)
	}
	var num [2]int
	for i, b := range []string{"pr-a", "pr-b"} {
		var pr struct{ Number int }
		c.api(t, "POST", c.repoPath(name)+"/pulls", map[string]any{"head": b, "base": "main", "title": "Title " + b}, &pr, 201)
		num[i] = pr.Number
	}
	merges := func() string {
		w := c.clone(t, url)
		return strings.TrimSpace(c.must(t, w, "rev-list", "--min-parents=2", "--count", "origin/main"))
	}
	before := merges()
	_, ok := eventually(30*time.Second, func() bool {
		st, _, _ := c.do("POST", fmt.Sprintf("/api/v1%s/pulls/%d/merge", c.repoPath(name), num[0]), map[string]any{"Do": "rebase"}, false)
		return st == 200
	})
	if !ok {
		t.Fatal("pull request could not be merged by replaying its commits (rebase)")
	}
	var list []struct {
		Number       int
		Title, State string
		Merged       bool
		Created      string `json:"created_at"`
		Updated      string `json:"updated_at"`
	}
	c.api(t, "GET", c.repoPath(name)+"/pulls?state=all", nil, &list, 200)
	states := map[string]string{}
	for _, p := range list {
		if p.Created == "" || p.Title == "" {
			t.Fatalf("pull request %d lacks a title or timestamp", p.Number)
		}
		states[p.Title] = fmt.Sprintf("%s merged=%v", p.State, p.Merged)
	}
	if len(list) != 2 || !strings.Contains(states["Title pr-a"], "merged=true") || states["Title pr-b"] != "open merged=false" {
		t.Fatalf("pull request states: %v", states)
	}
	r.Saw(t, "2 proposals listed with title and timestamps: %v", states)
	if after := merges(); after != before {
		t.Fatalf("replay merge added a merge commit to main (%s before, %s after)", before, after)
	}
	r.Saw(t, "replay (rebase) merge added no merge commit to main (%s before and after)", before)
	r.NotAutomated(t, "the 16 real pull requests equal to the existing host's (importer, needs both hosts); results attached to a proposal")
}

func TestAT_HG_10_Issues(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-10")
	name := c.newRepo(t)
	rp := c.repoPath(name)
	var lab struct{ ID int }
	if st, b, _ := c.do("POST", "/api/v1"+rp+"/labels", map[string]any{"name": "area/forge", "color": "00aabb"}, false); st == 403 && strings.Contains(string(b), "scope") {
		r.skip(t, "the token lacks the write:issue scope, so issues cannot be created; use a token with it")
	} else if st != 201 {
		t.Fatalf("create label: status %d %.200s", st, b)
	} else {
		_ = json.Unmarshal(b, &lab)
	}
	var ms struct{ ID int }
	c.api(t, "POST", rp+"/milestones", map[string]any{"title": "Now"}, &ms, 201)
	var is struct{ Number int }
	c.api(t, "POST", rp+"/issues", map[string]any{"title": "A story", "body": "Depends on #0", "labels": []int{lab.ID}, "milestone": ms.ID}, &is, 201)
	c.api(t, "POST", fmt.Sprintf("%s/issues/%d/comments", rp, is.Number), map[string]any{"body": "first comment"}, nil, 201)
	c.api(t, "PATCH", fmt.Sprintf("%s/issues/%d", rp, is.Number), map[string]any{"state": "closed"}, nil, 201)
	var got struct {
		State, Body, Title string
		Created            string `json:"created_at"`
		Closed             string `json:"closed_at"`
		Comments           int
		Labels             []struct{ Name string }
		Milestone          struct{ Title string }
	}
	c.api(t, "GET", fmt.Sprintf("%s/issues/%d", rp, is.Number), nil, &got, 200)
	if got.State != "closed" || got.Comments != 1 || len(got.Labels) != 1 || got.Labels[0].Name != "area/forge" || got.Milestone.Title != "Now" || got.Created == "" || got.Closed == "" || got.Body != "Depends on #0" {
		b, _ := json.Marshal(got)
		t.Fatalf("issue round trip lost something: %s", b)
	}
	r.Saw(t, "issue #%d: state %s, label %s, milestone %s, %d comment, created %s, closed %s, body kept", is.Number, got.State, got.Labels[0].Name, got.Milestone.Title, got.Comments, got.Created, got.Closed)
	r.NotAutomated(t, "681 real issues 1:1, the existing host down for an hour draining a queue, ten items claimed through MCP (doc 27 section 10.2)")
}

func TestAT_HG_11_Unused(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-11")
	name := c.newRepo(t)
	rp := c.repoPath(name)
	var rel []any
	c.api(t, "GET", rp+"/releases", nil, &rel, 200)
	if len(rel) != 0 {
		t.Fatalf("a new repository has %d releases", len(rel))
	}
	st, b, _ := c.do("GET", "/api/v1"+rp+"/wiki/pages", nil, false)
	var pages []any
	_ = json.Unmarshal(b, &pages)
	if (st != 200 && st != 404) || len(pages) != 0 {
		t.Fatalf("wiki pages: status %d, %d pages", st, len(pages))
	}
	r.Saw(t, "new repository: 0 releases, 0 wiki pages (wiki status %d)", st)
	var set struct {
		LFSDisabled bool `json:"lfs_disabled"`
	}
	c.api(t, "GET", "/settings/repository", nil, &set, 200)
	r.Saw(t, "settings/repository lfs_disabled=%v", set.LFSDisabled)
	st, b, _ = c.do("GET", "/api/v1/packages/"+c.User, nil, false)
	if st == 200 {
		var pk []any
		_ = json.Unmarshal(b, &pk)
		if len(pk) != 0 {
			t.Fatalf("%d packages exist: raise a story before cut-over", len(pk))
		}
		r.Saw(t, "GET /packages/%s: 0 packages", c.User)
		return
	}
	r.NotAutomated(t, fmt.Sprintf("package count: GET /packages/%s answered %d (the token lacks read:package)", c.User, st))
}

func TestAT_HG_14_PollingAndWebhook(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-14")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	for _, h := range []struct{ label, header string }{{"Basic user:token", c.basic()}, {"token header", "token " + c.Token}} {
		before, err := c.remoteRefs(url, false, h.header)
		if err != nil {
			t.Fatalf("poll with %s: %v", h.label, c.scrub(err.Error()))
		}
		id := c.commit(t, seed, "poll-"+randHex(2)+".txt", "x\n", "poll")
		c.must(t, seed, "push", "-q", url, "main")
		took, ok := eventually(15*time.Second, func() bool {
			m, err := c.remoteRefs(url, false, h.header)
			return err == nil && m["refs/heads/main"] == id && before["refs/heads/main"] != id
		})
		if !ok {
			t.Fatalf("a poll with %s did not see the new commit within 15 s", h.label)
		}
		r.Saw(t, "ls-remote with %s sees a new commit %s after the push (limit 90 s)", h.label, took.Round(time.Millisecond))
	}
	c.webhook(t, r, name, seed)
}

func (c *Config) webhook(t *testing.T, r *Result, name, seed string) {
	t.Helper()
	rc := c.startReceiver(t)
	secret := randHex(8)
	c.api(t, "POST", c.repoPath(name)+"/hooks", map[string]any{"type": "gitea", "active": true, "events": []string{"push"},
		"config": map[string]string{"url": rc.HookURL(), "content_type": "json", "secret": secret}}, nil, 201)
	id := c.commit(t, seed, "hook.txt", "h\n", "hook")
	c.must(t, seed, "push", "-q", c.repoURL(name), "main")
	// The host may still be working through the pushes made before the hook existed; find the one for this push.
	var d Delivery
	took, ok := eventually(15*time.Second, func() bool {
		for _, x := range rc.Deliveries() {
			if bytes.Contains(x.Body, []byte(id)) {
				d = x
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("no webhook delivery for commit %.12s within 15 s; %d other deliveries arrived (does the host refuse private addresses, or does a firewall block this machine?)", id, len(rc.Deliveries()))
	}
	var p struct {
		Ref, After string
		Repository struct {
			FullName string `json:"full_name"`
		}
	}
	if err := json.Unmarshal(d.Body, &p); err != nil || d.Event != "push" || p.Ref != "refs/heads/main" || p.After != id || !strings.HasSuffix(p.Repository.FullName, "/"+name) {
		t.Fatalf("payload: event %q ref %q after %.12s (want %.12s) repo %q (err %v)", d.Event, p.Ref, p.After, id, p.Repository.FullName, err)
	}
	if !d.signedBy(secret) {
		t.Fatal("the delivery's HMAC-SHA256 signature does not match the secret")
	}
	r.Saw(t, "webhook delivered %s after the push: event push, ref %s, after %.12s, HMAC-SHA256 valid (limit 15 s)", took.Round(time.Millisecond), p.Ref, p.After)
	r.NotAutomated(t, "60 Argo CD applications Synced at the same revision as the existing host (argocd app list)")
}

func TestAT_HG_15_WritePath(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-15")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	c.commit(t, seed, "values.yaml", "tag: 1\n", "image tag 1")
	c.must(t, seed, "push", "-q", url, "main")
	w := c.clone(t, url)
	bump := c.commit(t, w, "values.yaml", "tag: 2\n", "bump image to 2")
	c.must(t, w, "push", "-q", "origin", "main")
	c.must(t, w, "revert", "--no-edit", "HEAD")
	c.must(t, w, "push", "-q", "origin", "main")
	check := c.clone(t, url)
	b, _ := os.ReadFile(check + "/values.yaml")
	if string(b) != "tag: 1\n" {
		t.Fatalf("after the revert values.yaml is %q", b)
	}
	r.Saw(t, "bump %.12s pushed, revert commit pushed, values.yaml back to its first content", bump)
	r.NotAutomated(t, "the identity cannot write to other repositories or to main of ci-templates (needs a second token); Argo sync and rollout verify")
}

func TestAT_HG_16_Statuses(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-16")
	name, seed := c.pushed(t)
	post := func(sha, ctx, state string) {
		c.api(t, "POST", c.repoPath(name)+"/statuses/"+sha, map[string]any{"state": state, "context": ctx, "description": state, "target_url": c.URL + "/run/1"}, nil, 201)
	}
	// Each commit is read only after all its statuses are posted: the existing host caches the combined state of a commit it was just
	// asked about, so reading between posts would test its cache, not the statuses.
	newCommit := func(n string) string {
		id := c.commit(t, seed, n+".txt", n+"\n", "status "+n)
		c.must(t, seed, "push", "-q", c.repoURL(name), "main")
		return id
	}
	check := func(sha, want string, records int) {
		t.Helper()
		var s struct {
			State    string
			Statuses []any
		}
		c.api(t, "GET", c.repoPath(name)+"/commits/"+sha+"/status", nil, &s, 200)
		if s.State != want || len(s.Statuses) != records {
			t.Fatalf("commit %.12s: combined state %q with %d contexts, want %q with %d", sha, s.State, len(s.Statuses), want, records)
		}
	}
	a, b, d := newCommit("a"), newCommit("b"), newCommit("c")
	post(a, "lw/ci", "pending")
	check(a, "pending", 1)
	post(b, "lw/ci", "pending")
	post(b, "lw/ci", "success")
	check(b, "success", 1)
	post(d, "lw/ci", "success")
	post(d, "lw/scan", "failure")
	check(d, "failure", 2)
	var list []any
	c.api(t, "GET", c.repoPath(name)+"/commits/"+b+"/statuses", nil, &list, 200)
	r.Saw(t, "pending alone is pending; pending then success is success; success plus a failing second context is failure (2 contexts); %d status records kept for the second commit", len(list))
	r.NotAutomated(t, "Beacon Deliveries within 30 s; a failing run blocking a proposal (ref rule); a read between two posts of one commit (the existing host answers from a cache: Launchway must not)")
}

func TestAT_HG_17_SignInRedirect(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-17")
	resp, err := httpc.Get(c.URL + c.OIDCPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
		t.Skipf("%s answered %d with no redirect: no sign-in source named by LW_ACCEPT_OIDC_PATH", c.OIDCPath, resp.StatusCode)
	}
	u, _ := http.NewRequest("GET", loc, nil)
	r.Saw(t, "%s redirects (%d) to the identity provider at %s%s", c.OIDCPath, resp.StatusCode, u.URL.Host, u.URL.Path)
	r.NotAutomated(t, "login as jkhunter with account linking, MFA, callback on both replicas, a new user not auto-granted write (interactive)")
}
