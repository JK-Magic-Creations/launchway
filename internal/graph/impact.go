package graph

import (
	"context"
	"sort"
	"strings"
)

// nodesOfKinds reads every current node of the kinds, unfiltered: it is for
// derivations the server makes for itself (which hosts satisfy which needs).
func (s *Store) nodesOfKinds(ctx context.Context, kinds ...NodeKind) ([]Node, error) {
	a := []any{s.Org}
	for _, k := range kinds {
		a = append(a, string(k))
	}
	rows, err := s.DB.Query(ctx, `SELECT nid FROM graph_nodes WHERE org = ? AND removed_at = 0 AND kind IN (`+marks(len(kinds))+`) ORDER BY kind, key`, a...)
	if err != nil {
		return nil, err
	}
	return s.collect(ctx, rows)
}

func (s *Store) requirementNodes(ctx context.Context) ([]Node, error) {
	return s.nodesOfKinds(ctx, KindRequirement)
}

func (s *Store) collect(ctx context.Context, rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}) ([]Node, error) {
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	m, err := s.load(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out, nil
}

// visibleNodesOfKinds is nodesOfKinds for a viewer.
func (s *Store) visibleNodesOfKinds(ctx context.Context, v Viewer, when When, kinds ...NodeKind) ([]Node, error) {
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return nil, err
	}
	nv, nvArgs := vis.clause("subject")
	nw, nwArgs := when.node("graph_nodes")
	a := append([]any{s.Org}, nvArgs...)
	a = append(a, nwArgs...)
	for _, k := range kinds {
		a = append(a, string(k))
	}
	rows, err := s.DB.Query(ctx, `SELECT nid FROM graph_nodes WHERE org = ? AND `+nv+` AND `+nw+` AND kind IN (`+marks(len(kinds))+`) ORDER BY kind, key`, a...)
	if err != nil {
		return nil, err
	}
	return s.collect(ctx, rows)
}

// Affected is a node a change reaches.
type Affected struct {
	Hit
	Owner string `json:"owner"`
}

// OwnerGroup is what a change reaches, per owner.
type OwnerGroup struct {
	Owner string     `json:"owner"`
	Count int        `json:"count"`
	Items []Affected `json:"items"`
}

// RunningDeployment is a deployment that runs an affected (or searched-for)
// digest, and why.
type RunningDeployment struct {
	Deployment Node   `json:"deployment"`
	Airfield   string `json:"airfield,omitempty"`
	Digest     string `json:"digest,omitempty"`
	Version    string `json:"version,omitempty"`
	Since      int64  `json:"since,omitempty"`
	Exact      bool   `json:"exact"` // the thing is in the digest that runs (a package or commit it was built from), not only upstream of its build
	Why        []Step `json:"why"`
}

// ImpactResult answers "what is affected if this changes".
type ImpactResult struct {
	Start        Node                `json:"start"`
	Total        int                 `json:"total"`
	ByKind       map[string]int      `json:"by_kind"`
	ByOwner      []OwnerGroup        `json:"by_owner"`
	Repositories []string            `json:"repositories"` // the repositories the affected things live in
	Deployments  []RunningDeployment `json:"deployments"`
	Truncation
}

// Impact is the downstream closure of a node: everything a change to it
// reaches, with the way it gets there, grouped by owner, with the
// deployments that run affected digests. Only what the viewer may read is
// followed, listed or counted.
func (s *Store) Impact(ctx context.Context, v Viewer, ref NodeRef, lim Limits, when When) (ImpactResult, error) {
	r, err := s.Neighbours(ctx, v, ref, Downstream, lim, when)
	if err != nil {
		return ImpactResult{}, err
	}
	out := ImpactResult{Start: r.Start, Total: len(r.Hits), ByKind: map[string]int{}, Truncation: r.Truncation}
	owners, err := s.repoOwners(ctx, v)
	if err != nil {
		return out, err
	}
	groups := map[string]*OwnerGroup{}
	repoSet := map[string]bool{}
	for _, h := range r.Hits {
		out.ByKind[string(h.Node.Kind)]++
		o := ownerOf(h.Node, owners)
		g := groups[o]
		if g == nil {
			g = &OwnerGroup{Owner: o}
			groups[o] = g
		}
		g.Count++
		if p, ok := strings.CutPrefix(h.Node.Subject, subRepo); ok && p != ref.Key {
			repoSet[p] = true
		}
		g.Items = append(g.Items, Affected{Hit: h, Owner: o})
		if h.Node.Kind == KindDeployment {
			out.Deployments = append(out.Deployments, runningFrom(h, ref))
		}
	}
	for _, g := range groups {
		out.ByOwner = append(out.ByOwner, *g)
	}
	sort.Slice(out.ByOwner, func(i, j int) bool {
		if (out.ByOwner[i].Owner == noOwner) != (out.ByOwner[j].Owner == noOwner) {
			return out.ByOwner[j].Owner == noOwner
		}
		return out.ByOwner[i].Owner < out.ByOwner[j].Owner
	})
	out.Repositories = sortedKeys(repoSet)
	sort.SliceStable(out.Deployments, func(i, j int) bool { return out.Deployments[i].Deployment.Key < out.Deployments[j].Deployment.Key })
	return out, nil
}

