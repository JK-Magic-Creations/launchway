package source

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// forgeLink ties the watcher to the forge's rows: every watched project is a
// repository on the plain connection (provider zero), and what the watcher
// decides about a change is kept as an event of that connection. Nothing
// about how a project is watched depends on it: when the rows cannot be
// written the watcher goes on, and says so once.
type forgeLink struct {
	mu    sync.Mutex
	st    *store.Store
	conn  string
	repos map[string]string // project → repository ID
}

// syncForge records the projects as repositories on the plain connection.
func (m *Manager) syncForge(ctx context.Context) {
	st := store.New(m.eng.Database())
	var ps []store.Project
	for _, name := range m.c().order {
		p := m.c().projects[name]
		ps = append(ps, store.Project{Name: p.Name, URL: p.URL, DefaultBranch: p.DefaultBranch})
	}
	conn, err := st.SyncPlain(ctx, ps)
	if err != nil {
		m.log.Warn("recording the projects as forge repositories failed; watching goes on as before", "err", err)
		return
	}
	repos := map[string]string{}
	for _, name := range m.c().order {
		r, err := st.RepositoryOfProject(ctx, name)
		if err != nil {
			m.log.Warn("finding a project's repository", "project", name, "err", err)
			continue
		}
		repos[name] = r.ID
	}
	m.hosted.mu.Lock()
	m.hosted.st, m.hosted.conn, m.hosted.repos = st, conn.ID, repos
	m.hosted.mu.Unlock()
}

// noteForge appends what was decided about a change to the connection's
// events, so "why did this push not start a run?" has an answer there too.
func (m *Manager) noteForge(ctx context.Context, p *project, c engine.RefChange, outcome, detail string) {
	m.hosted.mu.Lock()
	st, conn, repo := m.hosted.st, m.hosted.conn, m.hosted.repos[p.Name]
	m.hosted.mu.Unlock()
	if st == nil {
		return
	}
	err := st.AppendEvent(ctx, store.Event{Connection: conn, Repository: repo, Kind: forge.EventRefUpdated,
		Ref: c.Ref, Old: c.Old, New: c.New, Outcome: outcome, Detail: redactLine(detail)})
	if err != nil {
		m.log.Warn("recording a forge event", "project", p.Name, "err", err)
	}
}

// ForgeRepository is the repository that stands for a project, and the
// connection it is on.
func (m *Manager) ForgeRepository(ctx context.Context, project string) (forge.Repository, error) {
	m.hosted.mu.Lock()
	st := m.hosted.st
	m.hosted.mu.Unlock()
	if st == nil {
		return forge.Repository{}, fmt.Errorf("%w: the forge repositories are not recorded yet", engine.ErrNotFound)
	}
	return st.RepositoryOfProject(ctx, project)
}

// Mirror returns the directory of a project's mirror, brought up to date
// unless it was fetched a moment ago. The directory is a bare repository the
// caller may read with git and must not change.
func (m *Manager) Mirror(ctx context.Context, name string) (string, error) {
	p := m.c().projects[name]
	if p == nil {
		return "", fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fetch(ctx, 3*time.Second); err != nil {
		return "", err
	}
	return p.dir, nil
}

// Projects lists the names of the watched projects in configuration order.
func (m *Manager) Projects() []string { return append([]string(nil), m.c().order...) }

func redactLine(s string) string { return redact.URLs(s) }
