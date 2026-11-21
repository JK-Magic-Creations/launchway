package archtest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The originality guard. Launchway is its own product: no other CI/CD, deployment or Git-hosting
// product is named for comparison, inspiration or description. A name may appear only where a
// user has to pick or configure a connection to that product (an integration's own code and
// reference page), where Launchway speaks its protocol, or in test data captured from a real
// tool. This test fails when a name from either list below shows up in a file that is not on the
// matching allow-list. To allow a new place, add the file here in the same pull request and say
// why it is an integration; do not add a comparison. See docs/development/quality-gates.md.

// neverNamed are products with no integration in Launchway: they may not appear anywhere
// except in test data and in this file.
var neverNamed = regexp.MustCompile(`(?i)\b(jenkins(file)?|circle ?ci|travis[- ]?ci|buildkite|teamcity|spinnaker|tekton|octopus deploy|codefresh|gocd|concourse ci|azure devops|azure pipelines|harness\.io|harness (ci|cd|platform)|drone ci|woodpecker ci|gitlab[- ]ci|spacelift|heroku|netlify|vercel|bitbucket pipelines|appveyor|cloudbees|bamboo ci|semaphore ?ci|codebuild|codepipeline|humanitec|backstage)\b`)

// namedForIntegration are Git hosts and CI services Launchway connects to or runs on; they may
// appear only in the files of allowedForIntegration.
var namedForIntegration = regexp.MustCompile(`(?i)\b(gitlab|bitbucket|forgejo|gitea|codeberg|github actions)\b`)

var allowedForIntegration = []string{
	// Webhook and pull-request providers: the code and the pages that document them.
	"internal/api/hooks.go", "internal/api/hooks_e2e_test.go",
	"internal/config/triggers.go", "internal/engine/triggers_test.go", "internal/config/triggers_test.go",
	"internal/source/edit.go", "internal/source/edit_test.go", "internal/source/triggers.go",
	"internal/findings/secrets.go", "internal/findings/secrets_test.go",
	"internal/pipeline/cache_test.go",
	"web/src/configKinds.ts",
	"docs/cspell.json", "docs/faq.md", "docs/guide/editor.md", "docs/guide/triggers.md",
	"docs/reference/http-api.md", "docs/reference/server-config.md",
	"docs/security/trust-model.md", "docs/tutorials/pull-requests.md",
	// Git hosting: the host kinds a connection may be of (internal/forge), the plain provider's
	// advice to connect one, and the repository calls and addresses Launchway answers so that
	// tools written for another host work unchanged (the protocol is theirs; the code names it
	// only where it speaks it).
	"internal/forge/doc.go", "internal/forge/kinds.go", "internal/forge/forge_test.go",
	"internal/forge/providers/plain/plain.go",
	"internal/api/forge_compat.go", "internal/api/forge_git.go", "internal/api/forge_git_e2e_test.go",
	"internal/api/access.go", "internal/api/access_test.go",
	// The Git hosting acceptance suite can be pointed at any compatible host. It names the host
	// kind it was first proven against only in its README and docs, in the flavor option, in the
	// webhook kind and headers that host's protocol uses, and in the access-log census tool for
	// leaving it.
	"test/acceptance/githost/README.md", "test/acceptance/githost/doc.go", "test/acceptance/githost/config.go",
	"test/acceptance/githost/servers.go", "test/acceptance/githost/at_access_test.go", "test/acceptance/githost/census/census.py",
	// Getting repositories in: a connection to another Git server is of a kind the person picks
	// (gitea or forgejo, whose REST API lists repositories), so the import code, its commands, tools,
	// pages and tests name the kind.
	"cmd/launchway/repos_import.go", "cmd/launchway/repos_mirror.go", "cmd/launchway/repos_test.go",
	"internal/api/forge_import_e2e_test.go", "internal/api/mcp_access_test.go", "internal/api/mcp_import.go",
	"internal/forge/mirror/import.go", "internal/forge/mirror/import_test.go", "internal/forge/mirror/source.go",
	"web/src/pages/ImportRepositories.tsx", "web/src/pages/ImportRepositories.test.tsx", "web/src/pages/Repository.tsx",
	"docs/reference/cli.md",
	// The project's own release and docs workflows run on a hosted CI service.
	".github/", "packaging/", "docs/contributing/docs.md",
	// The Argo CD test bed uses a throwaway Git server image.
	"deploy/test/argo/", "docs/tutorials/deploy-with-approval.md",
	// The maintainers' own migration item, kept out of the public roadmap.
	"scripts/docs/build-roadmap.mjs",
}

// exempt paths are never scanned: test data captured from real tools is data, and this file
// holds the lists.
func originalityExempt(path string) bool {
	return path == "internal/archtest/originality_test.go" ||
		strings.Contains(path, "/testdata/") ||
		strings.HasSuffix(path, "package-lock.json") || strings.HasSuffix(path, "go.sum") ||
		strings.HasSuffix(path, ".svg") || strings.HasSuffix(path, ".png") ||
		strings.HasPrefix(path, "docs/public/roadmap.html")
}

func originalityAllowed(path string) bool {
	for _, a := range allowedForIntegration {
		if path == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(path, a)) {
			return true
		}
	}
	return false
}

func TestNoOtherProductsAreNamed(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("not a Git checkout: %v", err)
	}
	for _, path := range strings.Split(string(out), "\x00") {
		if path == "" || originalityExempt(path) {
			continue
		}
		f, err := os.Open(filepath.Join(root, path))
		if err != nil {
			continue // a file deleted in the working tree
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		allowed := originalityAllowed(path)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if m := neverNamed.FindString(line); m != "" {
				t.Errorf("%s:%d names another product (%q); describe it generically", path, n, m)
			} else if m := namedForIntegration.FindString(line); m != "" && !allowed {
				t.Errorf("%s:%d names %q outside the integration allow-list in internal/archtest/originality_test.go", path, n, m)
			}
		}
		f.Close()
	}
}
