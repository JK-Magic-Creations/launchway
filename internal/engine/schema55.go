package engine

// Schema 55: the release published to an outside Git host (see
// docs/development/release-publish-outside.md). It is number 55, after the
// measured use of a step attempt (54). Another branch may take 55 first; the
// merge then renumbers this one and nothing else changes.

// schemaOutside is the schema that brought the outside-host release.
const schemaOutside = 55

func init() {
	if len(migrations) != schemaOutside-1 {
		panic("migration 55 must follow migrations 1 to 54")
	}
	migrations = append(migrations, outsideMigration)
}

// 55: release_outside, one row per release and push mirror that publishes
//
//	releases: state pending (waiting for its turn, or for the mirror to
//	have copied the tag), published or failed; outside_url is where the
//	release is on the outside host; reason says why it failed or waits,
//	with every credential removed; attempts counts the tries; next_at is
//	when a pending row is tried next. repo_mirrors gains publish_releases
//	(0, off, until a person turns it on) and outside_api, an address of
//	the outside host's API for a self-hosted host (empty: derived from the
//	mirror's address).
//	Compatibility: additive: one new table and two columns with defaults,
//	ignored by an older server.
//	Downgrade: nothing to undo.
var outsideMigration = migration{additive, `
CREATE TABLE release_outside (
  id          TEXT PRIMARY KEY,
  release_id  TEXT    NOT NULL,
  mirror_id   TEXT    NOT NULL,
  state       TEXT    NOT NULL DEFAULT 'pending',
  outside_url TEXT    NOT NULL DEFAULT '',
  reason      TEXT    NOT NULL DEFAULT '',
  attempts    INTEGER NOT NULL DEFAULT 0,
  next_at     {{BIG}} NOT NULL DEFAULT 0,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE (release_id, mirror_id)
);
CREATE INDEX release_outside_due ON release_outside(state, next_at);
ALTER TABLE repo_mirrors ADD COLUMN publish_releases INTEGER NOT NULL DEFAULT 0;
ALTER TABLE repo_mirrors ADD COLUMN outside_api TEXT NOT NULL DEFAULT '';
`}
