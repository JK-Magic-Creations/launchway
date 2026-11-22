package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HostedLocal lets a project whose repository is hosted by this same
// installation be read from its storage: no network, no token, no egress rule.
// The server implements it (internal/api/hosted_source.go).
type HostedLocal interface {
	// Resolve says whether an address is one of this installation's own
	// repositories, and which.
	Resolve(ctx context.Context, addr string) (id, path string, ok bool)
	// WithCache runs fn with a current local copy of the repository.
	WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
}

type hostedHolder struct{ h HostedLocal }

// SetHostedLocal turns on reading this installation's own repositories from
// its storage.
func (m *Manager) SetHostedLocal(h HostedLocal) { m.hostedPtr.Store(&hostedHolder{h}) }

func (m *Manager) hostedLocal() HostedLocal {
	if h := m.hostedPtr.Load(); h != nil {
		return h.h
	}
	return nil
}

// fetchHosted brings the mirror up to date from the installation's own
// storage when the project's repository is hosted here; done says it did (or
// failed to), so that the caller does not fetch over the network.
func (p *project) fetchHosted(ctx context.Context) (done bool, err error) {
	if p.hosted == nil {
		return false, nil
	}
	h := p.hosted()
	if h == nil {
		return false, nil
	}
	id, path, ok := h.Resolve(ctx, p.URL)
	if !ok {
		return false, nil
	}
	err = h.WithCache(ctx, id, func(gitDir string) error {
		if _, statErr := os.Stat(filepath.Join(p.dir, "HEAD")); statErr != nil {
			_ = os.RemoveAll(p.dir)
			if err := os.MkdirAll(filepath.Dir(p.dir), 0o750); err != nil {
				return err
			}
			if _, err := gitRun(ctx, "", nil, "", "-c", "protocol.ext.allow=always", "clone", "--mirror", "--quiet", localRemote(gitDir), p.dir); err != nil {
				_ = os.RemoveAll(p.dir)
				return err
			}
			return nil
		}
		_, err := gitRun(ctx, p.dir, nil, "", "-c", "protocol.ext.allow=always", "fetch", "--quiet", "--prune", localRemote(gitDir), "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		return err
	})
	if err != nil {
		err = fmt.Errorf("reading %s from this server's own storage: %w", path, err)
	}
	return true, err
}

// localRemote names a repository directory on this machine as a Git remote that
// needs no shell. Given a plain path, git starts "git-upload-pack '<path>'"
// through /bin/sh, and the server's image has no shell: every read of a hosted
// repository failed there with "cannot exec ... No such file or directory".
// The ext transport runs the command itself, split on spaces, with "% " for a
// space and "%%" for a percent sign in an argument.
func localRemote(gitDir string) string {
	esc := strings.NewReplacer("%", "%%", " ", "% ").Replace(gitDir)
	return "ext::git-upload-pack " + esc
}

// SelfCheckHostedRead proves, inside whatever environment it runs in, that a repository kept in this server's own
// storage can be read the way a hosted Flight or library is: it makes a bare repository (in a directory whose name
// has a space and a percent sign), pushes a commit to it and brings the commit into a mirror through the shell-free
// remote that fetchHosted uses. The shipped image has no shell, and this once failed there ("cannot exec
// git-upload-pack") although every test on a developer machine passed; `launchway self-check hosted-read` runs it in
// the image, so a need for a shell or a missing program fails the image check. dir must be writable.
func SelfCheckHostedRead(ctx context.Context, dir string) error {
	root, err := os.MkdirTemp(dir, "selfcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	src := filepath.Join(root, "a b", "100%.git")
	if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
		return err
	}
	work, mirror := filepath.Join(root, "work"), filepath.Join(root, "mirror.git")
	steps := [][]string{
		{"init", "--quiet", "--bare", src},
		{"init", "--quiet", work},
		{"-C", work, "-c", "user.name=self-check", "-c", "user.email=self-check@example.com", "commit", "--quiet", "--allow-empty", "-m", "one"},
		{"-C", work, "-c", "protocol.ext.allow=always", "push", "--quiet", strings.Replace(localRemote(src), "git-upload-pack", "git-receive-pack", 1), "HEAD:refs/heads/main"}, // a plain path would start receive-pack through a shell too
		{"-c", "protocol.ext.allow=always", "clone", "--mirror", "--quiet", localRemote(src), mirror},
		{"-C", mirror, "-c", "protocol.ext.allow=always", "fetch", "--quiet", "--prune", localRemote(src), "+refs/heads/*:refs/heads/*"},
	}
	for _, c := range steps {
		if _, err := gitRun(ctx, "", nil, "", c...); err != nil {
			return fmt.Errorf("git %s: %w", c[0], err)
		}
	}
	out, err := gitRun(ctx, mirror, nil, "", "for-each-ref", "--format=%(refname)")
	if err != nil || !strings.Contains(out, "refs/heads/main") {
		return fmt.Errorf("the commit did not arrive in the mirror: %q %v", out, err)
	}
	return nil
}
