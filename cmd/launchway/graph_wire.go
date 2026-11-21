package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing/fstest"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/depupdate/gitfs"
	depservice "github.com/jk-magic-creations/launchway/internal/depupdate/service"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/graph"
)

// hostedRepos is Git hosting as the graph's sources read it: the repositories
// that are hosted here, and the files of their default branches, read from
// the server's own copy without a working tree and without running anything
// from the repository.
type hostedRepos struct {
	prov api.HostedProvider
	git  interface {
		WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
	}
}

func (h hostedRepos) List(ctx context.Context) ([]graph.Repo, error) {
	var out []graph.Repo
	cursor := ""
	for {
		pg, err := h.prov.Repositories().List(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, r := range pg.Items {
			out = append(out, graph.Repo{ID: r.ID, Path: r.Path, DefaultBranch: r.DefaultBranch, Archived: r.Archived})
		}
		if pg.Next == "" || len(out) > 100000 {
			return out, nil
		}
		cursor = pg.Next
	}
}

func (h hostedRepos) Files(ctx context.Context, r graph.Repo, extra []string) (fs.FS, string, error) {
	if r.DefaultBranch == "" {
		return fstest.MapFS{}, "", nil
	}
	var tree fs.FS = fstest.MapFS{}
	var commit string
	err := h.git.WithCache(ctx, r.ID, func(dir string) error {
		c, err := gitfs.Commit(ctx, dir, r.DefaultBranch)
		if err != nil {
			return nil // no such branch yet: an empty repository
		}
		commit = c
		files, _, err := gitfs.Read(ctx, dir, c, append(append([]string{}, extra...), graph.RequirementsPath), gitfs.Limits{})
		tree = files
		return err
	})
	return tree, commit, err
}

// startGraph starts the software graph: the store (in the engine's database),
// the sources that exist on this server, and the watcher that keeps it
// current. It needs nothing but the database and, for repositories, Git
// hosting.
func startGraph(ctx context.Context, cfg *config.Config, eng *engine.Engine, log *slog.Logger, srv *api.Server) {
	db := eng.Database()
	store := graph.NewStore(db)
	gcfg := graph.Config{RegistryHosts: map[string]string{}}
	for _, kv := range strings.Split(os.Getenv("LAUNCHWAY_GRAPH_REGISTRIES"), ",") {
		if n, h, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && n != "" && h != "" {
			gcfg.RegistryHosts[n] = h
		}
	}
	var repos graph.Repos
	if srv.Forge != nil && srv.Forge.Pulls != nil {
		repos = hostedRepos{prov: srv.Forge.Provider, git: srv.Forge.Pulls.Git}
	}
	projectRepos := func(ctx context.Context) (map[string]string, error) {
		out := map[string]string{}
		if repos == nil {
			return out, nil
		}
		all, err := repos.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range cfg.Projects {
			for _, r := range all {
				u := strings.TrimSuffix(strings.TrimSuffix(p.Repo, "/"), ".git")
				if u == r.Path || strings.HasSuffix(u, "/"+r.Path) {
					out[p.Name] = r.Path
				}
			}
		}
		return out, nil
	}
	gcfg.ProjectRepos = projectRepos
	var sources []graph.Source
	if repos != nil {
		sources = append(sources, graph.ManifestSource{Repos: repos}, graph.ContainerSource{Repos: repos, Cfg: gcfg}, graph.RequirementsSource{Repos: repos, Cfg: gcfg})
	}
	// Machines, registry contents, infrastructure state and cluster inventory
	// plug in here when the work that owns them is on this server: each is a
	// graph.Source over a small interface (internal/graph/stubs.go).
	sources = append(sources, graph.SortieSource{DB: db, Cfg: gcfg}, graph.DeploymentSource{DB: db, Cfg: gcfg}, graph.PlacementSource{Store: store})
	runner := &graph.Runner{Store: store, Repos: repos, Sources: sources, Projects: func(ctx context.Context) ([]string, error) {
		var out []string
		for _, p := range cfg.Projects {
			out = append(out, p.Name)
		}
		return out, nil
	}}
	watch := &graph.Watcher{Runner: runner, DB: db, Log: log}
	svc := &api.GraphService{Store: store, Runner: runner, Watch: watch}
	if idx := depservice.FromEnv(os.Getenv).Vulnerabilities(egress.Default.HTTPClient(30 * time.Second)); idx != nil {
		svc.Advisories = graph.OSVAdvisories{Index: idx}
	}
	srv.Graph = svc
	go watch.Run(ctx)
}
