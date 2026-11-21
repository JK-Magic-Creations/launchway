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
| **The root key** (encryption at rest) | The key file named by `encryption.key_file` (or the chart's `<release>-launchway-root-key` Secret), the keystore and its passphrase, or the Vault or OpenBao transit key | **Yes, and apart from the database's backups.** A database dump holds only ciphertext for the server's own secrets and is useless without the key; the key cannot be recovered if lost. See [Encryption at rest](../guide/encryption-at-rest.md#back-up-the-key-separately). |
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

## Restore with encryption at rest

A restore needs two things: the dump and the root key. Put the key where the configuration says (or
mount the Secret), restore the dump, and run `launchway doctor encryption --restore-drill` before starting
servers: it opens every sealed secret, reading only. A dump without the key refuses to start a server with
the message *"this database is encrypted at rest, but no root key is configured"*, and a different key
refuses with *"does not open this database's encryption keys"*. Nothing is ever re-keyed by guessing. If a
backup is older than a root key replacement (`launchway encryption rewrap`), restoring it needs the old key.

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
| 22 | | `resources` on a step (the defaults and limits are kept in the database: `launchway step-resources`) |

Upgrading to schema 22 does not change how existing pipelines run: an installation that already has
runs starts with no step sized, and an administrator opts in (see [Resources](../reference/pipeline.md#resources)).

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

## Configuration in the database

Since schema 23 the settings an administrator manages are in the database, so **a database backup is
a configuration backup**, with every object's history. For a copy you can read, review and keep in Git:

```sh
launchway config export -o launchway-config.yaml
```

### Upgrading from a release that kept configuration in the file

1. Upgrade as usual. The first server of schema 23 to start imports the settings in its file into the
   database, once, and records the import (`launchway config imports` shows it and what it brought).
2. Every server logs each key of its file that it now ignores. Change those settings in the web UI,
   with `launchway config`, the API or Terraform from now on. With the Helm chart, the values still
   in `config.*` were imported the first time and are ignored after; take them out when convenient.
3. During a rolling upgrade, servers of the release before keep using their own file. Make no
   configuration change through the API until every server is replaced: an older server does not see
   it. **This matters most for access:** an older server keeps the role bindings of its file, so a
   revocation made in the database (taking an administrator out, narrowing a role) does **not** apply
   to it, and the person keeps their old access there until that server is upgraded. Upgrade all
   servers first, then change who may do what.

### Rolling back

Export, take the `objects:` list out of the text, merge the rest into each server's configuration
file, and roll the servers back; they use the file again, **with the file's role bindings**: a
revocation made in the database since the upgrade applies to the rolled-back servers only once the
exported `auth.access` is merged into their files, so check it before you roll back. The exact steps, and why they work, are in
[Migrations](../development/migrations.md) under schema 23. Settings changed in the
file after a rollback are not seen by the database; export the file's sections again and `launchway
config import --apply` before rolling forward.

### Locked out, or a stored configuration that will not load

Two protections keep a wrong configuration from locking everyone out:

- A change that would leave nobody able to administer everything is refused: with sign-in on, at
  least one role binding must give the `admin` role on every project (or the bootstrap file's
  `auth.admins` must name someone, which cannot be changed through the API).
- A server that cannot load the stored configuration at start-up does **not** exit. It starts in
  **safe mode**, logs `SAFE MODE` with the reason, runs with the last configuration that worked (kept in
  its data directory as `config-last-good.json`) or, if there is none, with the bootstrap file alone,
  and tries the stored configuration again every few seconds. `GET /api/v1/config` shows `safe_mode`
  in its status. The people named in the bootstrap file's `auth.admins` can still sign in and repair it.
  Objects of a kind a newer version wrote are ignored with a warning, never fatal.

If the API cannot be used at all, on the server host:

```sh
launchway config history --limit 20                     # if a server still answers
launchway config restore --offline --revision 41        # put revision 41 back, as a new revision
launchway server --ignore-database-config               # start with the bootstrap file only
```

`restore --offline` needs only the bootstrap file's database address, not a running server or a
sign-in; the history keeps everything, so the restore itself can be undone. `--ignore-database-config`
leaves the database untouched; restart without it after the repair.

### Restoring one object

Every change is in the object's history, and a deleted object is kept: `launchway config history
projects shop` shows who changed it, and `launchway config restore projects shop` brings a deleted
project back. To go back to an earlier version of the settings, take the spec from the history and
`launchway config set` it.
