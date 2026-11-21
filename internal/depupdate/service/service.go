// Package service is what the server runs for dependency updates: it reads a
// hosted repository's default branch from storage, asks the version sources
// and the vulnerability data (configured by the installation), and returns the
// updater's report. It changes nothing: opening pull requests is done by a
// pipeline step on a runner (the command line's "updates run"), never by the
// server.
package service

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate/gitfs"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/preview"
	"github.com/jk-magic-creations/launchway/internal/depupdate/propose"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Git gives a repository's bare Git directory (made current) to a function.
type Git interface {
	WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
}

// Config is how the installation reaches registries and vulnerability data.
// Every address is optional: an empty one means the public default, and in
// Offline mode a missing address means "not available" instead.
type Config struct {
	GoProxy, NPMRegistry, PyPI, HostAPI string
	// OCIMirrors maps a registry host to the address to ask instead.
	OCIMirrors map[string]string
	// OSVURL is an OSV API; OSVDir a directory holding an OSV database mirror
	// (one JSON record per file). OSVDir wins when both are set.
	OSVURL, OSVDir string
	// Offline: only the addresses above are used, never a public default.
	Offline bool
}

// FromEnv reads the configuration from environment variables
// (LAUNCHWAY_UPDATES_*), which is how the installation names its mirrors.
func FromEnv(getenv func(string) string) Config {
	c := Config{GoProxy: getenv("LAUNCHWAY_UPDATES_GOPROXY"), NPMRegistry: getenv("LAUNCHWAY_UPDATES_NPM_REGISTRY"),
		PyPI: getenv("LAUNCHWAY_UPDATES_PYPI"), HostAPI: getenv("LAUNCHWAY_UPDATES_HOST_API"),
		OSVURL: getenv("LAUNCHWAY_UPDATES_OSV_URL"), OSVDir: getenv("LAUNCHWAY_UPDATES_OSV_DIR"),
		Offline: getenv("LAUNCHWAY_UPDATES_OFFLINE") == "1" || getenv("LAUNCHWAY_UPDATES_OFFLINE") == "true"}
	for _, kv := range strings.Split(getenv("LAUNCHWAY_UPDATES_OCI_MIRRORS"), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" && v != "" {
			if c.OCIMirrors == nil {
				c.OCIMirrors = map[string]string{}
			}
			c.OCIMirrors[k] = v
		}
	}
	return c
}

// Sources builds the version sources. client is the egress guard's client.
func (c Config) Sources(client *http.Client, cache source.Cache) map[string]source.Source {
	f := &source.Fetcher{Client: client, Cache: cache, Offline: c.Offline}
	return map[string]source.Source{
		"go":        &source.GoProxy{F: f, BaseURL: c.GoProxy},
		"npm":       &source.NPMRegistry{F: f, BaseURL: c.NPMRegistry},
		"python":    &source.PyPI{F: f, BaseURL: c.PyPI},
		"container": &source.OCI{F: f, Mirrors: c.OCIMirrors},
		"workflow":  &source.HostTags{F: f, BaseURL: c.HostAPI},
	}
}

// Vulnerabilities builds the OSV index, or nil when none is configured and
// the installation is offline.
func (c Config) Vulnerabilities(client *http.Client) osv.Index {
	switch {
	case c.OSVDir != "":
		return &osv.DirIndex{FS: os.DirFS(c.OSVDir)}
	case c.OSVURL != "":
		return &osv.API{Client: client, BaseURL: c.OSVURL}
	case c.Offline:
		return nil
	}
	return &osv.API{Client: client, BaseURL: "https://api.osv.dev"}
}

// Service answers for repositories.
type Service struct {
	Git    Git
	Pulls  *pulls.Service
	Config Config
	Client *http.Client
	// Levels returns the settings layers of the levels above the repository
	// (nil: defaults and the repository's file only).
	Levels func(ctx context.Context, repoPath string) ([]plan.Layer, error)
	Now    func() time.Time

	cache source.Cache
	mu    sync.Mutex
	memo  map[string]memo
}

