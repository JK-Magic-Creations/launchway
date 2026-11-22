package engine

// Schema 58: protected tags and the count of release downloads (see
// docs/guide/protected-tags.md and docs/guide/releases.md). It is number 58,
// after the stand-in for 57.

// schemaTagRules is the schema that brought protected tags and download counts.
const schemaTagRules = 58

func init() {
	if len(migrations) != schemaTagRules-1 {
		panic("migration 58 must follow migrations 1 to 57")
	}
	migrations = append(migrations, tagRulesMigration)
}

// 58: tag_rules is, per hosted repository, the tags that are protected: pattern
// (v*), create_role (who may make a matching tag: admin or operator), change_role
// (who may move or delete one: empty, nobody, or admin) and pipelines (a JSON
// list of Flight patterns whose own credential may make one). release_downloads
// counts, per release and file name, the downloads that completed.
//
//	Compatibility: additive: two new tables, ignored by an older server (which
//	protects no tag and counts no download).
//	Downgrade: nothing to undo.
var tagRulesMigration = migration{additive, `
CREATE TABLE tag_rules (
  id          TEXT PRIMARY KEY,
  repo_id     TEXT    NOT NULL,
  pattern     TEXT    NOT NULL,
  create_role TEXT    NOT NULL DEFAULT 'admin',
  change_role TEXT    NOT NULL DEFAULT '',
  pipelines   TEXT    NOT NULL DEFAULT '[]',
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  created_by  TEXT    NOT NULL DEFAULT '',
  UNIQUE (repo_id, pattern)
);

CREATE TABLE release_downloads (
  release_id TEXT    NOT NULL,
  asset      TEXT    NOT NULL,
  downloads  {{BIG}} NOT NULL DEFAULT 0,
  last_at    {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (release_id, asset)
);
`}
