<!--
How this page is checked (read before editing).

Each table marked with the HTML comment "keys: server" (written as a comment on the line
before the table) lists settings: its rows start with the setting's full path in a code span,
such as `auth.oidc.issuer`. A list's items are written [] (`projects[].name`) and a map's
entries <name> (`projects[].webhooks.<name>.token_env`). The Go test
internal/config/docs_reference_test.go finds every setting launchway.yaml may hold, by
reflection over the structs the loader decodes into (it refuses unknown keys), and fails if a
setting has no row here, or a row names a setting that does not exist. Settings under runner.
other than runner.enabled are documented once, in runner-config.md, without the prefix.

A ```yaml block preceded by a line with the HTML comment "validate-config server" is loaded
with the server's loader by the same test.
-->

# Server configuration reference

This page lists every setting of the server's configuration file, `launchway.yaml`, read by
`launchway server --config FILE`: type, default, allowed values and the environment variables that
override it. It is for operators who install and run Launchway. For a walk-through, see
[Install on a single server](../howto/install-single-server.md); for the Helm chart, which writes
this file for you, see [Helm values](helm-values.md).

## What stays in the file, and what moved

Since schema 23 this file is **bootstrap only**: it holds what a server needs before it can read
the database, and nothing an administrator manages day to day.

| Stays in the file | Why |
|---|---|
| `listen`, `data`, `database`, `database_wait`, `resilience.stall_budget`, `user` | Where the server runs and how it reaches the database |
| `server.trust_proxy`, `server.drain_delay` | How it sits behind a proxy or load balancer |
| `auth.mode`, `auth.oidc`, `auth.idle_timeout`, `auth.session_lifetime`, `auth.allow_anonymous` | Sign-in must work before anyone can change anything |
| `auth.admins` | The first administrators, and the way back in if the role bindings lock everyone out |
| `runner` (the built-in runner and its secret stores, the AI secret stores) | Process settings of the machine the server runs on |
| `reports.origin` | The address the server is reached at |

Everything else lives in the database: `projects`, `libraries`, `environments`, `runner_pools`,
`runner_admin`, `auth.access` (role bindings), `auth.tokens`, `policy`, `verification`, `ai.models`,
`approvals`, `drafts`, `pull_requests`, `queues`, `schedules`, `loops`, `tests`, `findings`, `reports` (except
`origin`), `artifacts` (limits, retention and where artifacts are kept) and `mcp`. The sections below
that describe them document the **keys of the import format**: the keys a first start imports, that
`launchway config export` writes and `launchway config import` reads. A server that starts with a
database that already holds configuration ignores them, says so for each key, and names the import.
See [Configuration and levels](../guide/configuration.md) and [Configuration objects](config-objects.md).

## How the file is read

- **Unknown settings are refused.** A misspelt key stops the server from starting, naming the line.
- **A missing file gives the defaults.** `launchway server` without `--config` reads
  `launchway.yaml` in the current directory if it exists.
- **Relative paths are relative to the file's directory**: `data`, a SQLite `database`, local
  repository paths in `projects` and `libraries`, `artifacts.dir`, every `*_file` setting
  (`auth.oidc.client_secret_file`, `runner_pools[].token_file`, webhook secret files) and the
  file paths under [`runner`](runner-config.md).
- **Secrets are never in the file.** Client secrets, pool tokens and webhook secrets are read
  from an environment variable or a file the setting names; secret values a step needs stay in
  the runners' [secret stores](runner-config.md#secrets).
- **Every server that shares a database** must have the same `environments`, `runner_pools` and
  `policy`.

Mistakes in the settings below are reported when the file is read, before the server listens,
unless the table says otherwise.

## Command line and environment

| Flag or variable | Overrides | Description |
|---|---|---|
| `--config FILE` | | The file to read. Default `launchway.yaml` |
| `--listen ADDR` | `listen` | The address to listen on |
| `--data DIR` | `data` | The data directory |
| `LAUNCHWAY_DATABASE` | `database` | The database: a SQLite file or a `postgres://` URL. Keeps a password out of the file |
| `LAUNCHWAY_RUNNER_TOKEN` | the stored runner token | The token runners present. Without it, the first server creates a random token, keeps it in the database so every server shares it, and writes it to `<data>/runner-token` |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | | Credentials of the S3 artifact store. `AWS_ACCESS_KEY_ID_FILE` and `AWS_SECRET_ACCESS_KEY_FILE` name files instead |
| `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_<n>`, `GIT_CONFIG_VALUE_<n>` | | Git settings for fetching and pushing, such as an `http.<url>.extraHeader` with a token |

A flag wins over the file. The variables named by settings (`auth.oidc.client_secret_env`,
`runner_pools[].token_env`, webhook `*_env` settings) are read when the server starts.

## A complete example

