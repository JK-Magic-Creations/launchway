# Install Launchway on Kubernetes

This guide runs Launchway as a highly available service on Kubernetes with the Helm chart in
`deploy/helm/launchway`: several servers sharing one PostgreSQL database, Stations (runners) whose steps run
as pods in a namespace of their own, and the UI behind an Ingress or a Gateway. It is for
operators. For EKS and GKE specifics, see [Install on Amazon EKS](install-eks.md) and
[Install on Google GKE](install-gke.md).

> **Verified** on 2026-11-19 against commit f717337: `make image` (Podman) from a clean clone; the
> image loaded into a kind cluster (Kubernetes 1.37, kind 0.33 on rootless Podman, Helm 4.3);
> `helm install` with three servers, two Stations and the bundled database; step pods in a
> namespace enforcing Pod Security `restricted`; the UI and API through `kubectl port-forward`; a
> pipeline whose two steps ran in one pod and shared a file; a server pod deleted while a
> 90-second step ran (the Sortie (run) finished, every step on attempt 1); `helm upgrade` to four servers
> with a configuration change while a pod requested `/readyz` five times a second (no failures)
> and a Sortie went on; `helm rollback`; the "no database" refusal. The namespaces used were
> `docsk8s-ha` and `docsk8s-steps` rather than `launchway` and `launchway-steps`. **Not verified:**
> an external PostgreSQL (the chart was only run with its development database here; external
> PostgreSQL was used outside Kubernetes), an Ingress controller or Gateway serving the UI, sign-in
> through a real OpenID Connect provider on Kubernetes, pushing the image to a Depot (registry), Docker
> instead of Podman.

## What you need

- A Kubernetes cluster, version 1.30 or later, and `kubectl` for it.
- Helm 3.8 or later.
- Podman or Docker, to build the image.
- A container registry the cluster can pull from. For a local kind cluster you load the image
  into the cluster instead.
- For real use: a PostgreSQL database (a managed service or one run by an operator), and an
  OpenID Connect provider for sign-in. See [Sign-in with OpenID Connect and roles](oidc-and-roles.md).
- A Git repository with a `.launchway/pipeline.yaml` that the servers and the Stations can clone.

## How it fits together

