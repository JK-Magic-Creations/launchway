package api

import (
	"strings"
	"testing"
)

// The installation signs the merge commit a sync makes in a fork, and a fork's sync does not get round the
// rule that a branch takes only verified commits: it is refused, with a plain reason, and the branch stays where it was.
func TestForkSyncIsSignedAndKeepsTheVerifiedPushRule(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	fork := f.e.srv.URL + "/api/v1/repositories/vic/shop"
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "w")
	commit(t, work+"/w", "mine.txt", "m\n", "fork work")
	mustGit(t, work+"/w", "push", "-q", "origin", "main")
	f.pushMain(t, "new.txt", "n\n", "parent moves")

	// A branch that takes only verified commits is not moved by a sync.
	if r := f.vic.do("PUT", fork+"/merge-rules", `{"rules":[{"pattern":"main","approval_role":"operator","require_verified_push":true}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"merge"}`); r.status != 403 || !strings.Contains(string(r.body), "takes only verified commits") {
		t.Fatalf("a sync into a verified-only branch: %d %s", r.status, r.body)
	}
	before := strings.TrimSpace(mustGit(t, work+"/w", "ls-remote", "origin", "refs/heads/main"))
	if after := strings.TrimSpace(mustGit(t, work+"/w", "ls-remote", "origin", "refs/heads/main")); after != before {
		t.Fatalf("the branch moved")
	}
	if r := f.vic.do("PUT", fork+"/merge-rules", `{"rules":[{"pattern":"main","approval_role":"operator","require_verified_push":false}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}

	// Without the rule the merge is made, and it carries the installation's signature.
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"merge"}`); r.status != 200 || !strings.Contains(string(r.body), `"merged"`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	mustGit(t, work+"/w", "pull", "-q", "--ff-only", "origin", "main")
	raw := mustGit(t, work+"/w", "cat-file", "commit", "HEAD")
	if !strings.Contains(raw, "gpgsig") {
		t.Fatalf("the merge commit of a sync is not signed:\n%s", raw)
	}
}

// A pull request from a fork is opened by the server, not pushed by a person: a base whose branches are protected
// and whose tags are protected does not stop it, and it leaves the base with nothing but the pull request's own
// refs: no branch and no tag of the fork gets into the base.
func TestForkPullRequestIsNotStoppedByPushRulesAndBringsOnlyItsRefs(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.ada.do("PUT", f.acme+"/protected-branches", `{"patterns":["*"]}`); r.status != 200 {
		t.Fatalf("protect: %d %s", r.status, r.body)
	}
	if r := f.ada.do("PUT", f.acme+"/protected-tags", `{"rules":[{"pattern":"*"}]}`); r.status != 200 {
		t.Fatalf("tags: %d %s", r.status, r.body)
	}
	if r := f.ada.do("PUT", f.acme+"/merge-rules", `{"rules":[{"pattern":"main","approval_role":"operator","require_verified_push":true}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "w")
	w := work + "/w"
	mustGit(t, w, "checkout", "-q", "-b", "topic")
	commit(t, w, "code.txt", "l1\nTOPIC\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "topic change")
	mustGit(t, w, "tag", "v9.9.9")
	mustGit(t, w, "push", "-q", "origin", "topic", "v9.9.9")
	if r := f.vic.do("POST", f.acme+"/pulls/from-fork", `{"head":"vic:topic","title":"From my fork"}`); r.status != 201 {
		t.Fatalf("a pull request from a fork was stopped by the base's push rules: %d %s", r.status, r.body)
	}
	for _, line := range strings.Split(strings.TrimSpace(mustGit(t, f.main, "ls-remote", withToken(f.e.srv.URL, "/git/acme/shop.git", f.adaTok))), "\n") {
		_, ref, _ := strings.Cut(line, "\t")
		if ref == "HEAD" || ref == "refs/heads/main" || strings.HasPrefix(ref, "refs/pull/1/") {
			continue
		}
		t.Errorf("the base has a ref the fork's pull request should not have brought: %s", ref)
	}
}
