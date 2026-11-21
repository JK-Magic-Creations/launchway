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
reach the pods as environment variables from Secrets (made once by the install hook Job, below), never through a ConfigMap. Changing a
ConfigMap restarts the pods.

**The file the chart renders is bootstrap only** (see [what stays in the file](server-config.md#what-stays-in-the-file-and-what-moved)).
With the default values it holds `listen`, `data`, the runner switch, `auth.mode: local` and, with the bundled database, the artifact cap derived from its volume. A value that sets something
that now lives in the database (projects, libraries, environments, runner pools, policy, health
sources, models, limits, the MCP endpoint, where artifacts are kept) is **imported once**, by the first
server of a new installation to start, and ignored afterwards: each server logs the keys it ignores.
After that, change those settings in the web UI, with `launchway config`, the API or Terraform.
`values.schema.json` checks the types and ranges below when you install
or upgrade.

## The one required value, and the few you may want

A working, secure installation needs **one** value, the address people will open:

<!-- validate-helm -->
```yaml
externalURL: https://launchway.example.com
```

Left out of a new installation with built-in accounts (or with `ingress` or `httpRoute`), the install fails and says so; an installation that already has its own sign-in (`auth.mode: oidc`, or an explicit `none`) and publishes nothing through the chart needs none. The chart then starts with sign-in required
(built-in accounts, the first administrator made with a one-time setup code), encryption at rest on, a bundled
PostgreSQL on an 8Gi volume, default-deny network policies and restricted pods. Nothing else is assumed: no
secrets manager, operator or identity provider. `values.yaml` leads with the same short list and keeps
everything else below an "ADVANCED" divider.

Beyond the address, most installations set at most:

- `database.url` or `database.existingSecret`: a PostgreSQL of your own; the bundled one is then not created.
- `postgresql.persistence.size` / `storageClass`: the bundled database's volume.
- `ingress.*` or `httpRoute.*`: publish the address (the host defaults to the one in `externalURL`). Either also
  tells the servers they sit behind a proxy (`trust_proxy`).
- `server.replicas`: `2` by default; `1` for a trial.

### Secrets the chart makes, and keeps

A `helm template` of this chart holds **no secret material** and is byte-identical every time, so a GitOps
controller that renders it cannot churn a key. The secrets are made by a **pre-install and pre-upgrade hook
Job** (`bootstrap.*`) that runs `launchway bootstrap-secrets` with the Launchway image, with its own ServiceAccount,
and a Role that can only `get` the named Secrets and `create` Secrets. Each Secret is created only if absent and never
modified afterwards:

| Secret (default name) | Key | Made when |
|---|---|---|
| `<fullname>-root-key` | `root.key` | `encryption.holder` is `keyFile` and `encryption.keyFile.existingSecret` is empty |
| `<fullname>-runner-token` | `token` | `runnerToken.existingSecret` is empty |
| `<fullname>-postgresql` | `password` | the bundled database is used and `postgresql.password` is empty |
| `<fullname>-setup-code` | `code` | sign-in mode is `local` (the default) |
| `<fullname>-deploy-pool-token` | `token` | `delivery.deployRunner.enabled` and no `poolToken.existingSecret` |

**The root key Secret is kept when you uninstall.** It is not owned by Helm, so `helm uninstall` and a GitOps
prune leave it (and the other Secrets) in place; without it, a restored database's secrets cannot be read.
Copy it somewhere safe (see encryption at rest (`docs/guide/encryption-at-rest.md`, from the encryption work)). The hook's ServiceAccount, Role,
RoleBinding and NetworkPolicy are hooks too and also remain; remove them with
`kubectl -n NS delete serviceaccount,role,rolebinding,networkpolicy -l app.kubernetes.io/component=bootstrap`
when you remove the installation for good, and delete the Secrets only when you mean to destroy its data.
The servers read the one-time setup code from the file named by `LAUNCHWAY_SETUP_CODE_FILE`
(the `<fullname>-setup-code` Secret, mounted as an optional file) and the address from `LAUNCHWAY_EXTERNAL_URL`.

A production-shaped example with a database of your own (see also `deploy/helm/launchway/examples/ha.yaml`):

<!-- validate-helm -->
```yaml
externalURL: https://launchway.example.com
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
| `externalURL` | string | none (required for built-in accounts, `ingress` and `httpRoute`) | The address people open: `https://` or `http://`, a host, optional port, no path. Names the ingress and route host, is passed to the servers as `LAUNCHWAY_EXTERNAL_URL` and is shown in the install notes |
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
| `config.queues.maxWait` | string or null | unset (60m) | The longest a step waits for room in the step namespace before it fails with the reason, such as `30m`, or `off` to wait for ever. See [Queues and limits](../guide/queues.md) |
| `config.queues.runs` | integer or null | unset (10) | Runs of a project at once; 0 is no limit. Pushes to protected branches are never held |
| `config.queues.pipelineRuns` | integer or null | unset (no limit) | Runs of one pipeline at once |
| `config.queues.pullRequestRuns` | integer or null | unset (4) | Pull-request runs of a project at once |
| `config.queues.pushRuns` | integer or null | unset (no limit) | Push runs of a project at once |
| `config.queues.scheduleRuns` | integer or null | unset (no limit) | Scheduled runs of a project at once |
| `config.queues.manualRuns` | integer or null | unset (no limit) | Manually started runs of a project at once |
| `config.queues.automationRuns` | integer or null | unset (2) | Runs started by automation accounts at once |
| `config.queues.supersedePullRequests` | boolean or null | unset (true) | A new commit on a pull request cancels its older run |
| `config.automation.accounts` | list or null | unset (the built-in list) | Accounts that are automation: their runs are counted apart in the queue and their pull requests run restricted. The built-in list is `*[bot]`, `dependabot*`, `renovate*`, `github-actions*`, `release-please*` and `launchway-updater*`; `[]` names none |
| `config.pullRequests.untrusted` | string | `""` (restrict) | `restrict` or `allow`: whether pull requests from automation accounts and forks run restricted (no secrets, deploys or cache writes) until a maintainer approves. See [Pull-request trust](../guide/pull-request-trust.md) |
| `config.notifications.channels` | list | `[]` | Where messages go: `{ name, kind: webhook\|slack\|discord\|teams, url: { store, key, field }, secret: { store, key } }`. The address and signing key are secret references (read from `ai.serverSecretStores`), never values: a chat webhook's address is a credential, and a value there is refused |
| `config.notifications.rules` | list | `[]` | Which events go to which channels: `{ "on": [approval.requested, ...], projects, environments, branches, to: [channel names] }`. Write `"on"` in quotes: unquoted, Helm reads it as the boolean true |
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
| `config.extraConfig.auth.mode` | string | `local` | Sign-in: `local` (built-in accounts; the first administrator is made with the one-time setup code) or `oidc` (an identity provider; set `auth.oidc.*` beside it). `none` is for throwaway clusters and needs `auth.allow_anonymous` |
| `config.extraConfig` | map | `{auth: {mode: local}}` | Any other top-level settings of the [server configuration](server-config.md), merged into the file as they are, so new settings need no chart change. `listen`, `data`, `database`, `projects`, `tests`, `artifacts`, `findings`, `loops`, `schedules` and `policy` are refused here, and `runner.enabled` may not be true. A typo stops the servers from starting; the pod log names the key |

## Database and runner token

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `database.url` | string | `""` | A `postgres://` or `postgresql://` URL. The chart stores it in a Secret of its own and does not create the bundled database. Prefer `existingSecret`, so the password is not in your values |
| `database.existingSecret` | string | `""` | An existing Secret in the release namespace holding the URL. The bundled database is not created |
| `database.existingSecretKey` | string | `url` | The key in that Secret |
| `runnerToken.existingSecret` | string | `""` | An existing Secret holding the runner token. Empty: the install hook Job makes one with a random token if absent and keeps it on every upgrade |
| `runnerToken.existingSecretKey` | string | `token` | The key in that Secret |

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
| `server.resources` | object | requests `100m` CPU, `128Mi` memory, `512Mi` ephemeral storage; limits `512Mi` memory, `3Gi` ephemeral storage | Kubernetes resources of the server container |
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
| `ingress.enabled` | boolean | `false` | Publish `externalURL` through an Ingress. Sign-in is required by default; read [hardening](../security/hardening.md) before exposing Launchway to an untrusted network |
| `ingress.className` | string | `""` | |
| `ingress.annotations` | map | `{}` | |
| `ingress.hosts` | list | `[]` | Host names; each routes every path to the servers. Empty: the host of `externalURL` |
| `ingress.tls` | list | `[]` | `[{ secretName, hosts }]` |
| `httpRoute.enabled` | boolean | `false` | Expose them through a Gateway API HTTPRoute (`gateway.networking.k8s.io/v1`) |
| `httpRoute.annotations` | map | `{}` | |
| `httpRoute.parentRefs` | list | `[]` | The Gateways to attach to: `[{ name, namespace, sectionName }]` |
| `httpRoute.hostnames` | list | `[]` | Empty: the host of `externalURL` |

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
| `runner.resources` | object | requests `100m` CPU, `128Mi` memory, `2Gi` ephemeral storage; limits `1Gi` memory, `12Gi` ephemeral storage | Keep the ephemeral-storage limit above `runner.dataVolume.sizeLimit` |
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

## Bundled PostgreSQL

One PostgreSQL pod on one volume: the default, for a single installation that is backed up at the volume or a
trial. It is not highly available. For production at scale use a managed or operator-run PostgreSQL and set
`database.*`; the bundled one is then not created. Its password is made once by the install hook Job.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `postgresql.enabled` | boolean | `true` | Run the bundled database. Ignored (off) when `database.url` or `database.existingSecret` is set; set `false` only together with one of them |
| `postgresql.image.repository` | string | `docker.io/library/postgres` | |
| `postgresql.image.tag` | string | `17.12-alpine` | |
| `postgresql.image.pullPolicy` | string | `IfNotPresent` | |
| `postgresql.database` | string | `launchway` | |
| `postgresql.username` | string | `launchway` | |
| `postgresql.password` | string | `""` | Letters, digits and `. _ ~ -` only. Empty (the default): the hook Job makes a random password once and keeps it |
| `postgresql.persistence.size` | quantity | `8Gi` | The volume. With artifacts kept in the database (the default) the chart caps them at half of it, and refuses a larger `config.artifacts.database.maxTotalGiB` (more than three quarters of the volume) with a message saying so. Below `2Gi` it asks you to store artifacts elsewhere |
| `postgresql.persistence.storageClass` | string | `""` | Default: the cluster's default StorageClass |
| `postgresql.resources` | object | requests `50m` CPU, `128Mi`, `64Mi` ephemeral storage; limits `512Mi`, `512Mi` ephemeral storage | |

## Encryption at rest

Every secret the server stores is sealed under a root key that is not in the database. The servers refuse to start
without one. See encryption at rest (`docs/guide/encryption-at-rest.md`, from the encryption work) for the holders and for rotating the key.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `encryption.holder` | string | `keyFile` | Where the root key is held: `keyFile` (a Secret mounted as a file; made by the hook Job), `env`, `passphrase`, `transit` (Vault or OpenBao) or `none` (NOT encrypted at rest; a throwaway installation only) |
| `encryption.keyFile.generate` | boolean | `true` | Let the hook Job make the key Secret, if absent, when `existingSecret` is empty. Set `false` only with `existingSecret` |
| `encryption.keyFile.existingSecret` | string | `""` | A Secret you made that holds the key: 32 random bytes in base64, as `launchway encryption init` writes |
| `encryption.keyFile.existingSecretKey` | string | `root.key` | The key in that Secret |
| `encryption.env.existingSecret` | string | `""` | Holder `env`: a Secret whose key holds the root key itself (base64) |
| `encryption.env.existingSecretKey` | string | `root.key` | |
| `encryption.passphrase.keystoreSecret` | string | `""` | Holder `passphrase`: a Secret holding the keystore file |
| `encryption.passphrase.keystoreSecretKey` | string | `root.keystore` | |
| `encryption.passphrase.passphraseSecret` | string | `""` | A Secret holding the passphrase |
| `encryption.passphrase.passphraseSecretKey` | string | `passphrase` | |
| `encryption.transit.address` | string | `""` | Holder `transit`: the Vault or OpenBao address |
| `encryption.transit.mount` | string | `transit` | The transit engine's mount |
| `encryption.transit.key` | string | `launchway` | The transit key that wraps the installation's keys |
| `encryption.transit.namespace` | string | `""` | A Vault namespace |
| `encryption.transit.tokenSecret` | string | `""` | Log in with a token held in this Secret ... |
| `encryption.transit.tokenSecretKey` | string | `token` | |
| `encryption.transit.role` | string | `""` | ... or with Kubernetes auth, as this role |
| `encryption.transit.authPath` | string | `kubernetes` | The Kubernetes auth mount |
| `encryption.transit.caSecret` | string | `""` | A Secret holding the service's CA certificate (PEM), key `ca.crt` |
| `encryption.previousKey.existingSecret` | string | `""` | During a root key replacement: a Secret holding the key being replaced. Remove it afterwards |
| `encryption.previousKey.existingSecretKey` | string | `root.key` | |

## Network policies

On by default: a default-deny policy for each part, then exactly what it needs. They take effect only where the
cluster's network plugin enforces NetworkPolicy. Each policy selects its pods by label, so nothing else in the
namespace is affected.

| Part | In | Out |
|---|---|---|
| Servers | the servers themselves, runners, step pods, the deploy runner, and (with an Ingress or HTTPRoute) the proxy | DNS, the servers themselves (a project that watches a repository this installation hosts), the database, HTTPS to public addresses (`networkPolicy.server.publicHTTPS`), `networkPolicy.server.extraEgress`. Not the Kubernetes API, not private addresses |
| Runners | nothing | DNS, the servers, the Kubernetes API (to start step pods), `networkPolicy.runner.extraEgress` |
| Step pods | nothing | DNS, the Launchway server, and HTTP/HTTPS to the internet except private, link-local (cloud metadata), carrier-grade NAT and `exceptCIDRs` ranges. Never the database, the Kubernetes API or the metadata service |
| Bundled database | the servers | nothing |

Git hosts, an identity provider, webhook receivers and an S3 endpoint are not assumed: add them to
`networkPolicy.server.extraEgress` (the servers) and `networkPolicy.runner.extraEgress` (runners that clone).
Internal package mirrors or Git servers that step pods must reach go in `networkPolicy.steps.extraEgress`.
If your cluster's API server has a public address, add it to `networkPolicy.steps.exceptCIDRs`.

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `networkPolicy.enabled` | boolean | `true` | Create the policies. `false`: none, and every pod may connect anywhere |
| `networkPolicy.dns.enabled` | boolean | `true` | Allow DNS for every pod above |
| `networkPolicy.dns.namespace` | string | `kube-system` | The namespace of the cluster DNS |
| `networkPolicy.dns.podLabels.k8s-app` | string | `kube-dns` | The label of the DNS pods; add others with `--set` |
| `networkPolicy.server.ingressFrom` | list | `[]` | Who may reach the server port: NetworkPolicyPeer objects, such as your ingress controller's namespace. Empty: with an Ingress or HTTPRoute on, any source; without one, only runners, step pods and the deploy runner (port-forward still works) |
| `networkPolicy.server.publicHTTPS.enabled` | boolean | `true` | HTTPS from the servers to public addresses: the dependency updater's version sources and vulnerability data, chat services and webhook receivers of notifications, other Git servers. Private, link-local, carrier-grade NAT and loopback addresses are never included, and the installation's own egress allow-list narrows it further. `false`: only what `extraEgress` adds |
| `networkPolicy.server.publicHTTPS.ports` | list | `[443]` | The ports of that rule |
| `networkPolicy.server.publicHTTPS.exceptCIDRs` | list | `[]` | More ranges the servers may not reach over HTTPS |
| `networkPolicy.server.publicHTTPS.allowIPv6` | boolean | `false` | Also admit public IPv6 addresses |
| `networkPolicy.server.extraEgress` | list | `[]` | More egress rules for the servers, added to the policy (it stays on): a receiver or registry mirror in the cluster or on a private network, an identity provider, an S3 endpoint |
| `networkPolicy.database.port` | integer | `5432` | The port of an external database whose address is not a URL in `database.url` |
| `networkPolicy.database.cidrs` | list | `[]` | Addresses of an external database. Empty: any address on the port |
| `networkPolicy.runner.kubernetesAPI.cidrs` | list | `[]` | The Kubernetes API addresses runners and the hook Job may reach (`kubectl get endpoints kubernetes`). Empty: any address on the ports |
| `networkPolicy.runner.kubernetesAPI.ports` | list | `[443, 6443]` | |
| `networkPolicy.runner.extraEgress` | list | `[]` | More egress rules for runner pods: Git hosts, secret stores, BuildKit, registries |
| `networkPolicy.deployRunner.extraEgress` | list | `[]` | More egress rules for the deploy runner pods: Argo CD, GitOps repositories, metrics |
| `networkPolicy.steps.enabled` | boolean | `true` | The step pod policy, in `runner.kubernetes.namespace` |
| `networkPolicy.steps.internet` | boolean | `true` | Let step pods reach the internet on `ports`, except the ranges above. `false`: only DNS and the server, plus `extraEgress` |
| `networkPolicy.steps.ports` | list | `[80, 443]` | |
| `networkPolicy.steps.exceptCIDRs` | list | `[]` | More ranges step pods may not reach |
| `networkPolicy.steps.allowIPv6` | boolean | `false` | Allow IPv6 internet (except unique-local and link-local) |
| `networkPolicy.steps.extraEgress` | list | `[]` | More egress rules for step pods: an internal Git server, a package mirror, a registry |

## Install hook Job

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `bootstrap.resources` | object | requests `10m` CPU, `32Mi`, `16Mi` ephemeral storage; limits `128Mi`, `64Mi` ephemeral storage | Kubernetes resources of the hook Job's container |
| `bootstrap.activeDeadlineSeconds` | integer | `300` | The Job is failed after this long |
| `bootstrap.nodeSelector` | map | `{}` | |
| `bootstrap.tolerations` | list | `[]` | |

## Delivery

Deploy, rollback and rollout steps. Off unless set. Set these only once every server and runner
runs a release that knows them, and take them out before rolling back to one that does not. See
[Deploy with an approval](../tutorials/deploy-with-approval.md).

<!-- keys: helm -->
| Value | Type | Default | Description |
|---|---|---|---|
| `delivery.environments` | list or null | `null` | [Environments](server-config.md#environments), written as `environments`: `[{ name, target, projects, queue, protected }]`. Without any, deploy steps are refused |
| `delivery.verification.sources` | list or null | `null` | [Health sources](runner-config.md#health-sources) for verify steps. Each entry is split: `name`, `type` and `projects` go to the servers' `verification.sources`, and the whole entry to the deploy runner's `health_sources`, by name. Each is `{ name, type, url, projects }` (a source of type `check`, the deploy runner's own HTTP and TCP health proof, has no `url`: it is `{ name, type: check, projects, hosts, namespaces }` plus optionally `caFile` and `allowInsecureTLS`, and the chart admits egress from the deploy runner to exactly those namespaces and address ranges; a host written as a name cannot be written in a NetworkPolicy, so add its addresses to `hosts` or a rule to `networkPolicy.deployRunner.extraEgress`) and optionally `auth`, `headers`, `caFile`, `timeout`, `limits`, `statements`, `indices`, `timeField` and `messageField`, as in the runner reference (camelCase here). Needs `deployRunner.enabled`; credentials are secret references read from `deployRunner.secretStores`. Unset, verify steps are refused |
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
| `delivery.deployRunner.poolToken.existingSecret` | string | `""` | A Secret holding the pool token. Empty: made by the install hook Job if absent and kept on upgrade |
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
| `ai.serverSecretStores` | map or null | `null` | Secret stores the servers read model keys from, to explain failed steps themselves (the same map as `runner.secretStores`); written as `ai.secrets`. Mount any token or certificate files they refer to with `server.extraVolumes` and `server.extraVolumeMounts`. Unset, the servers explain only with models that need no key. Also the stores that `config.notifications` read channel addresses from, which then need no `ai.models` |

## What next

- [Install on Kubernetes](../howto/install-kubernetes.md) walks through an installation.
- [Server configuration](server-config.md) and [runner configuration](runner-config.md) explain
  the settings these values become.