const noOwner = "(no owner)"

// repoOwners maps a repository path to its owners, for what the viewer may see.
func (s *Store) repoOwners(ctx context.Context, v Viewer) (map[string]string, error) {
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return nil, err
	}
	rv, rvArgs := vis.clause("r.subject")
	a := append([]any{s.Org}, rvArgs...)
	rows, err := s.DB.Query(ctx, `SELECT r.key, o.key FROM graph_edges e JOIN graph_nodes r ON r.nid = e.src JOIN graph_nodes o ON o.nid = e.dst
		WHERE e.org = ? AND e.kind = 'owned-by' AND e.valid_to = 0 AND r.kind = 'repository' AND `+rv+` ORDER BY r.key, o.key`, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var repo, owner string
		if err := rows.Scan(&repo, &owner); err != nil {
			return nil, err
		}
		if out[repo] == "" {
			out[repo] = owner
		} else {
			out[repo] += ", " + owner
		}
	}
	return out, rows.Err()
}

func ownerOf(n Node, repoOwners map[string]string) string {
	if n.Owner != "" {
		return n.Owner
	}
	if n.Kind == KindRepository {
		if o := repoOwners[n.Key]; o != "" {
			return o
		}
	}
	if p, ok := strings.CutPrefix(n.Subject, subRepo); ok {
		if o := repoOwners[p]; o != "" {
			return o
		}
	}
	if p, ok := strings.CutPrefix(n.Subject, subProject); ok {
		return "project " + p
	}
	return noOwner
}

func runningFrom(h Hit, start NodeRef) RunningDeployment {
	d := RunningDeployment{Deployment: h.Node, Why: h.Path}
	d.Airfield = strOf(h.Node.Attrs["airfield"])
	d.Version = strOf(h.Node.Attrs["version"])
	if n, ok := h.Node.Attrs["since"].(float64); ok {
		d.Since = int64(n)
	}
	for _, st := range h.Path {
		if st.Edge.Kind == EdgeDeliveredTo {
			d.Digest = strOf(st.Edge.Attrs["digest"])
		}
		switch st.Edge.Kind {
		case EdgeContainsPkg, EdgeBuiltFrom:
			if st.Edge.To.Kind == KindCommit || st.Edge.Kind == EdgeContainsPkg {
				d.Exact = true
			}
		}
	}
	if start.Kind == KindBuilt || start.Kind == KindCommit {
		d.Exact = true
	}
	return d
}

// Where answers "where is this running": the deployments that run a
// repository's, a package's, a commit's or an image's output, by Airfield,
// with the digest each runs.
type WhereResult struct {
	Start       Node                `json:"start"`
	Deployments []RunningDeployment `json:"deployments"`
	Airfields   []string            `json:"airfields"`
	Truncation
}

// Where lists the deployments a thing reaches.
func (s *Store) Where(ctx context.Context, v Viewer, ref NodeRef, lim Limits, when When) (WhereResult, error) {
	im, err := s.Impact(ctx, v, ref, lim, when)
	if err != nil {
		return WhereResult{}, err
	}
	out := WhereResult{Start: im.Start, Deployments: im.Deployments, Truncation: im.Truncation}
	seen := map[string]bool{}
	for _, d := range im.Deployments {
		if d.Airfield != "" && !seen[d.Airfield] {
			seen[d.Airfield] = true
			out.Airfields = append(out.Airfields, d.Airfield)
		}
	}
	sort.Strings(out.Airfields)
	return out, nil
}

// Contents answers "what is in this": for a deployment, an image or a
// component, what feeds it: the images, base images, packages with the
// versions resolved, commits and repositories.
type ContentsResult struct {
	Start        Node  `json:"start"`
	Images       []Hit `json:"images"`
	BaseImages   []Hit `json:"base_images"`
	Packages     []Hit `json:"packages"`
	Commits      []Hit `json:"commits"`
	Repositories []Hit `json:"repositories"`
	Other        []Hit `json:"other,omitempty"`
	Truncation
}

// Contents walks upstream.
func (s *Store) Contents(ctx context.Context, v Viewer, ref NodeRef, lim Limits, when When) (ContentsResult, error) {
	r, err := s.Neighbours(ctx, v, ref, Upstream, lim, when)
	if err != nil {
		return ContentsResult{}, err
	}
	out := ContentsResult{Start: r.Start, Truncation: r.Truncation}
	for _, h := range r.Hits {
		switch h.Node.Kind {
		case KindBuilt:
			out.Images = append(out.Images, h)
		case KindImage:
			if strOf(h.Node.Attrs["role"]) == "base" {
				out.BaseImages = append(out.BaseImages, h)
			} else {
				out.Images = append(out.Images, h)
			}
		case KindPackage:
			out.Packages = append(out.Packages, h)
		case KindCommit:
			out.Commits = append(out.Commits, h)
		case KindRepository:
			out.Repositories = append(out.Repositories, h)
		default:
			out.Other = append(out.Other, h)
		}
	}
	return out, nil
}
