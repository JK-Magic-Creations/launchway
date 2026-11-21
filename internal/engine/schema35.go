package engine

// Schema 35, build cache correctness (see cache.go, cache_scopes.go and
// cache_generations.go), kept in a file of its own: db.go is over the size
// ratchet. It is named so that its init runs after schema34.go's.

// schemaCacheTrust is the schema that brought per-pipeline cache scopes,
// sensitive caches and generations.
const schemaCacheTrust = 35

func init() {
	if len(migrations) != schemaCacheTrust-1 {
		panic("the cache correctness migration (35) must follow migrations 1 to 34")
	}
	migrations = append(migrations, cacheTrustMigration)
}

// 35: cache correctness. An entry is now kept per pipeline (pipeline: the
//
//	pipeline's own name, or shared:<name> when its caches opt in to being
//	shared), so a lint pipeline cannot seed a release pipeline by using the
//	same cache name; may be marked sensitive (never read outside the scope
//	that wrote it); and has a generation, so that a key whose cached paths
//	changed during a step can be saved again as a newer generation without
//	changing any entry (the unique index now covers the generation; a
//	restore takes the newest). content is a digest of what the entry holds
//	(paths, modes, sizes and file hashes, not times), so a later step can tell
//	whether anything changed. cache_policy.max_age_days is how old an entry
//	may be, since it was made, before a restore treats it as missing; 0 is the
//	built-in 7 days.
//	Entries written before this have pipeline '' and no run reads them: their
//	keys changed with the previous release (the key now includes the image
//	digest), so none could have been restored anyway; they end under the
//	idle rule of the cache policy.
//	Compatibility: additive: new columns with defaults and a new unique index
//	in place of the old one. A server that does not know them never reads
//	them; the entries it writes have pipeline '' and are never restored by a
//	server of this schema.
//	Downgrade: nothing to undo; the columns are ignored.
var cacheTrustMigration = migration{additive, `
ALTER TABLE caches ADD COLUMN pipeline TEXT NOT NULL DEFAULT '';
ALTER TABLE caches ADD COLUMN sensitive INTEGER NOT NULL DEFAULT 0;
ALTER TABLE caches ADD COLUMN generation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE caches ADD COLUMN content TEXT NOT NULL DEFAULT '';
ALTER TABLE cache_policy ADD COLUMN max_age_days INTEGER NOT NULL DEFAULT 0;
DROP INDEX caches_entry;
CREATE UNIQUE INDEX caches_entry_generation ON caches(project, scope, pipeline, name, key, platform, generation) WHERE status IN ('uploading', 'kept');
`}

var schemaMarkersCacheTrust = schemaMarker{
	schema: schemaCacheTrust, feature: "cache scopes per pipeline, sensitive caches and generations",
	columns: []string{"caches.pipeline", "caches.sensitive", "caches.generation", "caches.content", "cache_policy.max_age_days"},
}
