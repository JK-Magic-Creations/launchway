package propose

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Auth is the credential for the repository (basic authentication).
type Auth struct{ Username, Password string }

// Author is who the commits are made as.
type Author struct{ Name, Email string }

// Workspace is a shallow clone of a repository's base branch, kept in a
// directory the proposer owns. Git runs without hooks, without credential
// helpers and with the credential given only through the process environment.
type Workspace struct {
	URL    string
	Base   string // the branch to start from (the default branch)
	Dir    string
	Auth   *Auth
	Author Author
}

func (w *Workspace) env() []string {
	env := []string{"GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG_COUNT=0"}
	if w.Auth != nil {
		h := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(w.Auth.Username+":"+w.Auth.Password))
		env = []string{"GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + h}
	}
	return env
}

func (w *Workspace) git(ctx context.Context, stdin string, args ...string) (string, error) {
	cfg := []string{"-c", "user.name=" + w.Author.Name, "-c", "user.email=" + w.Author.Email, "-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "gc.auto=0", "-c", "advice.detachedHead=false"}
	spec := gitexec.Spec{Dir: w.Dir, Args: append(cfg, args...), Env: w.env(), Inherit: gitexec.Remote, Untrusted: true}
	if w.Dir == "" || args[0] == "clone" {
		spec.Dir = filepath.Dir(w.Dir)
	}
	if stdin != "" {
		spec.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, spec)
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", args[0], w.clean(ee.Stderr+ee.Stdout))
		}
		return "", fmt.Errorf("git %s: %s", args[0], w.clean(err.Error()))
	}
	return res.Stdout, nil
}

func (w *Workspace) clean(s string) string {
	s = redact.URLs(strings.TrimSpace(s))
	if w.Auth != nil && w.Auth.Password != "" {
		s = strings.ReplaceAll(s, w.Auth.Password, "***")
	}
	return s
}

// Clone fetches the base branch (history depth 1) and returns its commit.
func (w *Workspace) Clone(ctx context.Context) (string, error) {
	if strings.HasPrefix(w.URL, "-") || w.Base == "" {
		return "", errors.New("a repository address and a base branch are needed")
	}
	if err := os.RemoveAll(w.Dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(w.Dir), 0o750); err != nil {
		return "", err
	}
	if _, err := w.git(ctx, "", "clone", "--quiet", "--depth", "1", "--branch", w.Base, "--no-tags", "--", w.URL, w.Dir); err != nil {
		return "", err
	}
	out, err := w.git(ctx, "", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Reset returns the working tree to the base branch's tip, discarding
// whatever an earlier proposal left.
func (w *Workspace) Reset(ctx context.Context) error {
	if _, err := w.git(ctx, "", "checkout", "--quiet", "--force", "--detach", "origin/"+w.Base); err != nil {
		return err
	}
	_, err := w.git(ctx, "", "clean", "--quiet", "-fdx")
	return err
}

// Commit puts the given files (path to content; nil deletes) on a new local
// branch started from the base and commits them.
func (w *Workspace) Commit(ctx context.Context, branch, message string, files map[string][]byte) (string, error) {
	if _, err := w.git(ctx, "", "checkout", "--quiet", "-B", branch, "origin/"+w.Base); err != nil {
		if _, err2 := w.git(ctx, "", "checkout", "--quiet", "-B", branch); err2 != nil {
			return "", err
		}
	}
	for p, data := range files {
		full, err := w.safe(p)
		if err != nil {
			return "", err
		}
		if data == nil {
			_ = os.Remove(full)
		} else {
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				return "", err
			}
			if err := os.WriteFile(full, data, 0o644); err != nil { //nolint:gosec // repository files keep their ordinary mode
				return "", err
			}
		}
		if _, err := w.git(ctx, "", "add", "-A", "--", p); err != nil {
			return "", err
		}
	}
	if _, err := w.git(ctx, message, "commit", "--quiet", "--no-verify", "--cleanup=verbatim", "-F", "-"); err != nil {
		return "", err
	}
	out, err := w.git(ctx, "", "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Push publishes the local branch, replacing the updater's own branch of the
// same name if it exists (force with a lease on what was seen). It refuses
// to touch any branch outside the updater's prefix.
func (w *Workspace) Push(ctx context.Context, branch, prefix string) error {
	if !strings.HasPrefix(branch, strings.TrimRight(prefix, "/")+"/") {
		return fmt.Errorf("refusing to push %q: only branches under %s/ belong to the updater", branch, strings.TrimRight(prefix, "/"))
	}
	if branch == w.Base {
		return errors.New("refusing to push the base branch")
	}
	_, err := w.git(ctx, "", "push", "--quiet", "--force", "origin", "refs/heads/"+branch+":refs/heads/"+branch)
	return err
}

// safe resolves a repository-relative path inside the workspace.
func (w *Workspace) safe(p string) (string, error) {
	c := filepath.Clean(filepath.FromSlash(p))
	if filepath.IsAbs(c) || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) || c == "." || strings.HasPrefix(c, ".git"+string(filepath.Separator)) || c == ".git" {
		return "", fmt.Errorf("%q is not a path inside the repository", p)
	}
	return filepath.Join(w.Dir, c), nil
}

// Read returns a file of the clone.
func (w *Workspace) Read(p string) (string, bool) {
	full, err := w.safe(p)
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(full)
	return string(b), err == nil
}
