# Back up, restore and upgrade

This guide says what to back up in a Launchway installation, how to restore it, and how to upgrade
and roll back safely, on one server with SQLite or on several servers sharing PostgreSQL. It is for
operators.

> **Verified** on 2026-11-19 against commit f717337: an online SQLite backup with `sqlite3 .backup`
> while the server ran, restored into an empty data directory, with the earlier run present and the
> runner reconnecting; `pg_dump` and `pg_restore` (PostgreSQL 17.12) of a database the server used,
> dropped, recreated and restored, after which the server started without migrating, showed the
> earlier run and numbered the next run after it; `pg_dump` of the Helm chart's development database
> through `kubectl exec`; on a kind cluster, `helm upgrade` with more servers and a changed
> configuration while a run went on and `/readyz` was requested five times a second (no failures),
> and `helm rollback`. **Not verified:** an upgrade between two released versions (there is one
> version so far), a breaking migration (none exists after schema 7), restoring an S3 artifact store,
> managed database snapshots (RDS, Cloud SQL).

## What you need

- Access to the server's data directory (SQLite) or a PostgreSQL client that can reach the
  database: `pg_dump` and `pg_restore` of the same major version as the server, or newer.
- For SQLite, the `sqlite3` command-line tool.
- For Kubernetes, `kubectl` and `helm` for the cluster.

## What to back up

| What | Where | Back it up? |
|---|---|---|
| The database: runs, steps, events, logs, approvals, test results, findings, artifact records (and artifacts themselves, with the default database store on PostgreSQL), sessions, API token hashes, the audit trail, the generated runner token | PostgreSQL, or `launchway.db` in the data directory | Yes. Everything that matters is here. |
| Artifacts and SBOMs | The database (the default on PostgreSQL, so already in its backup), the S3 bucket, or `artifacts/` in the data directory | Yes, at the same time as the database when they are outside it: the database records which objects exist. Hosted HTML reports and test attachments are artifacts too. |
| Configuration | `launchway.yaml`, `runner.yaml`, your Helm values | Yes, with your other configuration (in Git, for example). |
| Secrets the configuration names | OIDC client secret, `LAUNCHWAY_RUNNER_TOKEN` if you set one, runner pool tokens, S3 keys | Yes, in your secret manager. They are never in the database. |
| `runner-token` | Data directory | Optional: a copy of the generated token, which is also in the database. |
| Mirrors of project repositories | `repos/` in the data directory | No. They are rebuilt from the repositories when a server starts. |
| Runner workspaces and step pods | Runner data directories, the step namespace | No. A step that was running runs again. |

Secret values from Vault or OpenBao never reach Launchway, so there is nothing of them to back up.

## Back up SQLite

A single server without a `database` setting keeps everything in `launchway.db` in its data
directory, next to `artifacts/`, `repos/` and `runner-token`. The database runs in SQLite's
write-ahead-log mode, so while the server runs, recent changes may be in `launchway.db-wal` and not
yet in `launchway.db`. Copying `launchway.db` alone while the server runs can give you a backup that
is missing changes or is damaged.

Back it up while the server runs with SQLite's online backup, which copies a consistent snapshot:

```sh
mkdir -p backup
sqlite3 data/launchway.db ".backup 'backup/launchway.db'"
sqlite3 backup/launchway.db "PRAGMA integrity_check;"
cp -a data/artifacts data/runner-token backup/
chmod -R go-rwx backup
```

```text
ok
```

Here `data` is the server's data directory (`data:` in `launchway.yaml`). The backup holds API token
hashes and session hashes, so keep it as private as the server's data. To back up with the server
stopped instead, copy `launchway.db` together with `launchway.db-wal` if it exists.

## Restore SQLite

Stop the server, put the backup in an empty data directory, and start the server again:

```sh
mv data data.old
mkdir -m 700 data
cp backup/launchway.db backup/runner-token data/
cp -a backup/artifacts data/
launchway server --config launchway.yaml
```

