package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

// The description a pull request starts with comes from the target branch of the repository: the file named for
// Launchway first, a conventional top-level name second, nothing (an empty template, not an error) when there is
// none; anyone who may read the repository gets it.
func TestPullTemplateComesFromTheTargetBranch(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{}, githttp.Options{})
	hostedRepo(t, e, "acme/shop")
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	push := withToken(e.srv.URL, "/git/acme/shop.git", e.tokenFor(t, "ada", "git"))
	src := workingCopy(t)
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	api := e.srv.URL + "/api/v1/repositories/acme/shop/pull-template"
	type tmpl struct{ Path, Template string }
	var got tmpl
	vic.get(api).json(t, &got)
	if got.Path != "" || got.Template != "" {
		t.Fatalf("a repository with none: %+v", got)
	}
	mustGit(t, src, "checkout", "-q", "-b", "release")
	commit(t, src, "PULL_REQUEST_TEMPLATE.md", "## Release notes\n", "a conventional top-level template")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/release")
	mustGit(t, src, "checkout", "-q", "main")
	commit(t, src, ".launchway/pull_request_template.md", "## Why\n\n## How it was tested\n", "our template")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	vic.get(api).json(t, &got) // the default branch
	if got.Path != ".launchway/pull_request_template.md" || got.Template != "## Why\n\n## How it was tested\n" {
		t.Fatalf("main: %+v", got)
	}
	vic.get(api+"?ref=release").json(t, &got) // another target: its own file
	if got.Path != "PULL_REQUEST_TEMPLATE.md" || got.Template != "## Release notes\n" {
		t.Fatalf("release: %+v", got)
	}
	if r := newBrowser(t).get(api); r.status != 401 {
		t.Fatalf("unsigned: %d", r.status)
	}
	if r := ada.get(e.srv.URL + "/api/v1/repositories/acme/none/pull-template"); r.status != 404 || !strings.Contains(string(r.body), "no such repository") {
		t.Fatalf("a repository that is not there: %d %s", r.status, r.body)
	}
}
