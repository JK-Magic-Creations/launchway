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
| `listen`, `data`, `database`, `database_wait`, `multi_base`, `resilience.stall_budget`, `user` | Where the server runs and how it reaches the database |
| `server.trust_proxy`, `server.drain_delay` | How it sits behind a proxy or load balancer |
| `auth.mode`, `auth.local`, `auth.oidc`, `auth.idle_timeout`, `auth.session_lifetime`, `auth.allow_anonymous` | Sign-in must work before anyone can change anything |
| `auth.admins` | The first administrators, and the way back in if the role bindings lock everyone out |
| `runner` (the built-in Station (runner) and its secret stores, the AI secret stores) | Process settings of the machine the server runs on |
| `reports.origin` | The address the server is reached at |
| `encryption` | Where the root key for encryption at rest is held: it must be known before the database is opened, and is never kept in the database whose secrets it protects |

Everything else lives in the database: `projects`, `libraries`, `environments`, `runner_pools`,
`runner_admin`, `auth.access` (role bindings), `auth.tokens`, `policy`, `verification`, `ai.models`,
`approvals`, `drafts`, `pull_requests`, `queues`, `automation`, `schedules`, `loops`, `tests`, `findings`, `reports` (except
`origin`), `artifacts` (limits, retention and where Cargo (artifacts) are kept) and `mcp`. The sections below
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
  the Stations' [secret stores](runner-config.md#secrets).
- **Every server that shares a database** must have the same `environments`, `runner_pools` and
  `policy`.

Mistakes in the settings below are reported when the file is read, before the server listens,
unless the table says otherwise.

## Command line and Airfield (environment) {#command-line-and-environment}

