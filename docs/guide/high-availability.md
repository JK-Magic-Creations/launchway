# Running several servers

How several Launchway servers share one database, and how the Helm chart runs them.

## One database, several servers

Point every server at the same PostgreSQL database and put them behind a load balancer:

```yaml
# launchway.yaml
listen: 0.0.0.0:8080
database: postgres://launchway:...@db.internal:5432/launchway
auth:
  mode: oidc   # see Sign-in and access
  # ...
```

`LAUNCHWAY_DATABASE` overrides the file. Each server exposes `/healthz` (the process is up) and
`/readyz` (the database answers, and the server is not shutting down). A starting server waits up
to `database_wait` (default `60s`) for the database. On SIGTERM a server reports not ready at
once, keeps serving for `server.drain_delay` (default `0s`) so that load balancers checking
`/readyz` move away, and then gives requests in flight up to 15 seconds. Stations (runners) may connect to
any server. With no `database` setting a
single server keeps its data in an embedded SQLite file, which suits development and small
installs. Sessions, API tokens and sign-ins in progress are kept in the database too, so a browser
or script may be sent to any server.

## Riding through a short database outage

A failover, a restart of the database or a network break must not lose work. The server retries
database errors that pass by themselves (connection refused or reset, a failover in progress, a
read-only standby after a promotion, serialization failures, deadlocks, too many connections) with
growing pauses, in one place in its store layer, for up to `resilience.stall_budget` (default
`120s`). A request that outlives the budget is answered `503` with `Retry-After`, never `500`.
Errors that would fail again (a constraint, a syntax error, a missing row) are not retried, and a
statement that was cut off in the middle is never repeated unless repeating it is safe.

Every write a Station makes is safe to repeat: log batches carry their position and the server keeps
what it has, the end of a step is applied once, and other writes carry an `Idempotency-Key`. While
the server cannot be reached a Station keeps executing the step and keeps its logs and its result on
its own disk (`spool` in the Station's settings), then delivers them in order; the step's log shows
"The server could not be reached from 14:02:10 to 14:03:40; nothing was lost." After an outage
the server gives running steps a fresh lease, as after a restart, and judges leases on the
database's clock. When the connection that hears from other servers is back, the server also
re-reads the configuration; the revision poll remains the guarantee, so nothing depends on a
notification arriving. `/readyz` reports `read_only` for a database that answers but cannot be
written, and `launchway doctor database` says whether the installation would survive losing its
database.

## Run it on Kubernetes

`make image` builds a container image, and the Helm chart in `deploy/helm/launchway` runs
several servers against your PostgreSQL database, with Stations that execute steps in pods in a
namespace of your choice:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set externalURL=https://launchway.example.com --set database.existingSecret=launchway-database
```

See [deploy/README.md](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/README.md) for the required values, a high-availability example,
upgrades, and security notes.
