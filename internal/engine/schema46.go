package engine

// Schema 46, releases: the unit that is versioned, approved, promoted through Airfields and
// reported on (see releases.go and internal/release).

// schemaReleases is the schema that brought them.
const schemaReleases = 46

func init() {
	if len(migrations) != schemaReleases-1 {
		panic("the releases migration (46) must follow migrations 1 to 45")
	}
	migrations = append(migrations, releasesMigration)
}

// 46: releases.
//
//	release_settings is, per project, how versions are written and ordered (scheme), the tag
//	prefix and whether a project's releases may be downloaded without signing in (public, which
//	only counts when the installation allows public content). releases is one row per release:
//	its version, state (draft, candidate, published, yanked), notes, the commit and tag, the
//	Sortie that built it and its image digests (JSON), and who did each step; a published
//	release's commit, tag, version and assets never change. release_assets are the files and
//	images of a release with their SHA-256 and provenance; the content is kept under a key of
//	its own in the blob store, apart from the Cargo it was copied from, which expires.
//	release_gates is the checklist a release must pass to be promoted to an Airfield (JSON).
//	release_clearances are the clearances people gave a release for an Airfield.
//	release_promotions are the promotions: which release was promoted to which Airfield, with
//	the Sortie that did it, the exact commit and digest, and any override. release_freezes are
//	the freezes. release_formations and release_formation_members group releases of several
//	projects that are promoted together in an explicit order.
//	Compatibility: additive: new tables. An older server ignores them and promotes nothing.
//	Downgrade: nothing to undo.
var releasesMigration = migration{additive, `
CREATE TABLE release_settings (
  project     TEXT PRIMARY KEY,
  scheme      TEXT    NOT NULL DEFAULT 'semver',
  tag_prefix  TEXT    NOT NULL DEFAULT 'v',
  public      INTEGER NOT NULL DEFAULT 0,
  changelog   TEXT    NOT NULL DEFAULT 'CHANGELOG.md',
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE releases (
  id           TEXT PRIMARY KEY,
  project      TEXT    NOT NULL,
  version      TEXT    NOT NULL,
  scheme       TEXT    NOT NULL DEFAULT 'semver',
  state        TEXT    NOT NULL,
  title        TEXT    NOT NULL DEFAULT '',
  notes        TEXT    NOT NULL DEFAULT '',
  notes_source TEXT    NOT NULL DEFAULT '',
  branch       TEXT    NOT NULL DEFAULT '',
  sha          TEXT    NOT NULL DEFAULT '',
  tag          TEXT    NOT NULL DEFAULT '',
  tag_object   TEXT    NOT NULL DEFAULT '',
  tag_created  INTEGER NOT NULL DEFAULT 0,
  prerelease   INTEGER NOT NULL DEFAULT 0,
  public       INTEGER NOT NULL DEFAULT 0,
  run_id       TEXT    NOT NULL DEFAULT '',
  run_number   INTEGER NOT NULL DEFAULT 0,
  digests      TEXT    NOT NULL DEFAULT '[]',
  previous     TEXT    NOT NULL DEFAULT '',
  commit_time  {{BIG}} NOT NULL DEFAULT 0,
  created_at   {{BIG}} NOT NULL,
  created_by   TEXT    NOT NULL DEFAULT '',
  candidate_at {{BIG}} NOT NULL DEFAULT 0,
  candidate_by TEXT    NOT NULL DEFAULT '',
  published_at {{BIG}} NOT NULL DEFAULT 0,
  published_by TEXT    NOT NULL DEFAULT '',
  yanked_at    {{BIG}} NOT NULL DEFAULT 0,
  yanked_by    TEXT    NOT NULL DEFAULT '',
  yank_reason  TEXT    NOT NULL DEFAULT '',
  updated_at   {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE (project, version)
);
CREATE INDEX releases_project ON releases(project, created_at);
CREATE INDEX releases_sha ON releases(sha);
CREATE UNIQUE INDEX releases_tag ON releases(project, tag) WHERE tag <> '';

CREATE TABLE release_assets (
  id           TEXT PRIMARY KEY,
  release_id   TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  kind         TEXT    NOT NULL,
  size         {{BIG}} NOT NULL DEFAULT 0,
  sha256       TEXT    NOT NULL DEFAULT '',
  store_key    TEXT    NOT NULL DEFAULT '',
  image        TEXT    NOT NULL DEFAULT '',
  digest       TEXT    NOT NULL DEFAULT '',
  detail       TEXT    NOT NULL DEFAULT '',
  run_id       TEXT    NOT NULL DEFAULT '',
  run_number   INTEGER NOT NULL DEFAULT 0,
  step_id      TEXT    NOT NULL DEFAULT '',
  artifact_id  TEXT    NOT NULL DEFAULT '',
  source_sha   TEXT    NOT NULL DEFAULT '',
  added_by     TEXT    NOT NULL DEFAULT '',
  added_at     {{BIG}} NOT NULL,
  UNIQUE (release_id, name)
);

CREATE TABLE release_gates (
  project    TEXT NOT NULL,
  airfield   TEXT NOT NULL,
  gate       TEXT NOT NULL,
  updated_at {{BIG}} NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project, airfield)
);

CREATE TABLE release_clearances (
  id         TEXT PRIMARY KEY,
  release_id TEXT NOT NULL,
  airfield   TEXT NOT NULL,
  who        TEXT NOT NULL,
  role       TEXT NOT NULL DEFAULT '',
  comment    TEXT NOT NULL DEFAULT '',
  at         {{BIG}} NOT NULL,
  UNIQUE (release_id, airfield, who)
);

CREATE TABLE release_promotions (
  id           TEXT PRIMARY KEY,
  release_id   TEXT    NOT NULL,
  project      TEXT    NOT NULL,
  version      TEXT    NOT NULL,
  airfield     TEXT    NOT NULL,
  sha          TEXT    NOT NULL,
  digests      TEXT    NOT NULL DEFAULT '[]',
  run_id       TEXT    NOT NULL DEFAULT '',
  run_number   INTEGER NOT NULL DEFAULT 0,
  formation_id TEXT    NOT NULL DEFAULT '',
  planned_for  {{BIG}} NOT NULL DEFAULT 0,
  requested_by TEXT    NOT NULL,
  requested_at {{BIG}} NOT NULL,
  override     TEXT    NOT NULL DEFAULT '',
  overridden   TEXT    NOT NULL DEFAULT '',
  state        TEXT    NOT NULL DEFAULT 'started',
  detail       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX release_promotions_release ON release_promotions(release_id, requested_at);
CREATE INDEX release_promotions_airfield ON release_promotions(project, airfield, requested_at);

CREATE TABLE release_freezes (
  id          TEXT PRIMARY KEY,
  project     TEXT    NOT NULL DEFAULT '',
  airfield    TEXT    NOT NULL DEFAULT '',
  starts_at   {{BIG}} NOT NULL,
  ends_at     {{BIG}} NOT NULL,
  reason      TEXT    NOT NULL,
  created_by  TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL,
  canceled_at {{BIG}} NOT NULL DEFAULT 0,
  canceled_by TEXT    NOT NULL DEFAULT '',
  announced   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE release_formations (
  id         TEXT PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  note       TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'planned',
  airfield   TEXT    NOT NULL DEFAULT '',
  created_by TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  closed_at  {{BIG}} NOT NULL DEFAULT 0,
  order_source TEXT  NOT NULL DEFAULT 'explicit'
);

CREATE TABLE release_formation_members (
  formation_id TEXT    NOT NULL,
  release_id   TEXT    NOT NULL,
  position     INTEGER NOT NULL,
  state        TEXT    NOT NULL DEFAULT 'waiting',
  promotion_id TEXT    NOT NULL DEFAULT '',
  detail       TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (formation_id, release_id)
);
`}
