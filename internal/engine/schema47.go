package engine

// Schema 47, the integrated software graph (internal/graph): code, what is
// built from it, what it needs, and where it runs, as nodes and edges with
// provenance and validity. It is number 47, after releases (46).

// schemaGraph is the schema that brought the software graph.
const schemaGraph = 47

func init() {
	if len(migrations) != schemaGraph-1 {
		panic("the graph migration (47) must follow migrations 1 to 46")
	}
	migrations = append(migrations, graphMigration)
}

// 47: the software graph. graph_nodes holds one row per thing (a repository,
//
//	a package, a component, a requirement, a built thing, a pipeline, an
//	Airfield, a deployment, a machine, a platform, an owner); graph_edges one
//	row per relation, with the source that asserted it, the commit it was read
//	at, how sure the source is, and the time span it held (valid_to 0 means it
//	holds now: an edge is closed, never deleted, so "as of" can be answered).
//	An edge is stored in the direction a change travels (src affects dst) and
//	rev says the relation reads the other way round. subject names what a
//	reader must be allowed to read to see the row. graph_overrides are the
//	user's own corrections (a build-order edge, an ignored edge, a pin).
//	graph_runs says when each source last asserted each scope, which is how
//	"as of commit" is answered. Compatibility: additive: new tables, ignored
//	by an older server. Downgrade: nothing to undo.
var graphMigration = migration{additive, `
CREATE TABLE graph_nodes (
  nid        {{ID}},
  org        TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  key        TEXT    NOT NULL,
  name       TEXT    NOT NULL DEFAULT '',
  subject    TEXT    NOT NULL DEFAULT '',
  owner      TEXT    NOT NULL DEFAULT '',
  attrs      TEXT    NOT NULL DEFAULT '{}',
  origin     TEXT    NOT NULL DEFAULT '',
  scope      TEXT    NOT NULL DEFAULT '',
  ref        TEXT    NOT NULL DEFAULT '',
  confidence TEXT    NOT NULL DEFAULT 'extracted',
  first_seen {{BIG}} NOT NULL DEFAULT 0,
  last_seen  {{BIG}} NOT NULL DEFAULT 0,
  removed_at {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE(org, kind, key)
);
CREATE INDEX graph_nodes_scope ON graph_nodes(org, origin, scope);
CREATE INDEX graph_nodes_name ON graph_nodes(org, kind, name);

CREATE TABLE graph_edges (
  id          {{ID}},
  org         TEXT    NOT NULL DEFAULT '',
  kind        TEXT    NOT NULL,
  src         {{BIG}} NOT NULL,
  dst         {{BIG}} NOT NULL,
  rev         INTEGER NOT NULL DEFAULT 0,
  qualifier   TEXT    NOT NULL DEFAULT '',
  constraint_text TEXT NOT NULL DEFAULT '',
  resolved    TEXT    NOT NULL DEFAULT '',
  attrs       TEXT    NOT NULL DEFAULT '{}',
  subject     TEXT    NOT NULL DEFAULT '',
  origin      TEXT    NOT NULL DEFAULT '',
  scope       TEXT    NOT NULL DEFAULT '',
  ref         TEXT    NOT NULL DEFAULT '',
  confidence  TEXT    NOT NULL DEFAULT 'extracted',
  valid_from  {{BIG}} NOT NULL,
  valid_to    {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE(org, kind, src, dst, qualifier, origin, scope, valid_from)
);
CREATE INDEX graph_edges_src ON graph_edges(org, src, valid_to);
CREATE INDEX graph_edges_dst ON graph_edges(org, dst, valid_to);
CREATE INDEX graph_edges_scope ON graph_edges(org, origin, scope, valid_to);
CREATE INDEX graph_edges_kind ON graph_edges(org, kind, valid_to);

CREATE TABLE graph_overrides (
  id         {{ID}},
  org        TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  from_key   TEXT    NOT NULL,
  to_key     TEXT    NOT NULL DEFAULT '',
  value      TEXT    NOT NULL DEFAULT '',
  reason     TEXT    NOT NULL,
  author     TEXT    NOT NULL DEFAULT '',
  subject    TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  removed_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX graph_overrides_org ON graph_overrides(org, removed_at);

CREATE TABLE graph_runs (
  id       {{ID}},
  org      TEXT    NOT NULL DEFAULT '',
  origin   TEXT    NOT NULL,
  scope    TEXT    NOT NULL,
  ref      TEXT    NOT NULL DEFAULT '',
  at       {{BIG}} NOT NULL,
  nodes    {{BIG}} NOT NULL DEFAULT 0,
  edges    {{BIG}} NOT NULL DEFAULT 0,
  closed   {{BIG}} NOT NULL DEFAULT 0,
  detail   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX graph_runs_scope ON graph_runs(org, origin, scope, at);
`}
