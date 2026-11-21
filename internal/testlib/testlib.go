// Package testlib makes Git repositories for tests, such as a shared
// library of templates and an application that uses it. Each Repo is a
// working copy and a bare repository it pushes to, as a Git host would hold
// it; tests point Launchway at the bare one.
package testlib

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitEnv is the environment for any test that starts a git client: built
// explicitly, never inherited, so that no desktop session's askpass helper, agent
// or configuration can make git open a credential dialog or read a
// user's settings. An unauthenticated request fails at once instead.
func GitEnv(extra ...string) []string {
	home, _ := os.MkdirTemp("", "lw-githome-")
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home, "LC_ALL=C",
		"GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "SSH_ASKPASS_REQUIRE=never", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=core.askPass", "GIT_CONFIG_VALUE_0=/bin/false", "GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1="}
	return append(env, extra...)
}

// Git runs git in dir as a fixed author and returns its output.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = GitEnv("GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repo is a working copy and the bare repository it pushes to.
type Repo struct {
	Work string
	Bare string // what Launchway fetches
	t    testing.TB
}

// New makes a repository in dir with the files (path → content) committed
// on main. Files whose path ends in .sh are executable.
func New(t testing.TB, dir string, files map[string]string) *Repo {
	t.Helper()
	r := &Repo{Work: filepath.Join(dir, "work"), Bare: filepath.Join(dir, "origin.git"), t: t}
	if err := os.MkdirAll(r.Work, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, r.Work, "init", "-q", "-b", "main")
	Git(t, dir, "init", "-q", "--bare", "-b", "main", r.Bare)
	Git(t, r.Work, "remote", "add", "origin", r.Bare)
	r.Commit(files, "first")
	return r
}

// Commit writes files (an empty content removes the file), commits them on
// the current branch, pushes it, and returns the commit.
func (r *Repo) Commit(files map[string]string, msg string) string {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.Work, filepath.FromSlash(name))
		if content == "" {
			_ = os.Remove(path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			r.t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			r.t.Fatal(err)
		}
	}
	Git(r.t, r.Work, "add", "-A")
	Git(r.t, r.Work, "commit", "-q", "--allow-empty", "-m", msg)
	branch := Git(r.t, r.Work, "rev-parse", "--abbrev-ref", "HEAD")
	Git(r.t, r.Work, "push", "-q", "--force", "origin", branch)
	return r.Head()
}

// Head is the current commit of the working copy.
func (r *Repo) Head() string {
	r.t.Helper()
	return Git(r.t, r.Work, "rev-parse", "HEAD")
}

// Tag points a tag at a commit (moving it if it exists) and pushes it.
func (r *Repo) Tag(name, commit string) {
	r.t.Helper()
	Git(r.t, r.Work, "tag", "-f", name, commit)
	Git(r.t, r.Work, "push", "-q", "--force", "origin", "refs/tags/"+name)
}

// Branch points a branch at a commit (moving it if it exists) and pushes it.
func (r *Repo) Branch(name, commit string) {
	r.t.Helper()
	Git(r.t, r.Work, "branch", "-f", name, commit)
	Git(r.t, r.Work, "push", "-q", "--force", "origin", "refs/heads/"+name)
}

// CheckTemplate is a template whose step runs a script from its library
// and reports, in its output and outputs, which version of the script ran
// and whether the repository looked unchanged to Git.
const CheckTemplate = `template: check
description: Run the library's check script
params:
  greeting: { type: string, default: hello }
  image:    { type: string, default: docker.io/library/alpine:3.22 }
steps:
  run:
    image: { param: image }
    run: |
      "$LAUNCHWAY_LIB/scripts/check.sh"
      echo "lib=$LAUNCHWAY_LIB" >> "$LAUNCHWAY_OUTPUTS"
outputs:
  version: { from: run.version }
`

// CheckScript is the script CheckTemplate runs, in a given version.
func CheckScript(version string) string {
	return `#!/bin/sh
set -e
echo "check.sh version ` + version + ` says $PARAM_GREETING"
echo "version=` + version + `" >> "$LAUNCHWAY_OUTPUTS"
if command -v git >/dev/null 2>&1; then
  git -c safe.directory='*' status --porcelain
  echo "clean=$(git -c safe.directory='*' status --porcelain | wc -l | tr -d ' ')" >> "$LAUNCHWAY_OUTPUTS"
else
  echo "clean=nogit" >> "$LAUNCHWAY_OUTPUTS"
fi
`
}

// NewLibrary makes a library with CheckTemplate and CheckScript("1"),
// tagged v1.
func NewLibrary(t testing.TB, dir string) *Repo {
	t.Helper()
	r := New(t, dir, map[string]string{"templates/check.yaml": CheckTemplate, "scripts/check.sh": CheckScript("1")})
	r.Tag("v1", r.Head())
	return r
}

// AppPipeline is an application pipeline that uses the check template of
// the library called lib, at version.
func AppPipeline(lib, version string) string {
	return `pipeline: app
on:
  push: { branches: [main] }
libraries:
  platform: { repo: ` + lib + `, version: ` + version + ` }
steps:
  ci:
    uses: platform/check
    with: { greeting: hi }
  after:
    image: docker.io/library/alpine:3.22
    needs: ci
    run: echo "the check was version $LAUNCHWAY_NEEDS_CI_VERSION"
`
}
