<!--
How this page is checked (read before editing).

Each table marked with the HTML comment "keys: helm" (written as a comment on the line before
the table) lists chart values: its rows start with the value's full path in a code span, such
as `server.replicas`. The Go test internal/config/docs_reference_test.go reads
deploy/helm/launchway/values.yaml and values.schema.json and fails if a value has no row here,
or a row names a value the chart does not have. Values the chart hands to Kubernetes as they are
(resources, probes, namespaceLabels, podSecurityContext, securityContext) are one row each.

A ```yaml block preceded by a line with the HTML comment "validate-helm" is rendered with
`helm template` by the same test (skipped when helm is not installed), and every configuration
file the chart renders from it must load.
-->

# Helm values reference

This page lists every value of the Launchway Helm chart, `deploy/helm/launchway`, in the order
`values.yaml` writes them: path, type, default and meaning. It is for operators who install
Launchway on Kubernetes; for a walk-through, see [Install on Kubernetes](../howto/install-kubernetes.md).

The chart runs several servers against one PostgreSQL database, and runners that run each
[run](../glossary.md#run) in a pod of a step namespace. It renders the
[server configuration](server-config.md) into a ConfigMap from `config.*`, and the runners'
[configuration file](runner-config.md) from `runner.*`. The database URL and the runner token
reach the pods as environment variables from Secrets, never through a ConfigMap. Changing a
ConfigMap restarts the pods.

**The file the chart renders is bootstrap only** (see [what stays in the file](server-config.md#what-stays-in-the-file-and-what-moved)).
With the default values it holds `listen`, `data` and the runner switch. A value that sets something
that now lives in the database (projects, libraries, environments, runner pools, policy, health
sources, models, limits, the MCP endpoint, where artifacts are kept) is **imported once**, by the first
server of a new installation to start, and ignored afterwards: each server logs the keys it ignores.
After that, change those settings in the web UI, with `launchway config`, the API or Terraform.
`values.schema.json` checks the types and ranges below when you install
or upgrade.

## Required values

One of these is required, or the install fails with a message saying so:

- `database.existingSecret` (with `database.existingSecretKey`): a Secret holding a `postgres://` URL;
- `database.url`: a `postgres://` URL, stored in a Secret the chart makes;
- `postgresql.enabled: true`: a development database, for trying the chart only.

Only one of them may be set.

<!-- validate-helm -->
```yaml
postgresql:
  enabled: true
```

A production-shaped example (see also `deploy/helm/launchway/examples/ha.yaml`):

<!-- validate-helm -->
```yaml
database:
  existingSecret: launchway-database
image:
  tag: "1.0.0"
config:
  projects:
    - name: payments
      repo: https://git.example.com/team/payments.git
      branch: main
      poll: 30s
  artifacts:
    store: s3
    s3:
      endpoint: https://s3.eu-west-1.amazonaws.com
      region: eu-west-1
      bucket: launchway-artifacts
      existingSecret: launchway-s3
  extraConfig:
    approvals: { allow_self: false }
server:
  replicas: 3
ingress:
  enabled: true
  className: nginx
  hosts: [launchway.example.com]
  tls:
    - secretName: launchway-tls
      hosts: [launchway.example.com]
runner:
  replicas: 3
  concurrency: 4
  kubernetes:
    namespace: launchway-steps
    createNamespace: true
  secretStores:
    vault: { type: vault, address: "https://vault.example.com:8200", role: launchway }
```

## General

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `nameOverride` | string | `""` | Overrides the chart name in resource names |
| `fullnameOverride` | string | `""` | Overrides the full resource name prefix; default `<release>-launchway` |
| `commonLabels` | map | `{}` | Labels added to every resource the chart creates |
| `image.repository` | string | `ghcr.io/jk-magic-creations/launchway` | The Launchway image; servers and runners use the same one |
| `image.tag` | string | the chart's `appVersion` | The image tag |
| `image.digest` | string | `""` | An image digest, `sha256:...`; used instead of the tag when set |
| `image.pullPolicy` | string | `IfNotPresent` | `Always`, `IfNotPresent` or `Never` |
| `imagePullSecrets` | list | `[]` | Secrets for pulling the Launchway (and development PostgreSQL) image, by name |

## Server configuration