| Flag or variable | Overrides | Description |
|---|---|---|
| `--config FILE` | | The file to read. Default `launchway.yaml` |
| `--listen ADDR` | `listen` | The address to listen on |
| `--data DIR` | `data` | The data directory |
| `LAUNCHWAY_DATABASE` | `database` | The database: a SQLite file or a `postgres://` URL. Keeps a password out of the file |
| `LAUNCHWAY_RUNNER_TOKEN` | the stored Station token | The token Stations present. Without it, the first server creates a random token, keeps it in the database so every server shares it, and writes it to `<data>/runner-token` |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | | Credentials of the S3 Cargo store. `AWS_ACCESS_KEY_ID_FILE` and `AWS_SECRET_ACCESS_KEY_FILE` name files instead |
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
| `listen` | address | `127.0.0.1:8080` | Where the server listens for the API, the web UI and Stations. With `auth.mode: none` it must be a loopback address unless `auth.allow_anonymous` is true |
| `data` | directory | `launchway-data` | Holds the repository mirrors, the built-in Station's workspaces, the default SQLite database and the default Cargo store. Made absolute |
| `database` | path or URL | `<data>/launchway.db` | A SQLite file for one server, or a `postgres://` URL that several servers share. `LAUNCHWAY_DATABASE` overrides it |
| `database_wait` | duration | `60s` | How long a starting server keeps trying a database that does not answer. Checked when the server starts |
| `multi_base` | boolean | `false` | Turn on several Bases (customers) in one installation: the wall between them. With one Base, which is every installation today, nothing changes. In this release it only lets the console command `launchway bases` create a second Base and prove the separation; **the server refuses to start while it is on**, until the web and API paths know which Base a request is for. Turning it on rewrites the keys of the identity tables once and cannot be undone except from a backup; a build older than schema 52 will not start against the result. See [every table has an owner](../development/the-wall.md) |
| `resilience.stall_budget` | duration | `120s` | How long a request waits for a database that is briefly gone (a failover, a restart) before it is answered `503` with `Retry-After`. Transient errors (connection refused or reset, a failover in progress, a read-only standby, serialization failures, deadlocks, too many connections) are retried inside this budget in the store layer; constraint, syntax and not-found errors never are. A statement cut off in the middle is repeated only if it is a read or its whole transaction was returned to base; a cut-off commit is reported, never repeated blindly |
| `audit.stdout` | boolean | `true` | Also write every audit entry as one JSON line to standard output ([the audit stream](events.md#audit-stream-on-standard-output)). The database record is kept either way. Set `false` to turn the stream off |
| `user` | text | the operating system user, else `local` | Who every action is recorded as when sign-in is off (`auth.mode: none`) |

## Encryption

See [Encryption at rest](../guide/encryption-at-rest.md). The root key is held outside the database; these settings say where. With none set, a new installation generates a key file in its data directory, and an existing one starts "not encrypted at rest" with a warning until one is configured. At most one holder may be configured; set `encryption.holder` to say which when settings for several are present. Environment variables override the file: `LAUNCHWAY_ROOT_KEY_FILE`, `LAUNCHWAY_ROOT_KEY` (the key itself, base64), `LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE`, `LAUNCHWAY_ROOT_KEY_PREVIOUS`, `LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE`.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `encryption.holder` | `key_file`, `env`, `passphrase` or `transit` | the one that is set | Which kind of root key holder is in use |
| `encryption.key_file` | path | none | A file of 32 random bytes in base64, mode `0600` (or a mounted Secret's `0400` or `0440`). A file that other users can read is refused. `launchway encryption init` makes one. Only as strong as the file system or the cluster Secret that holds it |
| `encryption.previous_key_file` | path | none | The root key being replaced, given beside the new one while `launchway encryption rewrap` runs; remove it afterwards |
| `encryption.keystore_file` | path | none | A passphrase keystore file (`launchway encryption init --passphrase`): the root key, encrypted under a key made from a passphrase with Argon2id |
| `encryption.passphrase_file` | path | none | The keystore's passphrase, first line. Without it the server asks on the terminal at start, and cannot start unattended |
| `encryption.transit.address` | URL | none | Vault or OpenBao address; turns on the transit holder, whose key never leaves the service |
| `encryption.transit.mount` | path | `transit` | Where the transit secrets engine is mounted |
| `encryption.transit.key` | text | none | The transit key that wraps the installation's key-encryption keys. It needs `encrypt` and `decrypt` on `<mount>/encrypt/<key>` and `<mount>/decrypt/<key>` only |
| `encryption.transit.namespace` | text | none | Sent as `X-Vault-Namespace` |
| `encryption.transit.ca_file` | path | system roots | PEM certificates to trust for the service |
| `encryption.transit.token_env` | text | none | Log in with the token in this environment variable |
| `encryption.transit.token_file` | path | none | Or with the token in this file |
| `encryption.transit.role` | text | none | Or with Kubernetes auth, as this role |
| `encryption.transit.auth_path` | text | `kubernetes` | Where Kubernetes auth is mounted |
| `encryption.transit.jwt_file` | path | the pod's service account token | The service account token for Kubernetes auth |
| `encryption.require` | boolean | `false` | Refuse to start with no root key, even on an installation that would otherwise generate one or run unencrypted. Turn it on in production |
| `encryption.no_generate` | boolean | `false` | Do not let a new installation generate a key file; it then runs not encrypted, with the warning |

## Server

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `server.trust_proxy` | boolean | `false` | The older switch: believe `X-Forwarded-Proto` and `X-Forwarded-For` from every peer, which is safe only if clients cannot reach the server except through the proxy (a warning is logged). Prefer `server.trusted_proxies` |
| `server.trusted_proxies` | list of address ranges | none | The proxies in front (CIDR ranges or single addresses). Their `X-Forwarded-For` and `X-Forwarded-Proto` are believed; those of every other peer are ignored. Checked when the server starts |
| `server.client_address_header` | text | none | The header holding the one client address your edge sets (such as `X-Real-IP`), believed only from a trusted proxy. Empty reads `X-Forwarded-For` |
| `server.drain_delay` | duration | `0s` | How long a stopping server keeps serving after `/readyz` reports not ready, so load balancers that check `/readyz` move away first. Checked when the server starts |

## Auth

See [Set up sign-in and roles](../howto/oidc-and-roles.md) and [Roles](roles.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.mode` | text | `local` when `listen` is not a loopback address, else `none` | `local`: built-in accounts (a password and a second factor, created at first run with a one-time setup code). `oidc`: sign in with an OpenID Connect provider. `none`: no sign-in, everyone acts as `user` with full rights; only for a loopback address |
| `auth.allow_anonymous` | boolean | `false` | Lets a server with `mode: none` listen on an address other than loopback, when something in front already decides who may reach it |
| `auth.idle_timeout` | duration | `8h` | A session nobody has used this long ends. At least `1m`; at most `session_lifetime` |
| `auth.session_lifetime` | duration | `24h` | Every session ends this long after sign-in. At least `1m` |
| `auth.tokens.service_accounts_from_tokens` | boolean | `false` | Lets an administrator's API token, not only an administrator's signed-in session, make tokens for service accounts |
| `auth.tokens.max_days` | whole number | `365` | The longest an API token may last, in days, 1 to 3650. `0` means the default |

### OpenID Connect

Required with `mode: oidc`: `issuer`, `client_id`, `redirect_url` and one of
`client_secret_env` or `client_secret_file`.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.oidc.issuer` | URL | | The provider's issuer, exactly as in its discovery document. `http` or `https` |
| `auth.local.enabled` | yes/no | `false` | With `auth.mode: oidc`, also let people sign in with built-in accounts (the break-glass administrator survives a provider outage). Always on with `auth.mode: local` |
| `auth.local.require_second_factor` | text | `admins` | Who must set up a second factor (an authenticator app) before an account is created: `admins`, `all` or `off` |
| `auth.local.setup_code_file` | path | | A file holding the one-time setup code for a new installation (the chart mounts a Secret here). Without it the server makes a code and writes it to its log once |
| `auth.local.setup_code_ttl` | duration | `24h` | How long a setup code works; `launchway users setup-code` issues a new one |
| `auth.oidc.client_id` | text | | The client ID registered with the provider |
| `auth.oidc.client_secret_env` | variable name | | An environment variable holding the client secret |
| `auth.oidc.client_secret_file` | path | | Or a file holding it |
| `auth.oidc.redirect_url` | URL | | This server's address followed by `/auth/callback`. Cookies are marked `Secure` when it starts with `https://` |
| `auth.oidc.scopes` | list | `[openid, profile, email, groups]` | The scopes to ask for |
| `auth.oidc.username_claim` | text | `preferred_username`, then `email`, then `sub` | The claim that names a person. Choose one people cannot change themselves |
| `auth.oidc.groups_claim` | text | `groups` | The claim, in the ID token or the userinfo response, that lists a person's groups |

`auth.admins` names the first administrators: people or groups who are administrators of every Flight
whatever the role bindings in the database say. It stays in the file so that a new installation has
someone who can sign in and set the rest up, and so that bindings that lock everyone out can be
repaired by editing a file.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `auth.admins.users` | list of text | | Users, as `username_claim` gives them, who are administrators of every Flight |
| `auth.admins.groups` | list of text | | Groups, as `groups_claim` gives them, whose members are administrators of every Flight |

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
| `auth.access[].projects` | list of patterns | every Flight | The Flights it grants the role on |
| `auth.access[].environments` | list of patterns | every Airfield | Limits what it grants on deployments (rollout commands, approvals that name a deployment) to these Airfields |

## Flights {#projects}

`projects` lists the repositories to watch. Flights can be added only here.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `projects[].name` | name | required | `^[a-z0-9][a-z0-9-]{0,62}$`, unique |
| `projects[].repo` | URL or path | required | What the servers and Stations clone: `https://...`, `ssh://...`, or a local path. Credentials go in the URL or in `GIT_CONFIG_*` variables |
| `projects[].branch` | branch | `main` | The default branch: manual Sorties (runs) start from it, and schedules, webhooks and the pull-request setting are read from its pipeline |
| `projects[].poll` | duration | `30s` | How often to look for pushes and pull requests. At least `5s` (shorter is raised to 5s). Checked when the server starts |
| `projects[].drafts.allow` | text | the server's `drafts.allow` | Overrides [`drafts.allow`](#drafts-and-pull-requests) for this Flight |
| `projects[].drafts.secrets` | boolean | the server's `drafts.secrets` | Overrides `drafts.secrets` for this Flight |
| `projects[].pull_requests.secrets` | boolean | the server's `pull_requests.secrets` | Overrides `pull_requests.secrets` for this Flight |
| `projects[].pull_requests.untrusted` | `restrict` or `allow` | the server's `pull_requests.untrusted` | Overrides `pull_requests.untrusted` for this Flight |
| `projects[].queues.max_wait` | duration or `off` | the server's `queues.max_wait` | Overrides `queues.max_wait` for this Flight |
| `projects[].queues.runs` | whole number | the server's `queues.runs` | Overrides `queues.runs` for this Flight |
| `projects[].queues.pipeline_runs` | whole number | the server's `queues.pipeline_runs` | Overrides `queues.pipeline_runs` for this Flight |
| `projects[].queues.pull_request_runs` | whole number | the server's `queues.pull_request_runs` | Overrides `queues.pull_request_runs` for this Flight |
| `projects[].queues.push_runs` | whole number | the server's `queues.push_runs` | Overrides `queues.push_runs` for this Flight |
| `projects[].queues.schedule_runs` | whole number | the server's `queues.schedule_runs` | Overrides `queues.schedule_runs` for this Flight |
| `projects[].queues.manual_runs` | whole number | the server's `queues.manual_runs` | Overrides `queues.manual_runs` for this Flight |
| `projects[].queues.automation_runs` | whole number | the server's `queues.automation_runs` | Overrides `queues.automation_runs` for this Flight |
| `projects[].queues.supersede_pull_requests` | boolean | the server's `queues.supersede_pull_requests` | Overrides `queues.supersede_pull_requests` for this Flight |
| `projects[].push.branch_prefix` | text | `launchway/` | Lets the pipeline editor save to the repository. Saves go to a new branch `<prefix><you>/<date-time>`. Letters, digits, `-`, `_`, `.` and `/`, at most 100 |
| `projects[].push.allow_direct` | list of patterns | none | Branches that may be saved to directly |
| `projects[].schedules.enabled` | boolean | the server's `schedules.enabled` | Turns this Flight's [schedules](pipeline.md#schedule) on or off |
| `projects[].git_webhook.kind` | text | | The Git host's webhook, which makes the server poll at once: `github`, `gitea`, `forgejo` or `gitlab`. The host calls `POST /api/v1/projects/<name>/git-hook` |
| `projects[].git_webhook.secret_env` | variable name | | The webhook's secret, from an environment variable. Exactly one of `secret_env` or `secret_file` |
| `projects[].git_webhook.secret_file` | path | | Or from a file |
| `projects[].webhooks.<name>.token_env` | variable name | | What callers of the pipeline's [webhook trigger](pipeline.md#webhook) `<name>` must prove: a bearer token from this variable. Exactly one of the four settings. A trigger not listed cannot be called |
| `projects[].webhooks.<name>.token_file` | path | | Or a bearer token from a file |
| `projects[].webhooks.<name>.hmac_secret_env` | variable name | | Or an HMAC-SHA256 signing secret from a variable (preferred: a signed request cannot be replayed) |
| `projects[].webhooks.<name>.hmac_secret_file` | path | | Or a signing secret from a file |

### A pipeline from a library

A Flight may get its pipeline from a library template instead of a file in its repository. At a
commit that has its own `.launchway/pipeline.yaml`, that file wins.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `projects[].pipeline.library` | name | required, unless `detect` | One of [`libraries`](#libraries), or `launchway` for the [starters](../guide/starters.md) that ship with Launchway |
| `projects[].pipeline.version` | text | required, except for `launchway` | A tag, branch or full commit id; `launchway` has one version and needs none |
| `projects[].pipeline.template` | name | required, unless `detect` | The template to run |
| `projects[].pipeline.detect` | boolean | `false` | Run the starter that fits what the repository holds (`go.mod`: `go-service` or `go-library`, and so on) where it has no pipeline file; `library`, `version` and `template` are then left out. Nothing that matches means no pipeline, as before |
| `projects[].pipeline.with` | mapping | none | Values for the template's parameters |
| `projects[].pipeline.on.push.branches` | list of patterns | none | As a pipeline's [`on.push`](pipeline.md#push) |
| `projects[].pipeline.on.push.tags` | list of patterns | none | |
| `projects[].pipeline.on.pull_request` | empty mapping | none | Write `pull_request: {}` to run pull requests. It takes no settings |
| `projects[].pipeline.supersede` | boolean | `false` | As a pipeline's [`supersede`](pipeline.md#top-level-keys) |
| `projects[].pipeline.lane.name` | name | the Flight's name | The lane's name, as a pipeline's [`lane`](pipeline.md#lane): runs of the Flight take turns. Needs schema 24; not with `supersede` |
| `projects[].pipeline.lane.per` | list | none | `ref`, `pull_request` or `input:<name>`: what splits the lane |
| `projects[].pipeline.lane.when_busy` | `wait`, `cancel` or `skip` | `wait` | What a Sortie that arrives while the lane is held does |
| `projects[].pipeline.lane.queue` | `newest` or `all` | `newest` | For `wait`: whether a newer waiting run replaces an older one |
| `projects[].pipeline.true` | | | Not written by hand: tools that read YAML 1.1 (Helm among them) turn an unquoted `on` into `true`. It is read as `on`; having both is refused |
| `projects[].default_pipeline.enabled` | boolean | `true` | `false`: the Flight runs no [flight plan](config-objects.md#a-flight-plan-for-every-repository). A repository file still runs |
| `projects[].default_pipeline.with` | mapping | none | Other values for some parameters of the Flight plan's template; the rest stay as the Flight plan sets them. Ignored when the Flight has its own `pipeline` |

## Libraries

`libraries` lists the repositories pipelines may use templates from, and policy sources may be
read from. A pipeline names one of these by name; it can never give an address.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `libraries[].name` | name | required | `^[a-z][a-z0-9-]{0,39}$`, unique |
| `libraries[].repo` | URL or path | required | Fetched with the same credentials as Flights |

## Station {#runner}

The built-in Station runs steps inside the server process's machine. Its settings are the same as
a separate Station's: see the [Station configuration reference](runner-config.md), where
`concurrency` is written here as `runner.concurrency`, `kubernetes.namespace` as
`runner.kubernetes.namespace`, and so on.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner.enabled` | boolean | `true` | Start the built-in Station. Set `false` when steps run on separate Stations |
| `runner.concurrency` | whole number | `2` | Steps the built-in Station runs at once. See [Station settings](runner-config.md#runner-settings) |
| `runner.ai` | boolean | `false` | Whether the built-in Station may call the AI models listed under [`ai`](#ai-models), for `ai` steps. See [Station settings](runner-config.md#runner-settings) |

## Approvals

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `approvals.emergency_rollback` | `allow` or `require_approval` | `allow` | What happens when someone rolls back a protected Airfield after a successful deployment: `allow` goes ahead without an approval first and an approver reviews it afterwards; `require_approval` puts an approval step first. An Airfield's own `emergency_rollback` overrides it. See [Return to base (rollback) after success](../guide/rollback-after-success.md) |
| `approvals.allow_self` | boolean | `true` with `auth.mode: none`, else `false` | Lets the person who started a Sortie approve it, and promote or waive on a protected Airfield |

## Drafts and pull requests

[Draft runs](../glossary.md#draft-run) run pipeline text from the editor that is in no commit;
pull-request runs run code anyone who can open a pull request wrote. Each Flight may override
these settings.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `drafts.allow` | text | `operator` | The role needed on a Flight to start a draft run there: `admin`, `operator` or `none` (nobody) |
| `drafts.secrets` | boolean | `false` | Let steps of draft runs read secrets |
| `pull_requests.secrets` | boolean | `false` | Let steps of pull-request runs read secrets, and image steps use pull credentials and build secrets |
| `pull_requests.untrusted` | `restrict` or `allow` | `restrict` | What happens to a pull request from an [automation account](#automation-accounts): `restrict` runs it without secrets (even with `pull_requests.secrets: true`), deploy steps, saved caches or Depot (registry) pushes, using the pipeline of the default branch, until a maintainer approves it with *Run with secrets*; `allow` treats it like any other pull request. See [Pull-request trust](../guide/pull-request-trust.md) |

## Queues and limits

How long a step waits for room. The guide [Queues and limits](../guide/queues.md) explains it. Each
Flight may override these settings.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `queues.max_wait` | duration or `off` | `60m` | The longest a step waits for room (the step namespace's quota, a node with space) before it fails with the reason and what to change. `off` waits for ever |
| `queues.runs` | whole number | `10` | How many Sorties of a Flight go at once. `0` means no limit. Pushes to protected branches and runs started by hand are never held by it |
| `queues.pipeline_runs` | whole number | no limit | How many Sorties of one pipeline go at once |
| `queues.pull_request_runs` | whole number | `4` | How many pull-request runs of a Flight go at once |
| `queues.push_runs` | whole number | no limit | How many Sorties for pushes to branches that are not protected go at once |
| `queues.schedule_runs` | whole number | no limit | How many Patrols (scheduled Sorties (runs)) go at once |
| `queues.manual_runs` | whole number | no limit | How many Sorties started by hand, by a webhook or by a re-run go at once |
| `queues.automation_runs` | whole number | `2` | How many Sorties started by [automation accounts](#automation-accounts) go at once: a burst of Maintenance (dependency updates) runs this many at a time |
| `queues.supersede_pull_requests` | boolean | `true` | A new commit on a pull request cancels the older run of the same pull request, with the reason on the old run. A pipeline's `supersede: false` keeps every run of its own |

## Automation accounts

Accounts that are bots: dependency updaters and release tools. Their Sorties are counted apart in the
queue (`queues.automation_runs`).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `automation.accounts` | list of patterns | `*[bot]`, `dependabot*`, `renovate*`, `github-actions*`, `release-please*`, `launchway-updater*` | Account names, matched without regard to case against the commit's author and the person who started the Sortie; `*` and `?` match. An empty list names none |

## Tests

Limits on what the server keeps of the Debriefs (test reports) steps declare. `0` means the default.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `tests.max_upload_mib` | whole number | `24` | MiB of report files accepted per step attempt; Stations (runners) are told and send no more. At most 1024 |
| `tests.max_cases_per_run` | whole number | `50000` | Individual results one Sortie (run) keeps; beyond it, only the totals |
| `tests.max_cases_per_project` | whole number | `1000000` | Individual results a Flight keeps over all its Sorties, the oldest runs' removed first. At least `max_cases_per_run` (checked when the server starts) |
| `tests.max_age_days` | whole number | `180` | How long individual test results and a test's statistics (its typical duration, first and last appearance) are kept. Older results are removed whatever the count; each Sortie's totals stay. At most 3650 |

## Reports

Limits on test attachments, coverage and hosted HTML reports, and where hosted reports are served
from. `0` means the default. See [Debriefs](../guide/test-reports.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `reports.max_attachment_files` | whole number | `2000` | Files of a step's attachments directory the server accepts |
| `reports.max_attachment_mib` | whole number | `512` | MiB of attachments per step attempt |
| `reports.max_html_files` | whole number | `5000` | Files of one hosted HTML report |
| `reports.max_html_mib` | whole number | `256` | MiB of one hosted HTML report |
| `reports.max_coverage_upload_mib` | whole number | `24` | MiB of coverage reports accepted per step attempt. At most 1024 |
| `reports.max_coverage_files_per_run` | whole number | `20000` | Per-file coverage records one Sortie keeps; beyond it, only the totals |
| `reports.max_coverage_files_per_project` | whole number | `2000000` | Per-file coverage records a Flight keeps, the oldest runs' removed first |
| `reports.origin` | URL | none | A separate origin, such as `https://reports.example.com`, that alone serves hosted HTML reports, on its own host name pointing at the same servers. Without it, reports are served from Launchway's own origin, sandboxed |
| `reports.brand_name` | text | none | A name for the header and cover of exported Debriefs (HTML and PDF), at most 60 characters. Without it they say Launchway |
| `reports.brand_logo` | file path | none | A PNG or JPEG file of at most 256 KiB shown in exported Debriefs. The server reads it when it starts |
| `reports.change_coverage_floor` | number | `80` | The share of a pull request's changed lines that should be covered by tests, from 0 to 100. It advises in the Debrief and the pull request summary; nothing fails |

The other sizes are at most 1048576. Limits are checked when the server starts.

## Cargo {#artifacts}

Where the files steps keep are stored, and limits on them. Several servers need one store they all
share: the database (the default on PostgreSQL), `s3`, or a directory on a shared filesystem. See
[Where Cargo are kept](../artifacts.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `artifacts.store` | text | `database` on PostgreSQL, `filesystem` on SQLite | `database`, `filesystem` or `s3`. An installation that kept Cargo on the filesystem before the database store existed keeps doing so until they are copied with `launchway blobs copy` |
| `artifacts.database.max_total_gib` | whole number | `100` | How much the database store may hold in all, uploads in progress included, in GiB. Only with the database store |
| `artifacts.database.max_blob_mib` | whole number | `2048` | The largest single file the database store keeps, in MiB. Only with the database store |
| `artifacts.dir` | directory | `<data>/artifacts` | The filesystem store's directory. Not with `s3` |
| `artifacts.s3.endpoint` | URL | required with `s3` | Scheme and host, such as `https://s3.eu-west-1.amazonaws.com` or `http://minio.example.com:9000` |
| `artifacts.s3.region` | text | `us-east-1` | The region |
| `artifacts.s3.bucket` | text | required with `s3` | The bucket; it must exist |
| `artifacts.s3.prefix` | text | none | Prepended to every object's name, such as `launchway/` |
| `artifacts.s3.path_style` | boolean | `true` | Path-style requests, which MinIO, Ceph and AWS accept; `false` for virtual-hosted style |
| `artifacts.s3.ca_file` | path | none | An extra CA bundle (PEM) for a private endpoint |
| `artifacts.max_file_mib` | whole number | `1024` | The largest file a Cargo may hold, in MiB. At most 80000 |
| `artifacts.max_run_mib` | whole number | `4096` | How much one run may keep, in MiB. At least `max_file_mib` |
| `artifacts.max_project_gib` | whole number | `50` | How much a Flight may keep over all its Sorties, in GiB. At least `max_run_mib` |
| `artifacts.retain` | text | `30d` | How long Cargo are kept when the pipeline does not say: `30d`, `2w`, `12h`. From `1h` to `3650d` |
| `artifacts.max_retain` | text | `90d` | The longest any Cargo is kept, whatever the pipeline says. At least `retain` |

S3 credentials come from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or the `_FILE` variants),
never from the file.

## Forge

Launchway's own Git hosting: it keeps repositories in the database and the Cargo store and serves them
over HTTPS and, when `forge.ssh.listen` is set, SSH (see [Hosted repository storage](../development/forge-storage.md)). It needs `git` 2.38 or newer on
the server (older git: hosting refuses to turn on and says so; `launchway doctor git` shows the version) and a blob store shared by every server. Turning it off stops the Git endpoints and keeps the
data. These are Base settings, kept in the database: an administrator sets them under
Configuration, Base settings, with `launchway config` or through the API (a file's `forge:` section
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

## Depot {#registry}

Launchway's built-in OCI registry: container images, Helm charts and other OCI Cargo, served under `/v2/`
beside the API (see [the Depot guide](../guide/registry.md)). It keeps blobs in the Cargo store
(`artifacts.store`), so several servers need a store they all share, as Git hosting does. It is on by default
wherever the server has a Cargo store **and sign-in**: with sign-in off the Depot answers `503`
(`needs_sign_in`), because there is no anonymous Depot. These settings are read from the file when the
server starts. Quotas, retention and immutable tags are not here: they are policies kept in the database, set
per installation, Flight or repository (`launchway registry policy set`).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `registry.enabled` | boolean | `true` | Turn the Depot off with `false`. Nothing is deleted |
| `registry.public_url` | URL | `forge.public_url`, then the address a request came in on | The address clients push and pull at, without a path (`https://registry.example.com`); also the address pipelines' image steps use when they name no Depot |
| `registry.fallback` | text | none | The Depot (a name your Stations' configuration lists, such as `harbor`) that the starter Flight plans push to when the Depot cannot serve here, which is when sign-in is off. Without it they do not push by default in that case, and the Sortie says why. A Depot named by the variable `registry` is used first |
| `registry.allow_insecure_http` | boolean | `false` | Accept a password or token over plain HTTP from any address; otherwise only a loopback listener does |
| `registry.max_blob_mib` | whole number | `10240` | The largest layer or blob accepted, in MiB |
| `registry.max_manifest_kib` | whole number | `4096` | The largest manifest accepted, in KiB |
| `registry.default_quota_gib` | whole number | `20` | What a Flight may store when no policy says otherwise, in GiB; `-1` for no quota. Each distinct blob is counted once |
| `registry.requests_per_minute` | whole number | `3000` | The most requests one caller makes in a minute; `-1` for no cap |
| `registry.max_repositories_per_project` | whole number | `500` | The most repositories a Flight may have |
| `registry.max_tags_per_repository` | whole number | `5000` | The most tags a repository may have |
| `registry.gc_grace` | text | `1h` | How old an unreferenced blob must be before garbage collection removes it, so a push in progress is never collected. At least `1m` |
| `registry.maintain_every` | text | `6h` | How often retention rules are applied and garbage is collected; `off` turns the schedule off (`launchway registry gc` still works). At least `1m` |
| `registry.upload_ttl` | text | `24h` | How long an unfinished upload may sit idle before it is removed. At least `1m` |

## Findings

Limits on what the server keeps of security findings (SARIF) and SBOMs. `0` means the default.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `findings.max_upload_mib` | whole number | `32` | MiB of SARIF files accepted per step attempt. At most 1024 |
| `findings.max_per_run` | whole number | `20000` | Findings one Sortie keeps; beyond it, only the counts |
| `findings.max_per_project` | whole number | `500000` | Findings a Flight keeps, the oldest runs' removed first. At least `max_per_run` |
| `findings.max_components_per_run` | whole number | `20000` | SBOM components one Sortie keeps for searching |
| `findings.max_components_per_project` | whole number | `1000000` | SBOM components a Flight keeps. At least `max_components_per_run` |
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
| `schedules.enabled` | boolean | `true` | `false` turns every Flight's schedules off (a Flight's own setting wins). While off, firings are recorded as skipped and are not caught up later |

## Airfields {#environments}

`environments` lists where [launch (deploy) steps](pipeline.md#kind-deploy) may deploy. Without any, deploy
steps are refused. Checked when the server starts.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `environments[].name` | name | required | Lower-case letters, digits and hyphens, at most 63, unique |
| `environments[].target` | name | required | What launch Stations serve to reach it, in practice its cluster. Same rules as `name` |
| `environments[].projects` | list of patterns | required | The Flights that may launch here, such as `web-*` |
| `environments[].queue` | text | `newest` | `newest`: a Sortie waiting for the lock is skipped when a newer run of the same service starts waiting. `all`: they launch in turn |
| `environments[].protected` | boolean | `false` | The person who started a run may not promote or retry its deployment here, nor waive its launch gate, unless `approvals.allow_self` lets them approve their own runs |
| `environments[].reviewers.users` | list of names | none | With `reviewers.groups`: who may acknowledge an Abort (emergency Return to base (rollback)) of this Airfield; when neither is given, any operator of the Flight other than the person who asked for it (unless `approvals.allow_self` lets them) |
| `environments[].reviewers.groups` | list of names | none | Groups whose members may acknowledge an Abort of this Airfield |
| `environments[].emergency_rollback` | `allow` or `require_approval` | the installation's | Overrides `approvals.emergency_rollback` for this Airfield |
| `environments[].write_back.repo` | `owner/name` | none | A GitOps repository hosted by this Launchway that the Airfield's launch steps write to with a credential of their own (nothing stored). Without `write_back` they use the deploy Station's configured credentials. See [GitOps write-back](../guide/gitops-write-back.md) |
| `environments[].write_back.mode` | `direct` or `pull_request` | required with `write_back` | `direct`: the step pushes to the branch, and a pipeline writer rule of the repository admits its Flight for exactly the files it may write. `pull_request`: the step pushes a branch of its own and opens a pull request |
| `environments[].write_back.merge` | `auto` or `person` | `auto` | `pull_request` only: `auto` merges the pull request itself as soon as the repository's merge rules are met; `person` waits for a person to merge it |
| `environments[].write_back.method` | `merge`, `squash` or `rebase` | the repository's default | `pull_request` only: how the pull request is merged |
| `environments[].min_approvals` | whole number | `0` | With `protected: true`: the fewest distinct approvals an approval step that guards a deployment here needs, 1 to 5, whatever its pipeline's `approvers.count` says |

## Ramps (Station (runner) pools) {#runner-pools}

`runner_pools` gives Stations pools. A **launch** pool has targets, and its Stations take launch work
for them, with the pool's own token or enrolled with a join token. A **build** pool serves Flights
(and optionally only some events), and its Stations enrol with join tokens. The build pool named
`default` is the pool of the shared Station token: without it listed, it serves every Flight that no
other build pool serves; list it to limit what it serves or to require identities. See
[Stations on other machines and clusters](../guide/runner-identity.md). Checked when the server
starts.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner_pools[].name` | text | required | Unique |
| `runner_pools[].kind` | text | `deploy` | `deploy` (serves targets) or `build` (serves Flights) |
| `runner_pools[].token_env` | variable name | | A launch pool's token, from an environment variable. Exactly one of `token_env` or `token_file`, unless `require_identity` is set (then neither). At least 32 characters, different from every other pool's and from the shared Station token. A build pool has none: its Stations enrol with join tokens |
| `runner_pools[].token_file` | path | | Or from a file |
| `runner_pools[].targets` | list | required for a launch pool | Targets its Stations may serve; each must be some Airfield's `target` |
| `runner_pools[].projects` | list of patterns | required for a build pool (`["*"]` for all) | Flights a build pool's Stations take work for. The pool named `default` may leave it out |
| `runner_pools[].events` | list | every event | A build pool: only runs started this way, such as `push` or `manual`, and not, say, pull requests |
| `runner_pools[].require_identity` | boolean | `false` | Refuse shared tokens: only Stations enrolled with a join token for the pool serve it. For the pool named `default` (the build pool of the shared Station token), refuse the shared token; turn the built-in Station off first |

## Station administration {#runner-administration}

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `runner_admin` | text | `admin` | The role needed on every Flight to manage Stations: make join tokens, and revoke and rotate Stations. `admin`, `operator` or `none` (managing Stations over the API and the web UI is off) |

## Verification

`verification.sources` names the health sources that [verify steps](../guide/verification.md) may
read. Every server lists the same ones. Where each is and its credentials are in the launch Stations'
configuration (`health_sources`, see [Station configuration](runner-config.md#health-sources)).
Unset, verify steps are refused.

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `verification.sources[].name` | name | required | What pipelines call the source |
| `verification.sources[].type` | text | required | `prometheus`, `loki`, `clickhouse`, `elasticsearch`, `opensearch`, `postgres`, `http` or `check` |
| `verification.sources[].projects` | list of patterns | none | The Flights that may use it |

## AI models

`ai` is the Depot of models that `ai` steps, verifications and **Explain** may use. Nothing is
sent to any model unless one is listed here. See [AI models](../guide/ai.md).

<!-- keys: server -->
| Setting | Type | Default | Description |
|---|---|---|---|
| `ai.models[].name` | name | required | What pipelines call the model: a lower-case letter, then letters, digits and hyphens, at most 40 characters; unique |
| `ai.models[].provider` | text | required | `anthropic` (the Messages API), or `openai` for any OpenAI-compatible chat completions server (Ollama, vLLM, LM Studio) |
| `ai.models[].model` | text | required | The provider's model id, without spaces, such as `claude-sonnet-5-5` or `qwen3:8b` |
| `ai.models[].endpoint` | URL | `https://api.anthropic.com` for `anthropic`; required for `openai` | The base URL, `http` or `https`, without a query or a user name |
| `ai.models[].key.store` | name | | Where the API key is kept: a secret store of the Station that makes the call (and of `ai.secrets` on a server). A reference, never a value. Required for `api.anthropic.com`; a local server may need none |
| `ai.models[].key.key` | text | | The key in the store |
| `ai.models[].key.field` | text | | The field of the key |
| `ai.models[].projects` | list of patterns | none | The Flights that may use the model. Empty means none |
| `ai.models[].max_tokens` | whole number | `4096` | Output cap per call, 1 to 128000 |
| `ai.models[].max_input_bytes` | whole number | `262144` | Bytes of instructions and prompt per call, at most 8 MiB |
| `ai.models[].calls_per_run` | whole number | `20` | Calls one run may make, 1 to 1000 |
| `ai.models[].tokens_per_day` | whole number | `0` | Tokens (input and output) a Flight may use a day. `0` means no limit |
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
| `ai.secrets.<name>.type` | text | required | The server's own secret stores, for the keys of models the server calls itself (only to explain a failed step); Stations read keys from their own. Same types and settings as [`secrets`](runner-config.md#secrets): `env`, `file` or `vault`. Without them the server explains only with models that need no key |
| `ai.secrets.<name>.projects` | list of patterns | every Flight | As for Station secret stores |
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
| `egress.allow` | list | none | Internal hosts (`git.internal`, `*.corp.internal`), addresses and ranges (`10.0.0.0/8`) the server may reach, such as a self-hosted Git server. Kept in the database: an administrator sets it in the Base settings, and the wizards offer to add the host that was refused |

## History

How long the change history of the configuration kept in the database is kept. By default all of it
is, for ever. Like the other settings that moved, it is set in the Base settings once the
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
| `policy.waivers.allow` | text | `admin` | The role needed on a Flight to waive one of its policies' rules (on the Airfield too, for a launch rule): `admin`, `operator` or `none` |
| `policy.waivers.max_days` | whole number | `30` | The longest a waiver may last, in days, 1 to 365. Checked when the server starts |
| `policy.sources[].name` | name | required | Lower-case letters, digits and hyphens; unique, and not `config` |
| `policy.sources[].library` | name | required | One of [`libraries`](#libraries) |
| `policy.sources[].version` | text | required | A tag or a full commit id. A branch is refused (the source then fails closed) |
| `policy.sources[].path` | path | `policies` | The directory of `*.yaml` policy files in the library. No `..` |
| `policy.sources[].projects` | list of patterns | every Flight | The Flights it may govern |
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

- [Station configuration](runner-config.md): executors, secret stores, image builders and launch
  targets.
- [Helm values](helm-values.md): the same settings when Launchway runs on Kubernetes.
- [Back up, restore and upgrade](../howto/backup-restore-upgrade.md).

## Notifications

Who is told when an approval waits or something went wrong, and where. See [Notifications](../guide/notifications.md).

<!-- keys: server -->
| Setting | Type | Default | What it does |
|---|---|---|---|
| `notifications.channels[].name` | name | required | What rules call the channel |
| `notifications.channels[].kind` | text | required | `webhook` (a JSON body, signed when there is a `secret`), `slack`, `discord` or `teams` (the incoming-webhook address of that chat service) |
| `notifications.channels[].url.store` | name | required | The server's secret store (`ai.secrets`) that holds the channel's address. The address of a chat webhook is a credential, so it is only ever a reference |
| `notifications.channels[].url.key` | text | required | The key in the store |
| `notifications.channels[].url.field` | text | | The field of the key |
| `notifications.channels[].secret.store` | name | none | `webhook` only: the store that holds the key the body is signed with (`X-Launchway-Signature-256`, as repository webhooks are) |
| `notifications.channels[].secret.key` | text | | The key in the store |
| `notifications.channels[].secret.field` | text | | The field of the key |
| `notifications.rules[].on` | list | required | The events: `approval.requested`, `run.failed`, `deployment.succeeded`, `deployment.failed`, `deployment.rolled_back`, `rollback.needs_review`, `step.waiting_long`, `release.published`, `release.promoted`, `release.returned`, `freeze.started` |
| `notifications.rules[].to` | list | required | The names of the channels that get them |
| `notifications.rules[].projects` | list of patterns | all | Only events of these Flights |
| `notifications.rules[].environments` | list of patterns | all | Only events of these Airfields (deployments and Returns to base) |
| `notifications.rules[].branches` | list of patterns | all | Only runs made from these branches |
| `notifications.rules[].after` | duration | `1h` | For `step.waiting_long`: how long a step waits before it is reported, at least `1m` |
