package engine

// Schema 40, the built-in registry (internal/registry), in a file of its own:
// db.go is over the size ratchet. It follows the issue archive (39).

// schemaRegistry is the schema that brought the built-in OCI registry.
const schemaRegistry = 40

func init() {
	if len(migrations) != schemaRegistry-1 {
		panic("the built-in registry migration (40) must follow migrations 1 to 39")
	}
	migrations = append(migrations, registryMigration)
}

// 40: the built-in OCI registry.
//
//	registry_repositories: one row per repository ("project/name/..."), with
//	its project (the first path segment) and the organization whose blobs it
//	may share. registry_blobs: content-addressed objects in the blob store,
//	one per (organization, digest): deduplication never crosses an
//	organization. registry_repo_blobs: which repositories may see which
//	blobs (a blob is readable in a repository only through a link, so a
//	digest does not open another repository's content). registry_manifests,
//	registry_manifest_refs and registry_tags: what a repository holds and what
//	each manifest needs. registry_uploads and registry_upload_chunks: the
//	state of uploads in progress, in the database so that any server may take
//	the next chunk. registry_policies: quota, retention and immutability per
//	installation, project or repository. registry_gc_state: one row, the
//	lease that lets one server at a time collect garbage.
//	Compatibility: additive: new tables. Nothing older code reads changes.
//	Downgrade: nothing to undo; an older server ignores the tables (and the
//	blobs they name stay in the blob store until a newer server collects).
var registryMigration = migration{additive, `
CREATE TABLE registry_repositories (
  name       TEXT PRIMARY KEY,
  project    TEXT    NOT NULL,
  org        TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  pushed_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX registry_repositories_project ON registry_repositories(project);

CREATE TABLE registry_blobs (
  org        TEXT    NOT NULL,
  digest     TEXT    NOT NULL,
  size       {{BIG}} NOT NULL,
  store_key  TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  touched_at {{BIG}} NOT NULL,
  PRIMARY KEY (org, digest)
);

CREATE TABLE registry_repo_blobs (
  repo      TEXT    NOT NULL,
  digest    TEXT    NOT NULL,
  linked_at {{BIG}} NOT NULL,
  PRIMARY KEY (repo, digest)
);
CREATE INDEX registry_repo_blobs_digest ON registry_repo_blobs(digest);

CREATE TABLE registry_manifests (
  repo          TEXT    NOT NULL,
  digest        TEXT    NOT NULL,
  media_type    TEXT    NOT NULL,
  artifact_type TEXT    NOT NULL DEFAULT '',
  size          {{BIG}} NOT NULL,
  subject       TEXT    NOT NULL DEFAULT '',
  created_at    {{BIG}} NOT NULL,
  created_by    TEXT    NOT NULL DEFAULT '',
  run_id        TEXT    NOT NULL DEFAULT '',
  annotations   TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (repo, digest)
);
CREATE INDEX registry_manifests_subject ON registry_manifests(repo, subject);

CREATE TABLE registry_manifest_refs (
  repo     TEXT NOT NULL,
  manifest TEXT NOT NULL,
  ref      TEXT NOT NULL,
  kind     TEXT NOT NULL,
  PRIMARY KEY (repo, manifest, ref)
);
CREATE INDEX registry_manifest_refs_ref ON registry_manifest_refs(repo, ref);

CREATE TABLE registry_tags (
  repo       TEXT    NOT NULL,
  tag        TEXT    NOT NULL,
  digest     TEXT    NOT NULL,
  updated_at {{BIG}} NOT NULL,
  updated_by TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (repo, tag)
);
CREATE INDEX registry_tags_digest ON registry_tags(repo, digest);

CREATE TABLE registry_uploads (
  id         TEXT PRIMARY KEY,
  repo       TEXT    NOT NULL,
  org        TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  updated_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  size       {{BIG}} NOT NULL DEFAULT 0,
  next_seq   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE registry_upload_chunks (
  upload_id TEXT    NOT NULL,
  seq       INTEGER NOT NULL,
  size      {{BIG}} NOT NULL,
  store_key TEXT    NOT NULL,
  PRIMARY KEY (upload_id, seq)
);

CREATE TABLE registry_policies (
  scope      TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  body       TEXT    NOT NULL,
  updated_at {{BIG}} NOT NULL,
  updated_by TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (scope, name)
);

CREATE TABLE registry_gc_state (
  id           INTEGER PRIMARY KEY,
  lease_holder TEXT    NOT NULL DEFAULT '',
  lease_until  {{BIG}} NOT NULL DEFAULT 0,
  last_run_at  {{BIG}} NOT NULL DEFAULT 0,
  last_summary TEXT    NOT NULL DEFAULT ''
);
`}