`config.*` becomes the servers' `launchway.yaml`. Settings that newer versions added are unset by
default and written to the file only when set, so a server of the version before can read the
file during an upgrade or after an aborted one. Unset values take the
[server's built-in defaults](server-config.md). The chart always sets `data` to
`/var/lib/launchway` and turns the built-in runner off.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `config.listen` | string | `0.0.0.0:8080` | Where the servers listen inside the pod; its port is also the container port. Written as `listen` |
| `config.projects` | list | `[]` | The repositories to watch, exactly as [`projects`](server-config.md#projects) in the server configuration (snake_case keys: `name`, `repo`, `branch`, `poll`, `push`, `drafts`, `pull_requests`, `schedules`, `git_webhook`, `webhooks`, `pipeline`). Git credentials go in the URL or in `GIT_CONFIG_*` variables in `server.extraEnv` |
| `config.tests.maxUploadMiB` | integer or null | unset (24) | Written as `tests.max_upload_mib`. 1 to 1024 |
| `config.tests.maxCasesPerRun` | integer or null | unset (50000) | Written as `tests.max_cases_per_run` |
| `config.tests.maxCasesPerProject` | integer or null | unset (1000000) | Written as `tests.max_cases_per_project`. At least `maxCasesPerRun` |
| `config.artifacts.store` | string or null | unset (`database` on PostgreSQL, `filesystem` on SQLite) | `database`, `filesystem` or `s3`. Unset with PostgreSQL, artifacts are kept in the database itself, shared by every server, with nothing else to run. An installation that kept artifacts on the filesystem before the database store existed keeps doing so, and the servers' log says how to copy them (`launchway blobs copy`). With SQLite they go in the pod's data directory, an `emptyDir`: lost when the pod goes. Set `database` only once every server runs a release that knows it. See [Where artifacts are kept](../artifacts.md) |
| `config.artifacts.database.maxTotalGiB` | integer or null | unset (100) | Written as `artifacts.database.max_total_gib` |
| `config.artifacts.database.maxBlobMiB` | integer or null | unset (2048) | Written as `artifacts.database.max_blob_mib` |
| `config.artifacts.s3.endpoint` | string or null | unset | Scheme and host only, such as `https://s3.eu-west-1.amazonaws.com` or `http://minio.storage:9000` |
| `config.artifacts.s3.region` | string or null | unset (`us-east-1`) | |
| `config.artifacts.s3.bucket` | string or null | unset | The bucket; it must exist |
| `config.artifacts.s3.prefix` | string or null | unset | Prepended to every object's name, such as `launchway/` |
| `config.artifacts.s3.pathStyle` | boolean or null | unset (`true`) | Path-style requests |
| `config.artifacts.s3.existingSecret` | string or null | unset | Required with `s3`: a Secret with the keys `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (and optionally `AWS_SESSION_TOKEN`), given to the servers as environment variables |
| `config.artifacts.maxFileMiB` | integer or null | unset (1024) | The largest file an artifact may hold, in MiB |
| `config.artifacts.maxRunMiB` | integer or null | unset (4096) | How much one run may keep, in MiB |
| `config.artifacts.maxProjectGiB` | integer or null | unset (50) | How much a project may keep, in GiB |
| `config.artifacts.retain` | string or null | unset (`30d`) | How long artifacts are kept when the pipeline does not say |
| `config.artifacts.maxRetain` | string or null | unset (`90d`) | The longest any artifact is kept |
| `config.findings.maxUploadMiB` | integer or null | unset (32) | MiB of SARIF files accepted per step attempt |
| `config.findings.maxPerRun` | integer or null | unset (20000) | Findings one run keeps |
| `config.findings.maxPerProject` | integer or null | unset (500000) | Findings a project keeps |
| `config.findings.maxComponentsPerRun` | integer or null | unset (20000) | SBOM components one run keeps |
| `config.findings.maxComponentsPerProject` | integer or null | unset (1000000) | SBOM components a project keeps |
| `config.reports.maxAttachmentFiles` | integer or null | unset (2000) | Written as `reports.max_attachment_files` |
| `config.reports.maxAttachmentMiB` | integer or null | unset (512) | Written as `reports.max_attachment_mib` |
| `config.reports.maxHtmlFiles` | integer or null | unset (5000) | Written as `reports.max_html_files` |
| `config.reports.maxHtmlMiB` | integer or null | unset (256) | Written as `reports.max_html_mib` |
| `config.reports.maxCoverageUploadMiB` | integer or null | unset (24) | Written as `reports.max_coverage_upload_mib` |
| `config.reports.maxCoverageFilesPerRun` | integer or null | unset (20000) | Written as `reports.max_coverage_files_per_run` |
| `config.reports.maxCoverageFilesPerProject` | integer or null | unset (2000000) | Written as `reports.max_coverage_files_per_project` |
| `config.reports.origin` | string or null | unset | A separate origin that alone serves hosted HTML reports, such as `https://reports.ci.example.com`: a host name of its own routed to the same servers (add it to the ingress). Unset, reports are served from Launchway's own origin, sandboxed. Written as `reports.origin` |
| `config.mcp.enabled` | boolean or null | unset (on) | `false` turns the MCP endpoint off on every server |
| `config.mcp.allowedOrigins` | list | `[]` | Origins of web pages, besides the servers' own, that may call `/mcp` from a browser, such as `https://tools.example.com`. Requests without an `Origin` header are not affected |
| `config.mcp.requestsPerMinute` | integer or null | unset (120) | Requests one token may make to `/mcp` a minute, on each server |
| `config.mcp.concurrent` | integer or null | unset (4) | Requests one token may have in progress at once, on each server |
| `config.mcp.total` | integer or null | unset (32) | MCP requests in progress at once on each server, all tokens together |
| `config.mcp.oauth.resource` | string or null | unset | Also accept the sign-in provider's OAuth access tokens issued for `/mcp` (needs `auth.mode: oidc` in `extraConfig`): this endpoint's address as clients reach it, such as `https://launchway.example.com/mcp` |
| `config.mcp.oauth.scopes` | list | `[]` | Scopes a token must carry, if any |
| `config.runnerAdmin` | string or null | unset (`admin`) | The role needed on every project to make join tokens and to revoke or rotate runners: `admin`, `operator` or `none`. Written as `runner_admin` only when set |
| `config.runnerPools` | list or null | unset | [Runner pools](server-config.md#runner-pools), written as `runner_pools` in the configuration's own keys, after the deploy pool `delivery.*` makes. Unset, every build runner is in the default pool, which serves every project. Set it only once every server and runner runs a release that knows it, and take it out before rolling back. For example `[{ name: web, kind: build, projects: ["web-*"] }]` |
| `config.findings.maxSbomMiB` | integer or null | unset (64) | MiB of a step's SBOM files read to summarize them |
| `config.loops.maxMembers` | integer or null | unset (64) | Combinations one `for_each` loop may have, at most 500. Written as `loops.max_members` |
| `config.schedules.enabled` | boolean or null | unset (on) | `false` turns every project's schedules off. Written as `schedules.enabled` |
| `config.tokens.maxDays` | integer or null | unset (365) | The longest an API token may last, in days, at most 3650. Written as `auth.tokens.max_days` |
| `config.policy` | object or null | unset (no policies) | Delivery policies, written as [`policy`](server-config.md#policy). Set it only once every server runs a release that knows policies, and take it out before rolling back |
| `config.policy.waivers.allow` | string or null | unset (`admin`) | `admin`, `operator` or `none` |
| `config.policy.waivers.maxDays` | integer or null | unset (30) | Written as `max_days` |
| `config.policy.sources` | list or null | unset | Git sources: `{ name, library, version, path, projects, refresh, staleAfter }`; `staleAfter` is written as `stale_after`. The library comes from `config.extraConfig.libraries` |
| `config.policy.policies` | list or null | unset | Policies, as in the [policy rules reference](policy-rules.md), written as they are |
| `config.extraConfig` | map | `{}` | Any other top-level settings of the [server configuration](server-config.md), merged into the file as they are, so new settings need no chart change. `listen`, `data`, `database`, `projects`, `tests`, `artifacts`, `findings`, `loops`, `schedules` and `policy` are refused here, and `runner.enabled` may not be true. A typo stops the servers from starting; the pod log names the key |

## Database and runner token

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `database.url` | string | `""` | A `postgres://` or `postgresql://` URL. The chart stores it in a Secret of its own. Prefer `existingSecret`, so the password is not in your values |
| `database.existingSecret` | string | `""` | An existing Secret in the release namespace holding the URL |
| `database.existingSecretKey` | string | `url` | The key in that Secret |
| `runnerToken.existingSecret` | string | `""` | An existing Secret holding the runner token. Empty: the chart creates one with a random token on first install and keeps it on every upgrade |
| `runnerToken.existingSecretKey` | string | `token` | The key in that Secret |

## Encryption at rest

See [Encryption at rest](../guide/encryption-at-rest.md). The chart passes the holder to the servers as environment variables, so the configuration file gains no key. By default the servers refuse to start without a root key, and the chart makes one.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `encryption.holder` | `keyFile`, `env`, `passphrase`, `transit` or `none` | `keyFile` | Where the root key is held. `none` runs not encrypted at rest, with a warning at start, in `launchway doctor` and on the Settings page |
| `encryption.keyFile.generate` | boolean | `true` | With no `existingSecret`, make a key Secret on first install and keep it on every upgrade. It is annotated `helm.sh/resource-policy: keep`, so `helm uninstall` leaves it. Needs a live `helm install` or `helm upgrade`: rendered without a cluster (`helm template`, Argo CD) the chart refuses to generate a key, because it would differ at every render; under GitOps create the Secret once and set `existingSecret`. Only as strong as the cluster's own Secret encryption: it defeats a stolen database dump, not a cluster administrator |
| `encryption.keyFile.allowOfflineGeneration` | boolean | `false` | Render a generated key even when the chart cannot see the cluster. Only for linting and tests: such a render makes a different key every time, so applying it would replace the real key and lock the installation out of its sealed secrets |
| `encryption.keyFile.existingSecret` | string | `""` | An existing Secret holding the key file's text (32 random bytes, base64). Prefer this in production |
| `encryption.keyFile.existingSecretKey` | string | `root.key` | The key in that Secret |
| `encryption.env.existingSecret` | string | `""` | `holder: env`: a Secret whose key holds the root key itself |
| `encryption.env.existingSecretKey` | string | `root.key` | The key in that Secret |
| `encryption.passphrase.keystoreSecret` | string | `""` | `holder: passphrase`: a Secret holding the keystore file |
| `encryption.passphrase.keystoreSecretKey` | string | `root.keystore` | The key in that Secret |
| `encryption.passphrase.passphraseSecret` | string | `""` | A Secret holding the passphrase, so that restarts are unattended |
| `encryption.passphrase.passphraseSecretKey` | string | `passphrase` | The key in that Secret |
| `encryption.transit.address` | URL | `""` | `holder: transit`: the Vault or OpenBao address. The transit key never leaves the service |
| `encryption.transit.mount` | string | `transit` | Where the transit engine is mounted |
| `encryption.transit.key` | string | `launchway` | The transit key; the servers need only `encrypt` and `decrypt` on it |
| `encryption.transit.namespace` | string | `""` | Sent as `X-Vault-Namespace` |
| `encryption.transit.tokenSecret` | string | `""` | A Secret holding a token to log in with (or use `role`) |
| `encryption.transit.tokenSecretKey` | string | `token` | The key in that Secret |
| `encryption.transit.role` | string | `""` | Log in with Kubernetes auth as this role |
| `encryption.transit.authPath` | string | `kubernetes` | Where Kubernetes auth is mounted |
| `encryption.transit.caSecret` | string | `""` | A Secret holding the service's CA certificate, key `ca.crt` |
| `encryption.previousKey.existingSecret` | string | `""` | During a root key replacement: a Secret holding the key being replaced, beside the new one while `launchway encryption rewrap` runs. Remove it afterwards |
| `encryption.previousKey.existingSecretKey` | string | `root.key` | The key in that Secret |

## Servers

Servers serve the API and web UI and run the engine. They keep nothing that matters on disk; any
number can run against the same database.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `server.replicas` | integer | `2` | Number of servers. Two or more for high availability |
| `server.strategy.rollingUpdate.maxUnavailable` | integer or string | `0` | Keeps every server serving until its replacement is ready |
| `server.strategy.rollingUpdate.maxSurge` | integer or string | `1` | |
| `server.preStopSleepSeconds` | integer | `5` | How long a stopping server keeps serving after Kubernetes takes it out of the Service, before it is sent SIGTERM. After SIGTERM it reports not ready, waits `server.drain_delay` (set it in `config.extraConfig.server`), then gives requests in flight up to 15s |
| `server.terminationGracePeriodSeconds` | integer | `30` | Time allowed to stop, including the above |
| `server.resources` | object | requests `100m` CPU, `128Mi`; limit `512Mi` memory | Kubernetes resources of the server container |
| `server.dataVolume.sizeLimit` | quantity | `2Gi` | The data directory (an `emptyDir`) holds mirrors of the projects' repositories, rebuilt when a pod starts. Size it to fit them all |
| `server.startupProbe` | object | `GET /healthz`, every 2s, 90 failures | Allows time for database migrations, which servers take in turn under a lock |
| `server.livenessProbe` | object | `GET /healthz`, every 10s | |
| `server.readinessProbe` | object | `GET /readyz`, every 5s | `/readyz` reports the database answers and the server is not stopping |
| `server.spreadAcrossNodes` | boolean | `true` | A soft spread of servers across nodes. Ignored when `topologySpreadConstraints` is set |
| `server.topologySpreadConstraints` | list | `[]` | Replaces the default spread; use it to spread across zones as well |
| `server.affinity` | map | `{}` | Affinity rules |
| `server.nodeSelector` | map | `{}` | |
| `server.tolerations` | list | `[]` | |
| `server.priorityClassName` | string | `""` | |
| `server.podAnnotations` | map | `{}` | |
| `server.podLabels` | map | `{}` | |
| `server.extraEnv` | list | `[]` | Extra environment variables, such as webhook secrets or `GIT_CONFIG_*` |
| `server.extraVolumes` | list | `[]` | |
| `server.extraVolumeMounts` | list | `[]` | |
| `server.podDisruptionBudget.enabled` | boolean | `true` | |
| `server.podDisruptionBudget.maxUnavailable` | integer or string | `1` | At most this many servers evicted at once |
| `server.serviceAccount.create` | boolean | `true` | Create a ServiceAccount for the servers. It has no API permissions and its token is not mounted |
| `server.serviceAccount.name` | string | `""` | Default `<fullname>-server` |
| `server.serviceAccount.annotations` | map | `{}` | |

## Service ingress and route

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `service.type` | string | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer` |
| `service.port` | integer | `8080` | |
| `service.annotations` | map | `{}` | |
| `ingress.enabled` | boolean | `false` | Expose the UI and API through an Ingress. Turn on [sign-in](../howto/oidc-and-roles.md) before exposing Launchway to anyone |
| `ingress.className` | string | `""` | |
| `ingress.annotations` | map | `{}` | |
| `ingress.hosts` | list | `[]` | Host names; each routes every path to the servers |
| `ingress.tls` | list | `[]` | `[{ secretName, hosts }]` |
| `httpRoute.enabled` | boolean | `false` | Expose them through a Gateway API HTTPRoute (`gateway.networking.k8s.io/v1`) |
| `httpRoute.annotations` | map | `{}` | |
| `httpRoute.parentRefs` | list | `[]` | The Gateways to attach to: `[{ name, namespace, sectionName }]` |
| `httpRoute.hostnames` | list | `[]` | |

## Git over SSH

The servers serve Git over SSH themselves ([Git over SSH](../guide/git-over-ssh.md)). The chart opens the port and makes a Service for it; the address the servers listen on is the installation setting `forge.ssh.listen`, which must be `:<ssh.port>`.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `ssh.enabled` | boolean | `false` | Open the SSH port on the servers and make the Service `<release>-launchway-ssh` |
| `ssh.port` | integer | `2222` | The port the servers listen on inside the pod |
| `ssh.service.type` | string | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer` |
| `ssh.service.port` | integer | `22` | The port the Service offers. With 22, clone addresses are `git@host:owner/name.git` (set `forge.ssh.public_port` to match) |
| `ssh.service.annotations` | map | `{}` | For a cloud load balancer |

### Publishing the SSH port

SSH is plain TCP, so an HTTP Ingress cannot carry it. Two ways that work:

A LoadBalancer Service, with its own address and a DNS name such as `git.example.com`:

```yaml
ssh:
  enabled: true
  service:
    type: LoadBalancer
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-scheme: internal
```

A Gateway with a TCP listener (Gateway API `TCPRoute`, where your Gateway supports it):

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata:
  name: launchway-ssh
spec:
  parentRefs:
    - name: public
      sectionName: ssh      # a listener with protocol TCP on port 22
  rules:
    - backendRefs:
        - name: launchway-launchway-ssh
          port: 22
```

Several servers sit behind one address: they present the same host keys, because the keys are kept in the
database. Set the installation setting `forge.ssh.listen` to `:2222` (or your `ssh.port`) and, when the
Service port is not 22, `forge.ssh.public_port` to the port people use.

## Runners

Runners are `launchway runner` with the Kubernetes executor. Each run gets one pod in the step
namespace, held by one runner. Runners only make outbound connections, to the servers and the
Kubernetes API.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `runner.enabled` | boolean | `true` | Run runners. Turn off if your runners live elsewhere |
| `runner.replicas` | integer | `2` | Number of runner pods |
| `runner.concurrency` | integer | `2` | Steps each runner runs at once |
| `runner.serverURL` | string | the chart's Service | The URL runners connect to |
| `runner.join.existingSecret` | string or null | unset | Enrol each runner pod with a join token instead of using the shared runner token: a Secret holding a join token made in the UI (Runners, Add a runner), with enough uses for every pod start, ideally ephemeral. Unset, the shared runner token is used |
| `runner.join.existingSecretKey` | string | `token` | The key of that Secret |
| `runner.labels` | list or null | unset | Labels the runners have, for pipelines that ask for them (`runner: { labels }`). Written only when set |
| `runner.accept` | map or null | unset | The runners' own limits on the work they take, as [`accept`](runner-config.md#what-the-runner-accepts), checked on the runner before anything runs. Written only when set |
| `runner.ai` | boolean or null | unset (`false`) | Let runners take `ai` steps, calling the models in `ai.models`. A model's key is read from `runner.secretStores`. Needs `ai.models` |
| `runner.kubernetes.namespace` | string | the release namespace | Where step pods run. Runners are granted access to pods there only. It must exist unless `createNamespace` is true |
| `runner.kubernetes.createNamespace` | boolean | `false` | Create the step namespace with the chart (deleted on uninstall, with any step pods in it) |
| `runner.kubernetes.namespaceLabels` | map | `pod-security.kubernetes.io/enforce: restricted` | Labels of a namespace the chart creates. Use `baseline` when `restricted` is false |
| `runner.kubernetes.helperImage` | string | `""` (busybox 1.37) | The step pods' helper image; needs `sh` and `tar` |
| `runner.kubernetes.serviceAccount` | string | `""` (none, no token mounted) | The ServiceAccount step pods run as |
| `runner.kubernetes.nodeSelector` | map | `{}` | Node selector for step pods (`runner.nodeSelector` is for the runner pods) |
| `runner.kubernetes.cpu` | string or number | `""` (`50m`) | CPU request of each step container, unless the pipeline asks |
| `runner.kubernetes.memory` | string or number | `""` (`64Mi`) | Memory request of each step container, unless the pipeline asks |
| `runner.kubernetes.memoryLimit` | string or number | `""` (none) | Memory limit of each step container that does not ask for memory |
| `runner.kubernetes.maxCpu` | string or number | `""` (no cap) | The most CPU one step container may ask for, as a request or a limit; server defaults are lowered to it, an explicit ask above it fails the step. See [`kubernetes.max_cpu`](runner-config.md#kubernetes) |
| `runner.kubernetes.maxMemory` | string or number | `""` (no cap) | The most memory one step container may ask for; same behavior as `maxCpu` |
| `runner.kubernetes.imagePullSecrets` | list | `[]` | Secrets in the step namespace for pulling step images |
| `runner.kubernetes.keepFailed` | string | `""` (`0`) | A grace period before a failed or cancelled run's pod is removed; by default it goes when the run ends. See [What happens to a run's pod](runner-config.md#what-happens-to-a-runs-pod) |
| `runner.kubernetes.maxPodAge` | string | `""` (`24h`) | The longest a step pod lives unless a step of it is executing; the reaper's bound for pods left by a crashed runner. `0` turns it off |
| `runner.kubernetes.startTimeout` | string | `""` (`10m`) | How long to wait for a step pod's images and containers |
| `runner.kubernetes.restricted` | boolean | `true` | Step containers run as `runAsUser`, without privileges, meeting Pod Security `restricted`; see [`kubernetes.restricted`](runner-config.md#kubernetes). Pipelines cannot change it |
| `runner.kubernetes.runAsUser` | integer or null | unset (1000) | Not 0 |
| `runner.kubernetes.runAsGroup` | integer or null | unset (1000) | Also owns the pod's volumes |
| `runner.healthPort` | integer | `8081` | The port runners serve `/healthz` and `/readyz` on, for the probes. Not exposed outside the pod |
| `runner.livenessProbe` | object | `GET /healthz`, every 10s | |
| `runner.readinessProbe` | object | `GET /readyz`, every 10s | A runner that cannot reach the servers is not ready, which holds back a rollout that would replace working runners |
| `runner.extraArgs` | list | `[]` | Extra arguments for `launchway runner`. [Flags](runner-config.md#how-settings-are-read) override the rendered file |
| `runner.reports.maxFiles` | integer or null | unset (100) | Written as `reports.max_files` |
| `runner.reports.maxFileMiB` | integer or null | unset (10) | Written as `reports.max_file_mib` |
| `runner.reports.maxTotalMiB` | integer or null | unset (20) | Written as `reports.max_total_mib` |
| `runner.images.builders` | list | `[]` | BuildKit daemons for image steps: `[{ name, address, serverName, platforms, projects }]`. Unset, runners take no image steps. Set it only once every runner is upgraded |
| `runner.images.tlsSecret` | string | `""` | A Secret with `ca.crt`, `tls.crt` and `tls.key`, mounted at `/var/run/launchway/buildkit`. Required for `tcp://` builders |
| `runner.images.registries` | map | `{}` | Registries by the name pipelines use: `{ host, credentials, pullCredentials, pullProjects, caFile, insecure, push }`, as [`images.registries`](runner-config.md#registries). Credentials are secret references read from `runner.secretStores` |
| `runner.images.signingSecret` | string | `""` | A Secret with `cosign.key` (and `cosign.password`), mounted where step pods never see it. Unset, `sign: true` fails |
| `runner.images.scanner.image` | string | `""` (the built-in Trivy) | Pinned by digest |
| `runner.images.scanner.dbRepository` | string | `""` | Where the scanner downloads its database |
| `runner.images.scanner.javaDBRepository` | string | `""` | Where it downloads its Java database |
| `runner.images.maxContextMiB` | integer or null | unset (2048) | |
| `runner.images.maxImageMiB` | integer or null | unset (20480) | |
| `runner.secretStores` | map | `{}` | Secret stores runners may read, as [`secrets`](runner-config.md#secrets). Mount token or certificate files with `extraVolumes`. There is no `env` store unless you define one |
| `runner.resources` | object | requests `100m` CPU, `128Mi`; limit `1Gi` memory | |
| `runner.dataVolume.sizeLimit` | quantity | `10Gi` | Runners clone each run's commit here and copy it into the step pod |
| `runner.terminationGracePeriodSeconds` | integer | `60` | On SIGTERM a runner stops its steps (10s grace each) and hands them back to run again |
| `runner.nodeSelector` | map | `{}` | |
| `runner.tolerations` | list | `[]` | |
| `runner.affinity` | map | `{}` | |
| `runner.spreadAcrossNodes` | boolean | `true` | A soft spread across nodes |
| `runner.topologySpreadConstraints` | list | `[]` | |
| `runner.priorityClassName` | string | `""` | |
| `runner.podAnnotations` | map | `{}` | |
| `runner.podLabels` | map | `{}` | |
| `runner.extraEnv` | list | `[]` | |
| `runner.extraVolumes` | list | `[]` | |
| `runner.extraVolumeMounts` | list | `[]` | |
| `runner.podDisruptionBudget.enabled` | boolean | `true` | |
| `runner.podDisruptionBudget.maxUnavailable` | integer or string | `1` | |
| `runner.serviceAccount.create` | boolean | `true` | It is bound to a Role in the step namespace |
| `runner.serviceAccount.name` | string | `""` | Default `<fullname>-runner` |
| `runner.serviceAccount.annotations` | map | `{}` | |
| `runner.rbac.create` | boolean | `true` | Create the Role and RoleBinding that let runners manage step pods (pods, `pods/exec`, `pods/log`) in the step namespace only |

## Security contexts

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `podSecurityContext` | object | non-root, user and group 65532, `RuntimeDefault` seccomp | The pod security context of servers and runners. The image runs as user 65532 |
| `securityContext` | object | no privilege escalation, read-only root filesystem, all capabilities dropped | The container security context of servers and runners |

## Development PostgreSQL

One PostgreSQL pod, for trying the chart only: no backups, no high availability. Use a managed or
operator-run PostgreSQL in production.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `postgresql.enabled` | boolean | `false` | |
| `postgresql.image.repository` | string | `docker.io/library/postgres` | |
| `postgresql.image.tag` | string | `17.12-alpine` | |
| `postgresql.image.pullPolicy` | string | `IfNotPresent` | |
| `postgresql.database` | string | `launchway` | |
| `postgresql.username` | string | `launchway` | |
| `postgresql.password` | string | `""` | Empty: a random password, generated once and kept |
| `postgresql.persistence.size` | quantity | `1Gi` | |
| `postgresql.persistence.storageClass` | string | `""` | Default: the cluster's default StorageClass |
| `postgresql.resources` | object | requests `50m` CPU, `128Mi`; limit `512Mi` memory | |

## Delivery

Deploy, rollback and rollout steps. Off unless set. Set these only once every server and runner
runs a release that knows them, and take them out before rolling back to one that does not. See
[Deploy with an approval](../tutorials/deploy-with-approval.md).

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `delivery.environments` | list or null | `null` | [Environments](server-config.md#environments), written as `environments`: `[{ name, target, projects, queue, protected }]`. Without any, deploy steps are refused |
| `delivery.verification.sources` | list or null | `null` | [Health sources](runner-config.md#health-sources) for verify steps. Each entry is split: `name`, `type` and `projects` go to the servers' `verification.sources`, and the whole entry to the deploy runner's `health_sources`, by name. Each is `{ name, type, url, projects }` and optionally `auth`, `headers`, `caFile`, `timeout`, `limits`, `statements`, `indices`, `timeField` and `messageField`, as in the runner reference (camelCase here). Needs `deployRunner.enabled`; credentials are secret references read from `deployRunner.secretStores`. Unset, verify steps are refused |
| `delivery.deployRunner.enabled` | boolean | `false` | Run a deploy runner in this cluster, with a runner pool token of its own (the chart adds the pool to `runner_pools`) |
| `delivery.deployRunner.target` | string | `""` | The target this cluster is, as `delivery.environments` names it. Required when enabled |
| `delivery.deployRunner.replicas` | integer | `1` | One deploy runner observes each target at a time; others take over |
| `delivery.deployRunner.argocdNamespace` | string | `argocd` | Where the Argo CD Applications are |
| `delivery.deployRunner.applications` | list | `[]` | `[{ projects, names }]`: which projects may deploy which applications. Names written out, no patterns: the RBAC rules name each application. Required when enabled |
| `delivery.deployRunner.rolloutNamespaces` | list | `[]` | Namespaces whose rollouts deploy steps may watch |
| `delivery.deployRunner.repos` | map | `{}` | GitOps repositories, as [`deploy.repos`](runner-config.md#deploy) (`url`, `argocd_url`, `credentials`, `username`, `paths`, `author`). Credentials name a secret in `secretStores`. Required when enabled |
| `delivery.deployRunner.secretStores` | map | `{}` | Secret stores the deploy runner may read |
| `delivery.deployRunner.ai` | boolean or null | unset (`false`) | Let the deploy runner call the models in `ai.models` to explain its verdicts, with keys read from `secretStores`. Needs `ai.models` |
| `delivery.deployRunner.refresh` | boolean or null | `null` (`true`) | Ask Argo CD to look at the repository at once after a commit |
| `delivery.deployRunner.accept` | map or null | unset | The deploy runner's own limits on the work it takes, as [`accept`](runner-config.md#what-the-runner-accepts) (`projects`, `environments`, `executors`), checked on the runner before anything runs. Written only when set |
| `delivery.deployRunner.rbac.create` | boolean | `true` | Create the Roles below and bind them to the deploy runner |
| `delivery.deployRunner.rbac.patchApplications` | boolean | `true` | Patch on the named Applications, to request syncs and refreshes. Without it, deploy steps need `sync.request: auto` or `never` |
| `delivery.deployRunner.rbac.readPods` | boolean | `false` | Get and list on pods and get on pods/log in the rollout namespaces, so the run page can show the end of a failed analysis job's log and a rollout's pods |
| `delivery.deployRunner.rbac.readDeployments` | boolean | `false` | Get on Deployments in the rollout namespaces, for rollouts that take their pod template from a Deployment (`workloadRef`) |
| `delivery.deployRunner.rbac.controlRollouts` | map or null | `null` | Rollouts the deploy runner may promote, abort and retry, by namespace: `{ shop: [shop] }`. Each namespace must be in `rolloutNamespaces`. Unset, commands are not applied |
| `delivery.deployRunner.admissionPolicy.enabled` | boolean | `true` | A ValidatingAdmissionPolicy (Kubernetes 1.30 or newer) letting the deploy runner change only an Application's operation and refresh annotation |
| `delivery.deployRunner.poolToken.existingSecret` | string | `""` | A Secret holding the pool token. Empty: generated on first install and kept on upgrade |
| `delivery.deployRunner.poolToken.existingSecretKey` | string | `token` | |
| `delivery.deployRunner.resources` | object | requests `50m` CPU, `64Mi`; limit `256Mi` memory | |
| `delivery.deployRunner.healthPort` | integer | `8081` | |
| `delivery.deployRunner.nodeSelector` | map | `{}` | |
| `delivery.deployRunner.tolerations` | list | `[]` | |
| `delivery.deployRunner.podAnnotations` | map | `{}` | |
| `delivery.deployRunner.extraEnv` | list | `[]` | |
| `delivery.deployRunner.extraVolumes` | list | `[]` | |
| `delivery.deployRunner.extraVolumeMounts` | list | `[]` | |

<!-- validate-helm -->
```yaml
postgresql:
  enabled: true
delivery:
  environments:
    - { name: prod, target: main-cluster, projects: [shop], queue: newest, protected: true }
  deployRunner:
    enabled: true
    target: main-cluster
    applications: [ { projects: [shop], names: [shop] } ]
    rolloutNamespaces: [shop]
    repos:
      gitops:
        url: https://git.example.com/team/gitops.git
        credentials: { store: vault, key: ci/gitops, field: token }
        paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
    secretStores:
      vault: { type: vault, address: "https://vault.example.com:8200", role: launchway-deploy }
    rbac:
      controlRollouts: { shop: [shop] }
```

## AI models

Nothing is sent to any model unless `ai.models` lists one. See [AI models](../guide/ai.md).

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `ai.models` | list or null | `null` | The model registry, written into the servers' configuration as [`ai.models`](server-config.md#ai-models) exactly as given, so its keys are the configuration's own (snake_case): `name`, `provider`, `endpoint`, `model`, `key: { store, key, field }`, `projects`, `max_tokens`, `price` and the rest. A model's `projects` list says who may use it; empty means no project. The key is a secret reference, looked up in the secret stores of whoever calls the model (`runner.secretStores`, `delivery.deployRunner.secretStores` or `ai.serverSecretStores`). Unset, there are none |
| `ai.serverSecretStores` | map or null | `null` | Secret stores the servers read model keys from, to explain failed steps themselves (the same map as `runner.secretStores`); written as `ai.secrets`. Mount any token or certificate files they refer to with `server.extraVolumes` and `server.extraVolumeMounts`. Unset, the servers explain only with models that need no key |

## What next

- [Install on Kubernetes](../howto/install-kubernetes.md) walks through an installation.
- [Server configuration](server-config.md) and [runner configuration](runner-config.md) explain
  the settings these values become.
