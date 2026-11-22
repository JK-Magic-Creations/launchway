package engine

// Schema 51: how things are organized (folders, labels, layouts) and each
// person's own arrangement. It is number 51, after work tracking (50).

// schemaOrganize is the schema that brought folders, labels and layouts.
const schemaOrganize = 51

func init() {
	if len(migrations) != schemaOrganize-1 {
		panic("migration 51 must follow migrations 1 to 50")
	}
	migrations = append(migrations, organizeMigration)
}

// 51: the organizing model.
//
//	org_folders: named, described, ordered folders, nested by parent_id, in a
//	  scope (a level and its name: "base", "organization:x", "unit:x",
//	  "project:x") and an area (what they group: pipelines, repositories,
//	  projects, runs).
//	org_folder_items: which folder an item lives in; an item lives in one
//	  folder of an area, so the primary key is the item.
//	org_labels: free tags on items.
//	org_layouts: the shared default layout of a page at a level (the page's
//	  document, as JSON), with the lock that stops levels below changing it.
//	org_user_layouts: one person's own document for a page (their layer on
//	  top of the shared one); the page "prefs" holds favorites and dismissals.
//	org_activity: one person's trail of what they opened and did (kind and key
//	identify the object, so opening it again moves its entry), private to them.
//	Compatibility: additive: new tables, ignored by an older server.
//	Downgrade: nothing to undo.
var organizeMigration = migration{additive, `
CREATE TABLE org_folders (
  id          TEXT    PRIMARY KEY,
  scope       TEXT    NOT NULL,
  area        TEXT    NOT NULL,
  parent_id   TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  created_by  TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX org_folders_scope ON org_folders(scope, area);

CREATE TABLE org_folder_items (
  area      TEXT    NOT NULL,
  item      TEXT    NOT NULL,
  folder_id TEXT    NOT NULL,
  position  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (area, item)
);
CREATE INDEX org_folder_items_folder ON org_folder_items(folder_id);

CREATE TABLE org_labels (
  area  TEXT NOT NULL,
  item  TEXT NOT NULL,
  label TEXT NOT NULL,
  PRIMARY KEY (area, item, label)
);
CREATE INDEX org_labels_label ON org_labels(area, label);

CREATE TABLE org_layouts (
  scope      TEXT    NOT NULL,
  page       TEXT    NOT NULL,
  data       TEXT    NOT NULL DEFAULT '{}',
  locked     INTEGER NOT NULL DEFAULT 0,
  updated_by TEXT    NOT NULL DEFAULT '',
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (scope, page)
);

CREATE TABLE org_user_layouts (
  username   TEXT    NOT NULL,
  page       TEXT    NOT NULL,
  data       TEXT    NOT NULL DEFAULT '{}',
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (username, page)
);

CREATE TABLE org_activity (
  username TEXT    NOT NULL,
  kind     TEXT    NOT NULL,
  obj      TEXT    NOT NULL,
  project  TEXT    NOT NULL DEFAULT '',
  title    TEXT    NOT NULL DEFAULT '',
  href     TEXT    NOT NULL DEFAULT '',
  action   TEXT    NOT NULL DEFAULT 'viewed',
  state    TEXT    NOT NULL DEFAULT '',
  at       {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (username, kind, obj)
);
CREATE INDEX org_activity_recent ON org_activity(username, at);
`}