<!-- validate-config server -->
```yaml
listen: 0.0.0.0:8080
data: /var/lib/launchway
database: postgres://launchway@db.example.com:5432/launchway   # or LAUNCHWAY_DATABASE
database_wait: 60s
server:
  trust_proxy: true
  drain_delay: 5s
auth:
  mode: oidc
  oidc:
    issuer: https://sso.example.com/realms/acme
    client_id: launchway
    client_secret_env: LAUNCHWAY_OIDC_SECRET
    redirect_url: https://launchway.example.com/auth/callback
  access:
    - { group: platform-admins, role: admin }
    - { group: payments, role: operator, projects: ["payments-*"], environments: [staging] }
    - { group: everyone, role: viewer }
  tokens: { max_days: 180 }
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    branch: main
    poll: 30s
    push: { branch_prefix: launchway/, allow_direct: ["release/*"] }
    git_webhook: { kind: github, secret_env: PAYMENTS_HOOK_SECRET }
    webhooks:
      deploy-request: { hmac_secret_env: PAYMENTS_DEPLOY_HMAC }
libraries:
  - { name: ci-templates, repo: https://git.example.com/platform/ci-templates.git }
runner:
  enabled: false        # steps run on separate runners
approvals:
  allow_self: false
drafts:
  allow: operator
  secrets: false
pull_requests:
  secrets: false
artifacts:
  store: s3
  s3: { endpoint: https://s3.eu-west-1.amazonaws.com, region: eu-west-1, bucket: launchway-artifacts }
  retain: 30d
environments:
  - { name: staging, target: main-cluster, projects: ["*"] }
  - { name: prod, target: main-cluster, projects: [payments], queue: newest, protected: true }
runner_pools:
  - { name: deploy-main, token_env: LAUNCHWAY_POOL_DEPLOY_MAIN_TOKEN, targets: [main-cluster] }
```

## Top-level settings

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `listen` | address | `127.0.0.1:8080` | Where the server listens for the API, the web UI and runners. With `auth.mode: none` it must be a loopback address unless `auth.allow_anonymous` is true |
| `data` | directory | `launchway-data` | Holds the repository mirrors, the built-in runner's workspaces, the default SQLite database and the default artifact store. Made absolute |
| `database` | path or URL | `<data>/launchway.db` | A SQLite file for one server, or a `postgres://` URL that several servers share. `LAUNCHWAY_DATABASE` overrides it |
| `database_wait` | duration | `60s` | How long a starting server keeps trying a database that does not answer. Checked when the server starts |
| `resilience.stall_budget` | duration | `120s` | How long a request waits for a database that is briefly gone (a failover, a restart) before it is answered `503` with `Retry-After`. Transient errors (connection refused or reset, a failover in progress, a read-only standby, serialization failures, deadlocks, too many connections) are retried inside this budget in the store layer; constraint, syntax and not-found errors never are. A statement cut off in the middle is repeated only if it is a read or its whole transaction was rolled back; a cut-off commit is reported, never repeated blindly |
| `user` | text | the operating system user, else `local` | Who every action is recorded as when sign-in is off (`auth.mode: none`) |

## Server

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `server.trust_proxy` | boolean | `false` | A reverse proxy in front sets `X-Forwarded-Proto` and `X-Forwarded-For`, and clients cannot reach the server except through it. Set it only then |
| `server.drain_delay` | duration | `0s` | How long a stopping server keeps serving after `/readyz` reports not ready, so load balancers that check `/readyz` move away first. Checked when the server starts |

## Auth

