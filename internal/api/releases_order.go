package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/graph"
)

// formationByDependencies puts the releases of a formation in the software graph's dependency order: the release
// of a project whose repository other members' repositories depend on goes before theirs; the members that
// do not depend on each other keep the order they were written in (a dependency through a repository that is not
// a member counts: the order is by the graph's levels). It refuses, saying why, when it cannot
// tell: the graph is off, a project builds no hosted repository, or the members depend on each other in a
// cycle (the order is then the person's to write).
func (s *Server) formationByDependencies(ctx context.Context, id *identity, releaseIDs []string) ([]string, error) {
	if s.Graph == nil || s.Graph.ProjectRepos == nil {
		return nil, fmt.Errorf("%w: ordering by dependencies needs the software graph, which this server does not have; write the order yourself", engine.ErrConflict)
	}
	repos, err := s.Graph.ProjectRepos(ctx)
	if err != nil {
		return nil, err
	}
	type member struct{ id, project, repo string }
	var ms []member
	var unmapped []string
	for _, rid := range releaseIDs {
		r, err := s.Engine.GetRelease(rid)
		if err != nil {
			return nil, err
		}
		repo := repos[r.Project]
		if repo == "" {
			unmapped = append(unmapped, r.Project)
		}
		ms = append(ms, member{rid, r.Project, repo})
	}
	if len(unmapped) > 0 {
		return nil, fmt.Errorf("%w: the graph does not know which repository %s builds; write the order yourself", engine.ErrConflict, strings.Join(unmapped, ", "))
	}
	var names []string
	for _, m := range ms {
		names = append(names, m.repo)
	}
	order, err := s.Graph.Store.Order(ctx, s.projectRepoViewer(id, repos), graph.OrderQuery{Repositories: names, Upstream: true})
	if err != nil {
		return nil, err
	}
	if len(order.Cycles) > 0 {
		return nil, fmt.Errorf("%w: these releases depend on each other in a cycle (%s); write the order yourself", engine.ErrConflict, strings.Join(order.Cycles[0].Path, " -> "))
	}
	level := map[string]int{}
	for i, l := range order.Levels {
		for _, r := range l {
			level[r] = i
		}
	}
	sort.SliceStable(ms, func(i, j int) bool { return level[ms[i].repo] < level[ms[j].repo] })
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.id
	}
	return out, nil
}

// projectRepoViewer reads what the caller may read of the graph, and also the repository a project builds when the
// caller may read the project (a project and its repository are one thing to the person who works on it).
func (s *Server) projectRepoViewer(id *identity, repos map[string]string) graph.Viewer {
	readable := graph.Only{}
	for project, repo := range repos {
		if s.authorize(id, auth.Read, project) {
			readable[graph.RepoSubject(repo)] = true
		}
	}
	return orViewer{graphViewer{s, id}, readable}
}

type orViewer struct{ a, b graph.Viewer }

func (o orViewer) CanRead(subject string) bool { return o.a.CanRead(subject) || o.b.CanRead(subject) }
