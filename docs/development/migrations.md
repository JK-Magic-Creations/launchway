# Database schema and upgrades that can be rolled back

Launchway servers share one database and keep nothing in memory, so during an upgrade old and new
servers run side by side against the same data: in a rolling upgrade, in a canary, and again if
the new version is rolled back after it has migrated the database. This page is the rule that
keeps that safe, and what to do when you change the schema or the event log.

## How it works

Every migration in `internal/engine/db.go` has a number (its schema version) and declares a
**compatibility class**:

- **additive**: code that knows only the migrations before it keeps working. New tables, new
  columns that are nullable or have a default, new indexes. Nothing that older code would
  misread: no new meaning for an existing column, no new `NOT NULL` column without a default, no
  constraint that writes from older code would break, no data rewritten in place.
- **breaking**: anything else. Code older than this migration must not run against the database.

The database's `meta` table keeps `schema_version` (the newest migration applied) and
`schema_min`, the **minimum code schema**: the number of the newest breaking migration applied.
When a server starts:

- If the database is older than the server, the server applies the missing migrations, each in a
  transaction of its own that checks the version again. On PostgreSQL, servers starting together
  take turns under an advisory lock: exactly one applies each migration, and the others wait and
  then find nothing to do. Each migration applied is logged.
- If the database is newer but the server knows at least `schema_min`, the server starts and logs
  once that it is running against a newer schema.
- If the server knows less than `schema_min`, it refuses to start, and says which schema it needs.

`/readyz` and `/api/v1/info` report `schema.code` (what this build knows), `schema.database` and
`schema.minimum`. `/readyz` reads them on every call, so a server that is still running when a
newer one applies a breaking migration stops reporting ready.

The classes of migrations 1 to 7 were declared after they shipped, and are honest about the
release before each: 1, 2, 3, 4 and 7 are breaking, 5 and 6 additive (see the comments in
`db.go`). Releases before schema 8 also refuse any newer database whatever its class, so the
first rollback this makes possible is from a release after schema 8 to one at schema 8 or later.

## The event log is part of the schema

A run's state is folded from its events, and every server folds every run it touches. So a new
event type, or a new field whose meaning older servers must not ignore, is a schema change too:

- Each event type records the schema that brought it (`eventSince` in `events.go`). A new event
  type comes with a new migration, which may have no SQL at all.
- A server that meets an event type it does not know, an event marked `requires` a newer schema,
  a pipeline key it does not know, or a step kind, step outcome, run status or skip kind it does
  not know, does not guess. The run is marked as needing a newer server (`not_understood` and
  `needs_newer_server` in the API, a notice in the UI) and the server leaves it alone: it does
  not lease its steps, expire its leases or approvals, cancel, resume or re-run it. Every other
  run carries on as usual. A server of the newer version finishes it.
- Unknown fields in an event are ignored. Add a field only if older servers may safely ignore it
  (it only records more, as `inputs` on `step.leased` does). If they must not ignore it, set
  `Payload.Requires` on that event to the schema that understands it.
- `runs.min_schema` holds the newest schema among a run's events, so older servers skip such runs
  in SQL instead of reading them first.

New pipeline keys need care beyond the database. A server that does not know a key cannot parse
a pipeline file that uses it: when it polls a repository whose pipeline uses the key, it records
an error for the push and starts nothing, and since one server acts on each push, a push may be
lost that way during a rolling upgrade. Start using new keys in pipeline files only once every
server runs the release that knows them, and stop before rolling back. Schema 9's
`on.pull_request` and `supersede` are such keys; their runs are left to servers of schema 9 as
described above (a pull-request run's pipeline always has `on.pull_request`, which keeps older
servers from running it without the secrets rule they do not know). Schema 10's
`reports` is another: a run whose pipeline declares test reports has `runs.min_schema` 10, so a
schema-9 server leaves it alone rather than run its steps without collecting their reports. The
test summary schema 10 adds to `step.finished` and `step.canceled` events is a field older servers
may safely ignore, so it needs no `requires`.

Schema 11 brings deploy and rollback steps (`kind: deploy`, `kind: rollback`) and the `deploy.*`
events. It is additive: new tables and new columns with defaults. A run with such steps has
`runs.min_schema` 11 from creation, so a schema-10 server leaves it alone entirely: it does not
lease its steps (deploy steps are only ever leased to deploy runners anyway), expire its waits,
cancel, resume or re-run it, and it ignores the new tables, the environment locks among them. An
older server does list a waiting deploy step among its approvals, because it cannot tell the two
waits apart, but refuses to decide it. On a rollback to schema 10, open deployments keep their
locks and stay as they are; when a schema-11 server runs again they resume, and a deployment
whose deadline passed meanwhile is failed at once, saying what was last observed, which runs its
rollback step if it has one. The pipeline keys of deploy steps, and the `environments:` and
`runner_pools:` settings, carry the same caveat as schema 9's keys: use them only once every server
knows them.

