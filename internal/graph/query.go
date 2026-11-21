package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Viewer says what a reader may read. Every query of the graph takes one,
// and nothing is returned, counted or traversed that the viewer could not
// read in its own place: a node or edge whose subject the viewer may not
// read is treated as if it were not there.
type Viewer interface {
	// CanRead reports whether the viewer may read the thing a subject names.
	// The subject "" is public and is not asked.
	CanRead(subject string) bool
}

// AllowAll is the viewer of an operator task and of tests.
type AllowAll struct{}

// CanRead is always true.
func (AllowAll) CanRead(string) bool { return true }

// Only lets a viewer read exactly the subjects named.
type Only map[string]bool

// CanRead reports whether the subject is named.
func (o Only) CanRead(s string) bool { return o[s] }

// visibility turns a viewer into a SQL condition on a subject column.
type visibility struct {
	all    bool
	list   []any
	negate bool // list holds the denied subjects
}

const maxVisible = 20000

func (s *Store) visibility(ctx context.Context, v Viewer) (visibility, error) {
	if v == nil {
		return visibility{}, errors.New("a viewer is required")
	}
	if _, ok := v.(AllowAll); ok {
		return visibility{all: true}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT subject FROM graph_nodes WHERE org = ? AND subject <> '' GROUP BY subject
		UNION SELECT subject FROM graph_edges WHERE org = ? AND subject <> '' GROUP BY subject`, s.Org, s.Org)
	if err != nil {
		return visibility{}, err
	}
	defer rows.Close()
	defer rows.Close()
	var allowed, denied []any
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return visibility{}, err
		}
		if v.CanRead(sub) {
			allowed = append(allowed, sub)
		} else {
			denied = append(denied, sub)
		}
	}
	if err := rows.Err(); err != nil {
		return visibility{}, err
	}
	switch {
	case len(denied) == 0:
		return visibility{all: true}, nil
	case len(denied) <= len(allowed):
		if len(denied) > maxVisible {
			return visibility{}, errors.New("too many subjects to filter")
		}
		return visibility{list: denied, negate: true}, nil
	default:
		if len(allowed) > maxVisible {
			return visibility{}, errors.New("too many subjects to filter")
		}
		return visibility{list: allowed}, nil
	}
}

// predicate is the viewer's condition as a function, for what is joined in memory.
func (v visibility) predicate() func(subject string) bool {
	if v.all {
		return func(string) bool { return true }
	}
	set := make(map[string]bool, len(v.list))
	for _, x := range v.list {
		set[x.(string)] = true
	}
	return func(sub string) bool {
		if sub == "" {
			return true
		}
		if v.negate {
			return !set[sub]
		}
		return set[sub]
	}
}

// clause is the condition on a column and its arguments.
func (v visibility) clause(col string) (string, []any) {
	switch {
	case v.all:
		return "1 = 1", nil
	case v.negate:
		return "(" + col + " = '' OR " + col + " NOT IN (" + marks(len(v.list)) + "))", v.list
	case len(v.list) == 0:
		return col + " = ''", nil
	default:
		return "(" + col + " = '' OR " + col + " IN (" + marks(len(v.list)) + "))", v.list
	}
}

func marks(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// When selects the moment a query is about: zero is now.
type When struct {
	At int64 // milliseconds since the epoch; 0 is now
}

func (w When) edge(alias string) (string, []any) {
	if w.At == 0 {
		return alias + ".valid_to = 0", nil
	}
	return "(" + alias + ".valid_from <= ? AND (" + alias + ".valid_to = 0 OR " + alias + ".valid_to > ?))", []any{w.At, w.At}
}

func (w When) node(alias string) (string, []any) {
	if w.At == 0 {
		return alias + ".removed_at = 0", nil
	}
	return "(" + alias + ".first_seen <= ? AND (" + alias + ".removed_at = 0 OR " + alias + ".removed_at > ?))", []any{w.At, w.At}
}

// AsOfCommit finds the time a repository's graph was last read at the
// commit, so a query can be asked "as of that commit".
func (s *Store) AsOfCommit(ctx context.Context, v Viewer, repoPath, commit string) (When, error) {
	if _, err := s.find(ctx, v, NodeRef{KindRepository, repoPath}, When{}); err != nil {
		return When{}, err
	}
	var at int64
	err := s.DB.QueryRow(ctx, `SELECT max(at) FROM graph_runs WHERE org = ? AND scope = ? AND ref = ?`, s.Org, "repository:"+repoPath, commit).Scan(&at)
	if err != nil || at == 0 {
		return When{}, fmt.Errorf("the graph has no reading of %s at commit %s: %w", repoPath, shortRef(commit), ErrNotFound)
	}
	return When{At: at}, nil
}

func shortRef(c string) string {
	if len(c) > 9 {
		return c[:9]
	}
	return c
}

// EdgeView is an edge as it is shown: in its natural reading.
type EdgeView struct {
	ID         int64          `json:"id"`
	Kind       EdgeKind       `json:"kind"`
	From       NodeRef        `json:"from"`
	To         NodeRef        `json:"to"`
	Qualifier  string         `json:"qualifier,omitempty"`
	Constraint string         `json:"constraint,omitempty"`
	Resolved   string         `json:"resolved,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
	Origin     string         `json:"origin"`
	Ref        string         `json:"ref,omitempty"`
	Confidence Confidence     `json:"confidence"`
	ValidFrom  int64          `json:"valid_from"`
	ValidTo    int64          `json:"valid_to,omitempty"`
}

// Step is one hop of a path: the edge and the node it reached.
type Step struct {
	Edge EdgeView `json:"edge"`
	Node NodeRef  `json:"node"`
}

// Hit is one node a traversal reached, with why.
type Hit struct {
	Node  Node   `json:"node"`
	Depth int    `json:"depth"`
	Path  []Step `json:"path,omitempty"` // from the start to this node, along the shortest way found
}

// Direction says which way along the stored edges a walk goes.
type Direction int

const (
	// Downstream follows a change forward: from a thing to what it affects.
	Downstream Direction = iota
	// Upstream follows it backward: from a thing to what affects it.
	Upstream
)

func traversable() []any {
	var out []any
	for _, k := range EdgeKinds() {
		if info := edgeKinds[k]; !info.none && k != EdgeOrderBefore {
			out = append(out, string(k))
		}
	}
	return out
}

// walkResult is the raw reach of a traversal.
type walkResult struct {
	best  map[int64]walkRow // node -> shallowest way found
	order []int64           // nodes in depth order
	trunc Truncation
}

type walkRow struct {
	parent int64
	eid    int64
	depth  int
}

// walk is the bounded recursive traversal at the heart of the graph. It
// follows only edges and nodes the viewer can read and that hold at the
// chosen time, to a depth, a number of nodes and a time, and says when it
// stopped short.
func (s *Store) walk(ctx context.Context, v Viewer, starts []int64, dir Direction, kinds []any, lim Limits, when When) (walkResult, error) {
	lim = lim.Normalised()
	res := walkResult{best: map[int64]walkRow{}}
	if len(starts) == 0 {
		return res, nil
	}
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return res, err
	}
	if len(kinds) == 0 {
		kinds = traversable()
	}
	from, to := "src", "dst"
	if dir == Upstream {
		from, to = "dst", "src"
	}
	ev, evArgs := vis.clause("e.subject")
	nv, nvArgs := vis.clause("n.subject")
	ew, ewArgs := when.edge("e")
	nw, nwArgs := when.node("n")
	wc := walkCond{from: from, to: to, ev: ev, nv: nv, ew: ew, nw: nw, evArgs: evArgs, nvArgs: nvArgs, ewArgs: ewArgs, nwArgs: nwArgs, kinds: kinds}
	rowCap := lim.Nodes*8 + 64
	// SQLite's planner may start from the edges and scan the working set;
	// CROSS JOIN fixes the order (the working set first), which is what
	// every other database does anyway.
	join := "JOIN"
	if !s.DB.Postgres() {
		join = "CROSS JOIN"
	}
	// The recursion keeps only a node and its depth, and the rows come out
	// breadth first, so the first row of a node is its shallowest and a limit
	// on rows stops the work early. The way each node was reached (its
	// parent edge) is found afterwards, from the edges between the nodes
	// reached.
	q := `WITH RECURSIVE walk(nid, depth) AS (
  SELECT n.nid, 0 FROM graph_nodes n WHERE n.org = ? AND n.nid IN (` + marks(len(starts)) + `) AND ` + nv + `
  UNION
  SELECT e.` + to + `, w.depth + 1 FROM walk w
    ` + join + ` graph_edges e ON e.` + from + ` = w.nid AND e.org = ? AND ` + ew + ` AND e.kind IN (` + marks(len(kinds)) + `) AND ` + ev + `
    ` + join + ` graph_nodes n ON n.nid = e.` + to + ` AND ` + nv + ` AND ` + nw + `
  WHERE w.depth < ?
)
SELECT nid, depth FROM walk LIMIT ?`
	args := []any{s.Org}
	for _, id := range starts {
		args = append(args, id)
	}
	args = append(args, nvArgs...)
	args = append(args, s.Org)
	args = append(args, ewArgs...)
	args = append(args, kinds...)
	args = append(args, evArgs...)
	args = append(args, nvArgs...)
	args = append(args, nwArgs...)
	args = append(args, lim.Depth, rowCap+1)

	cctx, cancel := context.WithTimeout(ctx, time.Duration(lim.Time)*time.Millisecond)
	defer cancel()
	rows, err := s.DB.Query(cctx, q, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || cctx.Err() != nil {
			res.trunc = Truncation{true, "time"}
			return res, nil
		}
		return res, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var nid int64
		var depth int
		if err := rows.Scan(&nid, &depth); err != nil {
			if cctx.Err() != nil {
				res.trunc = Truncation{true, "time"}
				break
			}
			return res, err
		}
		n++
		if n > rowCap {
			res.trunc = Truncation{true, "nodes"}
			break
		}
		if old, ok := res.best[nid]; ok {
			if depth < old.depth {
				res.best[nid] = walkRow{depth: depth}
			}
			continue
		}
		if len(res.best) >= lim.Nodes {
			res.trunc = Truncation{true, "nodes"}
			continue
		}
		res.best[nid] = walkRow{depth: depth}
		res.order = append(res.order, nid)
	}
	err = rows.Err()
	_ = rows.Close() // before the next statements: the loop may have stopped early
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || cctx.Err() != nil {
			res.trunc = Truncation{true, "time"}
			if len(res.best) == 0 {
				return res, nil
			}
		} else {
			return res, err
		}
	}
	if err := s.parents(cctx, &res, wc); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || cctx.Err() != nil {
			res.trunc = Truncation{true, "time"}
			return res, nil
		}
		return res, err
	}
	sort.SliceStable(res.order, func(i, j int) bool { return res.best[res.order[i]].depth < res.best[res.order[j]].depth })
	if !res.trunc.Truncated && s.moreBeyond(ctx, res, lim.Depth, wc) {
		res.trunc = Truncation{true, "depth"}
	}
	return res, nil
}

