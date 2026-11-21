package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Dependency order across repositories. Two things make one repository
// depend on another when both are hosted here: a component of the first
// depends on a package a component of the second publishes (module path,
// package name), and a Containerfile of the first starts from an image the
// second's pipeline builds. People correct what the graph finds with
// overrides, which are kept as explicit rows with a reason and shown as
// overrides wherever the order is shown.

// RepoEdge says a repository depends on another, and through what.
type RepoEdge struct {
	Consumer string   `json:"consumer"`
	Provider string   `json:"provider"`
	Via      []string `json:"via"` // packages and images that make it so
	Kinds    []string `json:"kinds"`
	Versions []string `json:"versions,omitempty"` // what the consumer resolved
	// Override says how a person changed this edge: "ignored", "pinned" or "".
	Override string `json:"override,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Override is a person's correction to the order.
type Override struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // before, ignore or pin
	From      string `json:"from"`
	To        string `json:"to"`
	Value     string `json:"value,omitempty"`
	Reason    string `json:"reason"`
	Author    string `json:"author,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// The override kinds.
const (
	OverrideBefore = "before" // from must be built before to
	OverrideIgnore = "ignore" // from (the consumer) does not depend on to (the provider)
	OverridePin    = "pin"    // from (the consumer) is pinned to a version of to: a push to to does not rebuild it
)

// repoGraph reads the repository-level dependencies the viewer may see.
func (s *Store) repoGraph(ctx context.Context, v Viewer, when When) (map[[2]string]*RepoEdge, bool, error) {
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return nil, false, err
	}
	out := map[[2]string]*RepoEdge{}
	truncated := false
	const rowCap = 400000
	queries := []struct {
		kind string
		sql  string
	}{
		{"package", `SELECT cr.key, pr.key, pkg.key, d.constraint_text, d.resolved FROM graph_edges d
  JOIN graph_nodes pkg ON pkg.nid = d.src
  JOIN graph_nodes cc ON cc.nid = d.dst
  JOIN graph_edges cx ON cx.dst = cc.nid AND cx.kind = 'contains' AND cx.org = ? AND %[1]s
  JOIN graph_nodes cr ON cr.nid = cx.src AND cr.kind = 'repository'
  JOIN graph_edges pe ON pe.dst = pkg.nid AND pe.kind = 'contains' AND pe.org = ? AND %[2]s
  JOIN graph_nodes pc ON pc.nid = pe.src AND pc.kind = 'component'
  JOIN graph_edges px ON px.dst = pc.nid AND px.kind = 'contains' AND px.org = ? AND %[3]s
  JOIN graph_nodes pr ON pr.nid = px.src AND pr.kind = 'repository'
  WHERE d.org = ? AND d.kind = 'depends-on' AND %[4]s AND cr.key <> pr.key AND %[5]s AND %[6]s AND %[7]s AND %[8]s LIMIT ?`},
		{"image", `SELECT cr.key, pr.key, base.key, b.constraint_text, b.resolved FROM graph_edges b
  JOIN graph_nodes base ON base.nid = b.src AND base.kind = 'image'
  JOIN graph_nodes cc ON cc.nid = b.dst AND cc.kind = 'component'
  JOIN graph_edges cx ON cx.dst = cc.nid AND cx.kind = 'contains' AND cx.org = ? AND %[1]s
  JOIN graph_nodes cr ON cr.nid = cx.src AND cr.kind = 'repository'
  JOIN graph_edges bl ON bl.dst = base.nid AND bl.kind = 'builds' AND bl.org = ? AND %[2]s
  JOIN graph_edges df ON df.dst = bl.src AND df.kind = 'defined-in' AND df.org = ? AND %[3]s
  JOIN graph_nodes pr ON pr.nid = df.src AND pr.kind = 'repository'
  WHERE b.org = ? AND b.kind = 'built-from' AND %[4]s AND cr.key <> pr.key AND %[5]s AND %[6]s AND %[7]s AND %[8]s LIMIT ?`},
	}
	for _, q := range queries {
		var args []any
		var text string
		switch q.kind {
		case "package":
			text, args = buildRepoQuery(q.sql, s.Org, when, vis, "d", "cx", "pe", "px")
		default:
			text, args = buildRepoQuery(q.sql, s.Org, when, vis, "b", "cx", "bl", "df")
		}
		args = append(args, rowCap+1)
		rows, err := s.DB.Query(ctx, text, args...)
		if err != nil {
			return nil, false, err
		}
		n := 0
		for rows.Next() {
			var consumer, provider, via, con, res string
			if err := rows.Scan(&consumer, &provider, &via, &con, &res); err != nil {
				rows.Close()
				return nil, false, err
			}
			n++
			if n > rowCap {
				truncated = true
				break
			}
			k := [2]string{consumer, provider}
			e := out[k]
			if e == nil {
				e = &RepoEdge{Consumer: consumer, Provider: provider}
				out[k] = e
			}
			e.Via = addUnique(e.Via, via)
			e.Kinds = addUnique(e.Kinds, q.kind)
			if res != "" {
				e.Versions = addUnique(e.Versions, via+"@"+res)
			} else if con != "" {
				e.Versions = addUnique(e.Versions, via+"@"+con)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, false, err
		}
	}
	return out, truncated, nil
}

// buildRepoQuery fills the statement's placeholders for the edge conditions
// and the viewer's condition, and returns its arguments in order.
func buildRepoQuery(sqlText, org string, when When, vis visibility, a, b, c, d string) (string, []any) {
	var args []any
	// The arguments follow the order of the placeholders in the text: the
	// three joined edges (org, when) each, then the WHERE (org, when), then
	// the viewer's conditions.
	w1, a1 := when.edge(b)
	w2, a2 := when.edge(c)
	w3, a3 := when.edge(d)
	w4, a4 := when.edge(a)
	args = append(args, org)
	args = append(args, a1...)
	args = append(args, org)
	args = append(args, a2...)
	args = append(args, org)
	args = append(args, a3...)
	args = append(args, org)
	args = append(args, a4...)
	v5, va5 := vis.clause("cr.subject")
	v6, va6 := vis.clause("pr.subject")
	v7, va7 := vis.clause(a + ".subject")
	v8, va8 := vis.clause(b + ".subject")
	args = append(args, va5...)
	args = append(args, va6...)
	args = append(args, va7...)
	args = append(args, va8...)
	return fmt.Sprintf(sqlText, w1, w2, w3, w4, v5, v6, v7, v8), args
}

func addUnique(l []string, s string) []string {
	for _, v := range l {
		if v == s {
			return l
		}
	}
	return append(l, s)
}

// Overrides lists the corrections the viewer may see (both repositories
// readable).
func (s *Store) Overrides(ctx context.Context, v Viewer) ([]Override, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, kind, from_key, to_key, value, reason, author, created_at, subject FROM graph_overrides WHERE org = ? AND removed_at = 0 ORDER BY id`, s.Org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Override
	for rows.Next() {
		var o Override
		var sub string
		if err := rows.Scan(&o.ID, &o.Kind, &o.From, &o.To, &o.Value, &o.Reason, &o.Author, &o.CreatedAt, &sub); err != nil {
			return nil, err
		}
		if !canAll(v, strings.Split(sub, "\n")) {
			continue
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func canAll(v Viewer, subjects []string) bool {
	for _, s := range subjects {
		if s != "" && !v.CanRead(s) {
			return false
		}
	}
	return true
}

// ErrInvalid wraps a request the graph refuses.
var ErrInvalid = errors.New("invalid")

// AddOverride records a correction. Both repositories must exist and be
// readable by the person adding it, and a reason is required: an override
// without one is refused, so the page that shows it can always say why.
func (s *Store) AddOverride(ctx context.Context, v Viewer, o Override) (Override, error) {
	bad := func(f string, a ...any) (Override, error) {
		return Override{}, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...))
	}
	switch o.Kind {
	case OverrideBefore, OverrideIgnore, OverridePin:
	default:
		return bad("kind must be before, ignore or pin")
	}
	if strings.TrimSpace(o.Reason) == "" {
		return bad("say why: an override needs a reason")
	}
	if o.From == "" || o.To == "" || o.From == o.To {
		return bad("name two different repositories")
	}
	if o.Kind == OverridePin && o.Value == "" {
		return bad("a pin needs the version it is pinned to")
	}
	for _, p := range []string{o.From, o.To} {
		if _, err := s.find(ctx, v, NodeRef{KindRepository, p}, When{}); err != nil {
			return bad("%s is not a repository you can read", p)
		}
	}
	o.Reason = strings.TrimSpace(o.Reason)
	o.CreatedAt = s.now()
	subject := RepoSubject(o.From) + "\n" + RepoSubject(o.To)
	defer s.DB.WriteLock()()
	if _, err := s.DB.Exec(ctx, `INSERT INTO graph_overrides(org, kind, from_key, to_key, value, reason, author, subject, created_at, removed_at) VALUES(?,?,?,?,?,?,?,?,?,0)`,
		s.Org, o.Kind, o.From, o.To, o.Value, o.Reason, o.Author, subject, o.CreatedAt); err != nil {
		return Override{}, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT max(id) FROM graph_overrides WHERE org = ? AND created_at = ? AND from_key = ? AND to_key = ?`, s.Org, o.CreatedAt, o.From, o.To).Scan(&o.ID); err != nil {
		return Override{}, err
	}
	return o, nil
}

