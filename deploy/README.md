# Running Launchway on Kubernetes

The Helm chart in [`helm/launchway`](helm/launchway) runs Launchway as a shared, highly available
service:

- **Servers** (a Deployment, 2 by default): the API, the web UI and the run engine. They share
  one PostgreSQL database and keep nothing in memory or on disk that matters, so any of them can
  be lost or replaced at any time. Their built-in runner is turned off.
- **Runners** (a Deployment, 2 by default): `launchway runner` with the Kubernetes executor. Each
  run gets a pod in the step namespace; the runner copies the checkout into it and executes the
  steps there. Runners only make outbound connections: to the servers and to the Kubernetes API.
- **PostgreSQL**: yours. The chart can deploy a single development database for trying it out.

Kubernetes 1.30 or later and Helm 3.8 or later are needed.

## Install

The image is built from the [`Dockerfile`](../Dockerfile) at the top of the repository
(`make image`). Push it to your registry and set `image.repository` and `image.tag`.

To try it, with the development database:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set postgresql.enabled=true \
  --set image.repository=registry.example.com/launchway --set image.tag=0.1.0
kubectl -n launchway port-forward service/launchway 8080:8080    # then http://127.0.0.1:8080
```

For real use, create the database Secret first and start from
[`examples/ha.yaml`](helm/launchway/examples/ha.yaml):

```sh
kubectl create namespace launchway
kubectl -n launchway create secret generic launchway-database \
  --from-literal=url='postgres://launchway:PASSWORD@db.internal:5432/launchway?sslmode=verify-full'
