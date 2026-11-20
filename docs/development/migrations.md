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

Schema 14 brings commands on a deployment's rollout and rollout steps. It is additive: new columns
on `deploy_commands`, `deployments` and `environment_state`, and four events, `deploy.command`,
`deploy.command_withdrawn`, `deploy.command_taken` and `deploy.command_done`. A pipeline with a
rollout step, `rollout.until: paused`, `rollout.pauses`, an approval's `deployment`, `abort_first`
or `on_cancel: abort` has `runs.min_schema` 14 from creation; some of these a schema-11 server would
misread rather than refuse (it knows `until`, `on_cancel` and `deployment` as fields), so such a
pipeline also carries `"requires": 14`, a field older servers do not know, which makes them read it
as not understood. A command given to a deployment of an older run raises that run's `min_schema` to
14 the same way. On a rollback to an older release, open deployments with such runs keep their locks
and are left alone; commands nobody took stay pending (and can be withdrawn by a schema-14 server).
Older servers ignore the new columns, so what was last seen of idle services stops being refreshed
but is not lost. A deploy runner of schema 14 talking to an older server finds no commands endpoint
and asks again only every five minutes; an older deploy runner never takes commands and is not given
idle services (it does not say it can observe them slowly), but observes open deployments as before.
`protected:` on environments and `environments:` on access bindings are settings older servers
refuse: set them only once every server knows them.

