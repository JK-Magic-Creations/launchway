package api

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// signedCommit makes a commit in dir signed with the SSH key at keyPath.
func signedCommit(t *testing.T, dir, keyPath, file, msg string) string {
	t.Helper()
	if err := os.WriteFile(dir+"/"+file, []byte(msg+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	cmd := exec.Command("git", "-c", "gpg.format=ssh", "-c", "user.signingkey="+keyPath, "commit", "-q", "-S", "-m", msg)
	cmd.Dir = dir
	cmd.Env = testlib.GitEnv(t, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signed commit: %v\n%s", err, out)
	}
	return strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
}

// A branch that takes only verified commits refuses a push of anything else,
// over SSH and HTTPS alike and from an administrator too, with the same plain
// refusal; a commit signed by a key the pusher registered goes through; and
// the same commit is shown as verified by the signatures route.
func TestOnlyVerifiedCommitsMayBePushed(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada := h.e.signIn(t, "ada")
	adaKey, adaPub := h.key(t, "ada")
	otherKey, _ := h.key(t, "stranger")
	h.register(t, ada, "a", adaPub)
	adaTok := h.e.tokenFor(t, "ada", "t")
	api := h.e.srv.URL + "/api/v1/repositories/acme/shop"

	src := workingCopy(t)
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	if r := ada.do("PUT", api+"/protected-branches", `{"patterns":["main"]}`); r.status != 200 {
		t.Fatalf("protect: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/merge-rules", `{"rules":[{"pattern":"main","approval_role":"operator","require_verified_push":true,"require_signed_commits":true}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	var rules struct {
		Rules []struct {
			Verified bool `json:"require_verified_push"`
			Signed   bool `json:"require_signed_commits"`
		} `json:"rules"`
	}
	ada.get(api+"/merge-rules").json(t, &rules)
	if len(rules.Rules) != 1 || !rules.Rules[0].Verified || !rules.Rules[0].Signed {
		t.Fatalf("the rule was not kept: %+v", rules)
	}

	// An unsigned commit: refused on both transports, in the same words.
	commit(t, src, "u.txt", "x\n", "unsigned")
	httpsURL := withToken(h.e.srv.URL, "/git/acme/shop.git", adaTok)
	sshOut, sshErr := h.git(t, adaKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/main")
	httpsOut, httpsErr := git(t, src, "push", httpsURL, "HEAD:refs/heads/main")
	if sshErr == nil || httpsErr == nil {
		t.Fatalf("an unsigned commit was pushed: ssh %v https %v", sshErr, httpsErr)
	}
	for _, out := range []string{sshOut, httpsOut} {
		if !strings.Contains(out, "takes only verified commits") || !strings.Contains(out, "unsigned") || !strings.Contains(out, "Signing keys") {
			t.Errorf("the refusal does not say what is wrong and what to do:\n%s", out)
		}
	}
	// Another branch is not affected.
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/topic")

	// Signed by a key nobody registered: refused, saying so.
	mustGit(t, src, "reset", "-q", "--hard", "HEAD~1")
	signedCommit(t, src, otherKey, "s.txt", "by a stranger")
	if out, err := h.git(t, adaKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "unknown key") {
		t.Fatalf("a commit signed by an unregistered key: %v\n%s", err, out)
	}

	// Signed with the key ada registered as a signing key: accepted.
	if r := ada.do("POST", h.e.srv.URL+"/api/v1/user/signing-keys", `{"name":"laptop","public_key":"`+strings.TrimSpace(adaPub)+`"}`); r.status != 201 {
		t.Fatalf("register: %d %s", r.status, r.body)
	}
	mustGit(t, src, "reset", "-q", "--hard", "HEAD~1")
	sha := signedCommit(t, src, adaKey, "v.txt", "signed by ada")
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	var sigs struct {
		Items []struct {
			Object, State, Signer string
			SignerIsCommitter     bool `json:"signer_is_committer"`
		} `json:"items"`
	}
	ada.get(api+"/signatures?shas="+sha).json(t, &sigs)
	if len(sigs.Items) != 1 || sigs.Items[0].State != "verified" || sigs.Items[0].Signer != "ada" {
		t.Fatalf("signatures: %+v", sigs)
	}
}
