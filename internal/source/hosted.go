package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
			if err := os.MkdirAll(filepath.Dir(p.dir), 0o755); err != nil {
				return err
			}
			if _, err := gitRun(ctx, "", nil, "", "clone", "--mirror", "--quiet", gitDir, p.dir); err != nil {
				_ = os.RemoveAll(p.dir)
				return err
			}
			return nil
		}
		_, err := gitRun(ctx, p.dir, nil, "", "fetch", "--quiet", "--prune", gitDir, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
		return err
	})
	if err != nil {
		err = fmt.Errorf("reading %s from this server's own storage: %w", path, err)
	}
	return true, err
}