Schema 15 brings image steps (`kind: image`) and deploy changes that pin an image by `digest`. It is
additive: two new tables (`images`, `image_refs`, derived from a summary in the event that ends an
image step's attempt) and no new event type. A pipeline with an image step or a digest change has
`runs.min_schema` 15 from creation and carries `"requires": 15`, so older servers leave such runs
alone (`TestOlderBuildLeavesImageRunsAlone`) and refuse to start one; servers before schema 14 cannot
read the pipeline at all. Runners must say they can build images (`images`), so an older runner is
never given an image step, nor any step of a run that has one. A deploy change with a digest has no
tag, so a deploy runner older than this refuses the empty tag instead of writing a tag. The SBOM, the
findings and the provenance of an image step are ordinary reports and artifacts (schema 12). On a
rollback, image runs still open are left to a schema-15 server; the runners' `images:` setting and
the Helm chart's `runner.images` must be taken out first, as older runners refuse a setting they do
not know.

Schema 16 brings delivery policies. It is additive: four new tables (`policy_sources`, what servers
loaded from Git sources of policies; `policy_evaluations`, derived from events; `policy_waiver_events`,
the log of waivers, and `policy_waivers`, derived from it) and one event type, `policy.evaluated`.
A run a policy governs gets a `policy.evaluated` event in the transaction that creates it (or, if it
was created before a policy governed it, at the first gate it meets), which gives it
`runs.min_schema` 16, so a schema-15 server leaves it alone rather than run its steps past gates it
does not know (`TestOlderBuildLeavesPolicyRunsAlone`). Runs no policy governs are unchanged and stay
readable by schema 15. Findings summaries gain `by_tool`, which older servers ignore, as it only
records. A build limited to schema 15 refuses to start with policies configured. The `policy:`
setting is one older servers refuse, so the Helm chart writes it only when `config.policy` is set.

Rolling back from schema 16 to 15:

1. Take `policy:` out of every server's configuration (`config.policy` with the chart), and let the
   servers of schema 16 restart without it: new runs are then not governed, and older servers can
   handle them. Runs that already have decisions stay at `min_schema` 16.
2. Roll the servers back. Runs at `min_schema` 16 still open are left alone by the older servers
   (shown as needing a newer server); cancel them before rolling back, or let a schema-16 server
   finish them after a roll forward. Their decisions and the waivers stay in the database, unused.
3. Nothing needs restoring: the new tables are ignored by schema 15, and a roll forward picks up
   where it stopped (waivers that expired meanwhile are expired).

During a rolling upgrade, configure policies only once every server runs schema 16: until then an
older server could start a deploy that a newer one would have judged.

Schema 17 brings the built-in blob store: artifacts kept in the database (`blob_uploads`, `blob_parts`, `blob_chunks`, `blob_objects`, which are
`blobstore.DatabaseSchema` word for word; `TestBlobSchemaIsFrozen` fails if that text changes), and
`artifact_blobs.store`, the kind of store each blob was written to (`''` before 17). It is
additive and adds no event type. With no store configured, a server of 17 on PostgreSQL uses the
database store; an older server, with the same configuration, uses its own directory, finds the
store marker missing there (it is in the database store) and keeps no artifacts, saying why. So
that it does not run steps whose artifacts it cannot keep or fetch, a run that keeps or fetches
artifacts, created by a server using the database store, carries `"requires": 17` in its pipeline
(`runs.min_schema` 17): servers of 14 to 16 read it as not understood (in their words, "rollout
steps or keys that control a rollout", the only words they have for a pipeline's `requires`), list
and show it, and leave it alone (`TestOlderBuildLeavesDatabaseStoreRunsAlone`). `artifacts.store:
database` and `artifacts.database` are settings older servers refuse: the Helm chart writes them
only when set.

Rolling back from schema 17 to 16 is described in full in
[docs/artifacts.md](../artifacts.md#upgrading-to-schema-17-and-rolling-back). In short: take
`artifacts.store: database` and `artifacts.database` out of the configuration; to keep artifacts,
copy them into a store schema 16 knows first (`launchway blobs copy --from database --to s3`, or a
shared directory) and switch to it; then roll the servers back. Runs at `min_schema` 17 still open
wait for a schema-17 server. The new tables and column are ignored by schema 16.

Schema 18 brings richer test results, test attachments, coverage and HTML reports. It is additive:
three columns with defaults on `test_cases` (`tags`, `variant`, `extra`), two new tables
(`coverage_reports`, derived from the coverage summary in the event that ends an attempt, and
`coverage_files`, kept beside the events like `test_cases`), and no new event type. Attachments
and HTML reports are artifacts of kinds `test-attachments`, `test-embedded` and `html-report`
(schema 12's tables). Older servers write `test_cases` without the new columns (they take their
defaults), ignore the new tables, and ignore the new summary fields (`tests.attachments`,
`coverage`), which only record. A pipeline that uses `reports.tests.attachments`,
`reports.coverage` or `reports.html` has `runs.min_schema` 18 from creation, and older servers
cannot read it anyway: their strict reading of the pipeline fails on the new keys, so they leave
the run alone (`TestOlderBuildLeavesReportRunsAlone`). Runners are part of this: a step that
declares coverage is given only to a runner that reports the feature `coverage`; attachments and
HTML reports are kept by any runner that knows artifacts (the server adds them to the step's
artifacts, with their limits, and enforces the limits itself). A test's key now includes its
variant (browser or project) when a report gives one; results without one keep the keys they had,
so histories carry on.

Rolling back from schema 18 to 17:

1. Take `reports.tests.attachments`, `reports.coverage` and `reports.html` out of pipeline files
   first; runs already started with them stay with schema-18 servers, and an older server leaves
   them alone (they show as needing a newer server).
2. Take the `reports:` section out of the server configuration (with the Helm chart, unset every
   `config.reports.*` value): older servers refuse a setting they do not know.
3. Roll the servers back. Nothing in the database needs undoing: older servers ignore the new
   columns and tables. Attachments and HTML reports already kept stay listed as artifacts and
   expire as usual; the older UI shows them on the Artifacts tab only.
4. Runners may be rolled back in any order; an older runner simply never says `coverage`, so a
   step that declares coverage waits for a newer one.

Attachments and HTML reports are ordinary artifacts, so they are kept wherever the server keeps
artifacts, the database store included (`TestReportsInTheDatabaseStore`). Rolling back to 17 leaves
them readable there; going further back, to 16, they are copied with the other artifacts as above.

A run records the highest schema any of its features needs: a policy governing it 16, artifacts
kept in the database store 17, attachments, coverage or HTML reports 18
(`TestRunMinSchemaAcrossFeatures`). A database of schema 16 is migrated to 18 in one start, and a
build of 16 started against it again runs, says once that the database is newer, and leaves the
runs of 17 and 18 alone (`TestUpgradeFromSchema16AndBack`).

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
