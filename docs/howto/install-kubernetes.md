# Install Launchway on Kubernetes

This guide runs Launchway as a highly available service on Kubernetes with the Helm chart in
`deploy/helm/launchway`: several servers sharing one PostgreSQL database, runners whose steps run
as pods in a namespace of their own, and the UI behind an Ingress or a Gateway. It is for
operators. For EKS and GKE specifics, see [Install on Amazon EKS](install-eks.md) and
[Install on Google GKE](install-gke.md).

> **Verified** on 2026-11-19 against commit f717337: `make image` (Podman) from a clean clone; the
> image loaded into a kind cluster (Kubernetes 1.37, kind 0.33 on rootless Podman, Helm 4.3);
> `helm install` with three servers, two runners and the development database; step pods in a
> namespace enforcing Pod Security `restricted`; the UI and API through `kubectl port-forward`; a
> pipeline whose two steps ran in one pod and shared a file; a server pod deleted while a
> 90-second step ran (the run finished, every step on attempt 1); `helm upgrade` to four servers
> with a configuration change while a pod requested `/readyz` five times a second (no failures)
> and a run went on; `helm rollback`; the "no database" refusal. The namespaces used were
> `docsk8s-ha` and `docsk8s-steps` rather than `launchway` and `launchway-steps`. **Not verified:**
> an external PostgreSQL (the chart was only run with its development database here; external
> PostgreSQL was used outside Kubernetes), an Ingress controller or Gateway serving the UI, sign-in
> through a real OpenID Connect provider on Kubernetes, pushing the image to a registry, Docker
> instead of Podman.

## What you need

- A Kubernetes cluster, version 1.30 or later, and `kubectl` for it.
- Helm 3.8 or later.
- Podman or Docker, to build the image.
- A container registry the cluster can pull from. For a local kind cluster you load the image
  into the cluster instead.
- For real use: a PostgreSQL database (a managed service or one run by an operator), and an
  OpenID Connect provider for sign-in. See [Sign-in with OpenID Connect and roles](oidc-and-roles.md).
- A Git repository with a `.launchway/pipeline.yaml` that the servers and the runners can clone.

## How it fits together