kubectl create namespace launchway-steps
helm install launchway deploy/helm/launchway -n launchway -f deploy/helm/launchway/examples/ha.yaml
```

### Required values

| Value | Why |
|---|---|
| `database.existingSecret` (+ `database.existingSecretKey`, default `url`), **or** `database.url`, **or** `postgresql.enabled=true` | Every server must use the same PostgreSQL database. Exactly one must be set, or the install fails with an explanation; the chart never lets replicas fall back to SQLite files of their own. |
| `image.repository`, `image.tag` (or `image.digest`) | Where your image is. |
| `config.projects` | The repositories to watch; each `repo` must be a URL that servers and runners can clone. |

Everything else has a working default. [`values.yaml`](helm/launchway/values.yaml) documents
every value and `values.schema.json` rejects misspelt or mistyped ones.

### Configuration

The server's configuration file is rendered from `config.listen`, `config.projects` and
`config.extraConfig` into a ConfigMap. `extraConfig` is merged in at the top level as it is, so
settings the chart does not know about (such as `approvals:`, `user:`, or `auth:` when it exists)
need no chart change. The servers refuse unknown settings, so a typo there stops them starting;
`kubectl logs` names the key. Changing the configuration restarts the servers, one at a time.

The database URL and the runner token are not in the ConfigMap: they reach the servers as
environment variables (`LAUNCHWAY_DATABASE`, `LAUNCHWAY_RUNNER_TOKEN`) from Secrets.

The runner token is shared by servers and runners through a Secret. Name your own with
`runnerToken.existingSecret`, or let the chart create one; it keeps the same token across
upgrades. Pods do not restart when a Secret you manage changes; run
`kubectl rollout restart deployment -l app.kubernetes.io/instance=<release>` after rotating it,
or after changing `database.url`.

### Runners and step pods

`runner.kubernetes.namespace` (default: the release namespace) is where step pods run. A
dedicated namespace is better: quotas, limit ranges and network policies for builds can then be
set on it alone. The chart grants the runners' ServiceAccount a Role there, and nowhere else,
with exactly what the executor uses: `pods` (create, get, list, delete, patch), `pods/exec`
(create, get) and `pods/log` (get). Set `runner.kubernetes.createNamespace=true` to have the
chart create the namespace (it is then deleted on uninstall).

Step pods set no security context of their own, because they run arbitrary build images. The
step namespace must admit them: Pod Security `baseline` does, `restricted` does not.

`runner.secretStores` configures the secret stores runners read (Vault, OpenBao, mounted files),
as described in the main [README](../README.md#secrets). Add volumes for token or CA files with
`runner.extraVolumes` and `runner.extraVolumeMounts`.

Runners connected from outside the cluster need only the server URL and the token.

### Exposing the UI

The Service is `ClusterIP`. Turn on `ingress.enabled` or `httpRoute.enabled` (Gateway API) to
expose it. The UI keeps a long-lived request open for live updates (`/api/v1/stream`): raise
your proxy's read timeout and turn off response buffering for it (the HA example shows the
annotations for ingress-nginx).

## Upgrades

`helm upgrade` replaces servers one at a time: a new server must pass `/readyz` before an old one
stops (`maxUnavailable: 0`), and a stopping server keeps answering for
`server.preStopSleepSeconds` (10 s) after it leaves the Service so that requests already on
their way to it succeed. A PodDisruptionBudget keeps node drains from taking more than one
server (and one runner) at once.

Database migrations run when a server starts. Servers take them in turn under a PostgreSQL
advisory lock, so a rolling upgrade is safe; a startup probe allows up to 3 minutes. A server
refuses to start against a database written by a newer version, so roll back the image only
together with the database.

Runners being replaced stop their steps, which are then run again by another runner. Steps
are run again from the start, so long steps are interrupted by runner upgrades.

## What is kept where

| Data | Where | Lost when |
|---|---|---|
| Runs, steps, events, logs, approvals, runner list, the runner token (unless you set one) | PostgreSQL | Only with the database: back it up |
| Mirrors of project repositories | Each server's `emptyDir` (`server.dataVolume`) | The pod goes; rebuilt from the repository on start |
| Checkouts for running steps | Each runner's `emptyDir` (`runner.dataVolume`) | The pod goes; steps then run again elsewhere |
| A run's workspace | Its step pod in the step namespace | The run succeeds (at once) or fails (after 2 hours, to allow a resume) |
| The development database | A PersistentVolumeClaim | `helm uninstall` deletes it |

Nothing else is persisted. Secrets values never reach the servers or the database.

## Security

- **There is no sign-in yet** unless one is configured. Anyone who can reach the Service can
  start, cancel and approve runs. Keep it on a trusted network; do not enable the Ingress or
  HTTPRoute on a public one.
- Servers and runners run as user 65532 with a read-only root file system, no capabilities, no
  privilege escalation and the RuntimeDefault seccomp profile. The image has no shell and no
  package manager.
- Servers have a ServiceAccount with no permissions and no token mounted.
- Runners can manage pods in the step namespace only. Anyone who can run a pipeline can run any
  image there, so treat that namespace as untrusted: give it a ResourceQuota, a LimitRange and a
  NetworkPolicy suited to builds, and keep other workloads out of it.
- The chart defines an `env` secret store that allows nothing unless you define `env` yourself,
  because `launchway runner` would otherwise add one that lets every pipeline read the runner's
  environment.
- Use `database.existingSecret` rather than `database.url`, so the password is not in your
  values or Helm's release history.

## Known limitations

- `launchway runner` has command-line flags only for the namespace of step pods. The other
  Kubernetes executor settings (helper image, CPU and memory, node selector, image pull
  secrets, step-pod service account, `keep_failed`, `start_timeout`) cannot be set for the
  chart's runners yet and take their built-in defaults. `runner.extraArgs` will pass such flags
  once they exist.
- Runners are named after their pod. A runner pod that is deleted while it holds step pods
  leaves those pods behind, because each runner cleans up only its own; delete them by hand
  (`kubectl -n <step namespace> get pods -l app.kubernetes.io/managed-by=launchway`). Deleted
  runners also stay in the runner list.
- The steps of one run may be taken by different runners, and each runner gives a run its own
  pod with a fresh checkout. Files a step writes are therefore not guaranteed to be seen by a
  later step; pass values with `$LAUNCHWAY_OUTPUTS`.
- The runner has no health endpoint, so its pods have no probes.

## Testing the chart

[`test/README.md`](test/README.md) installs the chart on a kind cluster and checks a real
pipeline, failover of servers and runners, and a rolling upgrade.
