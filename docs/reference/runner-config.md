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
| `--token T` | `LAUNCHWAY_RUNNER_TOKEN` | never in the file | the shared runner token or a pool's token; give this or `--join`, not both. Not needed once the runner is enrolled |
| `--join J` | `LAUNCHWAY_JOIN` | never in the file | a join token, to enrol the runner once and give it an identity of its own. Not needed once the runner is enrolled |
| `--labels a,b` | | `labels` | none |
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
[runner pool's](server-config.md#runner-pools) token for a deploy runner. A runner enrolled with a
join token has no use for it: it keeps its identity (a key it made, under `<data>/identity`), and
starts without `--join` or `--token`. See
[Runners on other machines and clusters](../guide/runner-identity.md). Relative paths in the
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
  keep_failed: 0s
  start_timeout: 10m
  restricted: true
  run_as_user: 1000
  run_as_group: 1000
  user_entry: true
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
| `ai` | boolean | `false` | Whether this runner may call the AI models the server lists, for `ai` steps (and, on a deploy runner, to explain verifications). Keys come from the runner's own secret stores and never enter a step's container. See [AI models](../guide/ai.md) |
| `executor` | text | `auto` | Where steps run: `auto` (Podman or Docker if found, else the host), `container` (Podman or Docker), `podman`, `docker`, `host` (directly on the machine: only for trusted pipelines) or `kubernetes` (one pod per run) |
| `spool.max_mib` | whole number | `256` | While the server cannot be reached (no answer, or `503`), the runner keeps executing the step and keeps its log lines and its result on its own disk, under `<data>/spool`, and delivers them in order when the server answers. This is how many MiB of log lines it keeps per step; when it is full the oldest lines are dropped and one line in the log says how many. The result is never dropped. Nothing is written to disk until the first failure |
| `spool.max_age` | duration | `15m` | How old a kept log line may be before it is dropped, with the same marker. A runner that restarts during an outage delivers what it finds. The step's log shows a line such as "The server could not be reached from 14:02:10 to 14:03:40; nothing was lost." |

## Identity, labels and the network

A runner enrolled with a join token has an identity of its own and belongs to a pool; see
[Runners on other machines and clusters](../guide/runner-identity.md). These settings describe it
and its connection to the server. They are in the runner's file only: the runner built into the
server has no use for them.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `labels` | list | none | What the runner has, such as `arm64` or `gpu`. They add to the labels of its join token. A pipeline's `runner: { labels: [...] }` runs only on runners that have all of them |
| `proxy` | URL | `HTTPS_PROXY`, `HTTP_PROXY` | The proxy for the runner's requests to the server. Wins over the environment |
| `no_proxy` | text | `NO_PROXY` | Hosts reached without the proxy. Wins over the environment |
| `ca_file` | path | the system's | PEM certificates added to the system's, for a server or a proxy whose certificate a private authority signed, such as one that inspects TLS |

A runner waits for work in requests of up to 25 seconds, so a proxy must allow idle requests of at
least 30. Runners prove who they are inside each request, not with client certificates, so they
work through proxies that inspect TLS.

## What the runner accepts

`accept` is the runner's own limit on the work the server may send it. It is checked on the runner
before anything runs: no clone, no secret looked up, no container started. The server cannot change
it, because it lives only in this file. A job it does not allow is refused: the step fails without
retries, saying why, and the refusal is audited (`runner.refused`). Every list that is empty allows
anything; a list that is set allows only what matches one of its patterns. In patterns, `*` matches
any run of characters (slashes included) and `?` one character.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `accept.projects` | list of patterns | any project | Projects whose work the runner takes |
| `accept.repositories` | list of patterns | any | Repositories it clones, as URLs without credentials such as `https://git.example.com/shop/*`: the run's own and every library's |
| `accept.images` | list of patterns | any | Images steps and their containers may use, written in full (`docker.io/library/golang:1.27`, not `golang:1.27`), such as `registry.example.com/*` or `docker.io/library/golang@sha256:*` |
| `accept.images_by_digest` | boolean | `false` | Refuse images that are not pinned by digest (`name@sha256:…`) |
| `accept.executors` | list | any | The executors it may use: its own (`podman`, `docker`, `host`, `kubernetes`) for ordinary steps, `buildkit` for image steps and `deploy` for deploy steps |
| `accept.environments` | list of patterns | any | The environments a deploy runner deploys to |
| `accept.pull_requests` | boolean | allowed | `false` refuses runs of pull requests: code anyone who can open one wrote |
| `accept.drafts` | boolean | allowed | `false` refuses draft runs: pipeline text that is not in Git |
| `accept.max_timeout` | duration | none | Refuses steps that may run longer, such as `2h` |
| `accept.env_deny` | list of patterns | none | Refuses steps that set environment variables (or secrets) with these names, such as `LD_PRELOAD`, `BASH_ENV` or `GIT_*` |

Whatever its settings, a runner also refuses IDs from the server that do not look like the server's
own (they become paths), and a run whose source is a directory of another machine (only
`launchway run` makes those). An enrolled runner does not fall back to running steps on its host
when it finds no Podman or Docker, unless `executor: host` is chosen.

`accept` limits what work reaches the runner's network and secrets. It cannot make the work it does
accept safe: a server that has been taken over can still send any script for a project, repository
and image the runner accepts. Keep `accept`, the secret stores and the network as narrow as the
pool's work.

<!-- validate-config runner -->
```yaml
server: https://launchway.example.com
labels: [arm64]
accept:
  projects: [shop, "web-*"]
  repositories: ["https://git.example.com/shop/*"]
  images: ["registry.example.com/*", "docker.io/library/golang@sha256:*"]
  executors: [podman, buildkit]
  pull_requests: false
  drafts: false
  max_timeout: 2h
  env_deny: [LD_PRELOAD, BASH_ENV, ENV, "GIT_*"]
```

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
| `kubernetes.max_cpu` | quantity | none (no cap) | The most CPU one container may ask for, as a request or a limit. A default the server chose (the installation's step resource defaults) is lowered to this, and the log says so; a step or container that explicitly asks for more fails before the pod is made, naming this setting |
| `kubernetes.max_memory` | quantity | none (no cap) | The most memory one container may ask for, as a request or a limit. Server defaults are lowered to it; an explicit ask above it fails the step, naming this setting |
| `kubernetes.image_pull_secrets` | list | none | Secrets in the namespace for pulling step images |
| `kubernetes.keep_failed` | duration | `0` | A grace period before a failed or cancelled run's pod is removed. The default removes it as soon as the run ends: a running pod holds namespace quota, and a resume does not need it (see [What happens to a run's pod](#what-happens-to-a-runs-pod)) |
| `kubernetes.max_pod_age` | duration | `24h` | The longest a step pod lives, whatever the server says about its run, unless a step of it is executing on this runner. Bounds what a crashed runner or a forgotten run can leave behind. `0` turns the bound off |
| `kubernetes.start_timeout` | duration | `10m` | How long to wait for a pod's images to be pulled and its containers to start |
| `kubernetes.restricted` | boolean | `true` | Every container, services included, runs as `run_as_user` with no privilege escalation, no capabilities and the `RuntimeDefault` seccomp profile, so step pods meet Pod Security `restricted`. `HOME` is a writable `/home/launchway`. `false` runs images as their own users, often root, and needs a namespace that allows it. Pipelines cannot change this |
| `kubernetes.run_as_user` | whole number | `1000` | With `restricted`: the user containers run as. Not 0 |
| `kubernetes.run_as_group` | whole number | `1000` | With `restricted`: the group, which also owns the pod's volumes |
| `kubernetes.user_entry` | boolean | `true` | With `restricted`: give the step's user an account. A numeric user the image does not know makes `ssh`, `git` over ssh and some other tools fail with `No user exists for uid 1000`. With this on, an init container in each step image copies the image's `/etc/passwd` and `/etc/group` to a small in-memory volume, adds `launchway` (uid and gid as above, home `/home/launchway`, shell `/sbin/nologin`) in place of any entry for that uid or gid, and the step container mounts the two files read-only over the originals. `USER` and `LOGNAME` are `launchway`, `HOME` is `/home/launchway`, and each step's log starts with `Running as launchway (uid 1000, gid 1000)`. Set `false` for images that already handle a numeric user |

### The step's user

The Kubernetes executor runs steps as a numeric user (`run_as_user`) that most images' `/etc/passwd` does not list, so
`kubernetes.user_entry` (on by default) makes the user resolvable without relaxing anything: the user is still not root,
privilege escalation and all capabilities stay off, and the image is not changed (the two account files are read-only
mounts, so it also works with a read-only root filesystem). The init container needs only `/bin/sh` in the step's image,
which every step image needs anyway because steps run with `sh`; an image with no `/etc/passwd` or `/etc/group` is fine
(the files are made with `root` and `launchway`). An image that has no shell cannot be a step image at all, so there is no
shell-less case to support; services are untouched and keep their own account files. If the init container cannot run,
the step fails with a message that names the image and the setting. The container executor (`podman` or `docker`) runs
the image's own user, which the image's account files already describe, so it needs nothing.

### What happens to a run's pod

On Kubernetes each run has one step pod, which holds the run's workspace and its containers. A
running pod holds CPU and memory in the step namespace's quota for as long as it exists, so the
runner does not keep one for a run that has ended.

The runner's reaper removes a pod when any of these holds:

- the server says the run ended: succeeded, failed (at any step, including an image step, whether
  the later steps were skipped or not, and including a run that timed out) or canceled. A failed or
  canceled run's pod can be kept for a grace period with `kubernetes.keep_failed` (default 0, so no
  grace);
- the server does not know the run and this runner made the pod (pods of unknown runs that other
  runners made are left to their runner, or to the age bound);
- the pod is older than `kubernetes.max_pod_age` (24h) and no step of its run is executing on this
  runner, whatever the server says. This is what stops a crashed runner, or a run nobody holds a
  lease on, from leaking a pod.

A pod with a step executing on this runner is never removed. The reaper looks when the runner
starts (so a restarted runner clears what the crashed one left), soon after any step reports its
result, and every 30 seconds. A step that cannot start because the namespace's quota is used up
fails with the cause `infrastructure` and a message that names the quota, so `retry` with its
backoff applies, and `retry.on` must list `infrastructure` for it to be retried.

**What a resume keeps.** The run's workspace is the runner's checkout of the commit, on the
runner's disk under its data directory (kept 24 hours after it was last used), not the pod. A
resume, or a re-run from a step, makes a new pod and fills it from that checkout, so it still
starts from the same commit and the same libraries. The trade-off: files that earlier steps wrote
inside the pod (build output, downloaded dependencies) are not in the new pod, so a step that
depends on them must be run again, or hand them on as [artifacts](../guide/artifacts.md). This
costs a few seconds to start a pod and some re-work, and in return a failed run never holds quota.
An operator who prefers a resume to find the old pod may set `keep_failed` to a short period such
as `10m`; the pod then holds quota for that long, and `max_pod_age` is still the upper bound.

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
| `deploy.repos.<name>.hosted` | boolean | `false` | The repository is hosted by Launchway and `url` is its address. Deploy steps then write with their own credential, which the server gives each deployment (the environment's `write_back`), and never with a stored one; `credentials`, if any, only read. A hosted repository needs the environment's `write_back`, and `write_back` needs `hosted: true` |
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

## Health sources

On a deploy runner, `health_sources` says where the monitoring systems are, for
[verification steps](../guide/verification.md). The server's `verification.sources` lists the same
names, types and projects; the deploy runners that serve an environment's target hold the rest, because they are next
to the monitoring systems and never run anyone's script. A health source needs a `deploy` section.
Credentials are only secret references, read from the runner's own stores for each query.

<!-- keys: runner -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `health_sources.<name>.type` | text | required | `prometheus` (also Thanos, Mimir, VictoriaMetrics), `loki`, `clickhouse`, `elasticsearch` or `opensearch`, `postgres`, `http`, or `check` (the deploy runner's own health calls) |
| `health_sources.<name>.url` | URL | required (not for `check`) | The base URL. For `postgres`, a `postgres://` URL without a password |
| `health_sources.<name>.projects` | list of patterns | none: allows no project | The projects that may use the source |
| `health_sources.<name>.auth.type` | text | | `bearer`, `basic` or `header`. For `clickhouse` and `postgres` it may be left out: the user and the secret are the database account |
| `health_sources.<name>.auth.user` | text | | The user name, for `basic` and the databases |
| `health_sources.<name>.auth.header` | text | | The header that carries the secret, for `header` |
| `health_sources.<name>.auth.secret.store` | name | | The secret store holding the credential |
| `health_sources.<name>.auth.secret.key` | text | | The key in the store |
| `health_sources.<name>.auth.secret.field` | text | | The field of the key |
| `health_sources.<name>.headers` | mapping | none | Fixed headers that are not secret, such as `X-Scope-OrgID` to choose a tenant. `Authorization`, `Cookie` and the like are refused: use `auth` |
| `health_sources.<name>.ca_file` | path | the system's | PEM certificates to trust for this source |
| `health_sources.<name>.timeout` | duration | `10s` | The time one query may take, at most `60s` |
| `health_sources.<name>.limits.max_series` | whole number | `1` | Series a metric query may return. It can only be 1: a verification judges one series per query |
| `health_sources.<name>.limits.max_points` | whole number | `500` | Points of one metric query |
| `health_sources.<name>.limits.max_lines` | whole number | `5000` | Log lines of one query |
| `health_sources.<name>.limits.max_bytes` | whole number | 8 MiB | Bytes of one response |
| `health_sources.<name>.indices` | list of patterns | required for `elasticsearch` | The only indices pipelines may search |
| `health_sources.<name>.time_field` | text | `@timestamp` | `elasticsearch`: the document's time |
| `health_sources.<name>.message_field` | text | `message` | `elasticsearch`: the document's log line |
| `health_sources.<name>.hosts` | list | none | `check` only: the hosts a pipeline's health check may call: names (`api.example.com`, `*.shop.svc.cluster.local`), addresses and ranges. An address literal must be inside a listed range |
| `health_sources.<name>.namespaces` | list | none | `check` only: Kubernetes namespaces whose Services may be called as `<service>.<namespace>`, `.svc` or `.svc.cluster.local`. A `check` source needs `hosts` or `namespaces`; an empty list allows nothing |
| `health_sources.<name>.allow_insecure_tls` | boolean | `false` | `check` only: lets a pipeline's check skip certificate verification |
| `health_sources.<name>.statements.<name>.kind` | text | required | `metric` or `logs` |
| `health_sources.<name>.statements.<name>.sql` | text | | `clickhouse` and `postgres`: the query. ClickHouse uses parameters such as `{service:String}`, with `{start:DateTime64(3)}` and `{end:DateTime64(3)}` always passed; PostgreSQL uses `{{name}}` placeholders, sent as typed parameters, with `{{start}}` and `{{end}}`. A metric returns `t` and `value` columns (or one `value`); logs return `message` and optionally `t` |
| `health_sources.<name>.statements.<name>.path` | text | | `http`: the path added to the source's URL |
| `health_sources.<name>.statements.<name>.method` | text | `GET` | `http`: `GET` or `POST` |
| `health_sources.<name>.statements.<name>.body` | text | | `http` `POST`: a JSON document; `{{name}}` placeholders in its string values are replaced |
| `health_sources.<name>.statements.<name>.value` | text | | `http`: a dotted path into the JSON answer (numbers index arrays): a number, an array of numbers, or an array of `[time, value]` pairs for metrics; an array of strings for logs |
| `health_sources.<name>.statements.<name>.params` | mapping | none | The parameters a pipeline must pass, by name and type: a ClickHouse type such as `String` or `DateTime64(3)`; for PostgreSQL `text`, `int`, `float` or `timestamptz`; for `http` `string` |
| `health_sources.<name>.statements.<name>.projects` | list of patterns | the source's | Narrows which of the source's projects may run the statement |

Types `clickhouse`, `postgres` and `http` run only the named `statements`; pipelines never write
SQL of their own. Every query is read-only where the source can enforce it (ClickHouse with
`readonly=1`, PostgreSQL in a `READ ONLY` transaction, `http` only to the configured host and
without following redirects), has a timeout, and is bounded by the limits.

<!-- validate-config runner -->
```yaml
server: https://launchway.example.com
secrets:
  vault: { type: vault, address: https://vault.example.com:8200, role: launchway-deploy }
deploy:
  targets:
    main-cluster:
      kubernetes: { in_cluster: true }
      applications: [ { projects: [shop], names: [shop] } ]
  repos:
    gitops:
      url: https://git.example.com/team/gitops.git
      paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
health_sources:
  prometheus:
    type: prometheus
    url: http://prometheus-operated.monitoring.svc:9090
    projects: ["*"]
  loki:
    type: loki
    url: http://loki-gateway.monitoring.svc
    headers: { X-Scope-OrgID: platform }
    auth: { type: bearer, secret: { store: vault, key: ci/loki, field: token } }
    projects: [shop]
    timeout: 15s
    limits: { max_points: 500, max_lines: 5000, max_bytes: 8388608 }
  clickhouse:
    type: clickhouse
    url: https://clickhouse.example.com:8443
    projects: [shop]
    statements:
      service_errors:
        kind: logs
        params: { service: String, version: String }
        sql: >
          SELECT ts AS t, message FROM logs
          WHERE service = {service:String} AND version = {version:String} AND level = 'error'
            AND ts >= {start:DateTime64(3)} AND ts < {end:DateTime64(3)}
          ORDER BY ts
```

## What next

- [Server configuration](server-config.md): environments and runner pools that deploy runners
  serve.
- [Helm values](helm-values.md): the chart renders this file for its runners from `runner.*` and
  `delivery.deployRunner.*`.
