package engine

// Schema 66: the repository trash keeps who deleted a repository and when
// (see docs/guide/repositories.md). It is number 66, after the fork migrations (64, 65).

// schemaTrash is the schema that brought the trash ledger.
const schemaTrash = 66

func init() {
	if len(migrations) != schemaTrash-1 {
		panic("migration 66 must follow migrations 1 to 65")
	}
	migrations = append(migrations, trashMigration)
}

// 66: repo_trash has one row per repository that was deleted: when, by whom,
// and, once a permanent removal has begun, when it began (purge_started_at,
// so a removal that stopped half way is found and finished). The repository's
// own row keeps its owner and name; the row here is removed with it.
//
//	Compatibility: additive: one new table, ignored by an older server (which
//	shows no trash list and removes nothing).
//	Downgrade: nothing to undo.
var trashMigration = migration{additive, `
CREATE TABLE repo_trash (
  repo_id          TEXT PRIMARY KEY,
  deleted_at       {{BIG}} NOT NULL DEFAULT 0,
  deleted_by       TEXT    NOT NULL DEFAULT '',
  purge_started_at {{BIG}} NOT NULL DEFAULT 0
);
`}
