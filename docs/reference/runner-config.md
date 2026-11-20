<!--
How this page is checked (read before editing).

Each table marked with the HTML comment "keys: runner" (written as a comment on the line
before the table) lists settings: its rows start with the setting's full path in a code span,
such as `kubernetes.namespace`. A list's items are written [] (`images.builders[].name`) and a
map's entries <name> (`secrets.<name>.type`). The Go test internal/config/docs_reference_test.go
finds every setting a runner's --config file may hold, by reflection over the structs the
loader decodes into (it refuses unknown keys), and fails if a setting has no row here, or a row
names a setting that does not exist. The server's runner: section is checked against this page
too, without the prefix.

A ```yaml block preceded by a line with the HTML comment "validate-config runner" is loaded
with the runner's loader by the same test.
-->

# Runner configuration reference

This page lists every setting of a runner's configuration file, read by
`launchway runner --config runner.yaml`, with the flags and environment variables that override
them. The same settings, except `server`, `name`, `data` and `deploy`, make up the `runner:`
section of the [server's configuration](server-config.md#runner), for the runner built into the
server. It is for operators who run [runners](../glossary.md#runner); for a walk-through, see
[Connect a runner](../howto/connect-runner.md).

## How settings are read

A runner takes each setting from the first of:

1. a flag on the command line;
2. an environment variable, for the server URL and the token only;
3. the `--config` file;
4. the default.

| Flag | Variable | File setting | Default |
|---|---|---|---|
| `--server URL` | `LAUNCHWAY_SERVER` | `server` | required |
| `--token T` | `LAUNCHWAY_RUNNER_TOKEN` | never in the file | required |
| `--name NAME` | | `name` | the host name |
| `--data DIR` | | `data` | `launchway-runner` |
| `--concurrency N` | | `concurrency` | `2` |
| `--executor E` | | `executor` | `auto` |
| `--k8s-context C` | | `kubernetes.context` | the kubeconfig's current context |
| `--k8s-namespace NS` | | `kubernetes.namespace` | the context's namespace, else `default` |
| `--k8s-unrestricted` | | `kubernetes.restricted: false` | restricted. The flag also drops `run_as_user` and `run_as_group` from the file |
| `--secrets-config FILE` | | `secrets` | none. The file holds the same map as `secrets`, and replaces the file's `secrets` entirely |
| `--health-listen ADDR` | | | none. Serves `/healthz` (the process is up) and `/readyz` (the server answered within two minutes), such as `:8081` |
| `--config FILE` | | | none |

The token is the shared runner token for build runners, or a
[runner pool's](server-config.md#runner-pools) token for a deploy runner. Relative paths in the
file are relative to the file's directory. Unknown settings are refused, naming the line.

`launchway run`, which runs a pipeline on your own machine, takes `--executor`, the `--k8s-*` flags,
`--secrets-config FILE` and `--images-config FILE` (a file with an `images:` section as below). It
also has an `env` secret store over your own environment.

## A complete example

<!-- validate-config runner -->
```yaml
server: https://launchway.example.com
name: build-2
data: /var/lib/launchway-runner
concurrency: 4
executor: kubernetes
kubernetes:
  namespace: builds
  helper_image: registry.example.com/busybox:1.37
  service_account: builder
  node_selector: { pool: builds }
  cpu: 250m
  memory: 256Mi
  memory_limit: 4Gi
  image_pull_secrets: [registry-credentials]
  keep_failed: 2h
  start_timeout: 10m
  restricted: true
  run_as_user: 1000
  run_as_group: 1000
secrets:
  vault:
    type: vault
    address: https://vault.example.com:8200
    role: launchway
    projects: [payments, "web-*"]
    keys: ["apps/*/*"]
  mounted:
    type: file
    dir: /var/run/launchway-secrets
reports:
  max_files: 100
  max_file_mib: 10
  max_total_mib: 20
