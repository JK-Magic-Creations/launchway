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
`/readyz` move away, and then gives requests in flight up to 15 seconds. Runners may connect to
any server. With no `database` setting a
single server keeps its data in an embedded SQLite file, which suits development and small
installs. Sessions, API tokens and sign-ins in progress are kept in the database too, so a browser
or script may be sent to any server.

## Run it on Kubernetes

`make image` builds a container image, and the Helm chart in `deploy/helm/launchway` runs
several servers against your PostgreSQL database, with runners that execute steps in pods in a
namespace of your choice:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set database.existingSecret=launchway-database
```

See [deploy/README.md](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/README.md) for the required values, a high-availability example,
upgrades, and security notes.