See [Set up sign-in and roles](../howto/oidc-and-roles.md) and [Roles](roles.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.mode` | text | `none` | `none`: no sign-in, everyone acts as `user` with full rights. `oidc`: sign in with an OpenID Connect provider |
| `auth.allow_anonymous` | boolean | `false` | Lets a server with `mode: none` listen on an address other than loopback, when something in front already decides who may reach it |
| `auth.idle_timeout` | duration | `8h` | A session nobody has used this long ends. At least `1m`; at most `session_lifetime` |
| `auth.session_lifetime` | duration | `24h` | Every session ends this long after sign-in. At least `1m` |
| `auth.tokens.max_days` | whole number | `365` | The longest an API token may last, in days, 1 to 3650. `0` means the default |

### OpenID Connect

Required with `mode: oidc`: `issuer`, `client_id`, `redirect_url` and one of
`client_secret_env` or `client_secret_file`.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.oidc.issuer` | URL | | The provider's issuer, exactly as in its discovery document. `http` or `https` |
| `auth.oidc.client_id` | text | | The client ID registered with the provider |
| `auth.oidc.client_secret_env` | variable name | | An environment variable holding the client secret |
| `auth.oidc.client_secret_file` | path | | Or a file holding it |
| `auth.oidc.redirect_url` | URL | | This server's address followed by `/auth/callback`. Cookies are marked `Secure` when it starts with `https://` |
| `auth.oidc.scopes` | list | `[openid, profile, email, groups]` | The scopes to ask for |
| `auth.oidc.username_claim` | text | `preferred_username`, then `email`, then `sub` | The claim that names a person. Choose one people cannot change themselves |
| `auth.oidc.groups_claim` | text | `groups` | The claim, in the ID token or the userinfo response, that lists a person's groups |

`auth.admins` names the first administrators: people or groups who are administrators of every project
whatever the role bindings in the database say. It stays in the file so that a new installation has
someone who can sign in and set the rest up, and so that bindings that lock everyone out can be
repaired by editing a file.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.admins.users` | list of text | | Users, as `username_claim` gives them, who are administrators of every project |
| `auth.admins.groups` | list of text | | Groups, as `groups_claim` gives them, whose members are administrators of every project |

### Access bindings

Role bindings are kept in the database since schema 23; `auth.access` is the format the first start
imports.

`auth.access` is a list of bindings. Each grants a role to one group or one user. With
`mode: oidc` at least one is required; with `mode: none` it must be empty. Where several match,
the highest role wins.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.access[].group` | text | | A group, as `groups_claim` gives it. One of `group` or `user` |
| `auth.access[].user` | text | | A user, as `username_claim` gives it |
| `auth.access[].role` | text | required | `viewer`, `operator` or `admin` |
| `auth.access[].projects` | list of patterns | every project | The projects it grants the role on |
| `auth.access[].environments` | list of patterns | every environment | Limits what it grants on deployments (rollout commands, approvals that name a deployment) to these environments |

## Projects

`projects` lists the repositories to watch. Projects can be added only here.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `projects[].name` | name | required | `^[a-z0-9][a-z0-9-]{0,62}$`, unique |
| `projects[].repo` | URL or path | required | What the servers and runners clone: `https://...`, `ssh://...`, or a local path. Credentials go in the URL or in `GIT_CONFIG_*` variables |
| `projects[].branch` | branch | `main` | The default branch: manual runs start from it, and schedules, webhooks and the pull-request setting are read from its pipeline |
| `projects[].poll` | duration | `30s` | How often to look for pushes and pull requests. At least `5s` (shorter is raised to 5s). Checked when the server starts |
| `projects[].drafts.allow` | text | the server's `drafts.allow` | Overrides [`drafts.allow`](#drafts-and-pull-requests) for this project |
| `projects[].drafts.secrets` | boolean | the server's `drafts.secrets` | Overrides `drafts.secrets` for this project |
| `projects[].pull_requests.secrets` | boolean | the server's `pull_requests.secrets` | Overrides `pull_requests.secrets` for this project |
| `projects[].queues.max_wait` | duration or `off` | the server's `queues.max_wait` | Overrides `queues.max_wait` for this project |
| `projects[].push.branch_prefix` | text | `launchway/` | Lets the pipeline editor save to the repository. Saves go to a new branch `<prefix><you>/<date-time>`. Letters, digits, `-`, `_`, `.` and `/`, at most 100 |
| `projects[].push.allow_direct` | list of patterns | none | Branches that may be saved to directly |
| `projects[].schedules.enabled` | boolean | the server's `schedules.enabled` | Turns this project's [schedules](pipeline.md#schedule) on or off |
| `projects[].git_webhook.kind` | text | | The Git host's webhook, which makes the server poll at once: `github`, `gitea`, `forgejo` or `gitlab`. The host calls `POST /api/v1/projects/<name>/git-hook` |
| `projects[].git_webhook.secret_env` | variable name | | The webhook's secret, from an environment variable. Exactly one of `secret_env` or `secret_file` |
| `projects[].git_webhook.secret_file` | path | | Or from a file |
| `projects[].webhooks.<name>.token_env` | variable name | | What callers of the pipeline's [webhook trigger](pipeline.md#webhook) `<name>` must prove: a bearer token from this variable. Exactly one of the four settings. A trigger not listed cannot be called |
| `projects[].webhooks.<name>.token_file` | path | | Or a bearer token from a file |
| `projects[].webhooks.<name>.hmac_secret_env` | variable name | | Or an HMAC-SHA256 signing secret from a variable (preferred: a signed request cannot be replayed) |
| `projects[].webhooks.<name>.hmac_secret_file` | path | | Or a signing secret from a file |

### A pipeline from a library

A project may get its pipeline from a library template instead of a file in its repository. At a
commit that has its own `.launchway/pipeline.yaml`, that file wins.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `projects[].pipeline.library` | name | required | One of [`libraries`](#libraries) |
| `projects[].pipeline.version` | text | required | A tag, branch or full commit id |
| `projects[].pipeline.template` | name | required | The template to run |
| `projects[].pipeline.with` | mapping | none | Values for the template's parameters |
| `projects[].pipeline.on.push.branches` | list of patterns | none | As a pipeline's [`on.push`](pipeline.md#push) |
| `projects[].pipeline.on.push.tags` | list of patterns | none | |
| `projects[].pipeline.on.pull_request` | empty mapping | none | Write `pull_request: {}` to run pull requests. It takes no settings |
| `projects[].pipeline.supersede` | boolean | `false` | As a pipeline's [`supersede`](pipeline.md#top-level-keys) |
| `projects[].pipeline.lane.name` | name | the project's name | The lane's name, as a pipeline's [`lane`](pipeline.md#lane): runs of the project take turns. Needs schema 24; not with `supersede` |
| `projects[].pipeline.lane.per` | list | none | `ref`, `pull_request` or `input:<name>`: what splits the lane |
| `projects[].pipeline.lane.when_busy` | `wait`, `cancel` or `skip` | `wait` | What a run that arrives while the lane is held does |
| `projects[].pipeline.lane.queue` | `newest` or `all` | `newest` | For `wait`: whether a newer waiting run replaces an older one |
| `projects[].pipeline.true` | | | Not written by hand: tools that read YAML 1.1 (Helm among them) turn an unquoted `on` into `true`. It is read as `on`; having both is refused |
| `projects[].default_pipeline.enabled` | boolean | `true` | `false`: the project runs no [flight plan](config-objects.md#a-flight-plan-for-every-repository). A repository file still runs |
| `projects[].default_pipeline.with` | mapping | none | Other values for some parameters of the flight plan's template; the rest stay as the flight plan sets them. Ignored when the project has its own `pipeline` |

## Libraries

`libraries` lists the repositories pipelines may use templates from, and policy sources may be
read from. A pipeline names one of these by name; it can never give an address.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `libraries[].name` | name | required | `^[a-z][a-z0-9-]{0,39}$`, unique |
| `libraries[].repo` | URL or path | required | Fetched with the same credentials as projects |

## Runner

The built-in runner runs steps inside the server process's machine. Its settings are the same as
a separate runner's: see the [runner configuration reference](runner-config.md), where
`concurrency` is written here as `runner.concurrency`, `kubernetes.namespace` as
`runner.kubernetes.namespace`, and so on.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner.enabled` | boolean | `true` | Start the built-in runner. Set `false` when steps run on separate runners |
| `runner.concurrency` | whole number | `2` | Steps the built-in runner runs at once. See [runner settings](runner-config.md#runner-settings) |
| `runner.ai` | boolean | `false` | Whether the built-in runner may call the AI models listed under [`ai`](#ai-models), for `ai` steps. See [runner settings](runner-config.md#runner-settings) |

## Approvals

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `approvals.emergency_rollback` | `allow` or `require_approval` | `allow` | What happens when someone rolls back a protected environment after a successful deployment: `allow` goes ahead without an approval first and an approver reviews it afterwards; `require_approval` puts an approval step first. An environment's own `emergency_rollback` overrides it. See [rollback after success](../guide/rollback-after-success.md) |
| `approvals.allow_self` | boolean | `true` with `auth.mode: none`, else `false` | Lets the person who started a run approve it, and promote or waive on a protected environment |

## Drafts and pull requests

[Draft runs](../glossary.md#draft-run) run pipeline text from the editor that is in no commit;
pull-request runs run code anyone who can open a pull request wrote. Each project may override
these settings.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `drafts.allow` | text | `operator` | The role needed on a project to start a draft run there: `admin`, `operator` or `none` (nobody) |
| `drafts.secrets` | boolean | `false` | Let steps of draft runs read secrets |
| `pull_requests.secrets` | boolean | `false` | Let steps of pull-request runs read secrets, and image steps use pull credentials and build secrets |

## Queues and limits

How long a step waits for room. The guide [Queues and limits](../guide/queues.md) explains it. Each
project may override these settings.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `queues.max_wait` | duration or `off` | `60m` | The longest a step waits for room (the step namespace's quota, a node with space) before it fails with the reason and what to change. `off` waits for ever |

## Tests

Limits on what the server keeps of the test reports steps declare. `0` means the default.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `tests.max_upload_mib` | whole number | `24` | MiB of report files accepted per step attempt; runners are told and send no more. At most 1024 |
| `tests.max_cases_per_run` | whole number | `50000` | Individual results one run keeps; beyond it, only the totals |
| `tests.max_cases_per_project` | whole number | `1000000` | Individual results a project keeps over all its runs, the oldest runs' removed first. At least `max_cases_per_run` (checked when the server starts) |

## Reports

Limits on test attachments, coverage and hosted HTML reports, and where hosted reports are served
from. `0` means the default. See [Test reports](../guide/test-reports.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `reports.max_attachment_files` | whole number | `2000` | Files of a step's attachments directory the server accepts |
| `reports.max_attachment_mib` | whole number | `512` | MiB of attachments per step attempt |
| `reports.max_html_files` | whole number | `5000` | Files of one hosted HTML report |
| `reports.max_html_mib` | whole number | `256` | MiB of one hosted HTML report |
| `reports.max_coverage_upload_mib` | whole number | `24` | MiB of coverage reports accepted per step attempt. At most 1024 |
| `reports.max_coverage_files_per_run` | whole number | `20000` | Per-file coverage records one run keeps; beyond it, only the totals |
| `reports.max_coverage_files_per_project` | whole number | `2000000` | Per-file coverage records a project keeps, the oldest runs' removed first |
| `reports.origin` | URL | none | A separate origin, such as `https://reports.example.com`, that alone serves hosted HTML reports, on its own host name pointing at the same servers. Without it, reports are served from Launchway's own origin, sandboxed |

The other sizes are at most 1048576. Limits are checked when the server starts.

## Artifacts

Where the files steps keep are stored, and limits on them. Several servers need one store they all
share: the database (the default on PostgreSQL), `s3`, or a directory on a shared filesystem. See
[Where artifacts are kept](../artifacts.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `artifacts.store` | text | `database` on PostgreSQL, `filesystem` on SQLite | `database`, `filesystem` or `s3`. An installation that kept artifacts on the filesystem before the database store existed keeps doing so until they are copied with `launchway blobs copy` |
| `artifacts.database.max_total_gib` | whole number | `100` | How much the database store may hold in all, uploads in progress included, in GiB. Only with the database store |
| `artifacts.database.max_blob_mib` | whole number | `2048` | The largest single file the database store keeps, in MiB. Only with the database store |
| `artifacts.dir` | directory | `<data>/artifacts` | The filesystem store's directory. Not with `s3` |
| `artifacts.s3.endpoint` | URL | required with `s3` | Scheme and host, such as `https://s3.eu-west-1.amazonaws.com` or `http://minio.example.com:9000` |
| `artifacts.s3.region` | text | `us-east-1` | The region |
| `artifacts.s3.bucket` | text | required with `s3` | The bucket; it must exist |
| `artifacts.s3.prefix` | text | none | Prepended to every object's name, such as `launchway/` |
| `artifacts.s3.path_style` | boolean | `true` | Path-style requests, which MinIO, Ceph and AWS accept; `false` for virtual-hosted style |
| `artifacts.s3.ca_file` | path | none | An extra CA bundle (PEM) for a private endpoint |
| `artifacts.max_file_mib` | whole number | `1024` | The largest file an artifact may hold, in MiB. At most 80000 |
| `artifacts.max_run_mib` | whole number | `4096` | How much one run may keep, in MiB. At least `max_file_mib` |
| `artifacts.max_project_gib` | whole number | `50` | How much a project may keep over all its runs, in GiB. At least `max_run_mib` |
| `artifacts.retain` | text | `30d` | How long artifacts are kept when the pipeline does not say: `30d`, `2w`, `12h`. From `1h` to `3650d` |
| `artifacts.max_retain` | text | `90d` | The longest any artifact is kept, whatever the pipeline says. At least `retain` |

S3 credentials come from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or the `_FILE` variants),
never from the file.

## Forge

Launchway's own Git hosting: it keeps repositories in the database and the artifact store and serves them
over HTTPS and, when `forge.ssh.listen` is set, SSH (see [Hosted repository storage](../development/forge-storage.md)). It needs `git` 2.38 or newer on
the server (older git: hosting refuses to turn on and says so; `launchway doctor git` shows the version) and a blob store shared by every server. Turning it off stops the Git endpoints and keeps the
data. These are installation settings, kept in the database: an administrator sets them under
Configuration, Installation settings, with `launchway config` or through the API (a file's `forge:` section
is imported once, like the other settings that moved). Hosting is off until it is switched on, and a change
takes effect when each server next restarts.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `forge.hosting` | boolean | `false` | Turn Git hosting on. The first time, the connection named `launchway` is made |
| `forge.public_url` | URL | none | The address clients clone from, used in the clone address of each repository |
| `forge.default_owner` | text | `launchway` | The owner of a repository made without one |
| `forge.allow_insecure_http` | boolean | `false` | Accept Basic credentials over plain HTTP from any address; otherwise only a loopback listener does |
| `forge.max_repo_mib` | whole number | no limit | The most one repository's packs may total, in MiB |
| `forge.max_push_mib` | whole number | `2048` | The largest push (request body and pack), in MiB |
| `forge.max_per_owner` | whole number | no limit | The most repositories one owner may have |
| `forge.creates_per_minute` | whole number | `60` | How fast repositories may be created, in all |
| `forge.max_concurrent` | whole number | `32` | The most `git` processes serving Git requests at once; more requests wait, then are refused |
| `forge.cache_gib` | whole number | no limit | How much the repository caches on this server may take, in GiB; the least recently used are removed |
| `forge.maintain_every` | text | `15m` | How often packs are consolidated, collected and checked. At least `1m` |
| `forge.mirror_allow` | list of text | none | Internal hosts and networks that mirrors and imports may contact: a host name (`git.corp.example`, or `*.corp.example`), an address or a network (`10.20.0.0/16`). Launchway refuses to contact an internal address (loopback, private, link-local, cloud metadata) that is not listed here, so nobody who can set up a mirror can use the server to reach the network around it. Public addresses need no entry. Only `https` and `http` addresses are mirrored, and a redirect is not followed |
| `forge.ssh.listen` | text | none | Turn on Git over SSH: the address to listen on, such as `:2222`. Empty is off. See [Git over SSH](../guide/git-over-ssh.md) |
| `forge.ssh.public_host` | text | host of `forge.public_url` | The host SSH clone addresses name |
| `forge.ssh.public_port` | whole number | the port of `listen` | The port SSH clone addresses name; 22 makes them `git@host:owner/name.git` |
| `forge.ssh.user_ca` | text | none | Public keys (OpenSSH format, one per line) of the authorities whose user certificates are accepted; the first principal of a certificate is the person's name. Empty: certificates are not accepted |
| `forge.ssh.max_connections` | whole number | `256` | SSH connections in all |
| `forge.ssh.max_per_address` | whole number | `20` | SSH connections from one address |
| `forge.ssh.idle_timeout` | text | `10m` | An SSH connection that moves no bytes is closed. At least `1m` |
| `forge.ssh.max_duration` | text | `6h` | The longest any SSH connection lasts. At least `1m` |

## Findings

Limits on what the server keeps of security findings (SARIF) and SBOMs. `0` means the default.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `findings.max_upload_mib` | whole number | `32` | MiB of SARIF files accepted per step attempt. At most 1024 |
| `findings.max_per_run` | whole number | `20000` | Findings one run keeps; beyond it, only the counts |
| `findings.max_per_project` | whole number | `500000` | Findings a project keeps, the oldest runs' removed first. At least `max_per_run` |
| `findings.max_components_per_run` | whole number | `20000` | SBOM components one run keeps for searching |
| `findings.max_components_per_project` | whole number | `1000000` | SBOM components a project keeps. At least `max_components_per_run` |
| `findings.max_sbom_mib` | whole number | `64` | MiB of a step's SBOM files read to summarize them. At most 1024 |

## Loops

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `loops.max_members` | whole number | `64` | The most combinations one [`for_each`](pipeline.md#for-each) loop may have, up to 500. `0` means the default |

## Schedules

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `schedules.enabled` | boolean | `true` | `false` turns every project's schedules off (a project's own setting wins). While off, firings are recorded as skipped and are not caught up later |

## Environments

`environments` lists where [deploy steps](pipeline.md#kind-deploy) may deploy. Without any, deploy
steps are refused. Checked when the server starts.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `environments[].name` | name | required | Lower-case letters, digits and hyphens, at most 63, unique |
| `environments[].target` | name | required | What deploy runners serve to reach it, in practice its cluster. Same rules as `name` |
| `environments[].projects` | list of patterns | required | The projects that may deploy here, such as `web-*` |
| `environments[].queue` | text | `newest` | `newest`: a run waiting for the lock is skipped when a newer run of the same service starts waiting. `all`: they deploy in turn |
| `environments[].protected` | boolean | `false` | The person who started a run may not promote or retry its deployment here, nor waive its deploy gate, unless `approvals.allow_self` lets them approve their own runs |
| `environments[].reviewers.users` | list of names | none | With `reviewers.groups`: who may acknowledge an emergency rollback of this environment; when neither is given, any operator of the project other than the person who asked for it (unless `approvals.allow_self` lets them) |
| `environments[].reviewers.groups` | list of names | none | Groups whose members may acknowledge an emergency rollback of this environment |
| `environments[].emergency_rollback` | `allow` or `require_approval` | the installation's | Overrides `approvals.emergency_rollback` for this environment |
| `environments[].min_approvals` | whole number | `0` | With `protected: true`: the fewest distinct approvals an approval step that guards a deployment here needs, 1 to 5, whatever its pipeline's `approvers.count` says |

## Runner pools

`runner_pools` gives runners pools. A **deploy** pool has targets, and its runners take deploy work
for them, with the pool's own token or enrolled with a join token. A **build** pool serves projects
(and optionally only some events), and its runners enrol with join tokens. The build pool named
`default` is the pool of the shared runner token: without it listed, it serves every project that no
other build pool serves; list it to limit what it serves or to require identities. See
[Runners on other machines and clusters](../guide/runner-identity.md). Checked when the server
starts.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner_pools[].name` | text | required | Unique |
| `runner_pools[].kind` | text | `deploy` | `deploy` (serves targets) or `build` (serves projects) |
| `runner_pools[].token_env` | variable name | | A deploy pool's token, from an environment variable. Exactly one of `token_env` or `token_file`, unless `require_identity` is set (then neither). At least 32 characters, different from every other pool's and from the shared runner token. A build pool has none: its runners enrol with join tokens |
| `runner_pools[].token_file` | path | | Or from a file |
| `runner_pools[].targets` | list | required for a deploy pool | Targets its runners may serve; each must be some environment's `target` |
| `runner_pools[].projects` | list of patterns | required for a build pool (`["*"]` for all) | Projects a build pool's runners take work for. The pool named `default` may leave it out |
| `runner_pools[].events` | list | every event | A build pool: only runs started this way, such as `push` or `manual`, and not, say, pull requests |
| `runner_pools[].require_identity` | boolean | `false` | Refuse shared tokens: only runners enrolled with a join token for the pool serve it. For the pool named `default` (the build pool of the shared runner token), refuse the shared token; turn the built-in runner off first |

## Runner administration

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner_admin` | text | `admin` | The role needed on every project to manage runners: make join tokens, and revoke and rotate runners. `admin`, `operator` or `none` (managing runners over the API and the web UI is off) |

## Verification

`verification.sources` names the health sources that [verify steps](../guide/verification.md) may
read. Every server lists the same ones. Where each is and its credentials are in the deploy runners'
configuration (`health_sources`, see [runner configuration](runner-config.md#health-sources)).
Unset, verify steps are refused.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `verification.sources[].name` | name | required | What pipelines call the source |
| `verification.sources[].type` | text | required | `prometheus`, `loki`, `clickhouse`, `elasticsearch`, `opensearch`, `postgres` or `http` |
| `verification.sources[].projects` | list of patterns | none | The projects that may use it |

## AI models

`ai` is the registry of models that `ai` steps, verifications and **Explain** may use. Nothing is
sent to any model unless one is listed here. See [AI models](../guide/ai.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `ai.models[].name` | name | required | What pipelines call the model: a lower-case letter, then letters, digits and hyphens, at most 40 characters; unique |
| `ai.models[].provider` | text | required | `anthropic` (the Messages API), or `openai` for any OpenAI-compatible chat completions server (Ollama, vLLM, LM Studio) |
| `ai.models[].model` | text | required | The provider's model id, without spaces, such as `claude-sonnet-5-5` or `qwen3:8b` |
| `ai.models[].endpoint` | URL | `https://api.anthropic.com` for `anthropic`; required for `openai` | The base URL, `http` or `https`, without a query or a user name |
| `ai.models[].key.store` | name | | Where the API key is kept: a secret store of the runner that makes the call (and of `ai.secrets` on a server). A reference, never a value. Required for `api.anthropic.com`; a local server may need none |
| `ai.models[].key.key` | text | | The key in the store |
| `ai.models[].key.field` | text | | The field of the key |
| `ai.models[].projects` | list of patterns | none | The projects that may use the model. Empty means none |
| `ai.models[].max_tokens` | whole number | `4096` | Output cap per call, 1 to 128000 |
| `ai.models[].max_input_bytes` | whole number | `262144` | Bytes of instructions and prompt per call, at most 8 MiB |
| `ai.models[].calls_per_run` | whole number | `20` | Calls one run may make, 1 to 1000 |
| `ai.models[].tokens_per_day` | whole number | `0` | Tokens (input and output) a project may use a day. `0` means no limit |
| `ai.models[].timeout` | duration | `2m` | One call, retries included, at most `15m` |
| `ai.models[].effort` | text | the model's own | `anthropic` only: `low`, `medium`, `high`, `xhigh` or `max` |
| `ai.models[].stream` | boolean | stream when a call may write over 16000 tokens | Whether calls stream their answer |
| `ai.models[].headers` | mapping | none | Extra headers that are not secret |
| `ai.models[].max_tokens_field` | text | `max_tokens` | `openai` only: the request field carrying the output cap; `max_completion_tokens` for newer hosted models |
| `ai.models[].schema_mode` | text | `json_schema` | `openai` only: how JSON is asked for when a step gives a schema: `json_schema`, `json_object` or `prompt`. A rejected mode falls back to the next; the answer is checked against the schema in every mode |
| `ai.models[].price.input_per_mtok` | number | built in for current Claude models | US dollars per million input tokens, to show an estimated cost |
| `ai.models[].price.output_per_mtok` | number | built in | Per million output tokens |
| `ai.models[].price.cache_read_per_mtok` | number | a tenth of the input price | Per million cached tokens read |
| `ai.models[].price.cache_write_per_mtok` | number | 1.25 times the input price | Per million cached tokens written |
| `ai.models[].price.long_input_tokens` | whole number | `0` | Above this many input tokens in one call, the long prices apply to the whole call. `0` means no separate prices |
| `ai.models[].price.long_input_per_mtok` | number | | The long-prompt input price |
| `ai.models[].price.long_output_per_mtok` | number | | The long-prompt output price |
| `ai.secrets.<name>.type` | text | required | The server's own secret stores, for the keys of models the server calls itself (only to explain a failed step); runners read keys from their own. Same types and settings as [`secrets`](runner-config.md#secrets): `env`, `file` or `vault`. Without them the server explains only with models that need no key |
| `ai.secrets.<name>.projects` | list of patterns | every project | As for runner secret stores |
| `ai.secrets.<name>.keys` | list of patterns | every key | |
| `ai.secrets.<name>.dir` | directory | required for `file` | |
| `ai.secrets.<name>.address` | URL | required for `vault` | |
| `ai.secrets.<name>.mount` | text | `secret` | `vault` |
| `ai.secrets.<name>.namespace` | text | none | `vault` |
| `ai.secrets.<name>.ca_file` | path | the system's | `vault` |
| `ai.secrets.<name>.token_env` | variable name | | `vault`: exactly one of `token_env`, `token_file` or `role` |
| `ai.secrets.<name>.token_file` | path | | `vault` |
| `ai.secrets.<name>.role` | text | | `vault`: Kubernetes auth |
| `ai.secrets.<name>.auth_path` | text | `kubernetes` | `vault` with `role` |
| `ai.secrets.<name>.jwt_file` | path | the pod's service account token | `vault` with `role` |

## MCP

The endpoint AI agents use, `/mcp`. It is on by default, because it gives no right that a token does
not already have over the API. See [Agents (MCP)](../guide/agents.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `mcp.enabled` | boolean | `true` | `false` turns `/mcp` off |
| `mcp.allowed_origins` | list | none | Browser origins (`https://tools.example.com`), besides this server's own, whose pages may call `/mcp`. Anything else with an `Origin` header gets `403` |
| `mcp.requests_per_minute` | whole number | `120` | Requests a token may make a minute, on each server |
| `mcp.concurrent` | whole number | `4` | Requests of one token in progress at once, on each server |
| `mcp.total` | whole number | `32` | MCP requests in progress at once on each server, all tokens together |
| `mcp.oauth.resource` | URL | none | Makes `/mcp` an OAuth resource server for the sign-in provider (needs `auth.mode: oidc`): the endpoint's address as clients and tokens name it, such as `https://launchway.example.com/mcp` |
| `mcp.oauth.scopes` | list | none | Scopes an access token must carry |

The limits are whole numbers from 0 to 100000, where `0` means the default.

## Egress

What the server may reach when a person gives it an address: a repository to read (the check in the
wizards, and the mirrors it polls), an identity provider, an AI model. The server refuses addresses that lead
into its own network, loopback, link-local and cloud metadata (`169.254.169.254`), private ranges and the
shared `100.64.0.0/10` range, after looking the name up and again when it connects, so a name that changes its
answer does not get through. It does not follow redirects. Paths on the server and `file:` addresses are
refused too, because through the API they would read any repository the server's account can read.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `egress.allow_local` | boolean | `false` | Allows repository addresses that are paths on this server or `file:` addresses. Bootstrap file only, never through the API. `examples/local.yaml` (`make run`) sets it |
| `egress.allow` | list | none | Internal hosts (`git.internal`, `*.corp.internal`), addresses and ranges (`10.0.0.0/8`) the server may reach, such as a self-hosted Git server. Kept in the database: an administrator sets it in the installation settings, and the wizards offer to add the host that was refused |

## History

How long the change history of the configuration kept in the database is kept. By default all of it
is, for ever. Like the other settings that moved, it is set in the installation settings once the
database holds them.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `history.retain` | duration | keep all | How long changes are kept, such as `90d` or `52w`, at least `7d`. Older changes are removed hourly, except the newest change of every object. A revision older than what is kept can no longer be put back with `launchway config restore --offline` |

## Policy

Delivery policies: rules pipelines cannot opt out of. The rules themselves are listed in the
[policy rules reference](policy-rules.md); see also [Policy gates](../tutorials/policy-gates.md).
Unset, there are no policies.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `policy.policies` | list | none | Policies written in this file. Mistakes are reported with this file's line |
| `policy.waivers.allow` | text | `admin` | The role needed on a project to waive one of its policies' rules (on the environment too, for a deploy rule): `admin`, `operator` or `none` |
| `policy.waivers.max_days` | whole number | `30` | The longest a waiver may last, in days, 1 to 365. Checked when the server starts |
| `policy.sources[].name` | name | required | Lower-case letters, digits and hyphens; unique, and not `config` |
| `policy.sources[].library` | name | required | One of [`libraries`](#libraries) |
| `policy.sources[].version` | text | required | A tag or a full commit id. A branch is refused (the source then fails closed) |
| `policy.sources[].path` | path | `policies` | The directory of `*.yaml` policy files in the library. No `..` |
| `policy.sources[].projects` | list of patterns | every project | The projects it may govern |
| `policy.sources[].refresh` | duration | `1m` | How often servers look at the version again. From `10s` to `168h` |
| `policy.sources[].stale_after` | duration | `1h` | How long a version loaded earlier is used while the repository cannot be reached. From `10s` to `168h` |

<!-- validate-config server -->
```yaml
libraries:
  - { name: org-policies, repo: https://git.example.com/platform/policies.git }
policy:
  waivers: { allow: admin, max_days: 30 }
  sources:
    - { name: org, library: org-policies, version: v3, path: policies, projects: ["*"], refresh: 1m, stale_after: 1h }
  policies:
    - name: prod-evidence
      mode: enforce
      scope: { environments: [prod] }
      rules:
        tests: { min: 1 }
        approvals: { min: 1 }
```

## What next

- [Runner configuration](runner-config.md): executors, secret stores, image builders and deploy
  targets.
- [Helm values](helm-values.md): the same settings when Launchway runs on Kubernetes.
- [Back up, restore and upgrade](../howto/backup-restore-upgrade.md).