```

## Runner settings

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `server` | URL | required | The server's address. Runners only connect outbound, to any server sharing the database |
| `name` | text | the host name | Shown on the Runners page. The runner's identity is `runner-<name>` |
| `data` | directory | `launchway-runner` | Where runs' workspaces are cloned |
| `concurrency` | whole number | `2` | Steps it runs at once. All of a run's steps go to the runner that took its first step, so a run's parallel steps share this |
| `executor` | text | `auto` | Where steps run: `auto` (Podman or Docker if found, else the host), `container` (Podman or Docker), `podman`, `docker`, `host` (directly on the machine: only for trusted pipelines) or `kubernetes` (one pod per run) |

## Kubernetes

Settings of the `kubernetes` executor: each run gets one pod in `namespace`, with a helper container
holding the workspace and a container per image or toolbox.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `kubernetes.kubeconfig` | path | the usual rules, or in-cluster | The kubeconfig file |
| `kubernetes.context` | text | the current context | The kubeconfig context |
| `kubernetes.namespace` | text | the context's namespace, else `default` | Where step pods run. It must exist; the runner checks it can list pods there when it starts |
| `kubernetes.helper_image` | image | `docker.io/library/busybox:1.37` | The helper container's image. It needs `sh` and `tar` |
| `kubernetes.service_account` | text | none | The service account step pods run as. Without it, no token is mounted |
| `kubernetes.node_selector` | mapping | none | Node labels step pods must match |
| `kubernetes.cpu` | quantity | `50m` | CPU request of each container, unless the pipeline's container asks for its own |
| `kubernetes.memory` | quantity | `64Mi` | Memory request of each container, unless the pipeline asks |
| `kubernetes.memory_limit` | quantity | none | Memory limit of each container that does not ask for memory |
| `kubernetes.image_pull_secrets` | list | none | Secrets in the namespace for pulling step images |
| `kubernetes.keep_failed` | duration | `2h` | How long a failed or cancelled run's pod is kept, so a resume can reuse its workspace |
| `kubernetes.start_timeout` | duration | `10m` | How long to wait for a pod's images to be pulled and its containers to start |
| `kubernetes.restricted` | boolean | `true` | Every container, services included, runs as `run_as_user` with no privilege escalation, no capabilities and the `RuntimeDefault` seccomp profile, so step pods meet Pod Security `restricted`. `HOME` is a writable `/home/launchway`. `false` runs images as their own users, often root, and needs a namespace that allows it. Pipelines cannot change this |
| `kubernetes.run_as_user` | whole number | `1000` | With `restricted`: the user containers run as. Not 0 |
| `kubernetes.run_as_group` | whole number | `1000` | With `restricted`: the group, which also owns the pod's volumes |

## Secrets

`secrets` maps store names (`^[a-z][a-z0-9_-]{0,62}$`) to secret stores. A pipeline's
[secret references](pipeline.md#secret-references) name a store and a key; the runner looks values
up just before the step runs. A runner has only the stores you configure. See
[Manage secrets](../howto/secrets.md).

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `secrets.<name>.type` | text | required | `env` (a variable of the runner process, not `LAUNCHWAY_*`), `file` (a file in a directory, such as a mounted Kubernetes Secret) or `vault` (a KV version 2 engine of HashiCorp Vault or OpenBao; references need `field`) |
| `secrets.<name>.projects` | list of patterns | every project | The projects whose steps may read the store. `*` does not cross `/` |
| `secrets.<name>.keys` | list of patterns | every key | The keys that may be read |
| `secrets.<name>.dir` | directory | required for `file` | The directory; nothing outside it can be read |
| `secrets.<name>.address` | URL | required for `vault` | Such as `https://vault.example.com:8200` |
| `secrets.<name>.mount` | text | `secret` | `vault`: the KV version 2 mount |
| `secrets.<name>.namespace` | text | none | `vault`: sent as `X-Vault-Namespace` |
| `secrets.<name>.ca_file` | path | the system's | `vault`: PEM certificates to trust |
| `secrets.<name>.token_env` | variable name | | `vault`: log in with the token in this variable. Exactly one of `token_env`, `token_file` or `role` |
| `secrets.<name>.token_file` | path | | `vault`: or with the token in this file |
| `secrets.<name>.role` | text | | `vault`: or with Kubernetes auth, as this role |
| `secrets.<name>.auth_path` | text | `kubernetes` | `vault` with `role`: where Kubernetes auth is mounted |
| `secrets.<name>.jwt_file` | path | `/var/run/secrets/kubernetes.io/serviceaccount/token` | `vault` with `role`: the service account token |