The chart deploys two Deployments in the release namespace. **Servers** run the API, the web UI
and the Sortie engine; they share one PostgreSQL database and keep nothing that matters on disk, so
any of them can be lost at any time. **Stations** run `launchway runner` with the Kubernetes
[executor](../glossary.md#executor): each Sortie gets a pod in the step namespace, and the Sortie's steps
execute inside it. Stations only make outbound connections: to the servers, to the Kubernetes API
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
  s1 & s2 & s3 --> obj[("Optional: S3 artifact store")]
  s1 & s2 & s3 --> git["Git host"]
  r1 & r2 --> git
  r1 & r2 --> api["Kubernetes API"]
  api --> pods
  subgraph steps["Namespace launchway-steps (restricted)"]
    pods["Step pods, one per run"]
  end
```

All of a Sortie's steps go to the Station that took its first step, because they share the workspace in
that run's pod. If that Station goes away, another Station takes the run over in a fresh pod.

## Build the image

Releases publish the image as `ghcr.io/jk-magic-creations/launchway` (the chart's default, with
the chart's version as the tag) and the chart itself as `launchway-VERSION.tgz`, both signed (see
[Verifying downloads](../security/verify-downloads.md)). With a release, skip this section and
install the chart archive, `helm install launchway
https://github.com/JK-Magic-Creations/launchway/releases/download/v1.4.0/launchway-1.4.0.tgz ...`,
in place of `deploy/helm/launchway` below. No release has been published yet, so this guide builds
the image from a clone of the repository:

```sh
git clone https://github.com/jk-magic-creations/launchway.git
cd launchway
make image IMAGE=localhost/launchway:dev
```

`make image` uses Podman; add `CONTAINER=docker` for Docker. The image holds one static binary
(server and Station), Git and CA certificates, and nothing else: no shell and no package manager. It
runs as user 65532.

For a real cluster, tag and push it to your Depot, and set `image.repository` and `image.tag`
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

Every Flight's `repo` must be a URL that both the servers and the Stations can clone, such as
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

## Install the chart

The only required value is the address people will open. Write a values file; this one uses the
image loaded into kind and the chart's bundled PostgreSQL (the default):

```yaml
# try.yaml
externalURL: http://launchway.test   # the address you will open; with no Ingress yet, port-forward to it

image:
  repository: localhost/launchway
  tag: dev
  pullPolicy: Never          # the image was loaded into the cluster, not pushed

server:
  replicas: 3

runner:
  replicas: 2
  kubernetes:
    namespace: launchway-steps
```

Sign-in is required from the first moment: the chart's default is built-in accounts, and a one-time
setup code makes the first administrator (below). Nothing else needs setting; Flights (codebase), Git hosting
and Stations are set up in the browser.

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

Before the servers start, a hook Job (`launchway bootstrap-secrets`) makes the Secrets the installation
needs, only if they do not exist: the root key, the Station token, the database password and the setup
code. It never changes an existing one, so `helm upgrade`, `helm template` and a GitOps controller
that renders the chart leave them alone. Read the setup code to create the first administrator:

```sh
kubectl -n launchway get secret launchway-launchway-setup-code -o jsonpath='{.data.code}' | base64 -d; echo
```

> [!IMPORTANT]
> The chart applies default-deny NetworkPolicies. They take effect where your cluster's network
> plugin enforces them. The servers may reach DNS and the database only; add your Git hosts and
> identity provider to `networkPolicy.server.extraEgress`, and Git hosts the Stations clone to
> `networkPolicy.runner.extraEgress`. Step pods may reach DNS and the Launchway server only; the
> internet over HTTP and HTTPS for builds that download dependencies is an opt-in
> (`networkPolicy.steps.internet: true`), and never the database, the Kubernetes API or the cloud
> metadata service. Behind an Ingress, name the proxy's namespace in
> `networkPolicy.server.proxyNamespaces`. See [network policies](../reference/helm-values.md#network-policies).
>
> **If the cluster already has network policy**, `launchway install` says so and leaves these off. It looks
> in your namespace for Kubernetes NetworkPolicies that are not Launchway's own, and for any
> `*NetworkPolicy` kind of a non-core API group (a network plugin's own objects: CiliumNetworkPolicy,
> Calico's GlobalNetworkPolicy and the like, found by listing the cluster's API, not from a fixed list). On a plugin whose own policies **add** to Kubernetes ones, adding the
> chart's as well can widen access, and objects stay behind when they are turned off, so one system should be the
> authority. The installer asks (default: leave Launchway's off, `networkPolicy.enabled: false`); answer
> `--set kubernetes.network_policies=yes` to add them anyway. `launchway doctor network --namespace launchway`
> runs the same check on an installation that exists.

The servers wait for the database while it starts (each try is logged), so they should not restart.
With the bundled database switched off (`postgresql.enabled=false`) and none of your own named, the
install is refused with an explanation, because each server would otherwise keep its own SQLite file.

## Open the UI and check the Stations {#open-the-ui-and-check-the-runners}

```sh
kubectl -n launchway port-forward service/launchway 8080:8080
```

Browse to <http://127.0.0.1:8080/>. In another terminal, check that the servers are ready and the
Stations have connected:

```sh
curl -s http://127.0.0.1:8080/readyz
curl -s http://127.0.0.1:8080/api/v1/runners
```

```text
{"schema":{"code":21,"database":21,"minimum":7},"status":"ok"}
[{"id":"runner-launchway-runner-68b4f8f7bd-cnkks","name":"launchway-runner-68b4f8f7bd-cnkks","executor":"kubernetes (launchway-steps)",...,"online":true},
 {"id":"runner-launchway-runner-68b4f8f7bd-m25hz",...,"online":true}]
```

With sign-in turned on, `/api/v1/runners` needs a signed-in user or an API token; the **Stations**
page in the UI shows the same list.

## Run a pipeline as pods

Open **Flights → demo → Start a Sortie** in the UI, or start one over the API. Without sign-in, a
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

The Sortie's pod holds a helper container with the workspace and the step's container. The second
step's log shows that it ran in the same pod and read the file the first step wrote:

```text
Runner launchway-runner-68b4f8f7bd-cnkks, executor kubernetes (launchway-steps)
Using the run's workspace from an earlier step
Image docker.io/library/busybox:1.37
written by hello in pod launchway-run-06gzdcyzh6hj0a2z1g40-a6018a1a50
output and file received; running in pod launchway-run-06gzdcyzh6hj0a2z1g40-a6018a1a50
```

The pod is removed when the Sortie ends, whether it succeeded, failed (at any step, including an image
step), was canceled or timed out, because a running pod holds the step namespace's quota. A resume
makes a new pod from the Station's checkout of the Sortie; see
[What happens to a Sortie's pod](../reference/runner-config.md#what-happens-to-a-runs-pod).

> [!NOTE]
> The command line (`launchway runs start`) needs an API token, and tokens can only be made by
> someone signed in. On a server without sign-in, use the UI or `curl` as above.

## Lose a server while a Sortie goes on {#lose-a-server-while-a-run-goes-on}

Start a run of the `slow` branch, and while its long step runs, delete a server pod:

```sh
curl -s -X POST -H 'X-Launchway-CSRF: 1' -d '{"ref":"slow"}' \
  http://127.0.0.1:8080/api/v1/projects/demo/runs
kubectl -n launchway delete pod "$(kubectl -n launchway get pods \
  -l app.kubernetes.io/component=server -o name | head -1 | cut -d/ -f2)"
```

The Sortie carries on and succeeds, with every step on its first attempt: the Station holding the step
simply talks to another server, and the Sortie's state is in the database, not in the server that was
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
`deploy/helm/launchway/examples/ha.yaml`). Stations scale the same way with `runner.replicas`, and
each runs `runner.concurrency` steps at once.

## Use your own PostgreSQL

For real use, give every server the same PostgreSQL database. Put its URL in a Secret, so the
password is neither in your values nor in Helm's release history:

```sh
kubectl -n launchway create secret generic launchway-database \
  --from-literal=url='postgres://launchway:PASSWORD@db.internal:5432/launchway?sslmode=verify-full'
```

Then set (this also switches the bundled database off):

```yaml
database:
  existingSecret: launchway-database
  existingSecretKey: url
```

Set only one of `database.existingSecret` and `database.url`. The
database user needs to create tables in the database, because servers apply schema migrations when
they start (see [Back up, restore and upgrade](backup-restore-upgrade.md#upgrades)). The
`deploy/helm/launchway/examples/ha.yaml` file is a production-shaped starting point: three servers
across zones, an external database, Stations in their own namespace and an Ingress with TLS.

Cargo (artifacts) need nothing more with PostgreSQL: unset, `config.artifacts.store` makes the servers keep
them in the database, in chunks every server reads, so any number of servers share them (see
[Where Cargo are kept](../artifacts.md), including when to move to S3). For very large or busy
installations, keep them in an S3-compatible store instead:

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

## Turn on sign-in with an identity provider

The chart's default is built-in accounts (`auth.mode: local`), with the first administrator made
from the one-time setup code. To use your OpenID Connect provider instead, or to connect it later
from the first-run guide in the browser, set `mode: oidc`. When an Ingress or HTTPRoute is enabled the
chart already sets `server.trust_proxy`. Sign-in uses your OpenID Connect provider. The client secret reaches the servers as an Airfield (environment)
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

`access` must have at least one binding, or the
servers refuse to start. A server starts even if the provider cannot be reached yet; sign-in answers
`502` and logs why until it can. Roles and bindings are explained in
[Sign-in with OpenID Connect and roles](oidc-and-roles.md).

## Stations and step pods {#runners-and-step-pods}

The Stations' settings are under `runner.*` and are rendered into
`/etc/launchway-runner/runner.yaml` in each Station pod. The ones you are most likely to change:

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
    keepFailed: 0s                          # a grace period for a failed run's pod; 0 removes it at once
  secretStores:
    vault:
      type: vault
      address: https://vault.example.com:8200
      role: launchway-runner                # Vault Kubernetes auth with the runner pods' token
      keys: ["ci/*"]
```

The chart gives the Stations' ServiceAccount a Role in the step namespace only: `pods` (create, get,
list, delete, patch), `pods/exec` (create, get) and `pods/log` (get). Step pods get no ServiceAccount
token unless you name one in `runner.kubernetes.serviceAccount`.

Steps that need root, such as `apt-get install`, fail under `restricted`; use images that already
have their tools. To run images as their own users instead, set `runner.kubernetes.restricted:
false` and label the step namespace Pod Security `baseline`. A pipeline cannot change this. Step
pods take no tolerations, so a node pool for builds is chosen with `nodeSelector` alone and must not
be tainted.

Image steps (`kind: image`) need BuildKit daemons deployed apart from the step pods; see
`deploy/buildkit/README.md` and `runner.images` in the values. Stations elsewhere (another cluster or
a machine) connect with a join token or the Station token; in another cluster, install only Stations
with the runner-only chart `deploy/helm/launchway-runner` (released as
`launchway-runner-VERSION.tgz`). See [Connect a Station](connect-runner.md).

## Deploy Station {#deploy-runner}

Deploy steps (`kind: deploy`) are done by a [deploy Station](../glossary.md#deploy-runner) with a
Ramp (Station (runner) pool) token of its own, never by the build Stations. The chart can run one in the cluster that
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

The chart generates the pool token and gives it to the servers, and grants the deploy Station only
what it needs on the named Applications and rollouts. Set these values only once every server and
Station runs a release that knows them. The [deploy tutorial](../tutorials/deploy-with-approval.md)
walks through a deployment, and [Helm values](../reference/helm-values.md) lists every value.

## Upgrades

`helm upgrade` replaces servers one at a time: a new server must be ready before an old one stops,
and a stopping server keeps serving for a few seconds after it leaves the Service. A run in progress
is not affected by servers being replaced. Stations being replaced hand their steps back, and the
steps run again from the start on another Station.

```sh
helm upgrade launchway deploy/helm/launchway -n launchway -f try.yaml --set image.tag=0.2.0 --wait
helm history launchway -n launchway
helm rollback launchway 1 -n launchway --wait
```

Servers apply database migrations as they start, one server at a time. Read
[Back up, restore and upgrade](backup-restore-upgrade.md#upgrades) before upgrading: it covers
schema versions, returning to base, and the settings to add only once every server is upgraded. Take a
database backup before every upgrade.

## Remove it

```sh
helm uninstall launchway -n launchway --wait
kubectl delete namespace launchway launchway-steps
```

`helm uninstall` deletes the bundled database's volume with it. It does **not** delete the Secrets the
install hook Job made: the root key (`launchway-launchway-root-key`), the Station token, the database
password and the setup code are not owned by Helm, on purpose, because the root key is the only way
to read the installation's secrets again. Copy the root key somewhere safe, and delete these Secrets
only when you mean to destroy the installation's data:

```sh
kubectl -n launchway get secret launchway-launchway-root-key -o jsonpath='{.data.root\.key}' | base64 -d > root.key
kubectl -n launchway delete secret -l app.kubernetes.io/part-of=launchway,app.kubernetes.io/component=secrets
kubectl -n launchway delete serviceaccount,role,rolebinding,networkpolicy -l app.kubernetes.io/component=bootstrap
```

## What next

- [Connect a Station](connect-runner.md) on another machine or in another cluster.
- [Back up, restore and upgrade](backup-restore-upgrade.md).
- [Sign-in with OpenID Connect and roles](oidc-and-roles.md) and [Secrets](secrets.md).
- [Helm values](../reference/helm-values.md) for every setting, and `deploy/README.md` for network
  policies and what is kept where.