The chart deploys two Deployments in the release namespace. **Servers** run the API, the web UI
and the run engine; they share one PostgreSQL database and keep nothing that matters on disk, so
any of them can be lost at any time. **Runners** run `launchway runner` with the Kubernetes
[executor](../glossary.md#executor): each run gets a pod in the step namespace, and the run's steps
execute inside it. Runners only make outbound connections: to the servers, to the Kubernetes API
and to your Git host.

```mermaid
flowchart LR
  user["People and scripts"] --> lb["Ingress or Gateway"]
  lb --> svc["Service launchway"]
  subgraph ns["Namespace launchway"]
    svc --> s1["Server"]
    svc --> s2["Server"]
    svc --> s3["Server"]
    r1["Runner"] --> svc
    r2["Runner"] --> svc
  end
  s1 & s2 & s3 --> db[("PostgreSQL")]
  s1 & s2 & s3 --> obj[("Artifact store (S3)")]
  s1 & s2 & s3 --> git["Git host"]
  r1 & r2 --> git
  r1 & r2 --> api["Kubernetes API"]
  api --> pods
  subgraph steps["Namespace launchway-steps (restricted)"]
    pods["Step pods, one per run"]
  end
```

All of a run's steps go to the runner that took its first step, because they share the workspace in
that run's pod. If that runner goes away, another runner takes the run over in a fresh pod.

## Build the image

From a clone of the repository:

```sh
git clone https://github.com/jk-magic-creations/launchway.git
cd launchway
make image IMAGE=localhost/launchway:dev
```

`make image` uses Podman; add `CONTAINER=docker` for Docker. The image holds one static binary
(server and runner), Git and CA certificates, and nothing else: no shell and no package manager. It
runs as user 65532.

For a real cluster, tag and push it to your registry, and set `image.repository` and `image.tag`
(or `image.digest`) to match:

```sh
podman tag localhost/launchway:dev registry.example.com/launchway:0.1.0
podman push registry.example.com/launchway:0.1.0
```

### Load the image into kind

To try the chart on a local [kind](https://kind.sigs.k8s.io/) cluster, load the image into the
cluster's node instead of pushing it. With kind on Podman, save the image to an archive and load
that (here the cluster is called `launchway-test`):

```sh
podman save -o launchway-dev.tar localhost/launchway:dev
KIND_EXPERIMENTAL_PROVIDER=podman kind load image-archive launchway-dev.tar --name launchway-test
```

With kind on Docker, `kind load docker-image localhost/launchway:dev --name launchway-test` does
the same. Set `image.pullPolicy: Never` in the values so that the cluster never tries to pull it.

## Prepare the namespaces

Create the release namespace and a separate namespace for step pods. Label the step namespace so
that it enforces Pod Security `restricted`. Step pods meet it by default: every container runs as
user 1000, with no privileges and no capabilities.

```sh
kubectl create namespace launchway
kubectl create namespace launchway-steps
kubectl label namespace launchway-steps pod-security.kubernetes.io/enforce=restricted
```

A namespace of its own lets you give builds their own ResourceQuota, LimitRange and NetworkPolicy.
Anyone who can run a pipeline can run any image there, so treat it as untrusted and keep other
workloads out of it.

> [!NOTE]
> The chart can create the step namespace for you with `runner.kubernetes.createNamespace=true`.
> It then deletes the namespace, with any step pods in it, on `helm uninstall`.

## A Git repository the cluster can reach

Every project's `repo` must be a URL that both the servers and the runners can clone, such as
`https://git.example.com/team/payments.git`. For a quick trial without a Git host, the repository's
test setup serves a small repository inside the cluster. It needs Podman on your machine to build
the repository:

```sh
deploy/test/make-test-repo.sh /tmp/launchway-repo
kubectl -n launchway create configmap demo-repo --from-file=/tmp/launchway-repo/demo-repo.tar.gz
kubectl -n launchway apply -f deploy/test/git-server.yaml
kubectl -n launchway rollout status deploy/git-server
```

The repository is then at `http://git-server.launchway.svc:8080/demo.git`. Its `main` branch has a
quick two-step pipeline, and its `slow` branch has a step that takes 90 seconds, which is handy for
watching a failover. This is for testing only.

## Try it with the development database

Write a values file. This one uses the image loaded into kind and the chart's single development
PostgreSQL:

```yaml
# try.yaml
image:
  repository: localhost/launchway
  tag: dev
  pullPolicy: Never          # the image was loaded into the cluster, not pushed

postgresql:
  enabled: true              # a single development database: for trying only

config:
  extraConfig:
    auth:
      allow_anonymous: true  # no sign-in: only for a cluster nobody else can reach
  projects:
    - name: demo
      repo: http://git-server.launchway.svc:8080/demo.git
      branch: main
      poll: 10s

server:
  replicas: 3

runner:
  replicas: 2
  kubernetes:
    namespace: launchway-steps
```

> [!IMPORTANT]
> A server without sign-in refuses to listen on anything but a loopback address, and in a pod it
> listens on `0.0.0.0:8080`. So without either `auth.mode: oidc` or `auth.allow_anonymous: true`
> the server pods fail to start, with `refusing to listen on 0.0.0.0:8080 without sign-in` in their
> logs. `allow_anonymous` gives everyone who can reach the Service full rights; use it only on a
> throwaway cluster.

Install the chart:

```sh
helm install launchway deploy/helm/launchway -n launchway -f try.yaml --wait --timeout 5m
kubectl -n launchway get pods
```

```text
NAME                                    READY   STATUS    RESTARTS   AGE
git-server-79bd9d5478-hqlqj             1/1     Running   0          72s
launchway-postgresql-5f8df89786-n4wxq   1/1     Running   0          64s
launchway-runner-68b4f8f7bd-cnkks       1/1     Running   0          64s
launchway-runner-68b4f8f7bd-m25hz       1/1     Running   0          64s
launchway-server-67bff96d4d-9bg9r       1/1     Running   0          64s
launchway-server-67bff96d4d-9kjq2       1/1     Running   0          64s
launchway-server-67bff96d4d-xkdbm       1/1     Running   0          64s
```

The servers wait for the database while it starts (each try is logged), so they should not restart.
Without any database setting the install is refused with an explanation, because each server would
otherwise keep its own SQLite file.

## Open the UI and check the runners

```sh
kubectl -n launchway port-forward service/launchway 8080:8080
```

Browse to <http://127.0.0.1:8080/>. In another terminal, check that the servers are ready and the
runners have connected:

```sh
curl -s http://127.0.0.1:8080/readyz
curl -s http://127.0.0.1:8080/api/v1/runners
```

```text
{"schema":{"code":16,"database":16,"minimum":7},"status":"ok"}
[{"id":"runner-launchway-runner-68b4f8f7bd-cnkks","name":"launchway-runner-68b4f8f7bd-cnkks","executor":"kubernetes (launchway-steps)",...,"online":true},
 {"id":"runner-launchway-runner-68b4f8f7bd-m25hz",...,"online":true}]
```

With sign-in turned on, `/api/v1/runners` needs a signed-in user or an API token; the **Runners**
page in the UI shows the same list.

## Run a pipeline as pods

Open **Projects → demo → Start a run** in the UI, or start one over the API. Without sign-in, a
request that changes something must carry the header `X-Launchway-CSRF: 1`:

```sh
curl -s -X POST -H 'X-Launchway-CSRF: 1' -d '{"ref":"main"}' \
  http://127.0.0.1:8080/api/v1/projects/demo/runs
kubectl -n launchway-steps get pods
```

```text
NAME                                            READY   STATUS    RESTARTS   AGE
launchway-run-06gzdcyzh6hj0a2z1g40-a6018a1a50   2/2     Running   0          5s
```

The run's pod holds a helper container with the workspace and the step's container. The second
step's log shows that it ran in the same pod and read the file the first step wrote:

```text
Runner launchway-runner-68b4f8f7bd-cnkks, executor kubernetes (launchway-steps)
Using the run's workspace from an earlier step
Image docker.io/library/busybox:1.37
written by hello in pod launchway-run-06gzdcyzh6hj0a2z1g40-a6018a1a50
output and file received; running in pod launchway-run-06gzdcyzh6hj0a2z1g40-a6018a1a50
```

The pod is removed once the run succeeds. A failed run's pod is kept for `runner.kubernetes.keepFailed`
(2 hours by default) so that a resume can reuse its workspace.

> [!NOTE]
> The command line (`launchway runs start`) needs an API token, and tokens can only be made by
> someone signed in. On a server without sign-in, use the UI or `curl` as above.

## Lose a server while a run goes on

Start a run of the `slow` branch, and while its long step runs, delete a server pod:

```sh
curl -s -X POST -H 'X-Launchway-CSRF: 1' -d '{"ref":"slow"}' \
  http://127.0.0.1:8080/api/v1/projects/demo/runs
kubectl -n launchway delete pod "$(kubectl -n launchway get pods \
  -l app.kubernetes.io/component=server -o name | head -1 | cut -d/ -f2)"
```

The run carries on and succeeds, with every step on its first attempt: the runner holding the step
simply talks to another server, and the run's state is in the database, not in the server that was
lost. A new server pod replaces the deleted one.

`kubectl port-forward` to a Service connects to one pod and stops when that pod goes away
(`error: lost connection to pod`). If that happens, start it again. A real load balancer or Ingress
moves to the other servers by itself.

## Scale the servers

Change `server.replicas` in your values file and upgrade, or set it on the command line:

```sh
helm upgrade launchway deploy/helm/launchway -n launchway -f try.yaml --set server.replicas=4 --wait
```

Servers have no leader and no state of their own, so any number can run. Two is the least for high
availability; spread them across zones with `server.topologySpreadConstraints` (see
`deploy/helm/launchway/examples/ha.yaml`). Runners scale the same way with `runner.replicas`, and
each runs `runner.concurrency` steps at once.

## Use your own PostgreSQL

For real use, give every server the same PostgreSQL database. Put its URL in a Secret, so the
password is neither in your values nor in Helm's release history:

```sh
kubectl -n launchway create secret generic launchway-database \
  --from-literal=url='postgres://launchway:PASSWORD@db.internal:5432/launchway?sslmode=verify-full'
```

Then, instead of `postgresql.enabled`, set:

```yaml
database:
  existingSecret: launchway-database
  existingSecretKey: url
```

Exactly one of `database.existingSecret`, `database.url` and `postgresql.enabled` must be set. The
database user needs to create tables in the database, because servers apply schema migrations when
they start (see [Back up, restore and upgrade](backup-restore-upgrade.md#upgrades)). The
`deploy/helm/launchway/examples/ha.yaml` file is a production-shaped starting point: three servers
across zones, an external database, runners in their own namespace and an Ingress with TLS.

Keep artifacts somewhere all servers share, too. Without `config.artifacts.store: s3`, each server
keeps artifacts in its own `emptyDir`, which is lost with the pod and not shared:

```yaml
config:
  artifacts:
    store: s3
    s3:
      endpoint: https://s3.eu-west-1.amazonaws.com
      region: eu-west-1
      bucket: acme-launchway-artifacts
      existingSecret: launchway-s3   # AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY
```

Any S3-compatible store works this way, such as MinIO or Ceph.

## Expose the UI

The Service is `ClusterIP`. Turn on an Ingress:

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    # The UI keeps /api/v1/stream open for live updates.
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
  hosts: [launchway.example.com]
  tls:
    - secretName: launchway-tls
      hosts: [launchway.example.com]
```

or an HTTPRoute on a Gateway (Gateway API `gateway.networking.k8s.io/v1`):

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: public
      namespace: gateway-system
      sectionName: https
  hostnames: [launchway.example.com]
```

Whichever you use, raise the proxy's read timeout and turn off response buffering for
`/api/v1/stream`, and set `config.extraConfig.server.trust_proxy: true` so that Launchway takes the
client's address and the scheme from the proxy. Do not expose Launchway before sign-in is on.

## Turn on sign-in

Sign-in uses your OpenID Connect provider. The client secret reaches the servers as an environment
variable from a Secret; the rest goes in `config.extraConfig`:

```sh
kubectl -n launchway create secret generic launchway-oidc --from-literal=client-secret=CLIENT_SECRET
```

```yaml
config:
  extraConfig:
    server:
      trust_proxy: true
    auth:
      mode: oidc
      oidc:
        issuer: https://sso.example.com/realms/acme
        client_id: launchway
        client_secret_env: LAUNCHWAY_OIDC_SECRET
        redirect_url: https://launchway.example.com/auth/callback
      access:
        - { group: platform-admins, role: admin }
        - { group: everyone, role: viewer }

server:
  extraEnv:
    - name: LAUNCHWAY_OIDC_SECRET
      valueFrom:
        secretKeyRef: { name: launchway-oidc, key: client-secret }
```

Remove `auth.allow_anonymous` when you add this. `access` must have at least one binding, or the
servers refuse to start. A server starts even if the provider cannot be reached yet; sign-in answers
`502` and logs why until it can. Roles and bindings are explained in
[Sign-in with OpenID Connect and roles](oidc-and-roles.md).

## Runners and step pods

The runners' settings are under `runner.*` and are rendered into
`/etc/launchway-runner/runner.yaml` in each runner pod. The ones you are most likely to change:

```yaml
runner:
  replicas: 3
  concurrency: 4
  kubernetes:
    namespace: launchway-steps
    nodeSelector: { pool: builds }          # where step pods run
    cpu: 250m                               # request per container, unless the pipeline asks
    memory: 256Mi
    memoryLimit: 4Gi
    imagePullSecrets: [registry-credentials] # Secrets in launchway-steps
    keepFailed: 2h
  secretStores:
    vault:
      type: vault
      address: https://vault.example.com:8200
      role: launchway-runner                # Vault Kubernetes auth with the runner pods' token
      keys: ["ci/*"]
```

The chart gives the runners' ServiceAccount a Role in the step namespace only: `pods` (create, get,
list, delete, patch), `pods/exec` (create, get) and `pods/log` (get). Step pods get no ServiceAccount
token unless you name one in `runner.kubernetes.serviceAccount`.

Steps that need root, such as `apt-get install`, fail under `restricted`; use images that already
have their tools. To run images as their own users instead, set `runner.kubernetes.restricted:
false` and label the step namespace Pod Security `baseline`. A pipeline cannot change this. Step
pods take no tolerations, so a node pool for builds is chosen with `nodeSelector` alone and must not
be tainted.

Image steps (`kind: image`) need BuildKit daemons deployed apart from the step pods; see
`deploy/buildkit/README.md` and `runner.images` in the values. Runners elsewhere (another cluster or
a machine) connect with the runner token; see [Connect a runner](connect-runner.md).

## Deploy runner

Deploy steps (`kind: deploy`) are done by a [deploy runner](../glossary.md#deploy-runner) with a
runner pool token of its own, never by the build runners. The chart can run one in the cluster that
Argo CD manages:

```yaml
delivery:
  environments:
    - { name: prod, target: prod-cluster, projects: [payments] }
  deployRunner:
    enabled: true
    target: prod-cluster
    argocdNamespace: argocd
    applications:
      - { projects: [payments], names: [payments] }
    rolloutNamespaces: [payments]
    repos:
      gitops:
        url: https://git.example.com/team/gitops.git
        credentials: { store: vault, key: ci/gitops, field: token }
        paths: [ { projects: [payments], files: ["apps/payments/**"] } ]
    secretStores:
      vault: { type: vault, address: https://vault.example.com:8200, role: launchway-deploy }
```

The chart generates the pool token and gives it to the servers, and grants the deploy runner only
what it needs on the named Applications and rollouts. Set these values only once every server and
runner runs a release that knows them. The [deploy tutorial](../tutorials/deploy-with-approval.md)
walks through a deployment, and [Helm values](../reference/helm-values.md) lists every value.

## Upgrades

`helm upgrade` replaces servers one at a time: a new server must be ready before an old one stops,
and a stopping server keeps serving for a few seconds after it leaves the Service. A run in progress
is not affected by servers being replaced. Runners being replaced hand their steps back, and the
steps run again from the start on another runner.

```sh
helm upgrade launchway deploy/helm/launchway -n launchway -f try.yaml --set image.tag=0.2.0 --wait
helm history launchway -n launchway
helm rollback launchway 1 -n launchway --wait
```

Servers apply database migrations as they start, one server at a time. Read
[Back up, restore and upgrade](backup-restore-upgrade.md#upgrades) before upgrading: it covers
schema versions, rolling back, and the settings to add only once every server is upgraded. Take a
database backup before every upgrade.

## Remove it

```sh
helm uninstall launchway -n launchway --wait
kubectl delete namespace launchway launchway-steps
```

`helm uninstall` deletes the development database and its volume with it.

## What next

- [Connect a runner](connect-runner.md) on another machine or in another cluster.
- [Back up, restore and upgrade](backup-restore-upgrade.md).
- [Sign-in with OpenID Connect and roles](oidc-and-roles.md) and [Secrets](secrets.md).
- [Helm values](../reference/helm-values.md) for every setting, and `deploy/README.md` for network
  policies and what is kept where.
