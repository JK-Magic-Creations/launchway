package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Store keeps the graph in the engine's database (schema 47).
type Store struct {
	DB  *engine.Database
	Now func() time.Time
	// Org isolates one organisation's graph from another's; "" is the
	// installation's own.
	Org string
}

// NewStore makes a store over the engine's database.
func NewStore(db *engine.Database) *Store { return &Store{DB: db} }

func (s *Store) now() int64 {
	if s.Now != nil {
		return s.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// ErrNotFound means a node does not exist or the reader may not know of it.
var ErrNotFound = errors.New("no such node")

// ApplyResult says what one assertion changed.
type ApplyResult struct {
	NodesNew, NodesChanged, NodesRemoved int
	EdgesNew, EdgesChanged, EdgesClosed  int
	EdgesUnchanged                       int
}

// Changed reports whether anything changed.
func (r ApplyResult) Changed() bool {
	return r.NodesNew+r.NodesChanged+r.NodesRemoved+r.EdgesNew+r.EdgesChanged+r.EdgesClosed > 0
}

type edgeKey struct {
	kind     string
	src, dst int64
	qual     string
}

type curEdge struct {
	id         int64
	validFrom  int64
	constraint string
	resolved   string
	attrs      string
	subject    string
	conf       string
	ref        string
}

// Apply stores what a source asserts about one scope, as one transaction. It
// is idempotent: asserting the same again changes nothing. An edge the
// source asserted before for this scope and no longer asserts is closed at
// the time of the call; an edge whose constraint, resolved version or
// attributes changed is closed and a new one opened, so history is kept.
func (s *Store) Apply(ctx context.Context, origin, scope, ref string, a Assertion) (ApplyResult, error) {
	var res ApplyResult
	if origin == "" || scope == "" {
		return res, errors.New("a source and a scope are needed")
	}
	defer s.DB.WriteLock()()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	ids := map[NodeRef]int64{}
	asserted := map[int64]bool{}

	upsert := func(n Node, fromEdge bool) (int64, error) {
		r := NodeRef{n.Kind, n.Key}
		if id, ok := ids[r]; ok && fromEdge {
			return id, nil
		}
		if !n.Kind.Valid() || n.Key == "" {
			return 0, fmt.Errorf("a node needs a known kind and a key (got %q, %q)", n.Kind, n.Key)
		}
		conf := n.Confidence
		if conf == "" {
			conf = Extracted
		}
		nm := n.Name
		if nm == "" {
			nm = n.Key
		}
		var id, removed int64
		var name, subject, owner, attrs, o, sc, rf, cf string
		err := tx.QueryRow(ctx, `SELECT nid, name, subject, owner, attrs, origin, scope, ref, confidence, removed_at FROM graph_nodes WHERE org = ? AND kind = ? AND key = ?`, s.Org, string(n.Kind), n.Key).
			Scan(&id, &name, &subject, &owner, &attrs, &o, &sc, &rf, &cf, &removed)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(ctx, `INSERT INTO graph_nodes(org, kind, key, name, subject, owner, attrs, origin, scope, ref, confidence, first_seen, last_seen, removed_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, s.Org, string(n.Kind), n.Key, nm, n.Subject, n.Owner, encodeAttrs(n.Attrs), origin, scope, ref, string(conf), now, now); err != nil {
				return 0, err
			}
			if err := tx.QueryRow(ctx, `SELECT nid FROM graph_nodes WHERE org = ? AND kind = ? AND key = ?`, s.Org, string(n.Kind), n.Key).Scan(&id); err != nil {
				return 0, err
			}
			res.NodesNew++
		case err != nil:
			return 0, err
		default:
			if fromEdge {
				// A placeholder made for an edge's end changes nothing of a node that exists.
				ids[r] = id
				if removed != 0 {
					if _, err := tx.Exec(ctx, `UPDATE graph_nodes SET removed_at = 0, last_seen = ? WHERE nid = ?`, now, id); err != nil {
						return 0, err
					}
				}
				return id, nil
			}
			content := name != nm || subject != n.Subject || owner != n.Owner || attrs != encodeAttrs(n.Attrs) || removed != 0 || cf != string(conf)
			if content || o != origin || sc != scope || rf != ref {
				if content {
					res.NodesChanged++
				}
				if _, err := tx.Exec(ctx, `UPDATE graph_nodes SET name = ?, subject = ?, owner = ?, attrs = ?, origin = ?, scope = ?, ref = ?, confidence = ?, last_seen = ?, removed_at = 0 WHERE nid = ?`,
					nm, n.Subject, n.Owner, encodeAttrs(n.Attrs), origin, scope, ref, string(conf), now, id); err != nil {
					return 0, err
				}
			}
		}
		ids[r] = id
		asserted[id] = true
		return id, nil
	}
	for _, n := range a.Nodes {
		if _, err := upsert(n, false); err != nil {
			return res, err
		}
	}

	// The edges this source held for this scope.
	cur := map[edgeKey]curEdge{}
	rows, err := tx.Query(ctx, `SELECT id, kind, src, dst, qualifier, constraint_text, resolved, attrs, subject, confidence, ref, valid_from FROM graph_edges
		WHERE org = ? AND origin = ? AND scope = ? AND valid_to = 0`, s.Org, origin, scope)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var k edgeKey
		var c curEdge
		if err := rows.Scan(&c.id, &k.kind, &k.src, &k.dst, &k.qual, &c.constraint, &c.resolved, &c.attrs, &c.subject, &c.conf, &c.ref, &c.validFrom); err != nil {
			rows.Close()
			return res, err
		}
		cur[k] = c
	}
	if err := rows.Close(); err != nil {
		return res, err
	}
	seen := map[edgeKey]bool{}
	for _, e := range a.Edges {
		info, ok := edgeKinds[e.Kind]
		if !ok {
			return res, fmt.Errorf("unknown edge kind %q", e.Kind)
		}
		from, err := upsert(Node{Kind: e.From.Kind, Key: e.From.Key, Subject: e.Subject}, true)
		if err != nil {
			return res, err
		}
		to, err := upsert(Node{Kind: e.To.Kind, Key: e.To.Key, Subject: e.Subject}, true)
		if err != nil {
			return res, err
		}
		src, dst, rev := from, to, 0
		if info.rev {
			src, dst, rev = to, from, 1
		}
		k := edgeKey{string(e.Kind), src, dst, e.Qualifier}
		if seen[k] {
			continue
		}
		seen[k] = true
		conf := e.Confidence
		if conf == "" {
			conf = Extracted
		}
		attrs := encodeAttrs(e.Attrs)
		if c, ok := cur[k]; ok {
			if c.constraint == e.Constraint && c.resolved == e.Resolved && c.attrs == attrs && c.subject == e.Subject && c.conf == string(conf) {
				res.EdgesUnchanged++
				if c.ref != ref {
					if _, err := tx.Exec(ctx, `UPDATE graph_edges SET ref = ? WHERE id = ?`, ref, c.id); err != nil {
						return res, err
					}
				}
				delete(cur, k)
				continue
			}
			t := now
			if t <= c.validFrom {
				t = c.validFrom + 1
			}
			if _, err := tx.Exec(ctx, `UPDATE graph_edges SET valid_to = ? WHERE id = ?`, t, c.id); err != nil {
				return res, err
			}
			delete(cur, k)
			res.EdgesChanged++
			if err := s.insertEdge(ctx, tx, e, k, rev, origin, scope, ref, conf, attrs, t); err != nil {
				return res, err
			}
			continue
		}
		res.EdgesNew++
		if err := s.insertEdge(ctx, tx, e, k, rev, origin, scope, ref, conf, attrs, now); err != nil {
			return res, err
		}
	}
	for _, c := range cur { // no longer asserted: closed, never deleted
		t := now
		if t <= c.validFrom {
			t = c.validFrom + 1
		}
		if _, err := tx.Exec(ctx, `UPDATE graph_edges SET valid_to = ? WHERE id = ?`, t, c.id); err != nil {
			return res, err
		}
		res.EdgesClosed++
	}

	// Nodes this source made for this scope that it no longer asserts, and
	// that no current edge mentions, are gone.
	r2, err := tx.Query(ctx, `SELECT nid FROM graph_nodes WHERE org = ? AND origin = ? AND scope = ? AND removed_at = 0`, s.Org, origin, scope)
	if err != nil {
		return res, err
	}
	var stale []int64
	for r2.Next() {
		var id int64
		if err := r2.Scan(&id); err != nil {
			r2.Close()
			return res, err
		}
		if !asserted[id] {
			stale = append(stale, id)
		}
	}
	if err := r2.Close(); err != nil {
		return res, err
	}
	for _, id := range stale {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM graph_edges WHERE org = ? AND valid_to = 0 AND (src = ? OR dst = ?)`, s.Org, id, id).Scan(&n); err != nil {
			return res, err
		}
		if n == 0 {
			if _, err := tx.Exec(ctx, `UPDATE graph_nodes SET removed_at = ? WHERE nid = ?`, now, id); err != nil {
				return res, err
			}
			res.NodesRemoved++
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO graph_runs(org, origin, scope, ref, at, nodes, edges, closed, detail) VALUES(?,?,?,?,?,?,?,?,?)`,
		s.Org, origin, scope, ref, now, len(a.Nodes), len(a.Edges), res.EdgesClosed, ""); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

func (s *Store) insertEdge(ctx context.Context, tx *engine.Tx, e Edge, k edgeKey, rev int, origin, scope, ref string, conf Confidence, attrs string, at int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO graph_edges(org, kind, src, dst, rev, qualifier, constraint_text, resolved, attrs, subject, origin, scope, ref, confidence, valid_from, valid_to)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, s.Org, k.kind, k.src, k.dst, rev, e.Qualifier, e.Constraint, e.Resolved, attrs, e.Subject, origin, scope, ref, string(conf), at)
	return err
}

// Clear removes the whole graph of the organisation (for Rebuild). The
// overrides stay: they are people's, not a source's.
func (s *Store) Clear(ctx context.Context) error {
	defer s.DB.WriteLock()()
	for _, q := range []string{`DELETE FROM graph_edges WHERE org = ?`, `DELETE FROM graph_nodes WHERE org = ?`, `DELETE FROM graph_runs WHERE org = ?`} {
		if _, err := s.DB.Exec(ctx, q, s.Org); err != nil {
			return err
		}
	}
	return nil
}

// Counts says how much is in the graph (current nodes and edges), for the
// whole graph and not filtered: it is for operators and tests.
func (s *Store) Counts(ctx context.Context) (nodes, edges int, err error) {
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM graph_nodes WHERE org = ? AND removed_at = 0`, s.Org).Scan(&nodes); err != nil {
		return
	}
	err = s.DB.QueryRow(ctx, `SELECT count(*) FROM graph_edges WHERE org = ? AND valid_to = 0`, s.Org).Scan(&edges)
	return
}

// Fingerprint is a stable text of the current graph (nodes by kind and key,
// edges by kind, ends, qualifier, constraint and resolved version, in
// order), so a rebuild can be compared with the incremental graph.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	var b strings.Builder
	rows, err := s.DB.Query(ctx, `SELECT kind, key FROM graph_nodes WHERE org = ? AND removed_at = 0 ORDER BY kind, key`, s.Org)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var k, key string
		if err := rows.Scan(&k, &key); err != nil {
			rows.Close()
			return "", err
		}
		b.WriteString("N " + k + " " + key + "\n")
	}
	rows.Close()
	rows, err = s.DB.Query(ctx, `SELECT e.kind, a.kind, a.key, b.kind, b.key, e.rev, e.qualifier, e.constraint_text, e.resolved FROM graph_edges e
		JOIN graph_nodes a ON a.nid = e.src JOIN graph_nodes b ON b.nid = e.dst WHERE e.org = ? AND e.valid_to = 0 ORDER BY 1, 2, 3, 4, 5, 7`, s.Org)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var k, ak, akey, bk, bkey, q, c, r string
		var rev int
		if err := rows.Scan(&k, &ak, &akey, &bk, &bkey, &rev, &q, &c, &r); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "E %s %s:%s>%s:%s %d %s %s %s\n", k, ak, akey, bk, bkey, rev, q, c, r)
	}
	return b.String(), rows.Err()
}
