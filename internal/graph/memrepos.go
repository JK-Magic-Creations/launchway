package graph

import (
	"context"
	"io/fs"
	"sort"
	"sync"
	"testing/fstest"
)

// MemRepos is an in-memory set of repositories: the fixtures of tests, and a
// way to try a source without a Git server.
type MemRepos struct {
	mu    sync.Mutex
	repos map[string]*memRepo
}

type memRepo struct {
	files  fstest.MapFS
	commit string
}

// NewMemRepos makes an empty set.
func NewMemRepos() *MemRepos { return &MemRepos{repos: map[string]*memRepo{}} }

// Push replaces a repository's files and commit, as a push would.
func (m *MemRepos) Push(path, commit string, files map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := fstest.MapFS{}
	for p, c := range files {
		t[p] = &fstest.MapFile{Data: []byte(c)}
	}
	m.repos[path] = &memRepo{t, commit}
}

// Delete removes a repository.
func (m *MemRepos) Delete(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.repos, path)
}

// List returns the repositories.
func (m *MemRepos) List(context.Context) ([]Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Repo
	for p := range m.repos {
		out = append(out, Repo{ID: "rep_" + p, Path: p, DefaultBranch: "main"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Files returns the repository's tree.
func (m *MemRepos) Files(_ context.Context, r Repo, _ []string) (fs.FS, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp := m.repos[r.Path]
	if rp == nil {
		return fstest.MapFS{}, "", nil
	}
	cp := fstest.MapFS{}
	for k, v := range rp.files {
		cp[k] = v
	}
	return cp, rp.commit, nil
}
