package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A push to a branch without an open pull request prints one line with the
// address to open one; -o pr.create opens it as the push finishes; a branch
// that has a pull request, and the default branch, print nothing.
func TestPushPrintsTheAddressToOpenAPullRequest(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	tok := e.tokenFor(t, "ada", "t")
	url := withToken(e.srv.URL, "/git/acme/shop.git", tok)
	src := t.TempDir()
	date := "2026-06-01T10:00:00Z"
	commit := func(name, msg string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(msg+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitAt(t, src, date, "add", "-A")
		gitAt(t, src, date, "commit", "-q", "-m", msg)
	}
	gitAt(t, src, date, "init", "-q", "-b", "main")
	commit("a.txt", "first")
	if out := mustGit(t, src, "push", url, "HEAD:refs/heads/main"); strings.Contains(out, "pull request") {
		t.Fatalf("a push to the default branch mentions pull requests:\n%s", out)
	}
	gitAt(t, src, date, "checkout", "-q", "-b", "fix-rounding")
	commit("b.txt", "second")
	out := mustGit(t, src, "push", url, "HEAD:refs/heads/fix-rounding")
	if !strings.Contains(out, "remote: Open a pull request for fix-rounding: "+e.srv.URL+"/repositories/acme/shop/pulls/new?head=fix-rounding") {
		t.Fatalf("no line with the address:\n%s", out)
	}
	if n := strings.Count(out, "Open a pull request"); n != 1 {
		t.Fatalf("%d lines:\n%s", n, out)
	}

	// -o pr.create with a title and a target opens it as the push finishes.
	gitAt(t, src, date, "checkout", "-q", "-b", "other")
	commit("c.txt", "third")
	out = mustGit(t, src, "push", "-o", "pr.create", "-o", "pr.title=Round prices", "-o", "pr.target=main", url, "HEAD:refs/heads/other")
	if !strings.Contains(out, "Opening a pull request from other into main") {
		t.Fatalf("no line about the pull request being opened:\n%s", out)
	}
	var list struct {
		Items []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Source string `json:"source_branch"`
			Author string `json:"author"`
		} `json:"items"`
	}
	hostedWait(t, "the pull request the push asked for", func() bool {
		ada.get(e.srv.URL+"/api/v1/repositories/acme/shop/pulls").json(t, &list)
		return len(list.Items) == 1
	})
	if p := list.Items[0]; p.Title != "Round prices" || p.Source != "other" || p.Author != "ada" {
		t.Fatalf("%+v", p)
	}
	// With a pull request open, a push to its branch says nothing about opening one.
	commit("d.txt", "fourth")
	if out := mustGit(t, src, "push", url, "HEAD:refs/heads/other"); strings.Contains(out, "Open a pull request") || strings.Contains(out, "Opening a pull request") {
		t.Fatalf("a push to a branch with a pull request:\n%s", out)
	}
}
