package api

import (
	"strings"
	"testing"
)

// The two push rules that live in pushPolicy hold together: a branch that takes only verified commits and a
// protected tag pattern on the same repository. A push is refused by each rule on its own, and by neither when it
// is allowed by both; a signed commit on the branch and a tag by an administrator pass over SSH and HTTPS.
func TestVerifiedBranchAndProtectedTagRulesHoldTogether(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada, otto := h.e.signIn(t, "ada"), h.e.signIn(t, "otto")
	adaKey, adaPub := h.key(t, "ada")
	ottoKey, ottoPub := h.key(t, "otto")
	h.register(t, ada, "a", adaPub)
	h.register(t, otto, "o", ottoPub)
	adaTok := h.e.tokenFor(t, "ada", "t")
	api := h.e.srv.URL + "/api/v1/repositories/acme/shop"

	src := workingCopy(t)
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	for _, call := range [][3]string{
		{"PUT", "/protected-branches", `{"patterns":["main"]}`},
		{"PUT", "/merge-rules", `{"rules":[{"pattern":"main","approval_role":"operator","require_verified_push":true}]}`},
		{"PUT", "/protected-tags", `{"rules":[{"pattern":"v*"}]}`},
	} {
		if r := ada.do(call[0], api+call[1], call[2]); r.status != 200 {
			t.Fatalf("%s: %d %s", call[1], r.status, r.body)
		}
	}
	if r := ada.do("POST", h.e.srv.URL+"/api/v1/user/signing-keys", `{"name":"laptop","public_key":"`+strings.TrimSpace(adaPub)+`"}`); r.status != 201 {
		t.Fatalf("register: %d %s", r.status, r.body)
	}

	// Refused by the verified-push rule: an unsigned commit to the branch, from an administrator.
	commit(t, src, "u.txt", "x\n", "unsigned")
	out, err := h.git(t, adaKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/main")
	if err == nil || !strings.Contains(out, "takes only verified commits") {
		t.Fatalf("an unsigned commit reached the branch: %v\n%s", err, out)
	}
	// Refused by the protected-tag rule: an operator making a protected tag (the commit itself is no concern of the branch rule).
	out, err = h.git(t, ottoKey, src, "push", h.url("acme/shop"), "HEAD:refs/tags/v1.0.0")
	if err == nil || !strings.Contains(out, "only an administrator of the repository may make a tag like it") {
		t.Fatalf("an operator made a protected tag: %v\n%s", err, out)
	}
	// One push with both: the branch ref and the tag are each refused, and neither lands.
	out, err = h.git(t, ottoKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/main", "HEAD:refs/tags/v1.0.1")
	if err == nil {
		t.Fatalf("a push broken by both rules went through:\n%s", out)
	}

	// Allowed by both: a signed commit on the branch, then a tag on it by an administrator, over SSH and HTTPS.
	mustGit(t, src, "reset", "-q", "--hard", "HEAD~1")
	signedCommit(t, src, adaKey, "s.txt", "signed by ada")
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	mustGit(t, src, "tag", "v1.0.0")
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "refs/tags/v1.0.0")
	mustGit(t, src, "tag", "v1.0.2")
	if out, err := git(t, src, "push", withToken(h.e.srv.URL, "/git/acme/shop.git", adaTok), "refs/tags/v1.0.2"); err != nil {
		t.Fatalf("a signed, allowed tag push over HTTPS: %v\n%s", err, out)
	}
	// The protected tag stays protected after the branch took the signed commit.
	if out, err := h.git(t, adaKey, src, "push", h.url("acme/shop"), ":refs/tags/v1.0.0"); err == nil || !strings.Contains(out, "cannot be deleted") {
		t.Fatalf("a protected tag was deleted: %v\n%s", err, out)
	}
}
