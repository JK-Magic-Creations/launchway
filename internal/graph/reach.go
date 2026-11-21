package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
)

// Advisory is a known problem with a package version.
type Advisory struct {
	ID       string `json:"id"`
	Summary  string `json:"summary,omitempty"`
	Severity string `json:"severity,omitempty"`
	FixedIn  string `json:"fixed_in,omitempty"`
}

// Advisories says which advisories affect a package version. The dependency
// updater's vulnerability index (osv) is one implementation.
type Advisories interface {
	For(ctx context.Context, ecosystem, name, version string) ([]Advisory, error)
}

// OSVAdvisories answers from the updater's vulnerability index.
type OSVAdvisories struct{ Index osv.Index }

// For looks the version up. Packages of ecosystems the index has no records
// for (operating-system packages) have no advisories here, which is said in
// the answer's note, not hidden.
func (o OSVAdvisories) For(ctx context.Context, ecosystem, name, version string) ([]Advisory, error) {
	eco := osv.Ecosystem(ecosystem)
	if eco == "" || o.Index == nil {
		return nil, nil
	}
	vs, err := o.Index.Lookup(ctx, osv.Query{Ecosystem: eco, Name: name, Version: version})
	if err != nil {
		return nil, err
	}
	var out []Advisory
	for _, v := range vs {
		if ok, fixed := osv.Affects(v, eco, name, version); ok {
			sev := v.DBSpec.Severity
			out = append(out, Advisory{ID: v.ID, Summary: v.Summary, Severity: strings.ToLower(sev), FixedIn: fixed})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ReachVersion is one version of a package that has advisories, who uses
// it, and where it runs.
type ReachVersion struct {
	Version     string              `json:"version"`
	Advisories  []Advisory          `json:"advisories"`
	UsedBy      []Node              `json:"used_by"`
	Deployments []RunningDeployment `json:"deployments"`
}

// ReachResult answers "which deployments run a version of this package that
// has an advisory".
type ReachResult struct {
	Package  Node           `json:"package"`
	Versions []ReachVersion `json:"versions"`
	Note     string         `json:"note,omitempty"`
	Truncation
}

// Reach finds the versions of a package the graph knows are in use (the
// version a component resolved, the version an image's component list
// holds), asks which have advisories, and follows each to what runs it.
func (s *Store) Reach(ctx context.Context, v Viewer, pkg NodeRef, adv Advisories, lim Limits, when When) (ReachResult, error) {
	p, err := s.find(ctx, v, pkg, when)
	if err != nil {
		return ReachResult{}, err
	}
	out := ReachResult{Package: p}
	if p.Kind != KindPackage {
		return out, fmt.Errorf("%w: a reach is asked of a package", ErrInvalid)
	}
	if adv == nil {
		out.Note = "no vulnerability data is configured on this server"
		return out, nil
	}
	eco, name, _ := strings.Cut(p.Key, ":")
	if e, _ := p.Attrs["ecosystem"].(string); e != "" {
		eco = e
		name = strings.TrimPrefix(p.Key, ecoPrefix(e))
	}
	if osv.Ecosystem(eco) == "" {
		out.Note = "advisories are known for Go, npm and Python packages; this is a " + eco + " package"
	}
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return out, err
	}
	ev, evArgs := vis.clause("e.subject")
	ew, ewArgs := when.edge("e")
	a := append([]any{s.Org, p.ID}, ewArgs...)
	a = append(a, evArgs...)
	rows, err := s.DB.Query(ctx, `SELECT e.dst, e.resolved FROM graph_edges e WHERE e.org = ? AND e.src = ? AND `+ew+` AND e.kind IN ('depends-on','includes') AND e.resolved <> '' AND `+ev+` ORDER BY e.resolved, e.dst`, a...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	byVersion := map[string][]int64{}
	for rows.Next() {
		var dst int64
		var ver string
		if err := rows.Scan(&dst, &ver); err != nil {
			return out, err
		}
		byVersion[ver] = append(byVersion[ver], dst)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	versions := make([]string, 0, len(byVersion))
	for ver := range byVersion {
		versions = append(versions, ver)
	}
	sort.Strings(versions)
	if len(versions) > 200 {
		versions, out.Truncated, out.Reason = versions[:200], true, "nodes"
	}
	for _, ver := range versions {
		advs, err := adv.For(ctx, eco, name, ver)
		if err != nil {
			return out, fmt.Errorf("looking up %s %s: %w", p.Key, ver, err)
		}
		if len(advs) == 0 {
			continue
		}
		rv := ReachVersion{Version: ver, Advisories: advs}
		nodes, err := s.load(ctx, byVersion[ver])
		if err != nil {
			return out, err
		}
		seen := map[string]bool{}
		for _, id := range byVersion[ver] {
			n := nodes[id]
			rv.UsedBy = append(rv.UsedBy, n)
			w, err := s.walk(ctx, v, []int64{id}, Downstream, nil, lim, when)
			if err != nil {
				return out, err
			}
			hits, _, err := s.hits(ctx, w, nil)
			if err != nil {
				return out, err
			}
			for _, h := range hits {
				if h.Node.Kind == KindDeployment && !seen[h.Node.Key] {
					seen[h.Node.Key] = true
					d := runningFrom(h, NodeRef{n.Kind, n.Key})
					d.Exact = n.Kind == KindBuilt
					rv.Deployments = append(rv.Deployments, d)
				}
			}
			if w.trunc.Truncated {
				out.Truncation = w.trunc
			}
		}
		sort.Slice(rv.UsedBy, func(i, j int) bool { return rv.UsedBy[i].Key < rv.UsedBy[j].Key })
		sort.Slice(rv.Deployments, func(i, j int) bool { return rv.Deployments[i].Deployment.Key < rv.Deployments[j].Deployment.Key })
		out.Versions = append(out.Versions, rv)
	}
	return out, nil
}

func ecoPrefix(e string) string {
	if e == depupdate.Python {
		return depupdate.Python + ":"
	}
	return e + ":"
}

// Summary says what the graph holds, for a viewer: nodes by kind, edges, and
// when each source last read.
type Summary struct {
	Nodes   map[string]int `json:"nodes"`
	Edges   int            `json:"edges"`
	Sources []SourceRun    `json:"sources"`
}

// SourceRun is the last time a source read.
type SourceRun struct {
	Source string `json:"source"`
	At     int64  `json:"at"`
	Scopes int    `json:"scopes"`
}

// Summary counts only what the viewer may read.
func (s *Store) Summary(ctx context.Context, v Viewer) (Summary, error) {
	out := Summary{Nodes: map[string]int{}}
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return out, err
	}
	nv, nvArgs := vis.clause("subject")
	rows, err := s.DB.Query(ctx, `SELECT kind, count(*) FROM graph_nodes WHERE org = ? AND removed_at = 0 AND `+nv+` GROUP BY kind`, append([]any{s.Org}, nvArgs...)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return out, err
		}
		out.Nodes[k] = n
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	ev, evArgs := vis.clause("e.subject")
	av, avArgs := vis.clause("a.subject")
	bv, bvArgs := vis.clause("b.subject")
	args := append([]any{s.Org}, evArgs...)
	args = append(args, avArgs...)
	args = append(args, bvArgs...)
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM graph_edges e JOIN graph_nodes a ON a.nid = e.src JOIN graph_nodes b ON b.nid = e.dst
		WHERE e.org = ? AND e.valid_to = 0 AND a.removed_at = 0 AND b.removed_at = 0 AND `+ev+` AND `+av+` AND `+bv, args...).Scan(&out.Edges); err != nil {
		return out, err
	}
	rows, err = s.DB.Query(ctx, `SELECT origin, max(at), count(DISTINCT scope) FROM graph_runs WHERE org = ? GROUP BY origin ORDER BY origin`, s.Org)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	defer rows.Close()
	for rows.Next() {
		var r SourceRun
		if err := rows.Scan(&r.Source, &r.At, &r.Scopes); err != nil {
			return out, err
		}
		out.Sources = append(out.Sources, r)
	}
	return out, rows.Err()
}