// RemoveOverride withdraws a correction (the row stays, marked removed).
func (s *Store) RemoveOverride(ctx context.Context, v Viewer, id int64) error {
	all, err := s.Overrides(ctx, v)
	if err != nil {
		return err
	}
	found := false
	for _, o := range all {
		found = found || o.ID == id
	}
	if !found {
		return fmt.Errorf("%w: no such override", ErrNotFound)
	}
	defer s.DB.WriteLock()()
	_, err = s.DB.Exec(ctx, `UPDATE graph_overrides SET removed_at = ? WHERE org = ? AND id = ?`, s.now(), s.Org, id)
	return err
}

// OrderQuery asks for the build order of a set of repositories.
type OrderQuery struct {
	Repositories []string // empty: every repository the viewer can see that has a dependency
	Upstream     bool     // also include what they depend on
	Limits       Limits
	When         When
}

// Cycle is repositories that depend on each other, as a path that closes.
type Cycle struct {
	Path       []string `json:"path"` // a, b, c, a
	Members    []string `json:"members"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// OrderResult is the order repositories are built in: providers before
// consumers; each level can be built together.
type OrderResult struct {
	Order     []string   `json:"order"`
	Levels    [][]string `json:"levels"`
	Edges     []RepoEdge `json:"edges"`
	Cycles    []Cycle    `json:"cycles,omitempty"`
	Overrides []Override `json:"overrides,omitempty"`
	Unknown   []string   `json:"unknown,omitempty"` // asked for but not visible or not in the graph
	Truncation
}

type repoDeps struct {
	edges     map[[2]string]*RepoEdge
	overrides []Override
	truncated bool
}

func (s *Store) loadRepoDeps(ctx context.Context, v Viewer, when When) (repoDeps, error) {
	edges, tr, err := s.repoGraph(ctx, v, when)
	if err != nil {
		return repoDeps{}, err
	}
	ovs, err := s.Overrides(ctx, v)
	if err != nil {
		return repoDeps{}, err
	}
	for _, o := range ovs {
		switch o.Kind {
		case OverrideBefore:
			k := [2]string{o.To, o.From}
			e := edges[k]
			if e == nil {
				e = &RepoEdge{Consumer: o.To, Provider: o.From, Kinds: []string{"override"}, Via: []string{"override"}}
				edges[k] = e
			}
			e.Override, e.Reason = "added", o.Reason
		case OverrideIgnore:
			if e := edges[[2]string{o.From, o.To}]; e != nil {
				e.Override, e.Reason = "ignored", o.Reason
			}
		case OverridePin:
			if e := edges[[2]string{o.From, o.To}]; e != nil {
				e.Override, e.Reason = "pinned", o.Reason
				e.Versions = addUnique(e.Versions, "pinned "+o.Value)
			}
		}
	}
	return repoDeps{edges, ovs, tr}, nil
}

// Order computes the build order. Edges a person ignored are left out of the
// order, edges a person added are in it, and both are listed as overrides.
func (s *Store) Order(ctx context.Context, v Viewer, q OrderQuery) (OrderResult, error) {
	lim := q.Limits.Normalised()
	d, err := s.loadRepoDeps(ctx, v, q.When)
	if err != nil {
		return OrderResult{}, err
	}
	res := OrderResult{}
	res.Truncated = d.truncated
	if d.truncated {
		res.Reason = "nodes"
	}
	// The repositories involved.
	involved := map[string]bool{}
	if len(q.Repositories) == 0 {
		for k, e := range d.edges {
			if e.Override != "ignored" {
				involved[k[0]], involved[k[1]] = true, true
			}
		}
	} else {
		known := map[string]bool{}
		for k := range d.edges {
			known[k[0]], known[k[1]] = true, true
		}
		nodes, err := s.visibleNodesOfKinds(ctx, v, q.When, KindRepository)
		if err != nil {
			return res, err
		}
		for _, n := range nodes {
			known[n.Key] = true
		}
		for _, r := range q.Repositories {
			if known[r] {
				involved[r] = true
			} else {
				res.Unknown = append(res.Unknown, r)
			}
		}
		if q.Upstream {
			// close over providers
			for changed := true; changed; {
				changed = false
				for k, e := range d.edges {
					if e.Override != "ignored" && involved[k[0]] && !involved[k[1]] {
						involved[k[1]], changed = true, true
					}
				}
			}
		}
	}
	if len(involved) > lim.Nodes {
		res.Truncated, res.Reason = true, "nodes"
		names := sortedKeys(involved)
		involved = map[string]bool{}
		for _, n := range names[:lim.Nodes] {
			involved[n] = true
		}
	}
	// The graph among them.
	deps := map[string][]string{} // consumer -> providers
	for k, e := range d.edges {
		if e.Override == "ignored" || !involved[k[0]] || !involved[k[1]] {
			continue
		}
		deps[k[0]] = append(deps[k[0]], k[1])
		res.Edges = append(res.Edges, *e)
	}
	for _, e := range d.edges {
		if e.Override == "ignored" && involved[e.Consumer] && involved[e.Provider] {
			res.Edges = append(res.Edges, *e)
		}
	}
	sort.Slice(res.Edges, func(i, j int) bool {
		if res.Edges[i].Consumer != res.Edges[j].Consumer {
			return res.Edges[i].Consumer < res.Edges[j].Consumer
		}
		return res.Edges[i].Provider < res.Edges[j].Provider
	})
	for _, o := range d.overrides {
		if involved[o.From] || involved[o.To] {
			res.Overrides = append(res.Overrides, o)
		}
	}
	names := sortedKeys(involved)
	for k := range deps {
		sort.Strings(deps[k])
	}
	comp, cycles := condense(names, deps)
	res.Cycles = cycles
	levels := levelsOf(names, deps, comp)
	for _, l := range levels {
		res.Levels = append(res.Levels, l)
		res.Order = append(res.Order, l...)
	}
	return res, nil
}

// condense finds the groups of repositories that depend on each other
// (strongly connected sets) and the cycle paths they make.
func condense(names []string, deps map[string][]string) (map[string]int, []Cycle) {
	index, low := map[string]int{}, map[string]int{}
	on := map[string]bool{}
	var stack []string
	comp := map[string]int{}
	var groups [][]string
	n := 0
	var strong func(v string)
	strong = func(v string) {
		n++
		index[v], low[v] = n, n
		stack = append(stack, v)
		on[v] = true
		for _, w := range deps[v] {
			if index[w] == 0 {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var g []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp[w] = len(groups)
				g = append(g, w)
				if w == v {
					break
				}
			}
			sort.Strings(g)
			groups = append(groups, g)
		}
	}
	for _, v := range names {
		if index[v] == 0 {
			strong(v)
		}
	}
	var cycles []Cycle
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		in := map[string]bool{}
		for _, m := range g {
			in[m] = true
		}
		path := cyclePath(g[0], in, deps)
		c := Cycle{Path: path, Members: g}
		if len(path) >= 3 {
			c.Suggestion = fmt.Sprintf("if %s does not really need %s, ignore that dependency with a reason: launchway graph override ignore %s %s --reason ...", path[0], path[1], path[0], path[1])
		}
		cycles = append(cycles, c)
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i].Members[0] < cycles[j].Members[0] })
	return comp, cycles
}

// cyclePath finds a path from start back to start inside a group, the
// shortest by hops.
func cyclePath(start string, in map[string]bool, deps map[string][]string) []string {
	type item struct {
		node string
		from int
	}
	q := []item{{start, -1}}
	seen := map[string]bool{}
	for i := 0; i < len(q); i++ {
		for _, w := range deps[q[i].node] {
			if !in[w] {
				continue
			}
			if w == start {
				var p []string
				for j := i; j >= 0; j = q[j].from {
					p = append([]string{q[j].node}, p...)
				}
				return append(p, start)
			}
			if !seen[w] {
				seen[w] = true
				q = append(q, item{w, i})
			}
		}
	}
	return []string{start, start}
}

// levelsOf orders repositories so that providers come before consumers,
// level by level; members of one cycle share a level. Within a level names
// are sorted, so the answer is the same every time.
func levelsOf(names []string, deps map[string][]string, comp map[string]int) [][]string {
	groups := map[int][]string{}
	for _, n := range names {
		groups[comp[n]] = append(groups[comp[n]], n)
	}
	need := map[int]map[int]bool{}
	for c := range groups {
		need[c] = map[int]bool{}
	}
	for _, n := range names {
		for _, p := range deps[n] {
			if comp[p] != comp[n] {
				need[comp[n]][comp[p]] = true
			}
		}
	}
	placed := map[int]bool{}
	var levels [][]string
	for len(placed) < len(groups) {
		var ready []int
		for c := range groups {
			if placed[c] {
				continue
			}
			ok := true
			for p := range need[c] {
				if !placed[p] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, c)
			}
		}
		if len(ready) == 0 {
			break
		}
		var level []string
		for _, c := range ready {
			level = append(level, groups[c]...)
			placed[c] = true
		}
		sort.Strings(level)
		levels = append(levels, level)
	}
	return levels
}

// RebuildItem is a repository that must be built again after a push, with why.
type RebuildItem struct {
	Repository string     `json:"repository"`
	Level      int        `json:"level"`
	Why        []RepoEdge `json:"why"` // the way from the pushed repository, nearest first
}

// RebuildResult is what must be built after a push, in order.
type RebuildResult struct {
	Repository string        `json:"repository"`
	Items      []RebuildItem `json:"items"`
	Skipped    []RepoEdge    `json:"skipped,omitempty"` // dependencies a person pinned or ignored, which a push does not rebuild
	Cycles     []Cycle       `json:"cycles,omitempty"`
	Truncation
}

// Rebuild answers "what must rebuild after this push", in the order to build
// it: the consumers of the repository, and their consumers, minus what a
// person pinned or ignored.
func (s *Store) Rebuild(ctx context.Context, v Viewer, repo string, lim Limits, when When) (RebuildResult, error) {
	lim = lim.Normalised()
	if _, err := s.find(ctx, v, NodeRef{KindRepository, repo}, when); err != nil {
		return RebuildResult{}, err
	}
	d, err := s.loadRepoDeps(ctx, v, when)
	if err != nil {
		return RebuildResult{}, err
	}
	res := RebuildResult{Repository: repo}
	consumers := map[string][]*RepoEdge{}
	for _, e := range d.edges {
		consumers[e.Provider] = append(consumers[e.Provider], e)
	}
	for _, l := range consumers {
		sort.Slice(l, func(i, j int) bool { return l[i].Consumer < l[j].Consumer })
	}
	type step struct {
		depth int
		via   []RepoEdge
	}
	reached := map[string]step{repo: {}}
	frontier := []string{repo}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []string
		for _, p := range frontier {
			for _, e := range consumers[p] {
				if e.Override == "ignored" || e.Override == "pinned" {
					res.Skipped = append(res.Skipped, *e)
					continue
				}
				if _, ok := reached[e.Consumer]; ok {
					continue
				}
				if depth > lim.Depth {
					res.Truncated, res.Reason = true, "depth"
					continue
				}
				if len(reached) > lim.Nodes {
					res.Truncated, res.Reason = true, "nodes"
					continue
				}
				reached[e.Consumer] = step{depth, append(append([]RepoEdge{}, reached[p].via...), *e)}
				next = append(next, e.Consumer)
			}
		}
		frontier = next
	}
	// Order them with the same rules as Order.
	set := map[string]bool{}
	for r := range reached {
		set[r] = true
	}
	deps := map[string][]string{}
	for k, e := range d.edges {
		if e.Override != "ignored" && set[k[0]] && set[k[1]] {
			deps[k[0]] = append(deps[k[0]], k[1])
		}
	}
	names := sortedKeys(set)
	for k := range deps {
		sort.Strings(deps[k])
	}
	comp, cycles := condense(names, deps)
	res.Cycles = cycles
	n := 0
	for li, level := range levelsOf(names, deps, comp) {
		for _, r := range level {
			if r == repo {
				continue
			}
			res.Items = append(res.Items, RebuildItem{Repository: r, Level: li, Why: reached[r].via})
			n++
		}
	}
	return res, nil
}