// load reads nodes by number (in groups, so the statement stays small).
func (s *Store) load(ctx context.Context, ids []int64) (map[int64]Node, error) {
	out := make(map[int64]Node, len(ids))
	for i := 0; i < len(ids); i += 500 {
		j := min(i+500, len(ids))
		a := []any{s.Org}
		for _, id := range ids[i:j] {
			a = append(a, id)
		}
		rows, err := s.DB.Query(ctx, `SELECT nid, kind, key, name, subject, owner, attrs, origin, scope, ref, confidence, first_seen, last_seen, removed_at FROM graph_nodes WHERE org = ? AND nid IN (`+marks(j-i)+`)`, a...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var n Node
			var kind, attrs, conf string
			if err := rows.Scan(&n.ID, &kind, &n.Key, &n.Name, &n.Subject, &n.Owner, &attrs, &n.Origin, &n.Scope, &n.Ref, &conf, &n.FirstSeen, &n.LastSeen, &n.RemovedAt); err != nil {
				return nil, err
			}
			n.Kind, n.Attrs, n.Confidence = NodeKind(kind), decodeAttrs(attrs), Confidence(conf)
			out[n.ID] = n
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// loadEdges reads edges by number and shows them in their natural reading.
func (s *Store) loadEdges(ctx context.Context, ids []int64, nodes map[int64]Node) (map[int64]EdgeView, error) {
	out := make(map[int64]EdgeView, len(ids))
	type raw struct {
		v        EdgeView
		src, dst int64
		rev      int
	}
	var all []raw
	for i := 0; i < len(ids); i += 500 {
		j := min(i+500, len(ids))
		a := []any{s.Org}
		for _, id := range ids[i:j] {
			a = append(a, id)
		}
		rows, err := s.DB.Query(ctx, `SELECT id, kind, src, dst, rev, qualifier, constraint_text, resolved, attrs, origin, ref, confidence, valid_from, valid_to FROM graph_edges WHERE org = ? AND id IN (`+marks(j-i)+`)`, a...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var r raw
			var kind, attrs, conf string
			if err := rows.Scan(&r.v.ID, &kind, &r.src, &r.dst, &r.rev, &r.v.Qualifier, &r.v.Constraint, &r.v.Resolved, &attrs, &r.v.Origin, &r.v.Ref, &conf, &r.v.ValidFrom, &r.v.ValidTo); err != nil {
				return nil, err
			}
			r.v.Kind, r.v.Attrs, r.v.Confidence = EdgeKind(kind), decodeAttrs(attrs), Confidence(conf)
			all = append(all, r)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var missing []int64
	for _, r := range all {
		for _, id := range []int64{r.src, r.dst} {
			if _, ok := nodes[id]; !ok {
				missing = append(missing, id)
			}
		}
	}
	if len(missing) > 0 {
		more, err := s.load(ctx, missing)
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			nodes[k] = v
		}
	}
	for _, r := range all {
		a, b := nodes[r.src], nodes[r.dst]
		from, to := NodeRef{a.Kind, a.Key}, NodeRef{b.Kind, b.Key}
		if r.rev == 1 {
			from, to = to, from
		}
		r.v.From, r.v.To = from, to
		out[r.v.ID] = r.v
	}
	return out, nil
}

// find resolves a reference the viewer may know of.
func (s *Store) find(ctx context.Context, v Viewer, ref NodeRef, when When) (Node, error) {
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return Node{}, err
	}
	nv, nvArgs := vis.clause("n.subject")
	nw, nwArgs := when.node("n")
	a := append([]any{s.Org, string(ref.Kind), ref.Key}, nvArgs...)
	a = append(a, nwArgs...)
	var id int64
	err = s.DB.QueryRow(ctx, `SELECT n.nid FROM graph_nodes n WHERE n.org = ? AND n.kind = ? AND n.key = ? AND `+nv+` AND `+nw, a...).Scan(&id)
	if err != nil {
		return Node{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	m, err := s.load(ctx, []int64{id})
	if err != nil {
		return Node{}, err
	}
	return m[id], nil
}

// Get returns one node, or ErrNotFound when it does not exist or the viewer
// may not know of it (the two look the same).
func (s *Store) Get(ctx context.Context, v Viewer, ref NodeRef, when When) (Node, error) {
	return s.find(ctx, v, ref, when)
}

// hits turns a walk into hits with their paths. Start nodes are left out.
func (s *Store) hits(ctx context.Context, w walkResult, starts []int64) ([]Hit, map[int64]Node, error) {
	isStart := map[int64]bool{}
	for _, id := range starts {
		isStart[id] = true
	}
	var eids []int64
	ids := make([]int64, 0, len(w.order))
	for _, id := range w.order {
		ids = append(ids, id)
		if r := w.best[id]; r.eid != 0 {
			eids = append(eids, r.eid)
		}
	}
	nodes, err := s.load(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	edges, err := s.loadEdges(ctx, eids, nodes)
	if err != nil {
		return nil, nil, err
	}
	var out []Hit
	for _, id := range w.order {
		if isStart[id] {
			continue
		}
		var path []Step
		for cur := id; ; {
			r := w.best[cur]
			if r.eid == 0 {
				break
			}
			n := nodes[cur]
			path = append(path, Step{Edge: edges[r.eid], Node: NodeRef{n.Kind, n.Key}})
			cur = r.parent
			if len(path) > 200 {
				break
			}
		}
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		out = append(out, Hit{Node: nodes[id], Depth: w.best[id].depth, Path: path})
	}
	return out, nodes, nil
}

// Reach is what a traversal found.
type Reach struct {
	Start Node  `json:"start"`
	Hits  []Hit `json:"hits"`
	Truncation
}

// Neighbours walks from a node in one direction, to a depth.
func (s *Store) Neighbours(ctx context.Context, v Viewer, ref NodeRef, dir Direction, lim Limits, when When) (Reach, error) {
	start, err := s.find(ctx, v, ref, when)
	if err != nil {
		return Reach{}, err
	}
	w, err := s.walk(ctx, v, []int64{start.ID}, dir, nil, lim, when)
	if err != nil {
		return Reach{}, err
	}
	hits, _, err := s.hits(ctx, w, []int64{start.ID})
	if err != nil {
		return Reach{}, err
	}
	sortHits(hits)
	return Reach{Start: start, Hits: hits, Truncation: w.trunc}, nil
}

func sortHits(h []Hit) {
	sort.SliceStable(h, func(i, j int) bool {
		if h[i].Depth != h[j].Depth {
			return h[i].Depth < h[j].Depth
		}
		if h[i].Node.Kind != h[j].Node.Kind {
			return h[i].Node.Kind < h[j].Node.Kind
		}
		return h[i].Node.Key < h[j].Node.Key
	})
}

// PathBetween finds the shortest way between two nodes, going with a change
// or against it, and says which. No path is an empty answer, not an error.
type PathResult struct {
	From  Node   `json:"from"`
	To    Node   `json:"to"`
	Found bool   `json:"found"`
	Way   string `json:"way,omitempty"` // "affects" (a change in from reaches to) or "affected-by"
	Steps []Step `json:"steps,omitempty"`
	Truncation
}

// Path answers "how is A connected to B".
func (s *Store) Path(ctx context.Context, v Viewer, a, b NodeRef, lim Limits, when When) (PathResult, error) {
	from, err := s.find(ctx, v, a, when)
	if err != nil {
		return PathResult{}, err
	}
	to, err := s.find(ctx, v, b, when)
	if err != nil {
		return PathResult{}, err
	}
	out := PathResult{From: from, To: to}
	for _, d := range []struct {
		dir Direction
		way string
	}{{Downstream, "affects"}, {Upstream, "affected-by"}} {
		w, err := s.walk(ctx, v, []int64{from.ID}, d.dir, nil, lim, when)
		if err != nil {
			return out, err
		}
		if _, ok := w.best[to.ID]; ok {
			hits, _, err := s.hits(ctx, w, []int64{from.ID})
			if err != nil {
				return out, err
			}
			for _, h := range hits {
				if h.Node.ID == to.ID {
					out.Found, out.Way, out.Steps = true, d.way, h.Path
					return out, nil
				}
			}
		}
		if w.trunc.Truncated {
			out.Truncation = w.trunc
		}
	}
	return out, nil
}

// Search finds nodes by a part of their name or key, only those the viewer
// may read.
func (s *Store) Search(ctx context.Context, v Viewer, text string, kinds []NodeKind, limit int) ([]Node, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return nil, false, err
	}
	nv, nvArgs := vis.clause("n.subject")
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(text)) + "%"
	q := `SELECT n.nid FROM graph_nodes n WHERE n.org = ? AND n.removed_at = 0 AND ` + nv + ` AND (lower(n.key) LIKE ? ESCAPE '\' OR lower(n.name) LIKE ? ESCAPE '\')`
	a := append([]any{s.Org}, nvArgs...)
	a = append(a, like, like)
	if len(kinds) > 0 {
		q += ` AND n.kind IN (` + marks(len(kinds)) + `)`
		for _, k := range kinds {
			a = append(a, string(k))
		}
	}
	q += ` ORDER BY n.kind, n.key LIMIT ?`
	a = append(a, limit+1)
	ids, err := ids(ctx, s.DB, q, a...)
	if err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	m, err := s.load(ctx, ids)
	if err != nil {
		return nil, false, err
	}
	out := make([]Node, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out, more, nil
}

// Edges lists the edges the viewer may see around one node, both ways, with
// the direction of each in its natural reading.
func (s *Store) Edges(ctx context.Context, v Viewer, ref NodeRef, when When, limit int) ([]EdgeView, bool, error) {
	n, err := s.find(ctx, v, ref, when)
	if err != nil {
		return nil, false, err
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	vis, err := s.visibility(ctx, v)
	if err != nil {
		return nil, false, err
	}
	ev, evArgs := vis.clause("e.subject")
	nv, nvArgs := vis.clause("o.subject")
	ew, ewArgs := when.edge("e")
	a := []any{n.ID, s.Org, n.ID, n.ID}
	a = append(a, ewArgs...)
	a = append(a, evArgs...)
	a = append(a, nvArgs...)
	a = append(a, limit+1)
	ids, err := ids(ctx, s.DB, `SELECT e.id FROM graph_edges e JOIN graph_nodes o ON o.nid = CASE WHEN e.src = ? THEN e.dst ELSE e.src END
		WHERE e.org = ? AND (e.src = ? OR e.dst = ?) AND `+ew+` AND `+ev+` AND `+nv+` AND o.removed_at = 0 ORDER BY e.kind, e.id LIMIT ?`,
		a...)
	if err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	nodes := map[int64]Node{n.ID: n}
	m, err := s.loadEdges(ctx, ids, nodes)
	if err != nil {
		return nil, false, err
	}
	out := make([]EdgeView, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out, more, nil
}

// parents finds, for each node reached, the edge it was reached by: an edge
// from a node one step shallower. It reads the edges that leave the nodes
// reached once each.
func (s *Store) parents(ctx context.Context, res *walkResult, wc walkCond) error {
	from, to, ev, evArgs, ew, ewArgs, kinds := wc.from, wc.to, wc.ev, wc.evArgs, wc.ew, wc.ewArgs, wc.kinds
	ids := make([]int64, 0, len(res.best))
	for id := range res.best {
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i += 400 {
		j := min(i+400, len(ids))
		a := []any{s.Org}
		for _, id := range ids[i:j] {
			a = append(a, id)
		}
		a = append(a, ewArgs...)
		a = append(a, kinds...)
		a = append(a, evArgs...)
		rows, err := s.DB.Query(ctx, `SELECT e.id, e.`+from+`, e.`+to+` FROM graph_edges e WHERE e.org = ? AND e.`+from+` IN (`+marks(j-i)+`) AND `+ew+` AND e.kind IN (`+marks(len(kinds))+`) AND `+ev+` ORDER BY e.id`, a...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var eid, f, t int64
			if err := rows.Scan(&eid, &f, &t); err != nil {
				return err
			}
			fr, ok1 := res.best[f]
			to, ok2 := res.best[t]
			if ok1 && ok2 && to.depth == fr.depth+1 && to.eid == 0 {
				res.best[t] = walkRow{parent: f, eid: eid, depth: to.depth}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// walkCond is the SQL conditions of one walk: the direction, the viewer's
// conditions on edges and nodes, the moment, and the kinds followed.
type walkCond struct {
	from, to, ev, nv, ew, nw              string
	evArgs, nvArgs, ewArgs, nwArgs, kinds []any
}

// moreBeyond says whether any node at the depth limit has a way further on
// that the walk may follow.
func (s *Store) moreBeyond(ctx context.Context, res walkResult, depth int, wc walkCond) bool {
	var edge []any
	for id, r := range res.best {
		if r.depth == depth {
			edge = append(edge, id)
		}
	}
	if len(edge) == 0 || len(edge) > 2000 {
		return false
	}
	q := `SELECT count(*) FROM (SELECT 1 FROM graph_edges e JOIN graph_nodes n ON n.nid = e.` + wc.to + ` AND ` + wc.nv + ` AND ` + wc.nw + ` WHERE e.org = ? AND e.` + wc.from + ` IN (` + marks(len(edge)) + `) AND ` + wc.ew + ` AND e.kind IN (` + marks(len(wc.kinds)) + `) AND ` + wc.ev + ` LIMIT 1) x`
	a := append(append([]any{}, wc.nvArgs...), wc.nwArgs...)
	a = append(a, s.Org)
	a = append(a, edge...)
	a = append(a, wc.ewArgs...)
	a = append(a, wc.kinds...)
	a = append(a, wc.evArgs...)
	var more int
	return s.DB.QueryRow(ctx, q, a...).Scan(&more) == nil && more > 0
}