type memo struct {
	rep preview.Report
	at  time.Time
}

// New makes a service with a shared response cache.
func New(g Git, p *pulls.Service, c Config, client *http.Client) *Service {
	return &Service{Git: g, Pulls: p, Config: c, Client: client, cache: &source.MemCache{}, memo: map[string]memo{}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Report is the dependency view and the preview of one repository: what is
// in its default branch, what is newer and vulnerable, and what the updater
// would open under the policy. immediate ignores the schedule, which is what
// "preview what would be opened" asks. Answers are kept for five minutes per
// branch tip and mode.
func (s *Service) Report(ctx context.Context, repoID, repoPath, branch string, immediate bool) (preview.Report, string, error) {
	var tree fs.FS
	var commit string
	err := s.Git.WithCache(ctx, repoID, func(dir string) error {
		var err error
		if commit, err = gitfs.Commit(ctx, dir, branch); err != nil {
			return err
		}
		files, _, err := gitfs.Read(ctx, dir, commit, []string{"*.sh", "scripts/*", "*/scripts/*"}, gitfs.Limits{})
		tree = files
		return err
	})
	if err != nil {
		return preview.Report{}, "", err
	}
	key := fmt.Sprintf("%s|%s|%v", repoID, commit, immediate)
	s.mu.Lock()
	if m, ok := s.memo[key]; ok && s.now().Sub(m.at) < 5*time.Minute {
		s.mu.Unlock()
		return m.rep, commit, nil
	}
	s.mu.Unlock()
	var levels []plan.Layer
	if s.Levels != nil {
		if levels, err = s.Levels(ctx, repoPath); err != nil {
			return preview.Report{}, commit, err
		}
	}
	open, closed := s.pullState(ctx, repoID, levels)
	rep, err := preview.Run(ctx, preview.Input{FS: tree, Levels: levels, Sources: s.Config.Sources(s.Client, s.cache), OSV: s.Config.Vulnerabilities(s.Client),
		Open: open, Closed: closed, Now: s.now(), Immediate: immediate})
	if err != nil {
		return rep, commit, err
	}
	s.mu.Lock()
	s.memo[key] = memo{rep, s.now()}
	s.mu.Unlock()
	return rep, commit, nil
}

// pullState reads the updater's open pull requests and what people closed.
func (s *Service) pullState(ctx context.Context, repoID string, levels []plan.Layer) ([]plan.Open, map[string]bool) {
	if s.Pulls == nil {
		return nil, nil
	}
	pol, _ := plan.Merge(append([]plan.Layer{{Level: "defaults", Partial: plan.Defaults()}}, levels...)...)
	prefix := pol.Commit.BranchPrefix
	if prefix == "" {
		prefix = plan.DefaultBranchPrefix
	}
	var open []propose.PR
	var closed []propose.PR
	items, _, _ := s.Pulls.Store.List(ctx, repoID, pulls.Filter{State: "open", Limit: 200})
	for _, p := range items {
		if strings.HasPrefix(p.SourceBranch, prefix+"/") {
			open = append(open, propose.PR{Number: p.Number, Branch: p.SourceBranch, Body: p.Body})
		}
	}
	items, _, _ = s.Pulls.Store.List(ctx, repoID, pulls.Filter{State: "closed", Limit: 200})
	for _, p := range items {
		if !strings.HasPrefix(p.SourceBranch, prefix+"/") {
			continue
		}
		reason, byUpdater := "", false
		if cs, err := s.Pulls.Store.Comments(ctx, p.ID); err == nil {
			for _, c := range cs {
				if strings.HasPrefix(c.Body, propose.UpdaterClosing) {
					byUpdater = true
				}
			}
			if len(cs) > 0 {
				reason = cs[len(cs)-1].Body
			}
		}
		if byUpdater {
			continue
		}
		closed = append(closed, propose.PR{Number: p.Number, Branch: p.SourceBranch, Body: p.Body, Reason: reason})
	}
	return propose.OpenFrom(open), propose.IgnoreKeys(closed, func(k string) string { return strings.SplitN(k, "-", 2)[0] })
}