Do not leave a `launchway.db-wal` or `launchway.db-shm` from another database next to the restored
file. The server rebuilds the repository mirrors as it starts. Runs and their logs are back as they
were at the backup; runners reconnect by themselves, because the runner token in the database is
the same. Runs started after the backup are gone.

## Back up PostgreSQL

Several servers share one PostgreSQL database, and that database holds everything. Take backups
with your usual tools: a managed service's snapshots and point-in-time recovery, or `pg_dump`:

```sh
pg_dump --format=custom --file=launchway.dump \
  'postgres://launchway:PASSWORD@db.internal:5432/launchway'
```

`pg_dump` takes a consistent snapshot while the servers run; you do not need to stop them. Back up
the artifact bucket at about the same time if you use S3 (S3 versioning, or a copy of the bucket); with the default database store, artifacts are in the dump and its size grows with them (see [Where artifacts are kept](../artifacts.md) for what to know about vacuum and backups). An artifact
whose object is missing cannot be fetched or downloaded; a step that fetches it fails and says so.

With the Helm chart's development database, which is for trying only:

```sh
kubectl -n launchway exec deploy/launchway-postgresql -- \
  pg_dump -U launchway --format=custom launchway > launchway.dump
```

## Restore PostgreSQL

1. Stop every server, so that none writes to the database while it is replaced. With the Helm
   chart:

   ```sh
   kubectl -n launchway scale deployment launchway-server --replicas=0
   ```

   The Deployment is `<release>-server` for a release whose name contains `launchway`, and
   `<release>-launchway-server` otherwise.

2. Recreate the database and restore the dump into it:

   ```sh
   psql 'postgres://launchway:PASSWORD@db.internal:5432/postgres' \
     -c 'DROP DATABASE launchway' -c 'CREATE DATABASE launchway'
   pg_restore --no-owner --exit-on-error \
     --dbname='postgres://launchway:PASSWORD@db.internal:5432/launchway' launchway.dump
   ```

3. Start the servers again (`helm upgrade` with your values, or scale the Deployment back up). Check
   that the schema is what you expect:

   ```sh
   curl -s https://launchway.example.com/readyz
   ```

   ```text
   {"schema":{"code":21,"database":21,"minimum":7},"status":"ok"}
   ```

Restore a backup only into a release that knows its schema or a newer one: a newer server migrates
the restored database as it starts, an older one may refuse it (see below).

## Upgrades

### Schema versions

Every release knows a database **schema** version. `/readyz` and `/api/v1/info` report three numbers:

- `schema.code`: the newest schema this server knows;
- `schema.database`: the schema the database is at;
- `schema.minimum`: the oldest server schema the database still accepts.

When a server starts against an older database, it applies the missing migrations, each in its own
transaction. On PostgreSQL, servers that start together take turns under a lock, so exactly one
applies each migration. The Helm chart's startup probe allows three minutes for this.

Most migrations are **additive**: new tables and columns with defaults. A server one release older
keeps working against a database they have been applied to: it starts, logs once that the database
is newer, and carries on. A **breaking** migration raises `schema.minimum`; servers older than that
refuse to start and say which version they need. A release's notes say when it has one. So far,
none after schema 7 is breaking. [Database schema and upgrades](../development/migrations.md) has the
full rules.

### Before you upgrade

1. Take a database backup, and note the release you are on.
2. Read the release notes for breaking migrations and for new settings.
3. Check `/readyz` on every server.

### A rolling upgrade

On Kubernetes, change the image and upgrade:

```sh
helm upgrade launchway deploy/helm/launchway -n launchway -f values.yaml --set image.tag=NEW_VERSION --wait
```

Servers are replaced one at a time: a new server must be ready before an old one stops, and a
stopping server keeps serving for a few seconds after it leaves the Service. Runs carry on while
servers are replaced. Runners being replaced hand their steps back, and those steps run again from
the start on another runner, in a fresh workspace, so a long step is interrupted by a runner upgrade.

