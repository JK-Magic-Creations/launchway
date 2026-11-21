# Running Launchway on Kubernetes

The Helm chart in [`helm/launchway`](helm/launchway) runs Launchway as a shared, highly available
service:

- **Servers** (a Deployment, 2 by default): the API, the web UI and the run engine. They share
  one PostgreSQL database and keep nothing in memory or on disk that matters, so any of them can
  be lost or replaced at any time. Their built-in runner is turned off.
- **Runners** (a Deployment, 2 by default): `launchway runner` with the Kubernetes executor. Each
  run gets a pod in the step namespace; the runner copies the checkout into it and executes the
  steps there. All of a run's steps go to the runner that took its first step, so they share
  that pod. Runners only make outbound connections: to the servers and to the Kubernetes API.
- **PostgreSQL**: yours. The chart can deploy a single development database for trying it out.

The chart in [`helm/launchway-runner`](helm/launchway-runner) installs **runners only**, for a
server elsewhere: see [Runners in another cluster](#runners-in-another-cluster).

Kubernetes 1.30 or later and Helm 3.8 or later are needed.

## Install

Released images are `ghcr.io/jk-magic-creations/launchway` (the chart's default; the tag defaults
to the chart's version) and `ghcr.io/jk-magic-creations/launchway-runner`, for linux/amd64 and
linux/arm64, signed, with an SBOM and provenance attached ([Verifying downloads](../docs/security/verify-downloads.md) shows how
to verify them). Pin `image.digest` in production. To build your own from source, use the
[`Dockerfile`](../Dockerfile) (`make image`), or `make release-local release-images` for the
release images; push to your registry and set `image.repository` and `image.tag`. Without
internet access, use the air-gapped bundle (`make bundle`, `OFFLINE-INSTALL.md` inside).

To install, give it the address people will open. That is the only required value:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set externalURL=https://launchway.example.com
kubectl -n launchway get secret launchway-launchway-setup-code -o jsonpath='{.data.code}' | base64 -d; echo
```

The chart starts a secure installation by itself: sign-in required (built-in accounts; the first
administrator is made with that one-time setup code), encryption at rest on, a bundled PostgreSQL on an
8Gi volume, default-deny network policies, restricted pods. Open the address, enter the setup code and
follow the guide in the browser. Until you publish the address (`ingress.enabled` or `httpRoute.enabled`),
use `kubectl -n launchway port-forward service/launchway-launchway 8080:8080`.

**Nothing secret is rendered by the chart.** A pre-install and pre-upgrade hook Job
(`launchway bootstrap-secrets`) makes the root key, the runner token, the bundled database's password
and the setup code once, only if they do not exist, and never changes them, so `helm template`, Argo CD
and Flux render the same manifests every time. **The root key Secret
(`<release>-launchway-root-key`) is kept when you uninstall**: copy it somewhere safe, because a restored
database cannot be read without it (encryption at rest (`docs/guide/encryption-at-rest.md`, from the encryption work)).

For production at scale, create the database Secret first and start from
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
| `externalURL` | The address people open, such as `https://launchway.example.com`. The install fails without it. |

Everything else has a working, secure default: the database (bundled; or `database.existingSecret` /
`database.url` for your own, which switches the bundled one off), the image, replicas, network
policies, encryption. [`values.yaml`](helm/launchway/values.yaml) leads with the few values most people
set and keeps the rest under an "ADVANCED" divider; `values.schema.json` rejects misspelt or
mistyped ones. Projects, runners, Git hosting and sign-in are set up in the browser after the first
sign-in.

### Configuration

The server's configuration file is rendered from `config.listen`, `config.projects` and
`config.extraConfig` into a ConfigMap. `extraConfig` is merged in at the top level as it is, so
settings the chart does not know about (such as `approvals:`, `user:`, or `auth:` when it exists)
need no chart change. The servers refuse unknown settings, so a typo there stops them starting;
`kubectl logs` names the key. Changing the configuration restarts the servers, one at a time.

The database URL and the runner token are not in the ConfigMap: they reach the servers as
environment variables (`LAUNCHWAY_DATABASE`, `LAUNCHWAY_RUNNER_TOKEN`) from Secrets.

The runner token is shared by servers and runners through a Secret. Name your own with
`runnerToken.existingSecret`, or let the install hook Job make one; it keeps the same token across
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

The chart renders the runners' settings into a file, `/etc/launchway-runner/runner.yaml`, read
with `launchway runner --config`. Step pods are configured under `runner.kubernetes`: the helper
image, CPU and memory requests and the memory limit per container, a node selector, image pull
secrets, the ServiceAccount step pods run as (default: none, and no token mounted), how long a
failed run's pod is kept (`keepFailed`, default none), how old any pod may get (`maxPodAge`) and how long to wait for images (`startTimeout`).
`runner.extraArgs` adds flags, which override the file.

`runner.secretStores` configures the secret stores runners read (Vault, OpenBao, mounted files),
as described in the [secrets guide](../docs/guide/secrets.md). Add volumes for token or CA files with
`runner.extraVolumes` and `runner.extraVolumeMounts`. Runners have only the stores defined there.

Runners serve `/healthz` and `/readyz` on `runner.healthPort` (8081, inside the pod only), used by
their liveness and readiness probes. A runner is ready while the servers have answered it within
the last two minutes, so a rollout of runners that cannot reach the servers stops.

#### Step pods and Pod Security

By default step pods meet the Pod Security Standard `restricted`, so the step namespace can
enforce it (`pod-security.kubernetes.io/enforce=restricted`; a namespace the chart creates gets
that label). Every container of a step pod, the helper and services included:

- runs as user 1000 and group 1000 (`runner.kubernetes.runAsUser` and `runAsGroup`), whatever
  user its image names, and never as root;
- has no privilege escalation, no Linux capabilities, and the RuntimeDefault seccomp profile;
- has a writable home directory, `HOME=/home/launchway`, and a writable `/tmp`, both empty
  volumes of the pod. `PIP_CACHE_DIR`, `npm_config_cache` and `GOCACHE` point inside that home.
  A step that sets one of these variables itself keeps its own value, and so does a service that
  sets it in its `env`.

The checkout belongs to that user, so steps can change it, and Git accepts it (the pod's
`~/.gitconfig` marks `/workspace` as a safe directory; a step that moves `HOME` loses that).
Official images such as `python`, `node` and `golang` work as an unknown user with a writable
home. What does not work is anything that needs root: `apt-get install`, `apk add`, writing
under `/usr` or `/etc`, and on many clusters listening on a port below 1024. Such a step fails
with the tool's own "permission denied" or "are you root?" message; the log of the step that
starts the pod says which user the containers run as. Use an image that already has the tools,
or build one.

Services run the same way, and some service images expect to start as root. The step that
starts the pod then fails with the service's last lines of output. The official `postgres` image
works when its data directory is writable: give the service `env: { PGDATA: /tmp/pgdata }`. The
official `nginx` image does not work; use a variant made to run as any user, such as
`nginxinc/nginx-unprivileged` (which listens on port 8080), or give the service a `command` that
works as an ordinary user. Databases and other servers often have such a variant.

Only the operator decides this; nothing in a pipeline file can change a step pod's user or make
it privileged. To pick another user, set `runner.kubernetes.runAsUser` (any user but 0) and
`runAsGroup`. To run images as their own users, root for many, as Launchway did before, set
`runner.kubernetes.restricted=false`; the step namespace must then admit such pods, so label it
Pod Security `baseline` (with `createNamespace`, set
`runner.kubernetes.namespaceLabels."pod-security.kubernetes.io/enforce"=baseline`; the chart
refuses to create a `restricted` namespace for pods that cannot run in it). A runner started by
hand takes `kubernetes.restricted: false` in its file, or `--k8s-unrestricted`.

**Upgrading:** this is on by default. Pipelines whose steps install packages or otherwise need
root fail until their images are changed or `restricted` is turned off.

#### Which runner runs a step

The steps of a run share a workspace, which lives in the pod of the runner that took the run's
first step. So that runner keeps the run: its later steps are given to it alone, for as long as
it is seen (it asks for work, sends heartbeats or sends logs) at least every 90 seconds, three
lease periods. A runner that shuts down (SIGTERM, a rollout, `kubectl delete pod`) hands its
steps back and tells the server it is leaving, and another runner takes the run over at once.
One that disappears without saying so (a crashed node) loses its steps after 30 seconds, and its
runs move after 90. A run that moves continues in a fresh pod with a fresh checkout: files that
its earlier steps wrote are not there, and the step's log says so. A run's parallel steps are
limited to one runner's `runner.concurrency`; different runs spread across runners, because a
runner that is already busy leaves a new run to one that is idle.

Runners connected from outside the cluster need only the server URL and the token.

### Image steps

Image steps are built on rootless BuildKit daemons you deploy apart from the step pods, in a
namespace of their own: nothing that builds a Dockerfile runs under Pod Security "restricted", and
the step namespace stays restricted. [`buildkit/README.md`](buildkit/README.md) has the evidence,
the exact relaxations the daemon needs (Pod Security "baseline": privilege escalation for
`newuidmap`, SETUID and SETGID, and a Localhost seccomp profile installed on the build nodes), the
manifest and the certificates. Then point the runners at it with `runner.images` (unset by default,
when runners take no image steps):

```yaml
runner:
  images:
    builders:
      - { name: shared, address: "tcp://buildkitd.launchway-build.svc:1234", serverName: buildkitd.launchway-build.svc }
    tlsSecret: buildkit-client          # ca.crt, tls.crt, tls.key: the runners' client certificate
    registries:
      harbor:
        host: harbor.example.com
        credentials: { username: "robot$ci", password: { store: vault, key: ci/harbor, field: password } }
        push: [ { projects: [shop], repositories: ["team/**"] } ]
    signingSecret: launchway-cosign     # optional: cosign.key (and cosign.password), for sign: true
```

The runner copies each image's layers through its data volume: raise `runner.dataVolume.sizeLimit`
for large images. The daemon's NetworkPolicy must let the runners' pods in (port 1234); the
runners must reach the registries.

### Exposing the UI

The Service is `ClusterIP`. Turn on `ingress.enabled` or `httpRoute.enabled` (Gateway API) to
expose it. The UI keeps a long-lived request open for live updates (`/api/v1/stream`): raise
your proxy's read timeout and turn off response buffering for it (the HA example shows the
annotations for ingress-nginx).

## Runners in another cluster

The `launchway-runner` chart runs only runners, connecting out to a Launchway server installed
elsewhere: another namespace, another cluster, a VM. It renders the runners exactly as the main
chart does: the runner file, the step namespace, the ServiceAccount, the Kubernetes executor's
Role and RoleBinding (pods, `pods/exec`, `pods/log`, in the step namespace only), the Deployment
and its PodDisruptionBudget, and optionally a deploy runner with its Argo CD RBAC and admission
policies. Both charts copy these templates from [`helm/shared`](helm/shared)
(`make charts-sync`), and a test fails if they differ or grant different rights.

```sh
kubectl create namespace launchway-runners
kubectl -n launchway-runners create secret generic launchway-runner-token --from-literal=token="$(cat runner-token)"
helm install runners deploy/helm/launchway-runner -n launchway-runners \
  -f deploy/helm/launchway-runner/examples/remote.yaml        # server.url, the Secret, a step namespace
```

| Value | |
|---|---|
| `server.url` (required) | the server's URL as the runners reach it |
| `token.existingSecret` | a Secret holding the runner token or a runner pool's token; required for build runners unless they enrol with `runner.join.existingSecret`; the chart never creates it |
| `runner.*` | the same as the main chart's `runner.*` (replicas, concurrency, join tokens, labels and `accept`, `kubernetes.namespace` and `createNamespace`, which labels the namespace Pod Security "restricted", resources, PDB, secret stores, image steps) |
| `deployRunner.*` | the same as the main chart's `delivery.deployRunner.*`, with the pool token in `deployRunner.token.existingSecret`; `runner.enabled: false` for a cluster that only receives deployments ([example](helm/launchway-runner/examples/deploy-runner.yaml)) |
| `verification.sources` | the same health-source connection entries as the main chart's `delivery.verification.sources`, given only to the deploy runner; register their names, types and projects on the servers too |
| `proxy.httpsProxy`, `httpProxy`, `noProxy` | a proxy for the runners' connections; its idle timeout must exceed 30 seconds (long polls last 25) |
| `caBundle.configMap`, `key` | a CA bundle replacing the image's (it must include the public CAs you need) |
| `networkPolicy.*` | NetworkPolicies, off by default: the runner pods may receive nothing and reach only DNS, the server, the Kubernetes API and `extraEgress`; `steps.enabled` adds one for the step namespace (DNS and `steps.egress` only). See [examples/networkpolicy.yaml](helm/launchway-runner/examples/networkpolicy.yaml) |
| `join.existingSecret` | the original join-token setting, kept as an alias of `runner.join.existingSecret`; setting both is refused |

`values.schema.json` rejects unknown and mistyped values. Runners are ready once the server has
answered (`/readyz`), so `helm install --wait` fails if they cannot reach it. `make
test-runner-chart` installs the main chart as the server in one namespace and this chart in
another on a kind cluster, and runs a pipeline through the remote runner.

## Network policies and sign-in behind a gateway

Learned from a real deployment with default-deny Cilium policies and the sign-in provider published
on the same cluster's Gateway. A server that reaches its OpenID Connect issuer through the gateway
needs egress rules for all three of these, or discovery fails with `403 Access denied`:

- the gateway itself (in Cilium, `toEntities: [ingress]`);
- the gateway Service's own address, which Cilium treats as an outside address
  (`toCIDR: [<gateway ClusterIP>/32]`);
- the provider's pods and port, because the gateway forwards the request with the caller's
  identity, so the caller's egress policy is also checked against the final backend.

Do not restrict these by the gateway's Service port. Runners need none of this: they talk to the
servers, the Kubernetes API and your Git host only.

## Upgrades

`helm upgrade` replaces servers one at a time: a new server must pass `/readyz` before an old one
stops (`maxUnavailable: 0`). A stopping server keeps answering for `server.preStopSleepSeconds`
(5 s) after it leaves the Service, before it is even told to stop, so that requests already on
their way to it succeed. Once it gets SIGTERM, its `/readyz` reports not ready, each connection
closes after its current request, runners waiting for work are told to ask again, and requests
in flight get up to 15 s to finish. If a load balancer outside Kubernetes checks `/readyz`, set
`config.extraConfig.server.drain_delay` (such as `10s`) so that the server keeps serving that long
after reporting not ready, and raise `server.terminationGracePeriodSeconds` to match. A
PodDisruptionBudget keeps node drains from taking more than one server (and one runner) at once.

A starting server waits up to a minute for the database (`config.extraConfig.database_wait`),
logging each try, so servers that start before the database do not crash and restart.

Database migrations run when a server starts. Servers take them in turn under a PostgreSQL
advisory lock, so exactly one applies each migration while the others wait; a startup probe allows
up to 3 minutes. `/readyz` and `/api/v1/info` report the schema the server knows (`schema.code`),
the database's (`schema.database`) and the oldest the database still accepts (`schema.minimum`).

**Rolling and canary upgrades, and going back.** During a rolling upgrade, or a canary with Argo
Rollouts or similar, old and new servers run side by side on the same database, and the new ones
migrate it as they start. Most migrations are *additive* (new tables, new columns with defaults),
and an old server keeps running against a database they have been applied to: it starts, logs
once that the database is newer, and carries on. So aborting a rollout, `kubectl rollout undo` or
`helm rollback` to the previous image works: the old pods start and serve. Runs that use something
only the newer version understands (a re-run, for example, when rolling back to a release before
re-runs existed) are shown as needing a newer server and left alone by the old servers; they carry
on once a newer server is back. Every other run is unaffected.

Upgrading to schema 17 moves artifacts into the database (with `config.artifacts.store` unset):
before it, they were kept in each server's `emptyDir` and only one server kept any. Older servers
next to newer ones keep none and leave runs with artifacts to the newer ones; to roll back with
artifacts working, copy them to S3 first. See
[docs/artifacts.md](../docs/artifacts.md#upgrading-to-schema-17-and-rolling-back).

After `kubectl rollout undo`, the next `helm upgrade` (Helm 4, server-side apply) fails with a
field-manager conflict on the image; pass `--force-conflicts`, or go back with `helm rollback`
instead, which keeps Helm the owner of the Deployments.

This holds between releases from schema 8 on. A release older than schema 8 refuses any newer
database, as before: rolling back across the upgrade to schema 8 itself needs the database restored
from a backup taken before it.

**Breaking migrations.** A release whose migration is *breaking* (its release notes say so) raises
the database's minimum: from then on, older servers refuse to start, saying which version they
need, and running ones report not ready. Old and new servers cannot overlap, so for that upgrade
scale the servers to zero first (`kubectl scale deployment <release>-launchway-server --replicas=0`),
then `helm upgrade`, and do not use a canary. Going back after it means restoring the database
from a backup taken before the upgrade. Take one before every upgrade anyway.

Runners being replaced stop their steps, which are then run again by another runner. Steps
are run again from the start, in a fresh workspace, so long steps are interrupted by runner
upgrades.

## Saving pipelines from the editor

The pipeline editor can commit to a project's repository if the project has a `push` section
(see the main README's *Editing pipelines*):

```yaml
config:
  projects:
    - name: payments
      repo: https://git.example.com/team/payments.git
      push: { branch_prefix: launchway/ }
server:
  extraEnv:
    # The servers' Git credential, used to fetch and to push. Keep the value in a Secret.
    - { name: GIT_CONFIG_COUNT, value: "1" }
    - { name: GIT_CONFIG_KEY_0, value: "http.https://git.example.com/.extraHeader" }
    - name: GIT_CONFIG_VALUE_0
      valueFrom: { secretKeyRef: { name: launchway-git, key: header } }
```

The servers then need a credential that may push, not only fetch. Give it as little as the host
allows (one repository, no admin rights) and protect important branches on the host itself.
Saves are made in temporary clones under the data directory (`server.dataVolume`), which are
removed after each save. Who may start draft runs, and whether drafts may read secrets, is
`drafts:` in `config.extraConfig` and per project. Whether the steps of pull-request runs may read secrets
(no, by default) is `pull_requests:` in `config.extraConfig` and per project.

## What is kept where

| Data | Where | Lost when |
|---|---|---|
| Runs, steps, events, logs, approvals, runner list, the runner token (unless you set one), the text of draft runs | PostgreSQL | Only with the database: back it up |
| Artifacts, and the SBOMs and provenance kept as artifacts | PostgreSQL, unless `config.artifacts.store` says S3 ([docs/artifacts.md](../docs/artifacts.md): limits, backups, when to move to S3) | Their retention ends, or with the database |
| Mirrors of project repositories | Each server's `emptyDir` (`server.dataVolume`) | The pod goes; rebuilt from the repository on start |
| Checkouts for running steps | Each runner's `emptyDir` (`runner.dataVolume`) | The pod goes; steps then run again elsewhere |
| A run's workspace | Its step pod in the step namespace | The run ends in any state (at once; `keepFailed` can add a grace period), or the pod is older than `maxPodAge` (24 hours) with no step executing; any runner removes it, including the pods of runners that were replaced |
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
- Step pods run as an ordinary user under Pod Security `restricted` unless you turn that off
  (see [Step pods and Pod Security](#step-pods-and-pod-security)); a pipeline cannot.
- Runners can manage pods in the step namespace only. Anyone who can run a pipeline can run any
  image there, so treat that namespace as untrusted: give it a ResourceQuota, a LimitRange and a
  NetworkPolicy suited to builds, and keep other workloads out of it.
- Runners have only the secret stores in `runner.secretStores`; in particular no `env` store,
  which would let every pipeline read the runner's environment, unless you define one.
- Use `database.existingSecret` rather than `database.url`, so the password is not in your
  values or Helm's release history.

## Known limitations

- Runners are named after their pod, so a replaced runner is a new runner. Runners that stop
  cleanly leave the runner list at once; others are listed as offline after two minutes and
  forgotten after a day.
- When a run moves to another runner (see above), files its earlier steps wrote are lost; the
  step's log says so. Values passed with `$LAUNCHWAY_OUTPUTS` are not affected.
- Step pods of runs the server does not know are removed only by the runner that made them,
  because they may belong to another installation or to a `launchway run` sharing the namespace.
  Keep the step namespace to one installation; such pods left by a runner that is gone must be
  deleted by hand (`kubectl -n <step namespace> get pods -l app.kubernetes.io/managed-by=launchway`).

## Testing the chart

[`test/README.md`](test/README.md) installs the chart on a kind cluster and checks a real
pipeline, failover of servers and runners, and a rolling upgrade. `make chart-lint` lints both
charts; `go test ./internal/config/` renders them and checks that they agree; `make
test-runner-chart` and `make test-bundle` install them on kind (see
[packaging/README.md](../packaging/README.md)).
