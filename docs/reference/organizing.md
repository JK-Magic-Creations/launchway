# Organizing, attention and search: reference

The API behind the [explorer](../guide/organizing.md) and the [home page](../guide/home-page.md). The routes are in the [HTTP API](http-api.md) reference; this page has the shapes and rules.

## Scopes

A shared folder or layout lives at a scope: `base`, `organization`, `unit:<name>` or `project:<name>` (the API names of Base, Wing, Squadron and Project). A person may arrange at `base` or `organization` when they are an admin of everything, at `unit:x` when they are admin of every project in it, and at `project:x` when they are admin of that project. A folder at a Squadron scope holds only that Squadron's items.

## Areas

`pipelines`, `repositories`, `projects` and `runs`. An item lives in one folder of an area. Folder names have no slash (a slash in an item's name makes derived folders), at most 80 characters; labels are lower case, at most 40 characters, at most 20 for an item.

## The attention rule

`GET /api/v1/attention` answers `needs`, `dismissed`, `retrying`, `count` and `superseded`. An entry has a stable `key` (`failed:<run>`, `deployment:<id>`, `decision:<run>/<step>`, `stuck:<run>`), a `kind`, `why` and `do`. The rule is applied over the newest runs of the last 14 days, at most 30,000; the thresholds are a queue wait of 15 minutes and a running time over three times the pipeline's median (at least 30 minutes). `GET /api/v1/runs?attention=needs` or `running` lists the Sorties themselves.

## Layout documents

`GET /api/v1/layouts/{page}?scope=` returns each level's shared layout, `locked_at`, the caller's own (`personal`) and the result (`effective`). Keys of a lower level replace those of a higher one whole; a locked level cannot be overridden below it or by a person. The pages are `home`, `pipelines`, `projects`, `repositories` and `runs`. The Home page reads `view` (`mine` or `all`), `order` and `hidden` (section ids: `needs`, `running`, `favorites`, `categories`, `recent`) and `open` (a map of Squadron to open).

An export is

```json
{ "version": 1, "scope": "base", "layouts": { "home": { "order": ["running", "needs"] } }, "locked": { "home": true },
  "folders": [ { "area": "pipelines", "path": "Core services/Tier 1", "items": ["payments-ledger"] } ] }
```

and an import adds and updates, never deletes.

## Search

`GET /api/v1/search?q=` returns `hits` ranked by `score`: projects, repositories, folders and Sorties the caller may read. A Sortie matches by number, commit prefix (four or more hex digits) or branch, within the last 90 days. Names are ranked over the permission-filtered list in memory, not in an external search service.

## The trail

`GET /api/v1/activity` is the caller's own trail and no route takes a user name. `PUT /api/v1/activity` notes an entry (`kind`: `run`, `project`, `pipeline`, `repository`, `pull`, `deployment`, `settings` or `page`; `obj`, `title`, `href` on this server, `action`, `state`); a Sortie's project is read from the Sortie. At most 400 entries are kept for a person and an entry older than the retention is neither listed nor kept. Entries for a project the caller can no longer read, and settings pages for a caller who is not an administrator, are left out of every answer.

## Audit actions

`folder.create`, `folder.update`, `folder.delete`, `folder.move`, `labels.set`, `layout.set`, `layout.reset`, `layout.import` change what everyone sees. `prefs.attention.dismiss`, `prefs.attention.restore`, `prefs.layout.set`, `prefs.layout.reset`, `prefs.layout.reset_all`, `prefs.favorite`, `prefs.activity.clear` and `prefs.activity.pause` are personal preferences and change only the person's own view.