Old and new servers run side by side during the rollout, against the database the new ones migrated.
A run that uses something only the newer version understands is shown on the old servers as needing
a newer server and left alone; a newer server finishes it.

On one server, stop it, replace the binary or image, and start it; it migrates the database as it
starts.

### New features wait until every server is upgraded

A server or runner refuses a setting it does not know, and an older server cannot run pipeline keys
it does not know. So turn on what a release brings only once every server and runner runs it, and
take it out again before rolling back. The settings and keys this applies to so far:

| Schema | Settings | Pipeline keys |
|---|---|---|
| 9 | | `on.pull_request`, `supersede` |
| 10 | `tests:` (`config.tests` in the chart) | `reports` (test reports) |
| 11 | `environments:`, `runner_pools:` (`delivery.*`) | `kind: deploy`, `kind: rollback` |
| 12 | `artifacts:`, `findings:` (`config.artifacts`, `config.findings`) | `artifacts`, `fetch`, `reports.findings`, `reports.sbom` |
| 13 | `loops:`, `schedules:` | `inputs`, `for_each`, `when: { input }`, `on.schedule`, `on.webhook` |
| 14 | `protected:` on environments, `environments:` on access bindings | rollout steps, `until: paused`, `pauses`, `abort_first`, `on_cancel: abort` |
| 15 | the runners' `images:` (`runner.images`) | `kind: image`, deploy changes by `digest` |
| 16 | `policy:` (`config.policy`) | |
| 17 | `artifacts.store: database`, `artifacts.database` (`config.artifacts.store`, `config.artifacts.database`) | |
| 18 | `reports:` (`config.reports`) | `reports.tests.attachments`, `reports.coverage`, `reports.html` |
| 19 | `verification:`, `ai:`, the runners' `ai:` and `health_sources:` (`delivery.verification`, `ai.*`, `runner.ai`) | `kind: verify`, `kind: ai` |
| 20 | `runner_admin:`, `require_identity`, the runners' `labels:`, `accept:`, `proxy:`, `no_proxy:`, `ca_file:` (`config.runnerAdmin`, `runner.join`, `runner.labels`, `runner.accept`) | |
| 21 | `runner_pools:` with `kind: build` (`config.runnerPools`) | `runner:` |

The Helm chart writes these settings only when you set them, so its default configuration can be
read by the release before.

### Breaking migrations

When a release's notes say its migration is breaking, old and new servers cannot overlap. Stop all
servers first, then upgrade, and do not use a canary:

```sh
kubectl -n launchway scale deployment launchway-server --replicas=0
helm upgrade launchway deploy/helm/launchway -n launchway -f values.yaml --set image.tag=NEW_VERSION --wait
```

Going back after a breaking migration means restoring the database from the backup taken before it.

## Roll back

After an additive upgrade, the previous release starts and serves against the migrated database, so
going back is safe:

```sh
helm history launchway -n launchway
helm rollback launchway REVISION -n launchway --wait
```

Before rolling back, take out the settings the newer release brought (the table above), so that the
older servers can read the configuration. Open runs that need the newer release stay as they are,
shown as needing a newer server; cancel them first, or let them finish after you upgrade again.
Rolling back from schema 16 or later has its own steps for each schema in [Database schema and upgrades](../development/migrations.md).

> [!TIP]
> Prefer `helm rollback` to `kubectl rollout undo`. After `kubectl rollout undo`, the next
> `helm upgrade` with Helm 4 fails with a field-manager conflict on the image, and needs
> `--force-conflicts`.

Releases before schema 8 refuse any newer database, so rolling back across the upgrade to schema 8
needs the database restored from a backup taken before it.

## What next

- [Install on Kubernetes](install-kubernetes.md#upgrades) for how the chart replaces servers.
- [Monitoring](monitoring.md), to watch `/readyz` and the schema numbers.
- [Troubleshooting](troubleshooting.md).