Settings that belong to another type are refused. Stores are checked when the runner starts, without
contacting them: a `file` store's directory must exist, and a `token_env` variable must be set. A store
that cannot be reached when a step needs it is asked again for 30 seconds, then the step is handed
back to run later.

## Reports

Limits on the report files collected after each step. `0` means the default.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `reports.max_files` | whole number | `100` | Files collected per step attempt, the first in name order |
| `reports.max_file_mib` | whole number | `10` | MiB read of one file; a larger one is cut. At most 1024 |
| `reports.max_total_mib` | whole number | `20` | MiB read of all of an attempt's files. At most 1024, and no more than the server accepts ([`tests.max_upload_mib`](server-config.md#tests)) |

## Images

`images` lets the runner take [image steps](pipeline.md#kind-image): the BuildKit daemons it sends
builds to and the registries it pushes to. Unset, the runner takes no image steps. See
[Build and sign an image](../tutorials/build-and-sign-image.md).

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `images.builders[].name` | name | required | `^[a-z0-9][a-z0-9_-]{0,62}$`, unique |
| `images.builders[].address` | text | required | `tcp://host:port` (with mutual TLS) or `unix:///path` for a daemon on the runner's machine |
| `images.builders[].ca_file` | path | required for `tcp://` | The CA that signed the daemon's certificate |
| `images.builders[].cert_file` | path | required for `tcp://` | The runner's client certificate. Goes with `key_file` |
| `images.builders[].key_file` | path | required for `tcp://` | Its key |
| `images.builders[].server_name` | text | the address's host | The name in the daemon's certificate |
| `images.builders[].insecure` | boolean | `false` | A `tcp://` daemon without TLS. For tests on one machine only |
| `images.builders[].platforms` | list | every platform the daemon builds | The only platforms built here, such as `[linux/arm64]` |
| `images.builders[].projects` | list of patterns | every project | The projects that may build here |
| `images.max_context_mib` | whole number | `2048` | The largest build context a step may send, after `.dockerignore` |
| `images.max_image_mib` | whole number | `20480` | The largest image (all platforms, compressed) accepted back from a builder |

An image step uses the first builder that serves its project and every platform it asks for, or
one builder per platform when no single one does.

### Registries

`images.registries` maps the names pipelines use (`tags: [{ registry: harbor, ... }]`) to
registries. Names match `^[a-z0-9][a-z0-9_-]{0,62}$`.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `images.registries.<name>.host` | text | required | The registry as written in image references: `harbor.example.com` or `localhost:5000` |
| `images.registries.<name>.credentials.username` | text | | The push (and pull) account. Written in place, or as `username_from` |
| `images.registries.<name>.credentials.username_from.store` | name | | Or the user name from a secret store |
| `images.registries.<name>.credentials.username_from.key` | text | | |
| `images.registries.<name>.credentials.username_from.field` | text | | |
| `images.registries.<name>.credentials.password.store` | name | required with `credentials` | The password or token, always from a secret store |
| `images.registries.<name>.credentials.password.key` | text | required with `credentials` | |
| `images.registries.<name>.credentials.password.field` | text | | |
| `images.registries.<name>.pull_credentials.username` | text | | A read-only account for pulling base images and reading the cache. As `credentials` |
| `images.registries.<name>.pull_credentials.username_from.store` | name | | |
| `images.registries.<name>.pull_credentials.username_from.key` | text | | |
| `images.registries.<name>.pull_credentials.username_from.field` | text | | |
| `images.registries.<name>.pull_credentials.password.store` | name | | |
| `images.registries.<name>.pull_credentials.password.key` | text | | |
| `images.registries.<name>.pull_credentials.password.field` | text | | |
| `images.registries.<name>.pull_projects` | list of patterns | every project | The projects whose builds get `pull_credentials`. Needs `pull_credentials` |
| `images.registries.<name>.ca_file` | path | the system's | A CA the runner trusts for this registry |
| `images.registries.<name>.insecure` | boolean | `false` | Plain HTTP. Only for a registry on the runner's machine or in tests |
| `images.registries.<name>.push[].projects` | list of patterns | required | Projects a push rule allows |
| `images.registries.<name>.push[].repositories` | list of patterns | required | Repositories they may push to, without the host: `team/shop`, `team/*` (one level) or `team/**` (any depth). Nothing is pushed without a rule |

Pull-request and draft runs never push, never write the cache and never read `credentials`; they
get `pull_credentials` only when they may read secrets.

### Scanner and signing

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `images.scanner.image` | image pinned by digest | a pinned Trivy | The image that makes SBOMs and finds vulnerabilities, run inside the builder. Must end in `@sha256:...` |
| `images.scanner.db_repository` | text | the scanner's own | Where it downloads its vulnerability database, for installations that mirror it |
| `images.scanner.java_db_repository` | text | the scanner's own | The same for its Java database |
| `images.signing.key_file` | path | required with `signing` | The runner's own cosign key, for steps that say `sign: true`. Steps never see it |
| `images.signing.password_file` | path | none | The key's password, if it is encrypted |

## Deploy

A `deploy` section makes the runner a deploy runner: it does the work of
[deploy, rollback and rollout steps](pipeline.md#kind-deploy) for the targets listed, through the
Kubernetes API with its own identity, and runs no other steps. Its token must be a
[runner pool's](server-config.md#runner-pools). `executor` and `kubernetes` do not apply.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `deploy.targets.<name>.kubernetes.in_cluster` | boolean | `false` | Use the pod's own service account. Either this, or `context` |
| `deploy.targets.<name>.kubernetes.kubeconfig` | path | the usual kubeconfig files | With `context` |
| `deploy.targets.<name>.kubernetes.context` | text | | The kubeconfig context. The current context is never used |
| `deploy.targets.<name>.argocd_namespace` | text | `argocd` | Where the Argo CD Applications are |
| `deploy.targets.<name>.applications[].projects` | list of patterns | required | Projects a rule allows |
| `deploy.targets.<name>.applications[].names` | list of patterns | required | Applications they may deploy. At least one rule is required |
| `deploy.targets.<name>.rollout_namespaces` | list | none | Namespaces whose Argo Rollouts rollouts deploy steps may watch |
| `deploy.targets.<name>.refresh` | boolean | `true` | Ask Argo CD to look at the repository at once after a commit (the refresh annotation). Needs patch on the Application; without it, skipped with a note in the log |
| `deploy.repos.<name>.url` | URL or path | required | The GitOps repository the runner clones and pushes to, by the name pipelines use (`write_back.repo`). No credentials in the URL |
| `deploy.repos.<name>.argocd_url` | URL | `url` | The repository's address as Argo CD's Applications write it, when it differs |
| `deploy.repos.<name>.credentials.store` | name | none | The token or password for HTTP basic authentication, from a secret store |
| `deploy.repos.<name>.credentials.key` | text | | |
| `deploy.repos.<name>.credentials.field` | text | | |
| `deploy.repos.<name>.username` | text | `launchway` | The user name sent with the credentials |
| `deploy.repos.<name>.paths[].projects` | list of patterns | required | Projects a rule allows |
| `deploy.repos.<name>.paths[].files` | list of patterns | required | Files they may change; `**` matches any number of directories. At least one rule is required |
| `deploy.repos.<name>.author.name` | text | `Launchway` | The commits' author |
| `deploy.repos.<name>.author.email` | text | `launchway@localhost` | |

Target and repository names match `^[a-z0-9][a-z0-9_-]{0,62}$`. At least one target is required.

<!-- validate-config runner -->
```yaml
server: https://launchway.example.com
deploy:
  targets:
    main-cluster:
      kubernetes: { in_cluster: true }
      argocd_namespace: argocd
      applications: [ { projects: [shop], names: [shop] } ]
      rollout_namespaces: [shop]
  repos:
    gitops:
      url: https://git.example.com/team/gitops.git
      argocd_url: http://git-http.git.svc:3000/team/gitops.git
      credentials: { store: vault, key: ci/gitops, field: token }
      paths: [ { projects: [shop], files: ["apps/shop/**", "helm-values/shop.yaml"] } ]
secrets:
  vault: { type: vault, address: https://vault.example.com:8200, role: launchway-deploy }
```

## What next

- [Server configuration](server-config.md): environments and runner pools that deploy runners
  serve.
- [Helm values](helm-values.md): the chart renders this file for its runners from `runner.*` and
  `delivery.deployRunner.*`.
