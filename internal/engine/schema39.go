package engine

// Schema 39, delivery: pipeline identities that may write to a protected
// branch of a hosted repository (the GitOps write-back), and notifications.

// schemaDelivery is the schema that brought them.
const schemaDelivery = 39

func init() {
	if len(migrations) != schemaDelivery-1 {
		panic("the delivery migration (39) must follow migrations 1 to 38")
	}
	migrations = append(migrations, deliveryMigration)
}

// 39: delivery.
//
//	branch_writers admits a pipeline identity (a deploy step's short-lived
//	credential) to write to a branch that is protected: for the branches
//	matching branch, the projects matching projects may change exactly the
//	files matching files, and nothing else. One row per rule.
//	Compatibility: additive: new tables. Nothing older code reads changes.
//	Downgrade: nothing to undo for an older server; the tables stay and are
//	ignored (an older server then admits no pipeline identity, which is
//	the safe way to fail).
var deliveryMigration = migration{additive, `
CREATE TABLE branch_writers (
  id         TEXT PRIMARY KEY,
  repo_id    TEXT    NOT NULL,
  branch     TEXT    NOT NULL,
  projects   TEXT    NOT NULL,
  files      TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX branch_writers_repo ON branch_writers(repo_id);
`}