Schema 12's `artifacts`, `fetch`, `reports.findings` and `reports.sbom` are handled the same way
as schema 10's `reports`: runs whose pipeline uses them have `runs.min_schema` 12, so a schema-11
server leaves them alone (`TestOlderBuildLeavesArtifactRunsAlone`), and the summaries schema 12
adds to finishing events (`findings`, `sbom`, `artifacts`) only record. Its tables are new and
ignored by older servers. Runners are part of this too: a runner that does not report the feature
`artifacts` is never given a step that keeps or fetches artifacts, which it would silently skip.

Schema 13's `inputs`, `for_each` (with `each` on the steps a loop makes, and `loops` in the
graph), `when: { input: … }`, `on.schedule` and `on.webhook` are such keys too. A run whose
pipeline uses any of them, whose trigger carries input values, or whose event is `schedule` or
`webhook` has `runs.min_schema` 13, so a server of schema 12 leaves it alone
(`TestOlderBuildLeavesTriggerRunsAlone`). Schema 13 adds no event type. Its API tokens limited to
a role or projects are stored under a `v2:` hash that older servers never look up, so a rollback
refuses such tokens instead of granting their owner's full rights.

## Writing a migration

1. Append a new entry to `migrations`; never edit one that has shipped. Write in the comment what
   it is for and why its class is what it is.
2. Prefer additive. Give new columns a default. Add tables rather than changing old ones.
3. If the code that uses the new tables or columns may be built limited to an older schema (see
   *Testing* below), check `e.knows(N)` before using them, as the code for schema 8 does
   (`schemaReruns`).
4. New event types: add them to `eventSince` with the new schema's number.
5. Run the compatibility tests (`go test ./internal/engine/ -run 'Older|Minimum|Concurrent|LeftAlone'`),
   on SQLite and on PostgreSQL.

### Changing something in a way older code cannot follow: expand, then contract

Renaming a column, changing what a value means, making a column required or dropping one cannot
be additive in one step. Spread it over two releases:

1. **Expand (release N, additive).** Add the new column or table next to the old one. Write both;
   read the new one, falling back to the old. Old servers keep reading and writing the old one,
   so for rows they write the new one may be stale: the new code must cope with that (backfill
   on read, or recompute from the event log).
2. Ship N and let it run. A rollback from N to N-1 is safe.
3. **Contract (release N+1, breaking).** Backfill what is left, then drop the old column or add the
   constraint. Declare the migration `breaking`: `schema_min` becomes N+1, and servers older than
   N+1 refuse to start, saying which version they need. Rolling back from N+1 is then only
   possible together with a database backup taken before it. Say so in the release notes.

A breaking migration also means an upgrade cannot run old and new servers side by side: stop the
old servers first (with the Helm chart, scale the server Deployment to zero, then upgrade), or the
old ones stop being ready as soon as the new one migrates.

## Testing

`compat_test.go` builds a database at the current schema N with the current code, then opens it
with the same code limited to schema N-1 (`Options.schema` in tests; `-ldflags "-X
github.com/jk-magic-creations/launchway/internal/engine.schemaLimit=N-1"` for a real binary). The
limited build knows only migrations 1..N-1 and their event types. The test checks that it starts,
logs once that the database is newer, runs a run end to end, and leaves alone a run with events
of schema N while a server of schema N finishes it. If the newest migration is breaking, the
test is skipped and `TestBuildBelowTheMinimumRefuses` checks the refusal instead.

`deploy/test/README.md` has the same check on a real cluster: install a build limited to N-1,
upgrade to N, roll the Deployment back half-way, and watch the old pods start and serve.

## New settings and the Helm chart

The same rule that holds for the database holds for the configuration file. During an upgrade,
and after an aborted one, pods of the old and the new version read the same ConfigMap, and a
server or runner refuses a setting it does not know. So:

- A new setting is optional in the program, with its default built in.
- The chart leaves it unset in `values.yaml` (a comment says what the built-in default is) and
  writes it into the configuration only when the operator sets it. With the chart's default
  values, the configuration must be readable by the release before.
- An operator who sets a new setting should do so only once every server and runner is upgraded,
  and take it out again before rolling back, as with new pipeline keys.

This was learned the hard way: a chart that wrote `tests:` by default left the previous version
unable to start when a rollout was aborted.
