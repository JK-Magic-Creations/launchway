<!--
Machine-checked convention (internal/api/docs_reference_test.go):
every route the server registers appears exactly once as a table row of the form

  | `METHOD /path` | `access-class` | `audit.action` | what it does |

with the access class name from the "Access classes" table and the audit action the
route records ("—" when it records none). The test fails when a route is missing,
when a row names a route that does not exist, or when its access class or audit
action differs from the route table in internal/api/api.go.
-->

# HTTP API

This page describes every HTTP endpoint a Launchway server serves: who may call it, what it takes and what it returns. It is for people who write scripts and integrations against Launchway, and for operators who put it behind a proxy or load balancer.

> **Verified** on 2026-11-19 against commit f717337: a representative call of every group was made with `curl` against a local server on `127.0.0.1` without sign-in (health, projects, runs, logs, events, tests, findings, Cargo (artifacts), policies, waivers, validate, the live stream, the Logbook, the CSRF refusal, the unknown-endpoint answer); then, with OpenID Connect sign-in on (a test provider), sign-in and sign-out, making API tokens (and the refusal to make one with a token, or with a role above the maker's), and the `401`, `403` and `404` refusals of each kind with tokens and session cookies; the route list is checked against the server's route table by a test. **Not verified:** deployment commands (they need Argo CD), image search with real images, the Git-host webhooks, and the Station (runner) API beyond a refused call.

The command line (`launchway runs`, `policy`, `waivers`) is a client of this API. See [the command-line reference](cli.md).

## Conventions

- **Base URL.** Every path is relative to the server, such as `https://launchway.example.com`. Only these paths exist: anything else under `/api/` answers `404` with `{"error": "no such API endpoint"}`, and any other path serves the web UI.
- **JSON.** Request and response bodies are JSON (`Content-Type: application/json`), except where a route says otherwise. Request bodies are limited to 16 MiB unless stated. A body that is not valid JSON answers `400`.
- **Times** are Unix milliseconds (`created_at`, `finished_at`, `expires_at`, …).
- **IDs** have a prefix: `run_…` for runs, `tok_…` for API tokens, `art_…` for Cargo, `wvr_…` for waivers, `dep_…` for deployments, `cmd_…` for deployment commands.
- **Path values** are URL-escaped. A step ID that contains `/` (a step from a template, such as `vars/show`) must be sent as `vars%2Fshow`; a looped step's brackets as `compat%5Balpine%3D3.21%5D` (or as they are, if your client allows).
- **Security headers.** Every response carries `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`.

## Authentication

How a request is authenticated depends on the server's `auth.mode` and on the route's [access class](#access-classes).

| Server | Caller | How |
|---|---|---|
| Sign-in off (`auth.mode: none`) | anyone | No credentials. Every request acts as the configured `user`, with every right. An `Authorization` header is ignored. |
| Sign-in on (`auth.mode: oidc`) | a browser | The `launchway_session` cookie set by [`GET /auth/callback`](#sign-in). |
| Sign-in on | a script | `Authorization: Bearer lw_…`, an [API token](#api-tokens); `Authorization: token lw_…` and HTTP Basic with the token as the password are accepted too, for clients written for other Git hosts. A request that sends an `Authorization` header is judged by the token alone, never by a cookie. |
| Either | a Station | `Authorization: Bearer <runner token>`: the shared Station token, a launch (deploy) Flight line's token, or the short-lived access token of an enrolled Station. Only the [Station API](#runner-api) accepts these. |
| Either | a webhook caller | A secret the project's configuration names. See [Webhooks](#webhooks). |

A request without valid credentials answers `401` with `{"error": "sign in first, or send a valid API token"}`.

### CSRF header

A request that changes something (any method but `GET`, `HEAD` and `OPTIONS`) and is **not** authenticated by an API token must send:

```text
X-Launchway-CSRF: 1
```

If it also sends an `Origin` header, the host in it must be the server's own `Host`. Otherwise the server answers `403`:

```json
{"error": "this request did not come from Launchway's own pages (the X-Launchway-CSRF header is missing)"}
```

This applies to cookie sessions and to every request on a server with sign-in off, so that no web page can act on a Launchway running on `localhost`. Requests that carry an API token on a server with sign-in on need not send it.

> [!IMPORTANT]
> On a server with sign-in off, an `Authorization` header does not exempt a request from this rule. `launchway runs start`, `runs cancel`, `waivers grant` and `waivers revoke` do not send the header, so they fail with that `403` against such a server. Use the web UI, or `curl` with the header, there.

### Too many failures

An address that fails to authenticate 20 times within a minute (with API tokens, or with webhook secrets) is refused for the rest of that minute with `429 Too Many Requests` and a `Retry-After` header in seconds. Each server counts on its own. Behind a reverse proxy, the address is the last entry of `X-Forwarded-For` only with `server.trust_proxy: true`.

### Background requests

The web UI sends `X-Launchway-Background: 1` on requests it makes by itself, such as refreshing a list. They do not count as activity for the session's idle timeout. Scripts need not send it.

## Access classes

Every route belongs to one access class. The server checks it before the handler runs. A Sortie (run) or project the caller may not see answers `404`, exactly as if it did not exist; one the caller may see but not change answers `403`. See [Roles and permissions](roles.md) for how roles are worked out.

| Class | Who may call | Refusal |
|---|---|---|
| `public` | Anyone. The handler decides (health checks, sign-in, webhooks). | — |
| `runner` | A Station, with the shared Station token, a Flight line's token or an enrolled Station's access token. | `401` |
| `signed-in` | Anyone signed in (any role, or none). Lists show only what the caller may see. | `401` |
| `any-viewer` | Someone with at least the viewer role on at least one project. | `403` |
| `project-viewer` | A viewer of the project in `{name}`. | `404` |
| `project-operator` | An operator of the project in `{name}`. | `404` if not a viewer, else `403` |
| `project-draft` | Whoever `drafts.allow` allows to start draft runs of the project in `{name}` (operator by default). | `404` if not a viewer, else `403` |
| `run-viewer` | A viewer of the project of the run in `{id}`. | `404` |
| `run-operator` | An operator of the project of the run in `{id}`. | `404` if not a viewer, else `403` |
| `repo-viewer` | A viewer of the repository `<owner>/<name>` (bindings name it by that path). | `404` (also when Git hosting is off, with `code: feature_off`) |
| `repo-operator` | An operator of the repository. | `404` if not a viewer, else `403` |
| `repo-admin` | An admin of the repository. | `404` if not a viewer, else `403` |
| `admin` | An admin of every project (from an `access` entry without `projects`). | `403` |

Some handlers check more after the class: deployment commands and approvals that name a deployment need the role on the Airfield (environment), waivers need the role `policy.waivers.allow` names, saving with `force` needs admin on the project, and protected Airfields refuse the Sortie's starter. Those refusals are `403`.

## Errors

Every error is a JSON object with an `error` string:

```json
{"error": "not found: run run_nope"}
```

Some carry more:

| Status | When | Extra fields |
|---|---|---|
| `400` | the request is malformed or a value is out of range | — |
| `401` | no valid credentials | — |
| `403` | not allowed, or the CSRF header is missing | — |
| `404` | not found, or not visible to the caller | — |
| `409` | the request conflicts with the current state (such as re-running a Sortie that is still going) | a Cargo upload may add `received` |
| `410` | a Station's lease has ended | — |
| `413` | the body is too large | — |
| `422` | run inputs have problems | `fields`: an object of input name to problem |
| `422` | the pipeline has problems | `problems`: a list of `{line, column, path, message}` |
| `429` | too many failed authentications | `Retry-After` header |
| `500` | something went wrong on the server; the details are only in the server's log | — |
| `502` | the sign-in provider, or the Git server a save pushes to, could not be reached | — |
| `503` | the server is shutting down, or the database did not answer within the stall budget (`resilience.stall_budget`); nothing was lost and the request may be repeated | `Retry-After` header |

## Pagination

Lists return at most a fixed number of items. Ask for fewer or more with `limit`, up to the maximum:

| Route | Parameter | Default | Maximum | A larger value gives |
|---|---|---|---|---|
| `GET /api/v1/runs` | `limit` | 50 | 500 | the default |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `limit`, `after` | 5000 | 10000 | the default |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `stream` (`stdout`, `stderr`, `system` or `service`), `service` | all but `service` | | a name that is not one is refused |
| `GET /api/v1/runs/{id}/tests/cases` | `limit`, `offset` | 200 | 1000 | the maximum |
| `GET /api/v1/runs/{id}/findings` | `limit`, `offset` | 100 | 500 | the maximum |
| `GET /api/v1/projects/{name}/tests/history` | `limit` | 30 | 200 | the maximum |
| `GET /api/v1/projects/{name}/tests` | `runs` (how many Sorties back) | 30 | 100 | the maximum |
| `GET /api/v1/projects/{name}/security` | `runs` (how many Sorties back) | 30 | — | — |
| `GET /api/v1/components` | `limit` | 100 | 500 | the maximum |
| `GET /api/v1/images` | `limit` | 50 | 500 | the maximum |
| `GET /api/v1/waivers` | `limit` | 200 | 500 | the default |
| `GET /api/v1/audit` | `limit`, `before` | 100 | 500 | the default |

`GET /api/v1/runs/{id}/events` has no paging: it returns the whole log.

Logs page by sequence number: pass the `seq` of the last line you have as `after`. The Logbook pages by entry ID: pass the `next_before` of the previous page as `before`. `next_before` is `0` when a page is shorter than `limit`; when the last page happens to be exactly `limit` long, the next call returns an empty page. Test cases and findings page by `offset`.

## Health

For load balancers and orchestrators. Neither needs credentials.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /healthz` | `public` | — | The process is up: `200 {"status": "ok"}`. |
| `GET /schema/pipeline.json` | `public` | — | The JSON Schema of the pipeline file, for editors to complete and check keys. Also published as `pipeline.schema.json` with the documentation. |
| `GET /readyz` | `public` | — | The server can do useful work: the database answered within 2 seconds, it can be written (not only reached), and the server is not shutting down. |

`GET /readyz` answers `200` with the database schema:

```json
{"schema": {"code": 21, "database": 21, "minimum": 7}, "status": "ok"}
```

The answer also has `database`: its `kind`, `writable`, `standby` (PostgreSQL in recovery) and, on PostgreSQL when the database user may see them (the `pg_monitor` or `pg_read_all_stats` role), `replication` (standbys streaming and synchronous, lag; on a standby the WAL receiver and replay lag) and `archiving`.

It answers `503` with `status` `stopping` while the server drains before it stops, `unavailable` when the database did not answer, `read_only` when the database answers but cannot be written (a standby, or set read-only), so that a load balancer drains a server attached to it, and `incompatible` when the database was migrated by a newer server that this one cannot work with. A Station started with `--health-listen` serves its own `/healthz` and `/readyz`; see [the command-line reference](cli.md#launchway-runner).

## Sign-in

These routes exist only with `auth.mode: oidc`; with sign-in off they answer `404` with `sign-in is off on this server (auth.mode: none)`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /auth/login` | `public` | — | Starts sign-in: redirects (`302`) to the provider. `?next=/path` is where to return afterwards; only paths on this server are accepted. |
| `GET /auth/callback` | `public` | — | Where the provider returns. Checks the answer, sets the `launchway_session` cookie and redirects to `next`. A failure redirects to `/signin?error=<code>`. |
| `POST /auth/logout` | `public` | — | Ends the session on every server. Needs the [CSRF header](#csrf-header). Answers `204`. |

Sign-ins, failed sign-ins and sign-outs are recorded in the Logbook as `auth.sign_in` and `auth.sign_out`. See [Events and audit trail](events.md).

## Server and caller

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/info` | `public` | — | The server's version and schema, and whether sign-in is on. |
| `GET /api/v1/me` | `signed-in` | — | Who the caller is and what they may do. |
| `GET /api/v1/setup` | `public` | — | Whether this installation still waits for its first administrator, and when the setup code expires. |
| `POST /api/v1/auth/setup-now` | `admin` | `auth.setup_now` | Only on an installation that runs with no sign-in on an address others can reach: issues a one-time setup code (answered once) and records that the servers' next start runs with built-in accounts. `404` elsewhere; needs the [CSRF header](#csrf-header). |
| `POST /api/v1/setup/begin` | `public` | — | First-run step one: the one-time setup code, the administrator's name and password. Answers a second-factor secret (an `otpauth` address) and a sealed `pending` value. Needs the [CSRF header](#csrf-header). |
| `POST /api/v1/setup/complete` | `public` | — | First-run step two: the `pending` value and a code from the authenticator. Creates the administrator, signs them in and returns ten recovery codes once. |
| `POST /api/v1/auth/local/signin` | `public` | — | Built-in sign-in, step one: user name and password. Answers `signed_in`, or `second_factor` with a short-lived `ticket`. Locks an account with backoff after repeated failures (`429`, `Retry-After`). |
| `POST /api/v1/auth/local/second-factor` | `public` | — | Built-in sign-in, step two: the `ticket` and a TOTP `code` or a `recovery_code`. |
| `POST /api/v1/auth/link` | `public` | — | What a one-time link (invitation or reset) is for, without using it. `404` when it is unknown, used or expired. |
| `POST /api/v1/auth/link/begin` | `public` | — | Accepting a link, step one: the token and a new password. Answers the second-factor secret and a `pending` value. |
| `POST /api/v1/auth/link/complete` | `public` | — | Accepting a link, step two: `pending` and a code. Sets the password, signs the person in, returns recovery codes once. |
| `GET /api/v1/account` | `signed-in` | — | The caller's built-in account: second factor on, recovery codes left, whether a second factor is required for their role. |
| `POST /api/v1/account/password` | `signed-in` | `auth.local.password_changed` | Change the caller's password; asks for the current one (and a code). Other sessions end. |
| `POST /api/v1/account/second-factor` | `signed-in` | `auth.local.second_factor_begin` | Start adding an authenticator; answers the secret. |
| `POST /api/v1/account/second-factor/confirm` | `signed-in` | `auth.local.second_factor_added` | Prove the authenticator with a code; answers ten recovery codes once. |
| `DELETE /api/v1/account/second-factor` | `signed-in` | `auth.local.second_factor_removed` | Remove the second factor (refused where the role requires one). |
| `POST /api/v1/account/recovery-codes` | `signed-in` | `auth.local.recovery_codes_renewed` | Replace the recovery codes. |
| `POST /api/v1/account/sign-out-everywhere` | `signed-in` | `auth.sign_out_everywhere` | End every session of the caller. |
| `GET /api/v1/users` | `admin` | — | The built-in accounts, with no secrets. |
| `POST /api/v1/users` | `admin` | `user.invite` | Invite a person with a role; answers a one-time link (shown once, valid 72 hours). |
| `POST /api/v1/users/{name}/reset` | `admin` | `user.reset` | A one-time reset link for a person who is locked out (valid 24 hours). |
| `POST /api/v1/users/{name}` | `admin` | `user.change` | Change role or turn the account off or on. The last administrator is protected. |
| `DELETE /api/v1/users/{name}` | `admin` | `user.delete` | Delete an account (not the last administrator). |
| `POST /api/v1/users/{name}/sign-out` | `admin` | `user.sign_out` | End every session of that person. |

`GET /api/v1/info` with sign-in off:

```json
{"actor": "dana", "auth_mode": "none", "projects": 2, "schema": {"code": 21, "database": 21, "minimum": 7}, "version": "f717337"}
```

With sign-in on it has only `auth_mode` (`oidc`), `version` and `schema`.

`GET /api/v1/me`:

```json
{"admin": true, "auth_mode": "none", "groups": [], "roles": {"demo-app": "admin", "demo-lib-app": "admin"}, "runners": true, "user": "dana", "via": "none"}
```

`via` is `none` (sign-in off), `session` or `token`. `roles` has the caller's role on each project they have one on, after any [token limits](#api-tokens). `admin` says whether they may read the Logbook; `runners` whether they may see the Stations.

## Projects

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects` | `signed-in` | — | The projects the caller may see. |
| `GET /api/v1/projects/{name}` | `project-viewer` | — | One project. |
| `GET /api/v1/projects/{name}/refs` | `project-viewer` | — | The project's branches and tags. |
| `GET /api/v1/projects/{name}/inputs` | `project-viewer` | — | The inputs the pipeline declares at `?ref=` (default: the default branch), for a start form. |
| `GET /api/v1/projects/{name}/schedules` | `project-viewer` | — | The schedules read from the default branch, when each fires next, recent firings, and the project's webhook triggers. |
| `GET /api/v1/projects/{name}/lanes` | `project-viewer` | — | The project's [lanes](pipeline.md#lane) that have a Sortie going or runs waiting: for each, its `name`, `key` and `label`, the Sortie that holds it (`holder`) and the Sorties waiting, in the order they start (`waiting`, each with its `position`). A Sortie's own place is its `lane` block in the Sortie JSON: `name`, `key`, `label`, `state` (`holding`, `waiting`, `replaced` or `skipped`), `position`, and `behind` (the run it waits behind, or that replaced it). `GET /api/v1/runs?lane=NAME` lists only runs in that lane. A waiting run is canceled with the Sortie's cancel route. |

A project object has the configuration (`name`, `url` with credentials removed, `default_branch`, `poll_seconds`, `push`), the latest poll (`last_fetch`, `decisions`), its newest run as `last_run`, and `editor`, what the caller may do in the pipeline editor:

```json
"editor": {"drafts_allow": "operator", "may_draft": true, "draft_secrets": false, "may_save": true, "may_force": true}
```

A ref is `{"name", "kind" (branch or tag), "sha", "subject", "author", "committed_at"}`.

## Libraries

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/libraries` | `any-viewer` | — | The shared libraries pipelines may use, by `name`, with the time of the last fetch (`last_fetch`). The first is always `launchway`, marked `builtin: true`: the [starters](../guide/starters.md) that ship with Launchway. |
| `GET /api/v1/libraries/{library}/templates` | `any-viewer` | — | A library's templates at `?version=` (default: its newest tag, else its default branch), with their parameters and outputs. `404` when no libraries are configured. For `launchway` (one version, `builtin`) each template also has its `kind` (`flight plan` or `step template`), an `example` pipeline file and the `graph` of steps that file runs. |

## Runs

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /api/v1/projects/{name}/runs` | `project-operator` | `run.start` | Starts a Sortie. Answers `201` with the Sortie. |
| `GET /api/v1/runs` | `signed-in` | — | Runs of the projects the caller may see, newest first. |
| `GET /api/v1/runs/{id}` | `run-viewer` | — | One run in full. |
| `GET /api/v1/runs/{id}/events` | `run-viewer` | — | The Sortie's event log. |
| `GET /api/v1/runs/{id}/pipeline-file` | `run-viewer` | — | The pipeline text the Sortie ran. |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `run-viewer` | — | A step's log lines. |
| `GET /api/v1/projects/{name}/run-triggers` | `project-viewer` | — | The project's triggers on other projects' results: each with its `kind` (`run` or `launch`), `upstream_project`, `when`, `filter`, `ref` and `mapping`. See [Triggers on results](../guide/result-triggers.md). |
| `POST /api/v1/projects/{name}/run-triggers` | `project-operator` | `run_trigger.create` | Makes a trigger: `{"name", "upstream_project", "kind", "when", "upstream_pipeline", "filter", "ref", "mapping"}`. `409` if the upstream project has not allowed this one, or if it would make a cycle; `400` for an expression that cannot be read. The caller must be able to read the upstream project. |
| `GET /api/v1/projects/{name}/run-triggers/graph` | `project-viewer` | — | What the software graph says this project depends on, for starting it after them: the projects that build the repositories its repository depends on, in build order (`level` 0 first), each with whether it has allowed this project to trigger on its results (`allowed`), the trigger that already exists (`trigger`) and a plain `note`. Nothing is made. `404` when the graph is off. See [Triggers from the software graph](../guide/software-graph.md#start-dependents-after-a-push). |
| `POST /api/v1/projects/{name}/run-triggers/graph` | `project-operator` | `run_trigger.from_graph` | Makes the result triggers the plan shows, for the projects that have allowed this one: a trigger named `graph-<upstream>` that starts a run of this project when a run of the upstream ends well. Answers the plan with `created` set on each trigger made. A project that has not allowed it is left out, with the reason. Nothing is made until this is called. |
| `DELETE /api/v1/projects/{name}/run-triggers/{trigger}` | `project-operator` | `run_trigger.delete` | Removes a trigger. |
| `GET /api/v1/projects/{name}/run-trigger-allows` | `project-viewer` | — | The projects this project allows to trigger on its results, and whether each may read step outputs. |
| `POST /api/v1/projects/{name}/run-trigger-allows` | `project-operator` | `run_trigger.allow` | Allows `{"downstream_project", "share_outputs"}`. The caller is an operator of this (the upstream) project. |
| `DELETE /api/v1/projects/{name}/run-trigger-allows/{downstream}` | `project-operator` | `run_trigger.revoke` | Withdraws it; the downstream project's triggers stay and are refused, with the reason, until it is given again. |
| `GET /api/v1/projects/{name}/event-sources` | `project-viewer` | — | The project's event sources, each with its `url`, `mapping`, `filter`, `dedupe_key`, window and limits, and whether it has a secret (`has_secret`). The secret is never returned. See [Event sources](../guide/event-sources.md). |
| `POST /api/v1/projects/{name}/event-sources` | `project-operator` | `event_source.create` | Makes a source: `{"name", "mapping": {"input": "expression"}, "filter", "dedupe_key", "schema" (JSON Schema text), "ref", "debounce_seconds" or "batch_seconds", "max_body", "rate_per_minute"}`. Answers `201` with the source and its `secret`, **shown once**. Every expression is checked when it is saved (`400` says which). `409` with code `no_root_key` on a server with no root key. |
| `POST /api/v1/projects/{name}/event-sources/{source}` | `project-operator` | `event_source.change` | Changes the fields that are given, and with `"rotate_secret": true` makes a new secret (answered once). `"enabled": false` turns the source off. |
| `DELETE /api/v1/projects/{name}/event-sources/{source}` | `project-operator` | `event_source.delete` | Removes the source and what it remembers. |
| `POST /api/v1/projects/{name}/event-sources/{source}/test` | `project-operator` | `event_source.test` | Judges `{"body": {...}, "headers": {...}}` as a call would be (schema, filter, dedupe key, mapping, the pipeline's inputs) and records and starts nothing. Answers `status` (`ready`, `filtered`, `refused` or `error`), the `reason`, any `problems`, and the `inputs` the run would get. |
| `POST /api/v1/projects/{name}/sources/{source}` | `public` | — | The call itself: a JSON body up to the source's limit, signed with `X-Launchway-Timestamp` and `X-Launchway-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>">` (five minutes, once). `201` started or held, `202` waiting in a window, `200` filtered or duplicate, `422` refused or error (with `problems`), `404` for a source that does not exist or cannot prove itself, `409` for a signature used before, `413` too large, `429` over the rate. Every answer but `404` for a missing source is an event, and the call is in the Logbook as `event_source.call`. |
| `GET /api/v1/events` | `signed-in` | — | The event log: what the server received (pushes, pull requests, schedule firings, webhook calls, runs started by hand, other projects' results), newest first, limited to the projects the caller may read. Each event has its `id`, `project`, `at`, `source`, `verified`, `summary`, `payload` as kept (credential-looking values removed), and `decisions`: for each trigger judged, the `decision` (`started`, `held`, `waiting`, `filtered`, `superseded`, `skipped`, `duplicate`, `refused` or `error`), the `reason` in words and the `run_number` it started. Filters: `project`, `source`, `decision`, `verified` (`yes` or `no`), `ref`, `since` and `before` (Unix milliseconds or RFC 3339), `limit` (100 by default, at most 500). See [Events](../guide/events.md). |
| `GET /api/v1/events/{id}` | `signed-in` | — | One event with its decisions. An event of a project the caller cannot read is `404`. |
| `POST /api/v1/events/{id}/replay` | `signed-in` | `event.replay` | Starts the run the event asked for again, at the commit it was about and with the inputs it had, for an operator of its project (`403` otherwise). Answers `201` with the `run` and `replay_of`. A new event points back to the first. An event that was not verified is `409` with code `not_verified`; one with nothing to repeat is `409` with code `not_replayable`. |
| `GET /api/v1/queue` | `signed-in` | — | The Sorties held back by a limit on concurrent Sorties, or by an operator, in the order they will go, limited to the projects the caller may read (`?project=` for one): each with its `run`, `number`, `project`, `pipeline`, `class`, `position`, `code` (`automation_limit`, `kind_limit`, `pipeline_limit`, `project_limit` or `held_by_operator`), `reason`, the numbers of the runs it waits `behind`, and `actions`. See [Queues and limits](../guide/queues.md). |
| `POST /api/v1/runs/{id}/trust` | `run-operator` | `run.trust` | A maintainer's *Run with secrets* on a restricted run (`{"comment": "..."}`): the steps that have not started may read secrets and launch. Recorded in the Sortie's events and the Logbook. Refused for a Sortie that is not restricted, or is over. The Sortie's `restriction` block says whether it is restricted and why, and its `actions` list `run_with_secrets`. See [Pull-request trust](../guide/pull-request-trust.md) |
| `POST /api/v1/runs/{id}/queue` | `run-operator` | `run.queue` | Moves a Sortie that waits in a Holding pattern (queue): `{"action": "run_next"}` puts it at the front, `hold` keeps it waiting, `release` lets the limits decide again. Refused with a reason for a Sortie that already goes. A Sortie that is held lists these in its `actions`. |
| `POST /api/v1/runs/{id}/cancel` | `run-operator` | `run.cancel` | Cancels the Sortie. |
| `POST /api/v1/runs/{id}/resume` | `run-operator` | `run.resume` | Resumes a failed or canceled run, from the steps that did not succeed. |
| `GET /api/v1/runs/{id}/rerun` | `run-viewer` | — | What re-running from `?from=<step>` (given once for each step) or `?failed=true` would run again and reuse. |
| `POST /api/v1/runs/{id}/rerun` | `run-operator` | `run.rerun` | Starts a new Sortie that re-runs a finished one from a step, several steps or the failed ones. Answers `201` with the new Sortie. |

**Start a Sortie.** The body:

```json
{"ref": "main", "inputs": {"scan": "full", "alpine": ["3.21", "3.22"]}, "libraries": {"platform": "v2"}}
```

`ref` is a branch or tag; empty means the default branch. `inputs` gives values by name (a string, number, boolean or list of those; the others take their defaults). `libraries` overrides the version of a library the pipeline uses, by the pipeline's name for it. Values that do not fit answer `422` with `fields`.

```sh
curl -s -X POST -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' \
  -d '{"ref":"main","inputs":{"scan":"quick"}}' \
  http://127.0.0.1:8080/api/v1/projects/demo-app/runs
```

```json
{"id": "run_06gzdc4xkndxsbjw21eg", "project": "demo-app", "number": 1, "pipeline": "demo-app", "status": "queued", "event": "manual", "ref": "refs/heads/main", "branch": "main", "tag": "", "sha": "fa0b04cb…", "actor": "dana", "message": "Initial demo app", "created_at": 1795140132253, "steps": [{"id": "lint", "status": "queued"}, …], …}
```

**List runs.** `GET /api/v1/runs?project=demo-app&status=failed&limit=20`. `status` is one of `queued`, `running`, `succeeded`, `failed`, `canceled`.

**A Sortie.** `GET /api/v1/runs/{id}` adds to the summary: `step_list` (each step's `status`, `attempt`, `error`, `exit_code`, `outputs`, `runner`, times, and `inputs`: what the attempt was given, without secret values), `graph` and `levels` (the pipeline as run), `trigger_detail` (inputs, schedule, webhook), `tests`, `findings`, `artifacts`, `deployments`, `policy`, `reruns`, `source` (credentials removed from addresses) and `draft`.

**Events.** `GET /api/v1/runs/{id}/events` is the Sortie's log of what happened, in order: `[{"seq", "run_id", "type", "at", "data"}, …]`. The first event's pipeline, each `step.leased` event's inputs, and a draft run's text are left out (other routes return them). See [Run events](events.md#run-events).

**Pipeline file.** `{"text": "...", "draft": false}`; for a draft run also `hash`; for a pipeline the server configured, `origin`.

**Logs.** `GET /api/v1/runs/{id}/steps/{step}/logs?attempt=1&after=0&limit=1000`. `attempt` `0` (the default) is the latest:

```json
{"attempt": 1, "lines": [{"seq": 1, "at": 1795140132325, "stream": "system", "text": "Runner build-1, executor container (podman)"}]}
```

`stream` is `stdout`, `stderr` or `system` (what Launchway itself says). Secrets are masked before lines reach the server.

**Cancel and resume.** No body. Answers `{"ok": true}`. Resuming a draft run needs the right to start draft runs.

**Re-run.** `GET …/rerun?from=smoke-test` answers `{"from": ["smoke-test"], "run": ["smoke-test", …], "reuse": ["lint", …]}`, or `409` with why it cannot be re-run from there. `from=` may be repeated, and `failed=true` asks for the smallest set of steps that covers every failed and canceled step. `POST …/rerun` takes `{"from": "smoke-test"}`, `{"from": ["lint", "unit"]}` or `{"failed": true}`; the new Sortie's JSON has `rerun_from` (the first step) and, for several, `rerun_froms`. The command line has `launchway runs resume` and `launchway runs rerun`, and the MCP tool `retry_run` takes `from_step`, `from_steps` and `failed_only`.

## Approvals

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/approvals` | `signed-in` | — | Approval steps waiting for a decision, in the projects the caller may see. |
| `GET /api/v1/notifications` | `signed-in` | — | The log of notifications sent or tried: channel, event, status, attempts and why a delivery failed, for the projects the caller may read. `limit` (at most 500). |
| `POST /api/v1/runs/{id}/steps/{step}/approve` | `run-operator` | `approval.approve` | Approves a waiting approval step. |
| `POST /api/v1/runs/{id}/steps/{step}/reject` | `run-operator` | `approval.reject` | Rejects it. |

The body is optional: `{"comment": "Looks good"}`. Answers `{"ok": true}`. An approval that names a deployment also needs the operator role on that Airfield. With sign-in on, the person who started the run may not approve it unless `approvals.allow_self` is `true` (`403`).

## Pipeline editor

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/pipeline` | `project-viewer` | — | The pipeline file at the tip of `?ref=` (default: the default branch). |
| `POST /api/v1/projects/{name}/pipeline` | `project-operator` | `pipeline.save` | Commits pipeline text to the project's repository, as the caller. |
| `POST /api/v1/projects/{name}/draft-runs` | `project-draft` | `run.draft` | Runs pipeline text that is not in Git against a branch. |
| `POST /api/v1/validate` | `any-viewer` | — | Checks pipeline text without running it. With `?project=NAME` it checks it for that project too, and a valid pipeline may come back with `warnings` (for example, a pull-request filter by target branch in a project with no forge connection). |

**Read.** `{"ref", "kind", "sha", "subject", "author", "committed_at", "path", "exists", "configured", "text"}`.

**Save.** The body is `{"base_ref", "base_sha", "pipeline", "message", "branch", "force"}`. The project needs `push:` in the configuration. `force: true` saves text that has problems and needs the admin role on the project. Answers `201` with where it was saved (`path`, `branch`, `sha`, …); `409` if the branch moved since `base_sha`; `502` if the push failed.

**Draft run.** `{"ref": "main", "pipeline": "<YAML text>", "inputs": {…}, "libraries": {…}}`. Answers `201` with the Sortie. Its steps may read secrets only if `drafts.secrets` is `true`. The pipeline text may be at most the size a pipeline file may be (`413` otherwise).

**Validate.** The body is the pipeline YAML itself, not JSON (at most 1 MiB). With `?project=NAME` (which the caller must be able to see) or `?expand=1`, templates are expanded from the libraries' current versions. It always answers `200`:

```sh
curl -s -X POST -H 'X-Launchway-CSRF: 1' --data-binary @.launchway/pipeline.yaml http://127.0.0.1:8080/api/v1/validate
```

```json
{"expanded": false, "ok": false, "problems": [{"line": 4, "path": "steps.b.needs", "message": "needs \"c\", but there is no step with that name"}]}
```

When the text is valid, `ok` is `true` and `graph` and `levels` describe it.

## Test results

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/tests` | `run-viewer` | — | A Sortie's test totals, step by step, with what stands out. |
| `GET /api/v1/runs/{id}/tests/cases` | `run-viewer` | — | A Sortie's test results, failures first, then the slowest. |
| `GET /api/v1/runs/{id}/tests/cases/{case}` | `run-viewer` | — | One result in full, with its trace and captured output. |
| `GET /api/v1/projects/{name}/tests` | `project-viewer` | — | A project's tests over its last runs of a ref: totals, slowest, most failing, flaky. |
| `GET /api/v1/projects/{name}/tests/history` | `project-viewer` | — | One test's results in the project's last runs. |
| `GET /api/v1/runs/{id}/tests/compare` | `run-viewer` | — | A Sortie's tests beside the previous run of the same ref: new failures, fixed tests and tests still failing. |
| `GET /api/v1/runs/{id}/tests/cases/{case}/attachments/{n}` | `run-viewer` | — | One attachment of a test result (a screenshot, video or trace), number `n` from 0. Supports `Range`, so videos can be played and sought. Served as a download or an image, never as a page. |
| `GET /api/v1/runs/{id}/coverage` | `run-viewer` | — | A Sortie's coverage, step by step, with the previous run of its ref and the project's default branch to compare. |
| `GET /api/v1/runs/{id}/coverage/files` | `run-viewer` | — | A Sortie's per-file coverage: `?step=`, `?q=`, `?sort=` (`path`, `lines` or `missed`), `?limit=` and `?offset=`. |
| `GET /api/v1/runs/{id}/debrief` | `run-viewer` | — | The Debrief of a Sortie (run): summary, change against the default branch's last good run, failures with expected against actual values, where each first failed and whether it also fails on the default branch, flaky and quarantined tests, slowest tests, suites, grid, coverage including the coverage of a pull request's change, trend. |
| `GET /api/v1/runs/{id}/debrief/export` | `run-viewer` | — | The Debrief as a download: `?format=html` (one file, no network needed), `pdf`, `json` or `junit` (every result, merged). |
| `GET /api/v1/runs/{id}/debrief/summary` | `run-viewer` | — | The short Markdown form for a pull request comment; `?format=json` adds the headline and the annotations (failing assertions with a file and line). |
| `GET /api/v1/projects/{name}/tests/flaky` | `project-viewer` | — | The project's flaky tests from its history, with scores, affected branches, first seen and a quarantine entry to copy. `?limit=` (default 100, at most 500). |
| `GET /api/v1/projects/{name}/tests/timings` | `project-viewer` | — | Each test's typical duration from its statistics, slowest first. `?limit=` (at most 100000). |
| `GET /api/v1/projects/{name}/tests/shards` | `project-viewer` | — | A balanced split of the tests for `?count=` shards (1 to 256) by duration. |
| `GET /api/v1/projects/{name}/coverage` | `project-viewer` | — | A project's coverage over its last runs of a ref (`?ref=`, `?runs=`). |
| `GET /api/v1/runs/{id}/reports` | `run-viewer` | — | A Sortie's hosted HTML reports, each with a fresh signed link. |
| `GET /hosted/{link}/{path...}` | `public` | — | A file of a hosted HTML report. It takes no session: the signed `link`, scoped to one report for a few hours and given only to people who may see the Sortie, is the permission. Served with a sandboxing `Content-Security-Policy`, and from the separate origin `reports.origin` when one is configured. |

`…/tests/cases` takes `?status=failed|passed|skipped|flaky`, `?q=` (searched in the name, suite, file and message; at most 200 bytes), `?step=` (with `?attempt=`), `?limit=` and `?offset=`. `{case}` is a result's numeric `id`. The project routes take `?ref=` (a branch, a full ref, or `all`; default the default branch) and `?runs=`. `…/tests/history` needs `?key=`, the `key` of a result. See [Debriefs (test reports)](../tutorials/tests-and-reports.md).

## Cargo findings and SBOMs {#artifacts-findings-and-sboms}

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/artifacts` | `run-viewer` | — | The Cargo a Sortie kept, with their files. |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/file` | `run-viewer` | — | Downloads one file, `?path=`. Supports `Range`. |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/archive` | `run-viewer` | — | Downloads every file of the Cargo as a zip archive. |
| `DELETE /api/v1/runs/{id}/artifacts/{artifact}` | `run-operator` | `artifact.delete` | Deletes a kept Cargo. Answers with what was deleted. |
| `GET /api/v1/artifact-store` | `any-viewer` | — | Where this server keeps Cargo (`database`, `filesystem` or `s3`), why, and how full the store is. |
| `GET /api/v1/runs/{id}/findings` | `run-viewer` | — | A Sortie's security findings. |
| `GET /api/v1/projects/{name}/security` | `project-viewer` | — | A project's findings over its last runs of a ref (`?ref=`, `?runs=`). |
| `GET /api/v1/components` | `signed-in` | — | The Sorties whose SBOMs list a component: `?name=` (exact, ignoring case), `?version=`. |

Downloads are sent as `application/octet-stream` (or `application/zip`) with `Content-Disposition: attachment`, `Cache-Control: private, no-store`, a sandboxing `Content-Security-Policy`, and, for a file, `ETag` and `X-Launchway-SHA256` with its SHA-256.

`…/findings` takes `?severity=` (comma-separated: `critical`, `high`, `medium`, `low`, `unknown`), `?tool=`, `?status=new|existing|fixed`, `?q=`, `?step=`, `?limit=` and `?offset=`. Its answer has `counts`, `statuses`, `steps` and the findings. Secrets found by scanners are never returned.

## Images

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/images` | `run-viewer` | — | What a Sortie's image steps built: digest, tags, platforms, signature, provenance, SBOM and scan. `{"images": […]}` |
| `GET /api/v1/images` | `signed-in` | — | Which run built a digest (`?digest=`), the latest image of a repository (`?repository=host/path`, with `?tag=`), or a project's images (`?project=`). `{"hits": […]}` |

`GET /api/v1/images` needs one of `digest`, `repository` or `project` (`400` otherwise). See [Build and sign an image](../tutorials/build-and-sign-image.md).

## Deployments

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/environments` | `signed-in` | — | The configured Airfields that list a project the caller may see, with what is deployed in each. |
| `GET /api/v1/deployments` | `signed-in` | — | Deployments, newest first: `?environment=`, `?project=`, `?service=`, `?limit=`. |
| `GET /api/v1/home` | `signed-in` | — | The Tower in one answer: what needs you (by the attention rule), the Sorties running now (yours first, then your Squadron's, then the rest), the projects by Squadron with their health, favorites, and what finished lately. `?view=mine` or `all`. Only what the caller may see; counts never include anything else. |
| `GET /api/v1/attention` | `signed-in` | — | What needs a person: `needs` (each with `why` and `do`), `dismissed` (set aside by the caller), `retrying` (failures that a newer Sortie of the same pipeline and branch or pull request is retrying) and `count`. The rule is in the guide page *What the Tower shows*. |
| `POST /api/v1/attention/dismiss` | `signed-in` | `prefs.attention.dismiss` | Sets one entry aside for the caller only: `{"key", "snooze_hours"}`; without snooze it returns when something newer replaces it. A personal preference, recorded in the Logbook as such. |
| `POST /api/v1/attention/restore` | `signed-in` | `prefs.attention.restore` | Brings a set-aside entry back (`{"key"}`, or `*` for all). |
| `GET /api/v1/organize/{area}` | `signed-in` | — | How a list is organized: its folders (only those with something the caller can see in them, or at a level they may arrange), each item's folder and labels, the caller's favorites and the layout in effect. `area` is `pipelines`, `repositories`, `projects` or `runs`. |
| `POST /api/v1/organize/{area}/folders` | `signed-in` | `folder.create` | Makes a folder at a level (`scope`: `base`, `organization`, `unit:<name>` or `project:<name>`). Needs admin of everything at that level. |
| `PATCH /api/v1/organize/folders/{id}` | `signed-in` | `folder.update` | Renames, describes, reorders or moves a folder. A person who may not arrange its level gets 404. |
| `DELETE /api/v1/organize/folders/{id}` | `signed-in` | `folder.delete` | Deletes a folder; what was in it moves up to its parent. |
| `POST /api/v1/organize/{area}/move` | `signed-in` | `folder.move` | Files an item in a folder, or takes it out (`folder` empty). An item lives in one folder. Needs the right to arrange the folder's level. |
| `PUT /api/v1/organize/{area}/labels` | `signed-in` | `labels.set` | Replaces an item's labels (up to 20, lower case). Needs operator on the project. |
| `GET /api/v1/layouts/{page}` | `signed-in` | — | The layout of a page: each level's shared layout from the Base down (a locked level stops the ones below), the caller's own, and the result in effect. `?scope=`. |
| `PUT /api/v1/layouts/{page}` | `signed-in` | `layout.set` | Sets the shared layout of a page at a level, and whether it is locked for the levels below. Needs the right to arrange that level. |
| `DELETE /api/v1/layouts/{page}` | `signed-in` | `layout.reset` | Removes a level's shared layout of a page (`?scope=`). |
| `PUT /api/v1/me/layouts/{page}` | `signed-in` | `prefs.layout.set` | Saves the caller's own arrangement of a page; it follows them across devices and never changes what others see. |
| `DELETE /api/v1/me/layouts/{page}` | `signed-in` | `prefs.layout.reset` | Resets the caller's own arrangement of a page to the shared default. |
| `DELETE /api/v1/me/layouts` | `signed-in` | `prefs.layout.reset_all` | Resets all of the caller's own arrangements (favorites stay). |
| `POST /api/v1/me/favorites` | `signed-in` | `prefs.favorite` | Adds or removes a favorite for the caller: `{"item":"project:<name>" or "repository:<path>","on":true}`. |
| `GET /api/v1/layouts-export` | `signed-in` | — | A level's layouts and folders as one document (`?scope=`), to keep in Git. Needs the right to arrange the level. |
| `POST /api/v1/layouts-import` | `signed-in` | `layout.import` | Applies such a document: adds and updates what it names and never deletes what it leaves out. `?dry_run=1` lists what it would do. |
| `GET /api/v1/search` | `signed-in` | — | One search over what the caller may see: projects, repositories, folders, labels and Sorties (by number, commit or branch). `?q=`, `?limit=` (20, at most 50). Ranked: exact, prefix, word prefix, substring, then a typo. Each hit has a kind, a name, where it lives and a link. |
| `GET /api/v1/activity` | `signed-in` | — | The caller's own trail: `continue` (what they started or changed lately and the last things they looked at, with what changed since for a Sortie), `recent` (newest first), `paused` and `retention_days`. It is the caller's alone: no route takes another user's name, so administrators cannot read it. Entries for projects the caller can no longer see are left out. `?limit=` (100, at most 200). |
| `PUT /api/v1/activity` | `signed-in` | — | Notes (or moves) an entry of the caller's trail, one for each thing they opened: `{kind, obj, project, title, href, action, state}`. Answers 204 and writes nothing while the trail is paused or when the caller cannot see the project. |
| `DELETE /api/v1/activity` | `signed-in` | `prefs.activity.clear` | Deletes the caller's whole trail. |
| `PUT /api/v1/activity/pause` | `signed-in` | `prefs.activity.pause` | Pauses or resumes writing the caller's trail: `{"paused": true}`. |
| `GET /api/v1/launch-lines` | `signed-in` | — | Where each recent change is on its way, as stations worked out from a Sortie's steps and deployments (commit, checks, tests, package, each Airfield, verified), each with a state and one plain sentence; a station that stands for several steps also has `parallel` (how many steps, how many can run together, how many are done, running, waiting, failed or skipped, and each step's state): `?project=`, `?limit=` (12, at most 50), or `?run=` for the line of one Sortie (404 for a run of a project the caller may not see). Only projects the caller may see. |
| `GET /api/v1/settings/approvals` | `signed-in` | — | Whether the person who started a run may approve it here (`allow_self`), and whether people sign in. The approval cards use it to say exactly why someone cannot approve. |
| `GET /api/v1/runs/{id}/deployments/{dep}/controls` | `run-viewer` | — | Which commands the caller could give a deployment now, and why not the others. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote` | `run-operator` | `deployment.promote` | Promotes a paused rollout one step. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` | `run-operator` | `deployment.promote_full` | Promotes a rollout fully, skipping its remaining steps. |
| `POST /api/v1/runs/{id}/deployments/{dep}/abort` | `run-operator` | `deployment.abort` | Aborts a rollout. |
| `POST /api/v1/runs/{id}/deployments/{dep}/retry` | `run-operator` | `deployment.retry` | Retries an aborted rollout. |
| `GET /api/v1/projects/{name}/rollback` | `project-viewer` | — | What a service can be returned to base to in an Airfield after its deployment succeeded: `?environment=` (required), `?service=`. Answers the plan: what runs now (`current`), the lock holder if any (`holder`), and the `rollback` action as the server reports it (`available`, or `reason`, `instead` and `would_help`; `targets`, the earlier successful deployments, each with the deployments it reverts, its `consequence` and `suggestions`; `queueable`, `emergency`, `requires_approval`). Without the operator role on the project (and the Airfield) the action says so and offers no targets. See [Return to base (rollback) after success](../guide/rollback-after-success.md). |
| `POST /api/v1/projects/{name}/rollbacks` | `project-operator` | `deployment.rollback` | Rolls a service back: `{"environment", "service", "to", "reason", "queue"}`. `to` is a deployment ID or a version (default the previous successful deployment); `reason` is required; `queue` waits behind a deployment that holds the Airfield's lock. Answers `201` with the Return to base run. Refused with the action's own `reason` (`409`) when the action is not available. Needs the operator role on the Airfield too. |
| `GET /api/v1/rollbacks/review` | `signed-in` | — | Aborts (emergency Returns to base (rollbacks)) of protected Airfields that no approver has acknowledged, newest first, each with `acknowledge`: whether the caller may, and if not why. Only projects the caller may see. |
| `POST /api/v1/runs/{id}/review` | `run-operator` | `rollback.review` | Acknowledges an Abort: `{"comment"}` (required). Refused for the person who asked unless `approvals.allow_self` is `true`, and for anyone the Airfield's `reviewers` leave out. Answers `{"ok": true}`. |
| `POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw` | `run-operator` | `deployment.withdraw` | Withdraws a command that no launch Station has taken yet. Answers `{"ok": true}`. |

The command routes take an optional `{"comment": "..."}` and answer `201` with the command; a launch Station carries it out. They need the operator role on the deployment's Airfield too. On a `protected: true` Airfield, the person who started the run may not promote or retry (unless `approvals.allow_self` is `true`); anyone with the role may abort.

`…/controls` answers `{"environment", "protected", "controls": [{…, "available", "forbidden", "reason"}], "withdraw", "withdraw_reason"}`. See [Launch with approval](../tutorials/deploy-with-approval.md).

## Releases

Releases and release management (see [Making a release](../guide/releases.md)). Who may do what: a viewer reads published and withdrawn releases; an operator makes drafts, publishes, promotes and withdraws; an administrator of the project sets its release settings and checklists, freezes it and overrides what a checklist allows; an administrator of every project freezes every project and allows public downloads.

| Route | Access | Audit action | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/release-settings` | `project-viewer` | — | How the project's versions are written and ordered (`scheme`: `semver`, `calver`, `natural` or `created`), its `tag_prefix`, `changelog` file and `public` setting, whether the installation allows public downloads, and whether Launchway may write to its repository. |
| `PUT /api/v1/projects/{name}/release-settings` | `project-operator` | `release.settings` | Sets them: `{"scheme", "tag_prefix", "changelog", "public"}`. Needs the admin role on the project. The scheme cannot change once a release is a candidate, published or withdrawn. `public` is refused unless the installation allows public downloads. |
| `GET /api/v1/projects/{name}/releases` | `project-viewer` | — | The project's releases, newest first, without their notes: `?state=` draft, candidate, published or yanked. Drafts are shown only to operators. |
| `POST /api/v1/projects/{name}/releases` | `project-operator` | `release.create` | Makes a draft: `{"version", "from", "adopt", "title", "notes", "generate_notes", "run_id", "allow_lower"}`. `from` is a branch, tag or commit (default the default branch); `adopt` names an existing annotated tag to release instead. `generate_notes` writes the notes from the merged pull requests and commits since the previous release; `run_id` is the Sortie that built it, whose image digests are recorded. A version not higher than the highest so far is refused unless `allow_lower`. Answers `201`. |
| `GET /api/v1/projects/{name}/releases/next` | `project-viewer` | — | The next version proposed from the changes since the last release, with the reason for each change: `?from=` (branch, tag or commit), `?pre=` (a pre-release label such as `rc`). Nothing is made. |
| `GET /api/v1/projects/{name}/releases/notes` | `project-viewer` | — | Release notes generated for a version, not saved: `?to=`, `?version=`. |
| `GET /api/v1/projects/{name}/releases/compare` | `project-viewer` | — | What differs between two releases: `?a=`, `?b=`. The changes, the files and the deployment configuration changed, and the image digests of each. |
| `GET /api/v1/projects/{name}/releases/{version}` | `project-viewer` | — | One release with its notes, assets (SHA-256 and provenance), promotions, clearances and what the caller may do. `{version}` is a version, a tag or the release's id. |
| `PATCH /api/v1/projects/{name}/releases/{version}` | `project-operator` | `release.edit` | Changes the title or notes of a draft or candidate: `{"title", "notes"}`. A published release is refused with `409`. |
| `DELETE /api/v1/projects/{name}/releases/{version}` | `project-operator` | `release.delete` | Deletes a draft and its files. Anything else is refused with `409`. |
| `POST /api/v1/projects/{name}/releases/{version}/notes` | `project-operator` | `release.notes` | Writes the notes again from the changes since the previous release (a draft or candidate). |
| `PUT /api/v1/projects/{name}/releases/{version}/assets/{asset}` | `project-operator` | `release.asset.add` | Uploads a file: the request body is the file (up to 2 GiB). `X-Launchway-SHA256` may carry the checksum it must have. An asset is never replaced: a second file of the name is refused with `409`. |
| `POST /api/v1/projects/{name}/releases/{version}/assets` | `project-operator` | `release.asset.add` | Attaches what a Sortie made: `{"run_id", "files": [{"artifact", "path", "name"}], "images"}`. The files are copied from the Sortie's Cargo (checked against their checksums) and kept with the release; `images` records its image digests, SBOM and scan summaries. The Sortie must have run at the release's commit. |
| `DELETE /api/v1/projects/{name}/releases/{version}/assets/{asset}` | `project-operator` | `release.asset.remove` | Removes an asset from a draft or candidate. |
| `GET /api/v1/projects/{name}/releases/{version}/assets/{asset}` | `project-viewer` | — | Downloads a file of the release, signed in, with its checksum in `X-Launchway-SHA256`. |
| `POST /api/v1/projects/{name}/releases/{version}/candidate` | `project-operator` | `release.candidate` | Moves a draft to candidate. |
| `POST /api/v1/projects/{name}/releases/{version}/publish` | `project-operator` | `release.publish` | Publishes a draft or candidate: makes the annotated tag (or adopts the one at the release's commit) and fixes the release. `{"public"}` asks for public downloads (admin, and only where the installation and project allow). Refused with `409` if the tag is at another commit or Launchway cannot write to the repository. |
| `POST /api/v1/projects/{name}/releases/{version}/yank` | `project-operator` | `release.yank` | Withdraws a candidate or published release: `{"reason"}` (required). |
| `POST /api/v1/projects/{name}/releases/{version}/changelog` | `project-operator` | `release.changelog` | Commits the notes at the top of the changelog file on a new branch and opens a pull request into the release's branch, never pushing to the branch itself: `{"base"}`. |
| `GET /public/releases/{name}/{version}/{asset}` | `public` | — | A file of a release with no sign-in. Answers `404` unless the installation allows public downloads, the project asks for them, and the release is published and was made public. |
| `GET /api/v1/releases` | `signed-in` | — | Releases of every project the caller may see: `?project=`, `?state=`. |
| `GET /api/v1/release-matrix` | `signed-in` | — | Which release runs in which Airfield: for each project and Airfield the version, since when, who started it, its health and any launch in progress, and what the caller may promote. |
| `GET /api/v1/release-calendar` | `signed-in` | — | Planned, in-progress and done promotions and the freezes between `?from=` and `?to=`. |
| `GET /api/v1/release-report` | `signed-in` | — | What shipped and the four delivery measures over a period: `?project=`, `?from=`, `?to=` (default 90 days), `?format=html` for a self-contained page. |
| `GET /api/v1/release-installation` | `signed-in` | — | Whether this installation allows public downloads of releases. |
| `PUT /api/v1/release-installation` | `admin` | `release.installation` | Allows or forbids them: `{"public_downloads"}`. Administrators of every project. |
| `GET /api/v1/release-freezes` | `signed-in` | — | Freezes in force, coming, and over in the last 30 days. |
| `POST /api/v1/release-freezes` | `signed-in` | `release.freeze` | Makes a freeze: `{"project", "airfield", "from", "to", "reason"}`. Without `project` it freezes every project and needs the admin role everywhere; with one it needs the admin role on that project. |
| `POST /api/v1/release-freezes/{id}/cancel` | `signed-in` | `release.freeze.cancel` | Ends a freeze early, with the same rights as making it. |
| `GET /api/v1/projects/{name}/release-gates` | `project-viewer` | — | The checklists of the project, one per Airfield. |
| `PUT /api/v1/projects/{name}/release-gates/{airfield}` | `project-operator` | `release.gate` | Sets what a release must pass to reach an Airfield: `require_checks`, `require_notes`, `require_no_blocking_findings`, `soak_minutes`, `clearances`, `clearance_roles`, `previous_airfield`, `window` (`days`, `from`, `to`, `zone`). Admin on the project. |
| `GET /api/v1/projects/{name}/releases/{version}/checklist` | `project-viewer` | — | A release judged for an Airfield now: `?airfield=`. Each line passes, fails or waits, and whether an administrator may override what stops it. |
| `POST /api/v1/projects/{name}/releases/{version}/clearances` | `project-operator` | `release.clearance` | Gives clearance for an Airfield: `{"airfield", "comment"}`. The person who published the release may not. |
| `POST /api/v1/projects/{name}/releases/{version}/promote` | `project-operator` | `release.promote` | Promotes to an Airfield: `{"airfield", "override"}`. Starts the project's pipeline at the release's tag with the inputs `release`, `version`, `airfield`, `sha` and `digest`/`digests` it declares. `override` (a reason) is for administrators and only for lines that may be overridden. Answers `201` with the promotion and the Sortie, or `409` with the checklist. |
| `POST /api/v1/projects/{name}/releases/{version}/plan` | `project-operator` | `release.plan` | Writes a promotion on the calendar: `{"airfield", "at", "note"}`. A reminder; Launchway does not start it. |
| `POST /api/v1/projects/{name}/release-returns` | `project-operator` | `release.return` | Returns an Airfield to base: `{"airfield", "to", "reason", "queue"}`, by the existing Return to base (rollback). Answers `201` with the Return to base run. |
| `GET /api/v1/release-formations` | `signed-in` | — | Formations the caller may see. |
| `POST /api/v1/release-formations` | `signed-in` | `release.formation.create` | Makes a formation: `{"name", "note", "releases": ["project@version", ...], "order"}`, in promotion order; with `"order": "dependencies"` the [software graph](../guide/software-graph.md) puts the releases in dependency order instead (`409` with the reason when it cannot). Needs the operator role on every project in it. |
| `GET /api/v1/release-formations/{id}` | `signed-in` | — | A formation (by id or name) with the state of each member. A promoting formation moves on when it is read. |
| `POST /api/v1/release-formations/{id}/promote` | `signed-in` | `release.formation.promote` | Promotes it to an Airfield: `{"airfield", "override"}`. Nothing starts unless every member passes the checklist; then one member after another. |
| `POST /api/v1/release-formations/{id}/advance` | `signed-in` | `release.formation.advance` | Records which members launched or failed and starts the next. |

## Policies and waivers

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/policies` | `any-viewer` | — | The rule catalog, the policies in force with their sources and versions, and recent verdicts (`?recent=N` per policy, at most 100) of runs the caller may see. |
| `GET /api/v1/runs/{id}/policy` | `run-viewer` | — | A Sortie's policy decisions, rule by rule, the waivers that could apply, and whether the caller may waive. |
| `GET /api/v1/waivers` | `signed-in` | — | Waivers of the projects the caller may see: `?project=`, `?active=1`, `?limit=`. |
| `POST /api/v1/projects/{name}/waivers` | `project-viewer` | `policy.waive` | Waives one rule of one policy. Answers `201` with the waiver. |
| `POST /api/v1/projects/{name}/waivers/{waiver}/revoke` | `project-viewer` | `policy.revoke_waiver` | Ends a waiver. Answers with the waiver. |

Granting and revoking also need the role `policy.waivers.allow` names (admin by default; `403` otherwise). The grant body:

```json
{"policy": "prod-evidence", "rule": "tests", "reason": "hotfix, tests run by hand", "expires_in_hours": 24, "run": "", "environment": "prod", "finding": ""}
```

`reason` and an expiry are required; instead of `expires_in_hours` you may send `expires_at` (Unix milliseconds). `run`, `environment` and `finding` narrow the waiver. The revoke body is optional: `{"reason": "..."}`. See [Policy rules](policy-rules.md).

## Configuration

Everything an administrator manages is a configuration object kept in the database, with a version
and a history: see [Configuration objects](config-objects.md) for the kinds, their settings, how
changes are checked and how a Terraform provider uses them. Reading and changing configuration
needs the admin role on every project, except the overview, which any signed-in person with a role
may read (the web UI names the levels from it). A change names the version it expects (`If-Match`
or `version` in the body); `412` says someone changed the object meanwhile. `X-Launchway-Manager:
terraform` (or `git`), sent with an API token, says a tool manages the object; only that
manager may change it afterwards, unless the caller sends `take_ownership: true`, which is audited. `{kind}` is one of `projects`, `libraries`,
`environments`, `runner-pools`, `role-bindings`, `policies`, `policy-sources`, `health-sources`,
`models`, `secret-stores`, `step-resources`, `units`, `installation` and `organisation` (the kind names are fixed, whatever the installation calls its levels; `organization` is accepted as an alias). `GET /api/v1/config` also returns the chosen `terms` (`set` and `overrides`) and the level names they give.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/config` | `any-viewer` | — | The kinds, the names of the levels, the Squadrons, the revision this server runs with, and whether the caller is an administrator. `available` is false on a server limited to a schema before 23, which uses its file |
| `GET /api/v1/config/history` | `admin` | — | The changes of every object, newest first: who, when, the action, what changed. `kind`, `name`, `import`, `before` and `limit` narrow and page it |
| `GET /api/v1/config/export` | `admin` | — | Every object as configuration text (YAML), for backup, review or Git |
| `POST /api/v1/config/import` | `admin` | `config.import` | `{"text", "prune", "apply", "digest"}`. Without `apply`, plans the import: what would be created, changed or (with `prune`) deleted. With `apply` and the plan's `digest`, makes exactly those changes, recorded as one import |
| `GET /api/v1/config/imports` | `admin` | — | The imports so far, the first start's import of the configuration file among them |
| `POST /api/v1/config/validate` | `admin` | — | `{"changes": [...]}`: checks changes as `apply` would make them and stores nothing. The answer is `{"valid", "problems"}` and lists every problem. The wizards call it as people answer |
| `POST /api/v1/config/check/repository` | `admin` | `config.check_repository` | `{"repo", "branch"}`: reads a repository with the server's own Git credentials and keeps nothing. Internal addresses and local paths are refused (`blocked`, `blocked_host`; see Egress in the server settings). Audited and limited to a few a minute per caller. Answers `ok`, a plain `problem`, the `default_branch`, the `branches` and whether the branch has a pipeline file |
| `POST /api/v1/config/check/signin` | `admin` | `config.check_signin` | `{"issuer", "client_id", "redirect_url", "client_secret_env", "groups_claim", "admin_groups", "admin_users"}`: checks the answers against the identity provider and returns the `auth:` block for the configuration file (`snippet`). Sign-in stays in the file |
| `POST /api/v1/config/apply` | `admin` | `config.apply` | `{"changes": [{"kind", "name", "action", "unit", "spec", "version", "comment"}]}`, at most 200: all or none. `action` is `put` (the default), `delete` or `restore`; `version` -1 means the object must not exist yet |
| `GET /api/v1/config/effective/{project}` | `admin` | — | What applies to a project and where each part comes from: the Base, the Wing, a Squadron or the project |
| `GET /api/v1/config/{kind}` | `admin` | — | The objects of a kind; `?deleted=1` lists the deleted ones |
| `POST /api/v1/config/{kind}` | `admin` | `config.create` | `{"name", "unit", "spec", "comment"}`: creates an object; `409` if it exists |
| `GET /api/v1/config/{kind}/{obj}` | `admin` | — | One object: `spec`, `version` (also the `ETag`), `managed_by`, who created and last changed it, and `deleted` |
| `PUT /api/v1/config/{kind}/{obj}` | `admin` | `config.put` | Creates or replaces the object, declaratively: sending the same body twice changes nothing the second time (`changed` says so). `400` lists every problem the same checks as the configuration file find |
| `DELETE /api/v1/config/{kind}/{obj}` | `admin` | `config.delete` | Deletes the object; it is kept and can be restored. Refused if something still depends on it, if active runs use it (a project, an Airfield or a Flight line (Station (runner) pool): the answer lists them; `{"force_in_use": true}` deletes it anyway, recorded in the object's history and the Logbook) or if it would leave the configuration invalid |
| `POST /api/v1/config/{kind}/{obj}/restore` | `admin` | `config.restore` | Brings a deleted object back as it was |
| `GET /api/v1/config/{kind}/{obj}/history` | `admin` | — | The object's changes, newest first, each with the fields that changed |

## Step resources

The installation's [default CPU and memory](pipeline.md#resources) for each kind of step, and the most
any step may ask for. They are kept in the database. Anyone signed in may read them; changing them
needs an administrator, and every change is audited with the values before and after.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/step-resources` | `signed-in` | — | Everything: `defaults` (each with `kind`, `cpu`, `cpu_limit`, `memory`, `memory_limit`, `updated_at`, `updated_by`), `limits` (`max_cpu`, `max_memory`, `memory_per_thread`) and `kinds`, the step kinds that have defaults. A value that is empty is not set. |
| `GET /api/v1/step-resources/limits` | `signed-in` | — | The limits alone. |
| `PUT /api/v1/step-resources/limits` | `admin` | `step_resources.set_limits` | Replaces the limits: `{"max_cpu", "max_memory", "memory_per_thread"}`. A value left out is not set. `400` for a quantity that does not read, a `memory_per_thread` under 64Mi, or a maximum below a default (lower the default first). |
| `GET /api/v1/step-resources/defaults/{kind}` | `signed-in` | — | One kind's default. `404` for a kind that has none. |
| `PUT /api/v1/step-resources/defaults/{kind}` | `admin` | `step_resources.set_default` | Replaces a kind's default: `{"cpu", "cpu_limit", "memory", "memory_limit"}`. A value left out is not set. `400` for a quantity that does not read, a limit below what is promised, or a value above the installation's limits. |

## Run limits

The installation's limits on pipelines, kept in the database: `max_run_timeout`, the longest
[`timeout`](pipeline.md#top-level-keys) a pipeline may ask for. Anyone signed in may read them; changing
them needs an administrator, and every change is audited with the values before and after. A pipeline
whose timeout is longer is refused when a Sortie starts, when it is re-run or resumed (the limit may have been lowered since), and by `POST /api/v1/validate`, with the limit
in the message.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/encryption` | `admin` | — | Encryption at rest: `{"encrypted", "dump_alone_cannot_reveal_secrets", "root_key_holder": {"kind", "where", "strength", "reachable"}, "keys", "columns", "secrets_sealed", "secrets_remaining", "migration": {"state", ...}, "warnings", "advice", "not_yet_encrypted", "summary"}`. Never any key material. Read-only: the root key is a bootstrap setting held outside the database, and `launchway encryption` rotates or replaces it where the key is. `409` with code `no_root_key` is what a request for something that stores secrets (a webhook with a secret) gets on a server with no root key |
| `GET /api/v1/run-limits` | `signed-in` | — | `{"max_run_timeout", "updated_at", "updated_by"}`. An empty `max_run_timeout` is not set: pipelines may ask for up to `720h`. |
| `PUT /api/v1/run-limits` | `admin` | `run_limits.set` | Replaces the limits: `{"max_run_timeout": "48h"}`; empty or left out removes the limit. `400` for a duration that does not read or is outside `1m` to `720h`. |
## Built-in Depot (registry) {#built-in-registry}
Launchway serves an OCI registry for container images, Helm charts and other OCI Cargo. The Depot
protocol itself is under `/v2/` (see [the Depot guide](../guide/registry.md)); these routes manage what
is in it. A repository is `PROJECT/NAME`: the first path component is the project, and the project's roles
decide everything: viewer pulls and lists, operator pushes, admin deletes and sets policy. A repository
someone may not read is answered as one that does not exist. The Depot is off (`404`, code `feature_off`)
when `registry.enabled` is false or the server has no Cargo store, and answers `503` (code
`needs_sign_in`) while sign-in is off: there is no anonymous Depot.
| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/registry` | `signed-in` | — | Whether the Depot is on, its `address`, the `host` clients write before a repository, whether it is plain HTTP (`insecure`), the blob `store`, `limits`, the number of `repositories` the caller may see, how to log in, and `encryption`: `blobs_encrypted_by_launchway` is `false` in this release, with a note saying what protects blobs at rest. An administrator also gets `last_gc`. |
| `GET /api/v1/registry/gc` | `admin` | — | The report of the last garbage collection: `ran`, `at`, `report`. |
| `POST /api/v1/registry/gc` | `admin` | `registry.gc_run` | Collects garbage. Body `{"dry_run": true}` lists what would go (`blobs`, `bytes`, `links`, `uploads`, `strays`, the first `digests`) and removes nothing. `409` when another server is collecting. |
| `GET /api/v1/registry/policy` | `admin` | — | The installation's policy, or with `?scope=organization&name=` a Wing's: `quota_bytes`, `retention`, `immutable_tags`. |
| `PUT /api/v1/registry/policy` | `admin` | `registry.policy` | Replaces it. Body `{"scope": "installation", "policy": {...}}`; an empty policy removes it. |
| `GET /api/v1/projects/{name}/registry` | `project-viewer` | — | The project's repositories (`images`, `tags`, `bytes`, `pushed_at`), `used_bytes` against `quota_bytes` (`-1`: none), the `policy` in force with where each part comes from, and how to push. |
| `GET /api/v1/projects/{name}/registry/images` | `project-viewer` | — | `?repository=` the images of one repository, newest first: `digest`, `tags`, `total_size`, `platforms`, `created_by` and `run_id`, `signed`, `has_sbom`, `vulnerabilities` (counts from an attached scan result), `attached` (signatures, SBOMs, scan results) and a copyable `pull` reference. |
| `GET /api/v1/projects/{name}/registry/image` | `project-viewer` | — | `?repository=&reference=` (a tag or a digest): one image as above and its `manifest`. |
| `DELETE /api/v1/projects/{name}/registry/image` | `project-operator` | `registry.image_delete` | `?repository=&reference=` deletes the image with its tags and attachments; the caller must be an admin of the project (an operator gets `403`). Refused with `409` for an immutable tag or an image a deployment uses. The bytes go at the next collection. |
| `DELETE /api/v1/projects/{name}/registry/tag` | `project-operator` | `registry.tag_delete` | `?repository=&tag=` deletes only the tag; project admin only; `409` for an immutable tag. |
| `GET /api/v1/projects/{name}/registry/policy` | `project-viewer` | — | The project's policy (with `?repository=` one repository's) and the `effective` values after the levels are combined. |
| `PUT /api/v1/projects/{name}/registry/policy` | `project-operator` | `registry.policy` | Replaces it; project admin only. A project admin may lower the quota but not raise it above the installation's. |
| `GET /api/v1/projects/{name}/registry/retention` | `project-viewer` | — | What the retention rules would delete now (`?repository=` for one repository): each `candidate` with its tags, size and reason. Deletes nothing. |
| `POST /api/v1/projects/{name}/registry/retention` | `project-operator` | `registry.retention_run` | Deletes them; project admin only. Answers what was `deleted`. |
### The Depot protocol under `/v2/` {#the-registry-protocol-under-v2}
These endpoints speak the OCI Distribution API (version 1.1) and are not JSON-API routes: errors are the
protocol's (`{"errors": [{"code", "message"}]}`, with Launchway's `error`, `code` and `next` beside them).
Everything needs credentials: `GET /v2/` answers `401` with a `WWW-Authenticate: Bearer realm=".../v2/token"`
challenge. Clients send HTTP Basic with any user name and an API token as the password, either directly or to
`GET /v2/token` (which grants only what the caller may do, for five minutes); a pipeline's steps use a
credential made for their Sortie (`lwi_...`), which pulls its own project's repositories and those a project's
policy shares with it, pushes only to its own project and never for a pull request or draft run, and stops
working when the Sortie ends. Passwords and tokens are accepted over HTTPS, or over HTTP only on a loopback
listener or with `registry.allow_insecure_http`.
| Endpoint | Does |
|---|---|
| `GET /v2/` | version check (authenticated) |
| `GET /v2/_catalog` | the repositories the caller may pull |
| `GET /v2/{name}/tags/list?n=&last=` | tags, paginated with a `Link` header |
| `HEAD`, `GET`, `PUT`, `DELETE /v2/{name}/manifests/{reference}` | manifests by tag or digest; delete by tag removes the tag, by digest the image and its attachments |
| `HEAD`, `GET /v2/{name}/blobs/{digest}` | blobs, with `Range` reads; a blob is visible only in repositories it was pushed or mounted to |
| `POST /v2/{name}/blobs/uploads/` | start an upload; `?digest=` for a monolithic push, `?mount=&from=` for a cross-repository mount (inside one Wing only) |
| `GET`, `PATCH`, `PUT`, `DELETE /v2/{name}/blobs/uploads/{id}` | status, chunk (with `Content-Range`), finish (`?digest=`), cancel; any server of a group may take any request |
| `GET /v2/{name}/referrers/{digest}?artifactType=` | manifests whose `subject` is the digest (signatures, SBOMs, attestations) as an image index |
## Standing orders
The same scans and best-practice checks for every repository, on by default (see [Standing orders](../guide/standing-orders.md)). The routes serve what can run, the settings of each level, a repository's effective profile with where each value comes from, the findings the default branch has had and what people decided about them, and a Sortie's Inspection. A level's settings are changed by someone who administers the level (the project's admin at a project); a value weaker than a lock above is refused with `409` and the plain sentence that names the lock.
| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/standards/profile` | `signed-in` | — | The profile: every check with what it looks for, why, its ecosystem, tool, the tool's license and image digest, default state, whether it can block, its time budget and what it needs from the network; the gate defaults and the setting keys. |
| `GET /api/v1/standards/settings` | `signed-in` | — | The settings one level has: `?level=` (`installation`, `organization`, `unit` or `project`: a Base, a Wing, a Squadron or a Project) and `&scope=NAME`, with `version` (0 when none are stored). |
| `PUT /api/v1/standards/settings` | `signed-in` | `standards.settings_set` | Replaces one level's settings (`level`, `scope`, `settings`, and `version` as read: a different version is `409`). `400` with the plain problems for an invalid value; `409` for a value weaker than a lock above. Switching Standing orders off needs a reason and is recorded as such. |
| `GET /api/v1/standards/coverage` | `signed-in` | — | For the projects of a level that the caller may read: whether Standing orders are off, each check's state and whether it runs, open findings by severity. |
| `GET /api/v1/projects/{name}/standards` | `project-viewer` | — | The effective profile of a project at its default branch: each check, the gate, license lists, exemptions, where every value comes from and what a lock holds, notices, what was detected, and the repository's own file with its problems. |
| `GET /api/v1/projects/{name}/standards/file` | `project-viewer` | — | The repository's `.launchway/standards.yaml` as it is, its problems, and the file the project's own settings would make. |
| `GET /api/v1/projects/{name}/findings` | `project-viewer` | — | The findings the default branch has had: `?status=` open, fixed, accepted or all, `severity`, `check`, `q`, `sort=oldest`, `limit`, `offset`; with totals by severity and check. |
| `GET /api/v1/projects/{name}/findings/history` | `project-viewer` | — | Open findings by day, the mean days to fix, the oldest open finding. |
| `GET /api/v1/projects/{name}/findings/export` | `project-viewer` | — | The findings as `?format=` json, csv or sarif (`status` as above). SARIF carries the fingerprint as a partial fingerprint. |
| `GET /api/v1/projects/{name}/findings/{fingerprint}` | `project-viewer` | — | One finding with its decision. |
| `POST /api/v1/projects/{name}/findings/{fingerprint}/accept` | `project-operator` | `finding.accept` | Accepts a risk or marks a false positive: `reason` (at least 10 characters), `expires` (`YYYY-MM-DD`, at most 365 days away), `status` (`accepted-risk` or `false-positive`). Audited with the reason. |
| `POST /api/v1/projects/{name}/findings/{fingerprint}/reopen` | `project-operator` | `finding.reopen` | Ends the decision in force, so the finding counts again. |
| `GET /api/v1/runs/{id}/inspection` | `run-viewer` | — | A Sortie's Inspection: the verdict and its sentence, the profile in force when the run was made, each check and whether it ran, and the findings with their standing (new, existing, accepted, false positive), filtered by `standing`, `check`, `severity`. |

## Build caches

A step's `cache:` key (see [the pipeline reference](pipeline.md#cache)) keeps directories between runs.
Stations look entries up, send them in chunks and restore them under a lease, with the routes in the
Station API below. The request never says which scope an entry is in: the server works the scope out
from the run the lease belongs to (`default` for the default branch, `ref:<ref>` for another branch or
tag, `pull:<n>` for a pull request), so a Station cannot read or write an entry its Sortie's scopes do not
reach. People and agents see a project's entries with the rights they have on the project and delete them
as an operator; the policy (whether caches are kept, and how much) is an administrator's, in the
database, as the installation's default with an override for a project.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/caches` | `project-viewer` | — | The project's entries, most recently used first: `id`, `name`, `key`, `scope`, `platform`, `bytes`, `files`, `status` (`kept` or `uploading`), `created_at`, `last_used_at`, `hits`, `pipeline`, `generation`, `sensitive`, and `run_id` and `run_number` of the Sortie that wrote it; `totals` (`entries`, `bytes`, `quota_bytes`, `max_entry_bytes`, `idle_days`) and the `policy` that applies. |
| `DELETE /api/v1/projects/{name}/caches/{id}` | `project-operator` | `cache.delete` | Ends one entry; the next Sortie that wanted it starts without it. Answers `{"ended", "bytes"}`; `404` for an entry that does not exist or is another project's. |
| `DELETE /api/v1/projects/{name}/caches` | `project-operator` | `cache.delete` | Ends every entry of the cache named by `?name=pip`, in every scope, key and platform. `404` if there is none. |
| `GET /api/v1/cache-policy` | `signed-in` | — | The installation's policy: `enabled`, `max_project_gib`, `max_entry_gib`, `idle_days`, `max_age_days`, and `source` (`default` until an administrator sets one). |
| `PUT /api/v1/cache-policy` | `admin` | `cache_policy.set` | Changes the installation's policy; a field left out keeps its value. `400` for a value out of range (an entry limit above the project's quota, idle days outside 1 to 3650). Audited with the policy before and after. |
| `GET /api/v1/projects/{name}/cache-policy` | `project-viewer` | — | The policy that applies to the project, with `source` `project` (its own override), `installation` or `default`. |
| `PUT /api/v1/projects/{name}/cache-policy` | `admin` | `cache_policy.set_project` | Overrides the policy for one project; a field left out keeps the value that applies to it now. |
| `DELETE /api/v1/projects/{name}/cache-policy` | `admin` | `cache_policy.clear_project` | Removes the override: the installation's policy applies again. |

## Variables

A variable is a name with a type, a value, an optional Airfield and an optional lock, set at a level: `installation`, `organization`, `unit` or `project`. A level and its scope (the Squadron's or the project's name) are given as `?level=&scope=` or in the body. The rule, the same sentence everywhere: **The nearest setting wins: what is given when a Sortie starts, then the pipeline file's own `with:`, then the project, the Squadron, the Wing, the Base, then the template's own default; at the same level a variable set for the Airfield being deployed to beats the general one; and a lock set at a level beats everything below it.** Lists replace, never merge. Variables and locks never cross Wings. See the [guide](../guide/variables.md).

| Route | Who | Audit action | What it does |
|---|---|---|---|
| `GET /api/v1/variables` | `signed-in` | — | The variables at a level (`?level=&scope=&environment=&names=`) with what each inherits from above and whether this level may override it. `400` without a level; `404` for a project the caller cannot see |
| `PUT /api/v1/variables/{name}` | `signed-in` | `variable.set` | Sets a variable: `{level, scope, environment, type, of, value, description}`. Types: `string`, `number`, `boolean`, `list`, `map`, `secret` (a reference `{store, key, field}`, never text) and `connection` (a name, with `of`). A name keeps one type everywhere. Who: an operator of the project, or an admin of the Squadron's projects, or of every project for the Wing and the installation. `409` when a lock above forbids it, naming the level and who can change it |
| `DELETE /api/v1/variables/{name}` | `signed-in` | `variable.delete` | Deletes a variable at a level (`?level=&scope=&environment=`); what it overrode applies again |
| `PUT /api/v1/variables/{name}/lock` | `signed-in` | `variable.lock` | Locks it: `{level, scope, environment, reason}`; a reason is required. Needs an admin at that level (of the project, at a project). Answers the variable and the nearer settings now ignored |
| `DELETE /api/v1/variables/{name}/lock` | `signed-in` | `variable.unlock` | Removes the lock |
| `GET /api/v1/projects/{name}/variables` | `project-viewer` | — | Every variable in effect for a project, optionally `?environment=`, each with its whole chain: the winning value, its level, each overridden value in order, the lock and why |
| `GET /api/v1/runs/{id}/variables` | `run-viewer` | — | The variables the Sortie used, as recorded when it was created; a re-run shows the original's |

## Switches (feature toggles) {#feature-toggles}

A toggle is a value an application reads at run time, so that a feature can be turned on, off or rolled out to a share of users without a deployment. It belongs to a project, or is shared from a Squadron, the Wing or the installation (the levels variables have). Each Airfield has its own state; safeguards (a reason, an approval in a protected Airfield) are on by default. See the [guide](../guide/feature-toggles.md). People and agents use the routes of the first table; applications use the SDK routes of the second, with an SDK key and nothing else.

| Route | Who | Audit action | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/toggles` | `project-viewer` | — | The project's toggles with their state in each Airfield (`environments`), stale mentions and changes waiting (`waiting`). A toggle defined at a level above the project shows with `inherited: true`. |
| `POST /api/v1/projects/{name}/toggles` | `project-operator` | `toggle.create` | Makes a toggle: `{key, type, kind, default, safe_value, description, owner, expires, client_visible, tags, level, scope, reason}`. Types `boolean`, `string`, `number`, `json`. It starts off in every Airfield. At a level above the project it needs an administrator of that level. `409` if the key exists, or is locked above. |
| `GET /api/v1/projects/{name}/toggles/{key}` | `project-viewer` | — | One toggle: its definition, each Airfield's state and rules, lock, stale mentions, when it was last evaluated. |
| `PATCH /api/v1/projects/{name}/toggles/{key}` | `project-operator` | `toggle.update` | Changes the definition (`description`, `owner`, `kind`, `expires`, `tags`, `default`, `safe_value`, `client_visible`); never the type or the key. `400` for a client-visible toggle that needs one that is not. |
| `DELETE /api/v1/projects/{name}/toggles/{key}` | `project-operator` | `toggle.delete` | Removes the toggle with its states; `?reason=` is required. The removal is kept in the history and can be reverted. |
| `PUT /api/v1/projects/{name}/toggles/{key}/lock` | `project-operator` | `toggle.lock` | Locks a toggle shared from a level above its projects: `{reason}`. Needs an administrator of that level. |
| `DELETE /api/v1/projects/{name}/toggles/{key}/lock` | `project-operator` | `toggle.unlock` | Removes the lock. |
| `PUT /api/v1/projects/{name}/toggles/{key}/environments/{env}` | `project-operator` | `toggle.set` | Sets what the toggle does in an Airfield: `{state: {on, off_value, prerequisites, rules, fallthrough}, reason, at}`. Needs the operator role in the Airfield. `400` without a reason when the safeguard asks for one; `202` with `waiting: true` when it needs approval first; with `at` (RFC 3339) `202` with the scheduled change. |
| `GET /api/v1/projects/{name}/toggles/{key}/history` | `project-viewer` | — | Every change to the toggle, newest first (`?environment=&limit=`): who, when, why, how, before and after, approvals, and what it reverted. |
| `GET /api/v1/projects/{name}/toggles/{key}/usage` | `project-viewer` | — | Evaluation counts per Airfield, per value, for the last `?days=`, and when it was last evaluated. Counts only, never who. |
| `POST /api/v1/projects/{name}/toggles/{key}/evaluate` | `project-viewer` | `toggle.evaluate` | Tries `{context, environment}` against the toggle as it is now in each Airfield; nothing is counted or stored. The audit entry never holds the context. |
| `GET /api/v1/projects/{name}/toggle-changes` | `project-viewer` | — | Changes that wait for approval. |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/revert` | `project-operator` | `toggle.revert` | Goes back over a change: `{reason}` is required. A retreat: never held for approval. Says how many later changes it also undid (`also_undone`). `409` if already reverted. |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/approve` | `project-operator` | `toggle.approve` | Approves a waiting change: `{comment}`. Needs the operator role in its Airfield, and not to be the person who asked (unless `approvals.allow_self`); if the Airfield names reviewers, one of them. With enough approvals the change is made. |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/reject` | `project-operator` | `toggle.reject` | Turns a waiting change down. |
| `POST /api/v1/projects/{name}/toggle-changes/{id}/withdraw` | `project-operator` | `toggle.withdraw` | Takes back a change you asked for. |
| `GET /api/v1/projects/{name}/toggle-timeline` | `project-viewer` | — | The toggle changes of an Airfield (`?environment=` required; `?since=` Unix ms), newest first, for the Airfield's page beside its deployments. |
| `POST /api/v1/projects/{name}/toggle-revert-since` | `project-operator` | `toggle.revert_since` | Reverts every toggle change made in an Airfield since a time: `{environment, since, reason, dry_run}`. What a Return to base page offers. |
| `GET /api/v1/projects/{name}/toggle-schedules` | `project-viewer` | — | Scheduled changes (`?toggle=`, `?all=true` for those that ran too). |
| `DELETE /api/v1/projects/{name}/toggle-schedules/{id}` | `project-operator` | `toggle.schedule_cancel` | Cancels a scheduled change that has not run. |
| `GET /api/v1/projects/{name}/toggle-segments` | `project-viewer` | — | The segments the project sees. |
| `PUT /api/v1/projects/{name}/toggle-segments/{segment}` | `project-operator` | `toggle.segment_set` | Makes or replaces a segment: `{included, excluded, rules: [{clauses}], description, reason}`. |
| `DELETE /api/v1/projects/{name}/toggle-segments/{segment}` | `project-operator` | `toggle.segment_delete` | Removes a segment (`?reason=` required); `409` while a rule names it. |
| `GET /api/v1/projects/{name}/toggle-safeguards` | `project-viewer` | — | The safeguards in each Airfield and which level set each. |
| `PUT /api/v1/projects/{name}/toggle-safeguards` | `project-operator` | `toggle.safeguards_set` | Sets safeguards at the project (or `level`/`scope`): `{environment, require_reason, require_approval, approvals, watch_minutes, locked, reason}`. Needs the admin role on the project (the level's admin above it). Turning one down needs a `reason`; `409` for one a level above locked. |
| `GET /api/v1/projects/{name}/toggle-keys` | `project-operator` | — | The SDK keys of the project (`?environment=`): kind, prefix, expiry, last use. Never a secret. |
| `POST /api/v1/projects/{name}/toggle-keys` | `project-operator` | `toggle.key_create` | Makes an SDK key: `{environment, kind (server or client), name, origins}`. Needs the admin role. The key's text is in `key`, shown once. |
| `POST /api/v1/projects/{name}/toggle-keys/{id}/rotate` | `project-operator` | `toggle.key_rotate` | Makes a new key like the old one: `{overlap_hours}`; the old one works until then (default 24 h). Needs the admin role. |
| `DELETE /api/v1/projects/{name}/toggle-keys/{id}` | `project-operator` | `toggle.key_revoke` | Ends a key at once. Needs the admin role. |
| `GET /api/v1/projects/{name}/toggle-file` | `project-viewer` | — | The project's own toggles and segments as a YAML file (`file`), conventionally `.launchway/toggles.yaml`. |
| `POST /api/v1/projects/{name}/toggle-plan` | `project-viewer` | `toggle.plan` | How a file differs from the live state: `{file}`. `drift` lists `add`, `change` and `only_live`. Changes nothing. |
| `POST /api/v1/projects/{name}/toggle-apply` | `project-operator` | `toggle.apply` | Makes the live state match the file: `{file, reason, origin}`. Each change goes through the safeguards; those that need approval wait (`waiting`). Never removes what the file leaves out. |

The routes for applications, which prove themselves with an SDK key (`Authorization: Bearer lwt_srv_...` or `lwt_cli_...`) and record nothing in the Logbook (a context is never logged):

| Route | Who | Audit action | What it does |
|---|---|---|---|
| `GET /api/sdk/v1/ruleset` | `public` | — | With a server key: the Airfield's ruleset, with `ETag`; `304` for a matching `If-None-Match`. `401` for a key that is missing, unknown, revoked or expired, without saying which; `403` for a client key. Rate limited per key. See the [protocol](toggles-protocol.md). |
| `GET /api/sdk/v1/stream` | `public` | — | With a server key: server-sent events. `event: ruleset`, `id` the ETag, `data` the ruleset, on connection (unless `Last-Event-ID` is current) and whenever it changes; `: ping` comments in between. |
| `POST /api/sdk/v1/evaluate` | `public` | — | With a client or server key: `{context, toggles}` answers `{values: {key: {value, reason}}}`. A client key gets client-visible toggles only, never a rule, and only from the websites it names. |
| `OPTIONS /api/sdk/v1/evaluate` | `public` | — | The preflight of a browser's request; carries no key. |
| `POST /api/sdk/v1/telemetry` | `public` | — | With a server key: `{counts: [{toggle, value, reason, count}]}`, aggregated evaluation counts. No context. |

## Repositories (Git hosting)

Only with the Base setting `forge.hosting` on (Configuration, Base settings; a change applies when each server next restarts); otherwise every route answers `404` with `{"error": "feature_off: ...", "code": "feature_off"}` and the repositories are kept. A repository is `<owner>/<name>`; a bindings' project patterns match that path (`jkhunter/*`).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/repositories` | `signed-in` | — | The repositories the caller may read: `{"items": [...]}`, each with `id`, `path`, `home`, `name`, `default_branch`, `visibility`, `archived` and `clone.https`. |
| `POST /api/v1/repositories` | `admin` | `repository.create` | Creates one: `{"owner", "name", "default_branch", "visibility"}`. The owner defaults to `forge.default_owner`, the branch to `main`, the visibility to `private`. `400` for a name that is not a plain name (traversal, `.git`, control characters, reserved words, more than 100 characters), `409` for a name in use or an owner at its limit, `429` when repositories are made too fast. |
| `GET /api/v1/repositories/{owner}/{name}` | `repo-viewer` | — | One repository. |
| `PATCH /api/v1/repositories/{owner}/{name}` | `repo-admin` | `repository.update` | Changes `visibility`, `default_branch`, `archived`, or renames with `owner` and `name`. The old address stops working. |
| `DELETE /api/v1/repositories/{owner}/{name}` | `repo-admin` | `repository.delete` | Moves the repository to the trash: it is no longer served or listed, its data is kept, and its name can be used again. |


### Mirrors (Git hosting)

A **pull mirror** keeps a hosted repository an exact copy of an address on another server: every branch, tag and note, deletions followed, on a schedule and on demand. While it is a mirror the repository takes no pushes (`403`). A **push mirror** copies every accepted update of a hosted repository to an address elsewhere. Addresses are `https` or `http`; an internal address (loopback, private, link-local, cloud metadata) is refused with `400` unless the server's `forge.mirror_allow` lists it. A credential is a reference, `{"credential": {"env": "NAME"}}` (an environment variable of the server) or `{"credential": {"file": "/path"}}`, never a value, and is never stored in the repository's record, shown or logged; `username` goes with it (the token is sent as the password). Only with `forge.hosting` on.

A push mirror runs after every accepted update of the repository (a push, a pull mirror's sync, stopping mirroring), asynchronously, and again on its schedule; it pushes every branch, tag and note with force and prune, so the other side is an exact copy, and it never prunes the other side from a repository that has no refs at all. A failed run is retried after a minute, doubling up to an hour, and a push during the backoff does not cut it short. Each remote has its own status. A mirror that has waited more than 15 minutes (a pull mirror: more than two intervals and 15 minutes, or three failures in a row) is `behind`: that is shown here, logged at warning level, and recorded as a `mirror.behind` event in the connection's event log (`mirror.recovered` when it catches up). A repository may have up to 10 push mirrors.

A mirror is shown as `id`, `repository`, `direction`, `url`, `credential` (where it is read from), `interval_seconds`, `enabled`, `prune`, `state` (`pending`, `ok` or `error`), `running`, `last_attempt`, `last_success`, `last_error` (credentials removed), `failures`, `next_run`, `refs`, `lag_seconds` and `behind`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/mirrors` | `signed-in` | — | Every mirror of the repositories the caller may read; `?behind=true` lists only those that have fallen behind. |
| `GET /api/v1/repositories/{owner}/{name}/mirrors` | `repo-viewer` | — | The repository's mirrors with their status. |
| `POST /api/v1/repositories/{owner}/{name}/mirrors` | `repo-admin` | `mirror.create` | Sets a mirror up: `{"direction": "pull" (default), "url", "username", "credential", "interval_seconds" (at least 60; default 3600 for pull), "prune" (default true), "enabled"}`. For a pull mirror the repository must have no branches or tags of its own (`400`: mirroring would replace them); posting again changes the existing pull mirror. The first sync starts at once. `409` when the repository already has that mirror. |
| `PATCH /api/v1/repositories/{owner}/{name}/mirrors/{id}` | `repo-admin` | `mirror.update` | Pauses or resumes (`enabled`), changes `interval_seconds` or `prune`. |
| `DELETE /api/v1/repositories/{owner}/{name}/mirrors/{id}` | `repo-admin` | `mirror.delete` | Removes a push mirror. A pull mirror is ended with `stop-mirroring` (`400`). |
| `POST /api/v1/repositories/{owner}/{name}/mirrors/{id}/sync` | `repo-operator` | `mirror.sync` | Runs the mirror now and answers with its status. `409` when it is already running; `502` with `{"error", "mirror"}` when the Sortie failed (the mirror's `last_error` says why). A pull mirror whose source suddenly lists no branches or tags refuses to delete everything. |
| `POST /api/v1/repositories/{owner}/{name}/stop-mirroring` | `repo-admin` | `mirror.promote` | Makes the repository the primary: the pull mirror is deleted and pushes are accepted. `{"final_sync": true}` runs the mirror once more first, and changes nothing if that fails. `409` when it is not a mirror. |

### Importing repositories (Git hosting)

Getting existing repositories into Launchway, safely, before anything is switched over. A **connection** names the server they are on: its `kind` (`gitea` or `forgejo`, whose REST API lists the repositories; or `plain`, a list of addresses you give), its `base_url` and a credential reference (`{"env": "NAME"}` or `{"file": "/path"}`, never a value). An **import** makes each repository here with the same owner, name and visibility, copies every branch, tag and note, checks the copy ref by ref, and by default leaves it a pull mirror (the staged move). It can be run again: a repository already made as a mirror of the same address is resumed, one that exists in any other state is left alone, so an import never overwrites a repository that has content of its own. The connection's credential is sent only to the connection's own host; an internal address is refused unless `forge.mirror_allow` lists it (the refusal says so). All of these need the admin role on every project, and a server with `forge.hosting` off answers `404 feature_off`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/connections` | `admin` | — | The connections to import from: `name`, `kind`, `base_url`, `credential` (where it is read from) and `enabled`. |
| `POST /api/v1/connections` | `admin` | `connection.create` | `{"name", "kind", "base_url", "credential"}`. `400` for another kind, an address that is internal and not allowed, or a credential that is a value. |
| `POST /api/v1/connections/token` | `admin` | `connection.create` | `{"name", "kind", "base_url", "token"}`. The setup guide's way to give the token of another Git server once: it is kept sealed under the root key (a meta row, covered by the encryption Depot), the connection refers to it as `sealed:NAME`, and removing the connection removes it; it is never returned or audited. `409` (`unavailable`) when this server has no root key: create one, or give an environment variable instead. Otherwise as `POST /api/v1/connections`. |
| `DELETE /api/v1/connections/{name}` | `admin` | `connection.delete` | Removes a connection that no repository uses; Launchway's own is refused. |
| `POST /api/v1/imports` | `admin` | `repository.import` | `{"connection", "urls", "owner", "repository", "target_owner", "mirror" (default true), "interval_seconds", "username", "dry_run", "wait"}`. `owner` limits the listing to one user or Wing of the source, `repository` (`owner/name`) to one; without them it brings in everything the credential can see. `dry_run` lists what would be created and changes nothing. `wait` copies and checks each repository before answering; without it the repositories are made and their mirrors left to run in the background. `mirror: false` makes each an ordinary repository once copied and checked (needs `wait`). Answers `{"dry_run", "items", "summary"}`; each item has `source`, `repository`, `url`, `visibility`, `action` (`create`, `resume`, `skip`), `state` (`planned`, `queued`, `verified`, `skipped`, `failed`), `reason`, `error` (credentials removed), `source_refs`, `hosted_refs`, `verified`, `differences` and `mirror_id`. One repository failing does not stop the others. |
| `POST /api/v1/imports/verify` | `admin` | `repository.verify` | `{"connection", "urls", "owner", "repository", "target_owner"}`. Proves, or fails to prove, that every repository of the source exists here with exactly the branches, tags and notes at exactly the commits the source has: `{"equal", "items", "summary"}`, each item `equal`, `different` (with the refs that differ), `missing` or `error`. It reads only. |

### The first-run guide

The browser's walk-through after the first administrator. It asks the server what can be done now (each step lists its `actions`, each `available` or not with a plain `reason`), so nothing is offered that cannot work. All need the admin role.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/setup-guide` | `admin` | — | The steps of the guide (`sign-in`, `encryption`, `runner`, `git-hosting`, `import`, `pipeline`), each with `state` (`done`, `todo`, `unavailable`), a `summary` and its `actions`; and the plain security `checklist`. |
| `POST /api/v1/signin/test` | `admin` | `signin.test_begin` | The sign-in answers of a provider (`issuer`, `client_id`, `client_secret_env`, optional claims). Answers the address to open in a second window. Changes nothing about how people sign in. `400` lists what is wrong with the answers, `409` when the secret's environment variable is not set on this server. |
| `GET /api/v1/signin/test` | `admin` | — | How the last test ended: `tested`, `ok`, `at` and a `detail` naming who the provider said signed in. |
| `GET /auth/signin-test` | `public` | — | Where the provider sends the second window back. It checks the test this browser started, shows a page saying whether it worked, and records the result in the Logbook as `signin.test_result`. |
| `GET /api/v1/encryption/root-key-copy` | `admin` | — | Whether a recovery copy of the root key was taken (`taken`, `taken_at`) and whether this person can take one now (`can_take`, and the `reason` if not). Never the key. |
| `POST /api/v1/encryption/root-key-copy` | `admin` | `encryption.root_key_copy` | `{"current_password", "code", "passphrase", "passphrase_again"}`. Re-checks the password and second factor, then answers the root key wrapped with the passphrase (scrypt, AES-256-GCM) as a file. `409` when no root key is configured or the account cannot prove itself; `422` for a passphrase under 12 characters or two that differ. Only the fact and time of the copy are recorded. |

### SSH keys, deploy keys and protected branches

[Git over SSH](../guide/git-over-ssh.md). A person's keys are theirs: another person's key answers `404` as if it did not exist. A key is ed25519, ECDSA or RSA of at least 3072 bits, one line in the OpenSSH format with no options; the answer to a refused key says why. Every key shows its `fingerprint` (SHA256), `last_used_at` and `last_used_from`. A key belongs to one owner: adding one that is registered already, as anyone's, answers `409`. With Git hosting off these answer `404 feature_off` (except `GET /api/v1/ssh`, which says SSH is off).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/ssh` | `signed-in` | — | `{"enabled", "host", "port", "certificates", "host_keys": [{"algorithm", "type", "fingerprint", "public_key", "created_at"}]}`: whether Git over SSH is on, the address clone addresses name, and the server's host keys to verify. |
| `GET /api/v1/user/ssh-keys` | `signed-in` | — | The caller's keys: `{"items": [...]}`. |
| `POST /api/v1/user/ssh-keys` | `signed-in` | `ssh_key.add` | Adds one: `{"name", "public_key", "expires_at"}`; the expiry is RFC 3339 and optional, the name defaults to the key's comment. `400` says why a key is refused (DSA, RSA under 3072 bits, a private key, options, a certificate, an expiry in the past), `409` for a key in use. |
| `DELETE /api/v1/user/ssh-keys/{id}` | `signed-in` | `ssh_key.delete` | Deletes the caller's key; it stops working at once. |
| `GET /api/v1/repositories/{owner}/{name}/deploy-keys` | `repo-admin` | — | The repository's deploy keys: `{"items": [...]}`. |
| `POST /api/v1/repositories/{owner}/{name}/deploy-keys` | `repo-admin` | `deploy_key.add` | Adds one: `{"name", "public_key", "write", "expires_at"}`. Read-only unless `write` is true. |
| `DELETE /api/v1/repositories/{owner}/{name}/deploy-keys/{id}` | `repo-admin` | `deploy_key.delete` | Deletes a deploy key. |
| `GET /api/v1/repositories/{owner}/{name}/commits` | `repo-viewer` | — | The newest commits reachable from `?ref=` (a branch, tag or commit; default: the default branch), newest first, optionally only those that changed `?path=`; `?limit=` (default 30, at most 200). `{"ref", "items": [{"sha", "subject", "message", "author", "authored_at", "committer", "committed_at", "parents"}]}`. |
| `GET /api/v1/repositories/{owner}/{name}/files` | `repo-viewer` | — | What `?path=` is at `?ref=` (the root when empty): a directory (`type: dir`, `entries` with `name`, `path`, `type`, `size`, `sha`, and with `?last=1` the newest commit that changed each: `last`; `last_bounded` says some entries were last changed further back than the search reaches) or a file (`type: file`, `size`, `sha`, and `content` as `utf8`, or `base64` for binary; no content, `truncated: true`, above 1 MiB). |
| `PUT /api/v1/repositories/{owner}/{name}/files` | `repo-operator` | `repository.file_write` | One commit that adds or changes one file: `{"branch", "path", "content", "encoding" (utf8 or base64), "message", "expected_sha"}`. The branch must exist and the rules of protected branches apply as to a push; an empty `expected_sha` creates and refuses if the file exists, otherwise it must be the file's current `sha`. A mirror or archived repository takes none. Answers `{"branch", "path", "commit", "sha", "parent"}`. Webhooks, push mirrors and pull requests are told as for a push. |
| `DELETE /api/v1/repositories/{owner}/{name}/files` | `repo-operator` | `repository.file_delete` | One commit that removes one file: `{"branch", "path", "message", "expected_sha"}`; `expected_sha` (the file's current `sha`) is required, so a file changed in between is never removed by mistake. The same branch rules, follow-up and answer as `PUT`. The file stays in the history. |
| `POST /api/v1/repositories/{owner}/{name}/files` | `repo-operator` | `repository.files_upload` | One commit that adds several files, for an upload: `{"branch", "message", "replace", "files": [{"path", "content", "encoding"}]}`; `encoding` is `base64` (the default) or `utf8`. At most 100 files, 1 MiB each and 8 MiB in all (`400` otherwise). A file that exists is a `409` and nothing is written, unless `replace` is true. The branch must exist and the rules of protected branches apply as to a push, with the same refusal; a mirror or archived repository takes none. Answers 201 `{"branch", "paths", "commit", "parent"}`. Webhooks, push mirrors and pull requests are told as for a push. |
| `GET /api/v1/repositories/{owner}/{name}/branches` | `repo-viewer` | — | The branches, newest first: `{"items": [{"name", "sha", "subject", "author", "committed_at", "ahead", "behind", "default", "protected", "open_pull"}], "total", "truncated"}`. `?base=` is the branch ahead and behind are counted against (default: the default branch; -1 when they could not be counted); `?q=` keeps names containing the text; `?limit=` at most 500 (default 100). |
| `POST /api/v1/repositories/{owner}/{name}/branches` | `repo-operator` | `repository.branch_create` | Make a branch: `{"name", "from"}` where `from` is a branch, tag or commit (default: the default branch). The branch rules of a push apply, so a protected name is for administrators; a mirror or archived repository takes none. Answers 201 `{"name", "commit"}`; 409 when the name or a part of it is taken. |
| `DELETE /api/v1/repositories/{owner}/{name}/branches` | `repo-operator` | `repository.branch_delete` | Delete the branch named by `?name=`. Never the default branch; protected branches are for administrators. Answers `{"name", "was", "undo"}`: the commit it pointed at and the command that brings it back. The audit detail holds the same commit. |
| `POST /api/v1/repositories/{owner}/{name}/branches/rename` | `repo-operator` | `repository.branch_rename` | Rename a branch: `{"name", "new_name"}`. Never the default branch (`409`: make another the default first) and never onto a name that exists (`409`). The rules of a push apply to both names, so a protected name is for administrators. Pull requests and runs that name the old branch are not moved. Answers `{"name", "was", "commit", "note"}`. |
| `GET /api/v1/repositories/{owner}/{name}/tags` | `repo-viewer` | — | The tags, newest first: `{"items": [{"name", "commit", "subject", "annotated", "message", "tagger", "committed_at"}], "total", "truncated"}`; `?q=` and `?limit=` as for branches. |
| `POST /api/v1/repositories/{owner}/{name}/tags` | `repo-operator` | `repository.tag_create` | Make a lightweight tag: `{"name", "from"}` where `from` is a branch, tag or commit (default: the default branch). The rules of a push apply, so a protected name is for administrators; a mirror or archived repository takes none. A tag that exists is never moved (`409`). Answers 201 `{"name", "commit"}`. |
| `DELETE /api/v1/repositories/{owner}/{name}/tags` | `repo-operator` | `repository.tag_delete` | Delete the tag named by `?name=`. Answers `{"name", "was", "undo"}`: the object it pointed at and the command that brings it back. |
| `GET /api/v1/repositories/{owner}/{name}/commits/{sha}` | `repo-viewer` | — | One commit (a full or abbreviated name, branch or tag): `{"commit": {...as in the log, with "parent_shas", the whole "message"}, "runs": [...]}`; the Sorties are those of projects the caller may see that were started for this commit. |
| `GET /api/v1/repositories/{owner}/{name}/diff` | `repo-viewer` | — | The files changed from `?from=` to `?to=` (commits, branches or tags; `from=root` is the empty tree): `{"files", "total", "additions", "deletions", "truncated", "next_offset"}`, paged by `?offset=` and `?limit=` (at most 500). Pinned to a full commit name, the answer is cacheable for ever. |
| `GET /api/v1/repositories/{owner}/{name}/diff/patch` | `repo-viewer` | — | The unified patch of one or more files (`?path=`, repeatable) between `?from=` and `?to=`, as for a pull request: a file over the size limits is held back (`held`) until `?full=1`. |
| `GET /api/v1/repositories/{owner}/{name}/blame` | `repo-viewer` | — | Who last changed each line of `?path=` at `?ref=`, for lines `?from=` to `?to=` (at most 1000 per answer; `next` is where the next range starts): `{"lines": [{"n", "sha", "text"}], "commits": {sha: {"author", "at", "subject", "previous", "previous_path"}}, "total"}`; `previous` is the commit before that change, for blaming the file as it was. A file over 1 MiB or a binary file is refused. Limited to 120 a minute per person. |
| `GET /api/v1/repositories/{owner}/{name}/search` | `repo-viewer` | — | Search the files at `?ref=` for `?q=` (literal; `regex=1` for a POSIX extended expression; `case=1` to match case; `in=content`, `path` or `both`; `dir=` a directory; `ext=go,ts`; `limit=` at most 500). `{"files": [{"path", "matches": [{"line", "text", "ranges"}]}], "matches", "partial", "reason", "millis"}`. It stops after 10 seconds or 4 MiB of matches and says so in `partial` and `reason`: the answer is never silently short. Limited to 30 a minute per person. |
| `GET /api/v1/repositories/{owner}/{name}/raw` | `repo-viewer` | — | A file's bytes at `?ref=`. Always a download (`Content-Disposition: attachment`, `application/octet-stream`, `nosniff`, a sandbox policy that forbids scripts and framing); `?inline=1` serves only a picture type (png, jpeg, gif, webp, avif, svg) as itself, still under the sandbox policy. A commit-pinned `ref` is cacheable for ever. Files up to 200 MiB. |
| `GET /api/v1/repositories/{owner}/{name}/archive/issues` | `repo-viewer` | — | The repository's imported issues, newest number first, without their text: `{"items": [...], "total", "open", "closed", "page", "per_page"}`. Query: `state` (`open`, `closed`, `all`), `q` (text in titles, bodies and comments, ignoring case), `page`, `per_page` (default 50, at most 200). Read-only history; see [the archive format](archive-format.md). |
| `POST /api/v1/repositories/{owner}/{name}/archive/issues` | `repo-admin` | `archive.import` | Stores a document of the archive format (`launchway-archive/1`: `source`, `issues` with their `comments`), at most 500 issues a call. Importing a number again replaces it. Answers `{"issues", "comments", "replaced", "total"}`. |
| `GET /api/v1/repositories/{owner}/{name}/archive/issues/{number}` | `repo-viewer` | — | One imported issue with its text and its comments in their original order. |
| `GET /api/v1/repositories/{owner}/{name}/protected-branches` | `repo-viewer` | — | `{"patterns": [...]}`: the branches that take a direct push only from the repository's administrators and cannot be deleted. |
| `PUT /api/v1/repositories/{owner}/{name}/protected-branches` | `repo-admin` | `repository.protect` | Replaces the patterns: `{"patterns": ["main", "release/*"]}`; `*` matches anything but a slash; an empty list removes the protection. |
| `GET /api/v1/repositories/{owner}/{name}/pulls` | `repo-viewer` | — | The repository's pull requests, newest first: `?state=` open (the default), closed, merged or all, `author`, `assignee`, `reviewer` (`@me` is the caller), `label`, `source`, `target`, `q` (title), `limit`, `offset`. `{"items": [...], "total"}`. |
| `POST /api/v1/repositories/{owner}/{name}/pulls` | `repo-operator` | `pull.create` | Opens a pull request from a branch of this repository: `{"title", "body", "source_branch", "target_branch" (default: the default branch), "draft", "labels", "assignees", "requested_reviewers"}`. Answers with the pull request view. Its head is kept as `refs/pull/N/head`. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}` | `repo-viewer` | — | One pull request with `mergeability` (clean, conflicts with the files, checking), `readiness` (whether it can merge, and `why` in one sentence, the blockers, the checks and approvals), and `actions`: what the caller may do now, each with the reason when it may not. |
| `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}` | `repo-viewer` | `pull.update` | Changes a pull request: any of `{"title", "body", "draft", "labels", "assignees", "target_branch", "state" (closed or open), "auto_merge" (merge, squash, rebase or "")}`. The author or an operator. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/merge` | `repo-operator` | `pull.merge` | Merges: `{"method" (merge, squash, rebase), "message", "delete_branch", "expect_head", "bypass_reason"}`. Made on the server through the ledger's compare-and-swap, so a push that arrives meanwhile is never lost: the merge is made again on the new tip. 409 `not_ready` with the sentence when the rules are not met. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/revert` | `repo-operator` | `pull.revert` | Opens a new pull request that undoes a merged one. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/files` | `repo-viewer` | — | The files changed (merge base to head) with status, additions, deletions, renames and binary flags, paged (`offset`, `limit`); `?since=N` compares with the head of revision N (changes since a review); `?from=&to=` compares two commits. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/patch` | `repo-viewer` | — | The unified patch: of one file (`?path=`, twice for a rename) or of everything, bounded; a file over the limits is held back until `?full=1` (load anyway). Takes the same `since`, `from` and `to`. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/commits` | `repo-viewer` | — | The commits of the pull request, oldest first: `{"items": [...]}`. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/conversation` | `repo-viewer` | — | Conversation comments, review threads (anchored to a line, outdated or resolved), reviews and revisions. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments` | `repo-viewer` | `pull.comment` | Comments: `{"body"}` for the conversation, or with `{"path", "line", "start_line", "side", "commit"}` on a line or range, or `{"reply_to"}` in a thread. A ```suggestion block is a suggested change. Markdown, kept as written. |
| `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}` | `repo-viewer` | `pull.comment_update` | Edits a comment's text (its author) or resolves or reopens its thread: `{"body", "resolved"}`. |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}` | `repo-viewer` | `pull.comment_delete` | Deletes a comment (its author or an administrator). |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}/apply` | `repo-operator` | `pull.suggestion_apply` | Applies a suggestion as a commit on the source branch (someone with push access); fails, saying why, when the code under it changed or the branch moved. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews` | `repo-viewer` | `pull.review` | Submits a review: `{"verdict": "comment", "approve" or "request_changes", "body", "comments": [...]}`. The reviewer's role is recorded with it; a rule may count only approvals of a role. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews/{id}/dismiss` | `repo-operator` | `pull.review_dismiss` | Dismisses a review: `{"reason"}` is required. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests` | `repo-operator` | `pull.review_request` | Asks people to review: `{"reviewers": [...], "groups": [...]}`: people, and groups any of whose members may answer. |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests/{reviewer}` | `repo-operator` | `pull.review_unrequest` | Takes one person (or a group, written `group:name`) off the list of those asked to review (a review they already gave stays); `404` if they were not asked, `409` if the pull request is not open. Answers with the pull request. |
| `GET /api/v1/repositories/{owner}/{name}/dependencies` | `repo-viewer` | — | What a hosted repository's default branch depends on, read from its manifests and lock files (Go modules, npm, Python, container images, workflow references): per dependency the version in use, the newest, the version the policy would move to, its age, known vulnerabilities (advisory, severity, fixed version) and why nothing is proposed. Also the settings that apply (`policy`, with which level locked what), files read, files that could not be read, and the ecosystems that are coming. Reads the default branch only, changes nothing; answers are kept for five minutes per branch tip. `404 feature_off` without Git hosting, `409` for a repository with no default branch, `502 report_failed` when the Depots (registries) or the vulnerability data cannot be reached. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/suggestions/apply` | `repo-operator` | `pull.suggestion_apply` | Applies several suggestions as one commit on the source branch: `{"comment_ids": [...]}`. All or none: two that change the same lines are refused, naming both (`409`); the branch's rules apply as to a push. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/queue` | `repo-viewer` | — | The branch's merge queue and this pull request's place in it: `{"enabled", "branch", "entry", "queue"}`; a removed entry keeps its `reason`. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/queue` | `repo-operator` | `pull.queue` | Adds a ready pull request to the merge queue of its target branch: `{"method", "priority"}` (priority needs the repository admin role). `409` with the sentence of what is missing; `400` when the branch has no queue. |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/queue` | `repo-viewer` | `pull.dequeue` | Takes it out of the merge queue; the author or an operator (`403` otherwise), `404` when it is not queued. |
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/update-branch` | `repo-operator` | `pull.update_branch` | Merges the target into the source branch (`{"method": "merge"}`, or `"rebase"`, which rewrites it). A push: protected source branches are refused to non-administrators. Answers `{"head"}`. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/viewed` | `repo-viewer` | — | The files the caller marked viewed that have not changed since. |
| `PUT /api/v1/repositories/{owner}/{name}/pulls/{number}/viewed` | `repo-viewer` | — | Marks files viewed or not for the caller only: `{"paths": [...], "viewed": true}`. A mark remembers the file's content and lapses when it changes. |
| `GET /api/v1/pulls/inbox` | `signed-in` | — | The open pull requests that wait on the caller, in repositories they may read: review asked of them or their groups, changes asked on their own, their own taken out of a merge queue, each with `why`. |
| `GET /api/v1/repositories/{owner}/{name}/pulls/{number}/owners` | `repo-viewer` | — | Who owns each changed file, read from `.launchway/owners` on the target branch, and the file's problems. |
| `POST /api/v1/repositories/{owner}/{name}/owners/check` | `repo-viewer` | `pull.owners_check` | Checks the text of an owners file (`{"text"}`) and says what is wrong, by line; stores nothing. |
| `GET /api/v1/repositories/{owner}/{name}/updates/preview` | `repo-viewer` | — | What the dependency updater would do now, ignoring its schedule: `decisions`, each a pull request to `open`, update in place, `close` or `keep`, or one that is `queued` behind a limit with the reason; security fixes first. Opens nothing: the updater runs as a pipeline step (`launchway updates run`). Same answer shape as `/dependencies`. |
| `GET /api/v1/repositories/{owner}/{name}/compare` | `repo-viewer` | — | What a pull request from `?head=` into `?base=` (default: the default branch) would contain: commits, files, ahead and behind, mergeability, and the number of an open pull request between them. |
| `GET /api/v1/repositories/{owner}/{name}/pull-settings` | `repo-viewer` | — | The repository's merge settings: which methods are allowed, the default, delete the branch after a merge, dismiss stale approvals, the message templates. |
| `PUT /api/v1/repositories/{owner}/{name}/pull-settings` | `repo-admin` | `repository.pull_settings` | Sets them; what the request leaves out stays. At least one method must stay allowed. |
| `GET /api/v1/repositories/{owner}/{name}/merge-rules` | `repo-viewer` | — | The merge rules of the protected branches: approvals (count, role), required checks, up to date, resolved threads, no self-approval, administrator bypass. Also `actions`: `edit_rules` and `edit_settings` (available to administrators of the repository, with the reason when not) and `require_signed_commits` (never available yet), so an interface offers only what can work. |
| `PUT /api/v1/repositories/{owner}/{name}/merge-rules` | `repo-admin` | `repository.merge_rules` | Sets rules: `{"rules": [{"pattern", "required_approvals", "approval_role", "required_checks", ...}]}`. A rule protects its branch pattern. Signed commits are reserved and refused. |

### Webhooks

[Webhooks and events](../guide/webhooks.md) of one repository (`repositories/{owner}/{name}/hooks`, its administrators) or of an owner (`owners/{owner}/hooks`, an administrator): the same seven routes under each Base. The secret is write-only: it is never in an answer, except one Launchway made because none was given, which the answer that creates the hook carries once as `secret`. A hook of another repository or owner answers `404`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/repositories/{owner}/{name}/hooks` | `repo-admin` | — | `{"items": [{"id", "scope", "repository", "name", "url", "events", "active", "has_secret", "failures", "disabled_reason", "created_at"}], "events": [...]}`. |
| `POST /api/v1/repositories/{owner}/{name}/hooks` | `repo-admin` | `webhook.add` | Adds one: `{"name", "url", "events", "active", "secret"}`; `events` are `push`, `create`, `delete`, `repository` and `work_item` (`pull_request` and `pull_request_review` are reserved). `201` with `{"hook", "secret", "secret_note"}`; `400` for an address that is not `http(s)://`, carries a password, or an unknown event. |
| `PATCH /api/v1/repositories/{owner}/{name}/hooks/{id}` | `repo-admin` | `webhook.update` | Changes any of `name`, `url`, `events`, `active`, `secret`; turning a hook on clears its failure count and notice. |
| `DELETE /api/v1/repositories/{owner}/{name}/hooks/{id}` | `repo-admin` | `webhook.delete` | Deletes the hook and its log. |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/test` | `repo-admin` | `webhook.test` | Sends a `ping` now, once, and answers with the delivery (state, status, error in words). |
| `GET /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries` | `repo-admin` | — | The newest 50 deliveries: `event`, `state`, `attempts`, `status`, `duration_ms`, `error`, `response` (the start of the answer) and `payload` (the request). |
| `POST /api/v1/repositories/{owner}/{name}/hooks/{id}/deliveries/{delivery}/redeliver` | `repo-admin` | `webhook.redeliver` | Sends that delivery's request again, now, once, as a new delivery. |
| `GET /api/v1/owners/{owner}/hooks` | `admin` | — | As for a repository, for every repository of the owner. |
| `POST /api/v1/owners/{owner}/hooks` | `admin` | `webhook.add` | |
| `PATCH /api/v1/owners/{owner}/hooks/{id}` | `admin` | `webhook.update` | |
| `DELETE /api/v1/owners/{owner}/hooks/{id}` | `admin` | `webhook.delete` | |
| `POST /api/v1/owners/{owner}/hooks/{id}/test` | `admin` | `webhook.test` | |
| `GET /api/v1/owners/{owner}/hooks/{id}/deliveries` | `admin` | — | |
| `POST /api/v1/owners/{owner}/hooks/{id}/deliveries/{delivery}/redeliver` | `admin` | `webhook.redeliver` | |

### Gitea-style repository calls

The few calls tools that create and inspect repositories send to a Gitea server, so they work unchanged against Launchway. This is the start of the compatibility layer, not all of it: there are no issues, pull requests or webhooks here. Answers carry `name`, `full_name`, `owner.login`, `private`, `archived`, `empty`, `default_branch` and `clone_url`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/user` | `signed-in` | — | The caller: `login`, `username`, `full_name`, `email`. |
| `POST /api/v1/user/repos` | `admin` | `repository.create` | Creates a repository under the caller's own name: `{"name", "private", "default_branch"}`. `private` is true unless given as false. |
| `GET /api/v1/repos/{owner}/{name}` | `repo-viewer` | — | One repository. |
| `PATCH /api/v1/repos/{owner}/{name}` | `repo-admin` | `repository.update` | Changes `name`, `private`, `archived` or `default_branch`; other fields are ignored. |
| `DELETE /api/v1/repos/{owner}/{name}` | `repo-admin` | `repository.delete` | Moves the repository to the trash. |
| `GET /api/v1/repos/{owner}/{name}/branches/{branch}` | `repo-viewer` | — | `{"name", "commit": {"id"}}`, or `404` when there is no such branch (yet): a push may take a moment to show. |

### Git over HTTP

Clone, fetch and push with the real `git`. Every address has two forms: `/git/<owner>/<name>.git/...` and the form other Git hosts use, `/<owner>/<name>.git/...`, so a remote moves to Launchway by changing its host. `.git` may be left off. A browser that opens `/<owner>/<name>` (no `.git`) and may read the repository is redirected to `/repositories/<owner>/<name>`, the repository's screen; anyone else gets the web UI's own answer. Owner names Launchway keeps for its own paths (`api`, `git`, `projects`, `runs` and others) cannot be used, so the second form never collides with the web UI. Only the smart protocol is served (protocol version 2 for fetch, gzip request bodies).

These routes do not use cookies. They take HTTP Basic (the password is a Launchway API token, the user name is ignored; a token alone as the user name works too), `Authorization: Bearer <token>`, `Authorization: token <token>`, or a Sortie's read-only credential. A token in the query string is refused with `400`. Basic over plain HTTP is refused with `403` unless the listener is loopback or `forge.allow_insecure_http` is on. A failure answers `401` with `WWW-Authenticate: Basic`; a repository that does not exist, one the caller may not read and, without credentials, a private one are told apart by nobody. Anonymous clones work only on a repository whose visibility is `public`. A push to an archived or mirror repository answers `403`. With Git hosting off every route answers `404 feature_off`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /git/{owner}/{name}/info/refs` | `repo-viewer` | — | The ref advertisement (`?service=git-upload-pack`, or `git-receive-pack`, which needs push rights). The dumb protocol is not served. |
| `POST /git/{owner}/{name}/git-upload-pack` | `repo-viewer` | — | Fetch and clone. The request body is limited to 32 MiB. |
| `POST /git/{owner}/{name}/git-receive-pack` | `repo-operator` | `git.push` | Push. The body is limited by `forge.max_push_mib`; the audit entry names the refs and the token. |
| `GET /{owner}/{name}/info/refs` | `repo-viewer` | — | The same, at the address other Git hosts use. |
| `POST /{owner}/{name}/git-upload-pack` | `repo-viewer` | — | The same. |
| `POST /{owner}/{name}/git-receive-pack` | `repo-operator` | `git.push` | The same. |

## The software graph

One graph from code to what is built from it to what it needs and where it runs: repositories, packages, components, requirements, images and what was built, pipelines, Airfields, deployments, machines and platforms. It lives in the server's own database. Every answer is for the caller alone: a node, an edge or a count that the caller may not read in its own place is not in it, and a path that would run through something unreadable is not shown. A node is named `kind:key`, for example `repository:team/app`, `package:go:example.com/lib`, `image:registry.example/team/app` or `deployment:prod/shop/api`. Traversals take `depth` (default 8, at most 64), `nodes` (default 2000, at most 50000) and `timeout_ms` (default 5000, at most 60000), and say `truncated` with `truncated_reason` (`depth`, `nodes` or `time`) when they stopped short. `as_of` (RFC 3339) or `commit` with `repository` answers as the graph stood then. See [the guide](../guide/software-graph.md).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/graph` | `signed-in` | — | What the caller may read of the graph: nodes by kind, edges, and when each source last read. |
| `GET /api/v1/graph/search` | `signed-in` | — | `?q=` a part of a name or key, optionally `kind=` (repeat), `limit=`: `{"items", "more"}`. |
| `GET /api/v1/graph/node` | `signed-in` | — | `?ref=`: the node and the edges directly around it (`edges`, `more_edges`), each edge with its constraint, resolved version, source, commit and confidence. `404` for a node that does not exist or that the caller may not read: the two look the same. |
| `GET /api/v1/graph/neighbours` | `signed-in` | — | `?ref=&direction=` (`downstream` or `upstream`) `&depth=`: what is reached, each with its depth and the path to it. |
| `GET /api/v1/graph/path` | `signed-in` | — | `?from=&to=`: the shortest way between two nodes, going with a change (`affects`) or against it (`affected-by`); `found` false when there is none the caller may see. |
| `GET /api/v1/graph/impact` | `signed-in` | — | `?ref=`: what is affected if it changes: `total`, `by_kind`, `by_owner` (each item with its `path`, the why), the affected `repositories`, and `deployments` that run affected digests with their Airfield, digest and version. |
| `GET /api/v1/graph/where` | `signed-in` | — | `?ref=`: the deployments a repository, package, commit or image reaches, with `airfields`; each says whether the digest itself contains the thing (`exact`). |
| `GET /api/v1/graph/contents` | `signed-in` | — | `?ref=`: what is in a deployment, image or component: `images`, `base_images`, `packages` (with resolved versions on the edges), `commits`, `repositories`. |
| `GET /api/v1/graph/order` | `signed-in` | — | `?repository=` (repeat; none means every repository with a dependency), `upstream=true`: the build `order`, parallel `levels`, the `edges` (each with the packages and images that make it so, and any override), `cycles` (each with its closing path and a suggestion) and the `overrides` in force. |
| `GET /api/v1/graph/rebuild-set` | `signed-in` | — | `?repository=`: what must be built again after a push to it, in order, each with the way it depends on it; dependencies a person pinned or ignored are listed as `skipped`. |
| `GET /api/v1/graph/overrides` | `signed-in` | — | The corrections to the build order the caller may see. |
| `POST /api/v1/graph/overrides` | `signed-in` | `graph.override.add` | `{"kind", "from", "to", "value", "reason"}` where `kind` is `before`, `ignore` or `pin`, with repositories as `owner/name`. A reason is required; the caller needs the operator role on both repositories. |
| `DELETE /api/v1/graph/overrides/{id}` | `signed-in` | `graph.override.remove` | Withdraws an override (the row is kept, marked removed); needs the operator role on both repositories. |
| `GET /api/v1/graph/needs` | `signed-in` | — | `?ref=` a component or a repository: what each component needs, each fact with its `source` (`declared` in `.launchway/requirements.yaml` or `inferred`, with the `evidence`) and `advice` where an inferred fact differs from a declared one. |
| `GET /api/v1/graph/placement` | `signed-in` | — | `?ref=`: the machines and platforms that satisfy each component (`fits`), near misses with what is missing, and `gaps`. |
| `GET /api/v1/graph/specification` | `signed-in` | — | `?ref=` (repeat for components that run on one machine), `headroom=` percent (default 25), `format=yaml`: a neutral machine specification with totals, headroom, provenance and conflicts. An export; nothing is provisioned. |
| `GET /api/v1/graph/reach` | `signed-in` | — | `?ref=` a package: the versions in use that have known advisories, what uses each, and the deployments that run them. Needs vulnerability data on the server; `note` says when there is none. |
| `POST /api/v1/graph/rescan` | `admin` | `graph.rescan` | Reads every repository, project and the installation again. |
| `POST /api/v1/graph/rebuild` | `admin` | `graph.rebuild` | Clears the graph and reads everything again; the result equals what incremental updates built. Overrides are kept. |
| `POST /api/v1/repositories/{owner}/{name}/graph/refresh` | `repo-operator` | `graph.refresh` | Reads the repository again now (pushes do this by themselves). |
| `POST /api/v1/repositories/{owner}/{name}/requirements/proposal` | `repo-operator` | `graph.requirements.propose` | `{"content", "title"}`: opens a pull request that makes `.launchway/requirements.yaml` what `content` says; the file is checked first (`422` with what is wrong), and nothing lands until a person merges it. `404 feature_off` without Git hosting. |

## API tokens

These routes need sign-in on: with sign-in off they answer `404` with `API tokens need sign-in, which is off on this server (auth.mode: none)`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/tokens` | `signed-in` | — | The caller's own tokens, without their values. |
| `POST /api/v1/tokens` | `signed-in` | `token.create` | Makes a token. Only from a signed-in browser session, never with another token (`403`). |
| `DELETE /api/v1/tokens/{id}` | `signed-in` | `token.revoke` | Revokes one of the caller's tokens. Answers `204`. |
| `GET /api/v1/tokens/limits` | `signed-in` | — | The longest a new token may last and the highest role it may have. |
| `GET /api/v1/admin/tokens` | `admin` | — | Everyone's tokens. |
| `DELETE /api/v1/admin/tokens/{id}` | `admin` | `token.revoke` | Revokes anyone's token. Answers `204`. |

The create body:

```json
{"name": "deploy-script", "expires_in_days": 30, "role": "viewer", "projects": ["pay-*"]}
```

`name` is 1 to 100 characters. `expires_in_days` defaults to 90 and may be at most `auth.tokens.max_days` (default 365). `role` (`viewer`, `operator` or `admin`) and `projects` (at most 20 names or patterns) limit the token; see [Roles and permissions](roles.md#api-token-limits). The answer (`201`, `Cache-Control: no-store`) is the only time the value is shown:

```json
{"id": "tok_06gzddzevx4hx1awkvyg", "name": "deploy-script", "created_at": 1795140611807, "expires_at": 1797732611807, "last_used": 0, "role": "viewer", "projects": ["pay-*"], "token": "lw_…"}
```

`GET /api/v1/tokens/limits` answers `{"max_days": 365, "default_days": 90, "your_role": "operator"}`.

## Stations {#runners}

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runners` | `any-viewer` | — | The Stations that have contacted the server: `id`, `name`, `executor`, `version`, `last_seen`, `online`, their pool, labels and whether they are enrolled (with the key's fingerprint and age), and for launch Stations their targets. |
| `GET /api/v1/runner-pools` | `any-viewer` | — | The Flight lines (`runner_pools` in the configuration, and the default pool): name, kind, projects, events and whether the pool requires identity. |
| `GET /api/v1/runner-join-tokens` | `signed-in` | — | The join tokens that are still usable, never their values. Needs the role `runner_admin` names. |
| `POST /api/v1/runner-join-tokens` | `signed-in` | `runner.join_token.create` | Makes a join token for a pool: `{"pool", "labels", "description", "max_uses", "ttl", "ephemeral"}` (`ttl` such as `1h`, the default; at most 7 days). Answers `201` with the value (`lwj_…`) in `token`, shown once: only its hash is stored. Needs the role `runner_admin` names. |
| `POST /api/v1/runner-join-tokens/{id}/revoke` | `signed-in` | `runner.join_token.revoke` | Revokes a join token. |
| `POST /api/v1/runners/{id}/revoke` | `signed-in` | `runner.revoke` | Revokes an enrolled Station: every server refuses it from then on, and the steps it held are queued again. |
| `POST /api/v1/runners/{id}/rotate` | `signed-in` | `runner.rotate` | Asks an enrolled Station to move to a new key at its next request. |

Making and revoking join tokens, and revoking and rotating Stations, need the role that `runner_admin` names on every project (admin by default); the handlers check it after the access class. See [Stations on other machines and clusters](../guide/runner-identity.md).

## Verifications and AI

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/verifications` | `run-viewer` | — | A Sortie's verifications: each interval's readings, the verdict and its reason in words, log clusters, and any explanation. |
| `GET /api/v1/verifications` | `signed-in` | — | Recent verifications of the projects the caller may see: `?environment=`, `?project=`, `?service=`, `?limit=`. |
| `GET /api/v1/runs/{id}/steps/{step}/verify/controls` | `run-viewer` | — | Whether the caller could mark the verification passed or failed now, and why not, in plain words. |
| `POST /api/v1/runs/{id}/steps/{step}/verify/override` | `run-operator` | `verification.override` | Marks a running or paused verification passed or failed, with a reason: `{"outcome", "reason"}` where `outcome` is `passed` or `failed`. On a protected Airfield the person who started the run may not mark it passed. |
| `GET /api/v1/health-sources` | `signed-in` | — | The health sources the caller's projects may use, by name, type and projects; never where they are or how they are reached. |
| `GET /api/v1/ai/models` | `signed-in` | — | The configured models the caller may use: name, limits and today's use, never endpoints or keys; and `server_can_explain`. |
| `GET /api/v1/runs/{id}/steps/{step}/explanations` | `run-viewer` | — | The explanations models gave of a failed step. |
| `POST /api/v1/runs/{id}/steps/{step}/explain` | `run-operator` | `step.explain` | Asks a configured model (`{"model"}`) to explain a failed step from its log tail, failed tests and findings, redacted. `409` when the server has no model or no key store for it. |

## Work tracking

Items, boards, sprints, charts, settings, the inbox and agents. Who may do what is decided by the store, not
by the route class: seeing work needs the viewer role on the Project, changing it the operator role, and
settings, agent registration and imports the admin role (see [Roles and permissions](roles.md#work-tracking)).
A Squadron's work is open to people who may act on any of its Projects; a Wing's and the Base's to people who
may act on any Project. Scopes are written `project:owner/name`, `unit:name`, `organization` or `installation`.
Routes under `/api/v1/work/agent/` are the calls an agent makes with its `lwa_` token; the rest are for people (an agent
token is offered the item routes only for the work it holds). The guides are [tracking work](../guide/tracking-work.md),
[boards and sprints](../guide/boards-and-sprints.md), [work and code](../guide/work-and-code.md) and
[working with agents](../guide/working-with-agents.md).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/work/items` | `signed-in` | — | Lists items you may see. `?scope=`, `?q=` (the [query language](work-queries.md)), `?sort=`, `?desc=true`, `?limit=`, `?offset=`, `?parent=`. Answers `{items, total, limit, offset}`. |
| `POST /api/v1/work/items` | `signed-in` | `work.create` | Creates an item: `{scope, type, title, body, state, priority, estimate, labels, assignees, parent, milestone, sprint, criteria, custom, confidential, …}`..  `201`. |
| `GET /api/v1/work/items/{ref}` | `signed-in` | — | One item with its comments count, links, code, criteria, claim and, once merged, [where the fix is running](../guide/work-and-code.md#where-the-fix-is-running). `{ref}` is `PAY-12`. `404` for one you may not see. |
| `PATCH /api/v1/work/items/{ref}` | `signed-in` | `work.update` | Changes fields, labels, assignees, criteria, state, parent, milestone, sprint, or watching. `expected_version` makes it fail with `409` if the item changed. The workflow and the agent rules apply. |
| `POST /api/v1/work/items/{ref}/rank` | `signed-in` | `work.rank` | Puts the item `top`, `bottom`, `before` or `after` another item of the same Project (`{where, other}`). Not for agents. |
| `POST /api/v1/work/items/{ref}/move` | `signed-in` | `work.move` | Moves a card: `{state, before}`. The same call the drag, the menu and the keyboard make. |
| `POST /api/v1/work/items/{ref}/take-over` | `signed-in` | `work.take_over` | A person takes the item from an agent: the lease ends, the person is the assignee, the agent's branch stays. |
| `GET /api/v1/work/items/{ref}/history` | `signed-in` | — | The item's history, oldest first: who (person, agent or system), how, what changed. |
| `GET /api/v1/work/items/{ref}/comments` | `signed-in` | — | Comments, oldest first. |
| `POST /api/v1/work/items/{ref}/comments` | `signed-in` | `work.comment` | Adds a comment (`{body}`, up to 64 KiB); `@name` tells that person. |
| `PATCH /api/v1/work/comments/{id}` | `signed-in` | `work.comment_edit` | The author edits a comment; the earlier text is kept. |
| `GET /api/v1/work/comments/{id}/edits` | `signed-in` | — | The earlier texts of a comment, oldest first. |
| `GET /api/v1/work/items/{ref}/links` | `signed-in` | — | Links to other items (blocks, relates, duplicates), both directions, hiding items you may not see. |
| `POST /api/v1/work/items/{ref}/links` | `signed-in` | `work.link` | Adds a link `{kind, to}`. `400` for a cycle of `blocks`, which names the chain; `409` for a link that exists. |
| `DELETE /api/v1/work/links/{id}` | `signed-in` | `work.unlink` | Removes a link. |
| `GET /api/v1/work/items/{ref}/code` | `signed-in` | — | The commits, branches, pull requests and runs tied to the item. |
| `POST /api/v1/work/items/{ref}/code` | `signed-in` | `work.code_link` | Ties a `{kind, repo, ref, title, state}` to the item; the same kind, repository and reference updates it. |
| `GET /api/v1/work/items/{ref}/delivery` | `signed-in` | — | Where the item's merged change is running, per Airfield (environment), from real deployments, with a sentence. `null` before anything merged. |
| `GET /api/v1/work/items/{ref}/attachments` | `signed-in` | — | The files on the item (name, type, size, SHA-256, kind, who, when). |
| `POST /api/v1/work/items/{ref}/attachments` | `signed-in` | `work.attach` | Adds a file (up to 10 MB; 50 an item). `kind` is `file`, `transcript` or `evidence`. |
| `GET /api/v1/work/attachments/{id}` | `signed-in` | — | One file, as a download (PNG, JPEG, GIF, WebP and plain text may be shown in a page; `?download=1` forces a download). Sniffing is off. |
| `GET /api/v1/work/views` | `signed-in` | — | Saved queries you may use: yours, and shared ones. |
| `POST /api/v1/work/views` | `signed-in` | `work.view_save` | Saves a query under a name `{scope, name, query, shared}`. |
| `DELETE /api/v1/work/views/{id}` | `signed-in` | `work.view_delete` | Deletes a view of yours. |
| `GET /api/v1/work/by-code` | `signed-in` | — | The items tied to a commit, pull request or branch: `?kind=&repo=&ref=`. |
| `GET /api/v1/work/board` | `signed-in` | — | The board of a scope: a column for each state, items in backlog order, over-limit warnings. `?scope=`, `?q=`, `?lanes=assignee, epic or priority`, `?sprint=` (a name, or `active`). |
| `GET /api/v1/work/milestones` | `signed-in` | — | Milestones of a scope with their progress. |
| `POST /api/v1/work/milestones` | `signed-in` | `work.milestone_create` | Creates a milestone `{scope, title, description, due_at}`. |
| `PATCH /api/v1/work/milestones/{id}` | `signed-in` | `work.milestone_update` | Changes a milestone, or closes it. |
| `GET /api/v1/work/sprints` | `signed-in` | — | Sprints of a scope, newest first, with committed, completed and added work and capacity advice. |
| `POST /api/v1/work/sprints` | `signed-in` | `work.sprint_create` | Plans a sprint `{scope, name, goal, start_at, end_at, capacity}`. |
| `GET /api/v1/work/sprints/{id}` | `signed-in` | — | One sprint with its numbers. |
| `PATCH /api/v1/work/sprints/{id}` | `signed-in` | `work.sprint_update` | Changes a planned or active sprint's name, goal, dates or capacity. |
| `POST /api/v1/work/sprints/{id}/start` | `signed-in` | `work.sprint_start` | Starts a planned sprint and records what it committed to. One sprint of a scope is active. Not for agents. |
| `POST /api/v1/work/sprints/{id}/complete` | `signed-in` | `work.sprint_complete` | Completes the active sprint; `{carry_to}` names the sprint unfinished items move to (none returns them to the backlog). Not for agents. |
| `GET /api/v1/work/sprints/{id}/burndown` | `signed-in` | — | The burndown of a sprint, day by day. |
| `GET /api/v1/work/charts/velocity` | `signed-in` | — | What the last completed sprints committed to and completed, and the average. |
| `GET /api/v1/work/charts/flow` | `signed-in` | — | The cumulative flow: items in each state each day (`?days=`, 30 by default). |
| `GET /api/v1/work/charts/times` | `signed-in` | — | Lead and cycle time of the work finished in the last `?days=` (90 by default): median, 85th percentile, mean, a histogram and each item. |
| `GET /api/v1/work/roadmap` | `signed-in` | — | Epics, milestones and sprints of a scope with dates and progress (draw them as bars, or read them as a table). |
| `GET /api/v1/work/release-notes` | `signed-in` | — | The work finished between `?since=` and `?until=`, grouped by type, with a Markdown text. |
| `GET /api/v1/work/settings` | `signed-in` | — | The settings of a level, those in force after inheritance, the autonomy policy and where it comes from, and whether you may change them. See the [work format](work-format.md#settings). |
| `PUT /api/v1/work/settings` | `signed-in` | `work.settings` | Replaces the settings of one level `{scope, settings}`. Needs a person who maintains it; loosening what agents may do needs a reason. |
| `GET /api/v1/work/inbox` | `signed-in` | — | Your inbox entries, newest first (`?unread=true`), and the unread count. |
| `POST /api/v1/work/inbox/read` | `signed-in` | `work.inbox_read` | Marks entries read by id, or all of them. |
| `GET /api/v1/work/home` | `signed-in` | — | What Home shows about work: yours, and what agents did for you and what waits for your review. |
| `GET /api/v1/work/agents` | `signed-in` | — | The agents you may see, with what each holds, took today and has waiting. |
| `POST /api/v1/work/agents` | `signed-in` | `work.agent_create` | Registers an agent and answers its token **once**. Needs a person who maintains the scope; `403` for anyone else. `201`. |
| `GET /api/v1/work/agents/{name}` | `signed-in` | — | One agent. |
| `PATCH /api/v1/work/agents/{name}` | `signed-in` | `work.agent_update` | Changes an agent's projects, types, labels, limits, budgets, model, class or owner, or switches it off (`disabled`): its token stops working at once and what it holds returns to Ready. |
| `DELETE /api/v1/work/agents/{name}` | `signed-in` | `work.agent_delete` | Deletes an agent. |
| `POST /api/v1/work/agents/{name}/rotate-token` | `signed-in` | `work.agent_rotate` | Replaces the token and answers the new one once; the old one stops working. |
| `GET /api/v1/work/agents/{name}/actions` | `signed-in` | — | What the agent did lately: claims, heartbeats, hand-backs, reviews. |
| `GET /api/v1/work/spend` | `signed-in` | — | What agents reported spending: each agent over 24 hours against its budget, each Project over all time, and the costliest items. |
| `POST /api/v1/work/autonomy/clear-pause` | `signed-in` | `work.autonomy_clear_pause` | A person who maintains the scope lets autonomy resume `{scope, reason}`. |
| `GET /api/v1/work/samples` | `signed-in` | — | Merges an agent approved that were flagged for a person to look at, those not yet looked at unless `?all=true`. |
| `POST /api/v1/work/samples/{id}/complete` | `signed-in` | `work.sample_complete` | Records that a maintainer looked at a flagged merge. |
| `GET /api/v1/work/agent/me` | `signed-in` | — | For an agent token: who the agent is, its limits and what it holds. |
| `GET /api/v1/work/agent/queue` | `signed-in` | — | For an agent token: what waits for it, in order (`?limit=`). |
| `POST /api/v1/work/agent/claim` | `signed-in` | `work.agent_claim` | For an agent token: takes the named item, or the next, on a lease (`{item, lease_seconds, note}`). `409` when a limit or a budget is reached or the item is held. |
| `POST /api/v1/work/agent/items/{ref}/heartbeat` | `signed-in` | `work.agent_heartbeat` | For an agent token: extends the lease; `409` when it has run out. |
| `POST /api/v1/work/agent/items/{ref}/hand-back` | `signed-in` | `work.agent_hand_back` | For an agent token: gives the item back to Ready with a reason (required). |
| `POST /api/v1/work/agent/items/{ref}/request-review` | `signed-in` | `work.agent_request_review` | For an agent token: moves the item to In review, ends the lease and tells the people concerned. It never finishes the item. |
| `POST /api/v1/work/agent/items/{ref}/usage` | `signed-in` | `work.agent_usage` | For an agent token: reports tokens, cents and seconds used; the answer says when a budget is used up. |
| `POST /api/v1/work/agent/pull-review` | `signed-in` | `work.agent_pull_review` | For an agent token: stores a structured review of a pull request; whether it counts as an approval depends on the policy and the reviewer's independence. |
| `GET /api/v1/work/pull-reviews` | `signed-in` | — | The agents' reviews of a pull request (`?repo=&pull=`), each with whether it counts and, if not, why. |
| `POST /api/v1/work/import/stories` | `signed-in` | `work.import_stories` | Imports epics and stories from the [stories JSON](work-format.md#stories-json) shape; `preview` and `update`. |
| `POST /api/v1/work/import/archive` | `signed-in` | `work.import_archive` | Promotes a repository's read-only archive into live items; `preview` counts only. |
| `GET /api/v1/work/proposals` | `signed-in` | — | The groups of findings analyzers proposed at a scope (`?scope=&state=`), each with its state, severity, open count, item and occurrences. |
| `POST /api/v1/work/proposals` | `signed-in` | `work.propose` | An analyzer's batch (`{scope, source, run_id, report_url, complete, proposals}`): grouped by group key, capped, and held for a review unless the level lets the source file without one. Not for agent tokens. |
| `POST /api/v1/work/proposals/accept` | `signed-in` | `work.proposal_accept` | Opens the items of proposed groups (`{scope, source, groups, edits}`), each with a checklist; `edits` changes a title, body or priority first. |
| `POST /api/v1/work/proposals/dismiss` | `signed-in` | `work.proposal_dismiss` | Dismisses proposed groups with a reason (required); the findings are remembered and not proposed again. |
| `POST /api/v1/work/proposals/merge` | `signed-in` | `work.proposal_merge` | Joins proposed groups of one source into the group named in `into`. |
| `POST /api/v1/work/health` | `signed-in` | `work.health` | Checks a hosted repository (`{repository}`) for a README, license, owners file, pipeline file, protected default branch and tests, and proposes what is missing as one group. |

## MCP

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /mcp` | `public` | — | The [MCP](../guide/agents.md) endpoint (Streamable HTTP, stateless). The handler checks the `Origin`, the token (`Authorization: Bearer lw_…`, or the sign-in provider's access token when `mcp.oauth` is set) and the per-token limits; session cookies are not accepted. Changes made through a tool are audited under the tool's own action, with `via MCP` in the detail. `404` when `mcp.enabled` is false. |
| `GET /mcp` | `public` | — | Answers `405`: the server keeps no sessions and has no event stream. |
| `DELETE /mcp` | `public` | — | Answers `405`: there is no session to end. |
| `GET /.well-known/oauth-protected-resource/mcp` | `public` | — | OAuth 2.0 Protected Resource Metadata (RFC 9728) for `/mcp`. `404` unless `mcp.oauth.resource` is set. |

## Audit trail

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/audit` | `admin` | — | The Logbook, newest first. |

Filter with `?user=`, `?project=`, `?action=`, `?from=` and `?to=` (Unix milliseconds); page with `?before=` and `?limit=`:

```json
{"entries": [{"id": 11, "at": 1795140189526, "user": "dana", "source": "127.0.0.1", "action": "run.draft", "project": "demo-lib-app", "target": "run_06gzdcbxc5pnfzy16300", "outcome": "ok", "detail": "draft run #1 of main at 029559c9, pipeline sha256:3384…"}], "next_before": 0}
```

The **Audit** column of the tables on this page names the action each call records. Calls in the [Access classes](#access-classes) other than `public` and `runner` that change something are recorded whether they are allowed, refused (`denied`) or fail (`failed`). Every action is described in [Events and audit trail](events.md).

## Live stream

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/stream` | `signed-in` | — | Live changes, as server-sent events. |

The answer is `text/event-stream`. It starts with `retry: 2000` and the comment `: connected`, sends `: ping` every 15 seconds, and one event per change:

```text
event: run
data: {"id":"run_…","project":"demo-app","number":3,"status":"running",…}
```

Without parameters it sends `run` events (Debriefs) of the projects the caller may see. With `?run=<id>` it sends only that run's changes, including its log lines. With sign-in on, the session or token is checked again at every ping, and the stream ends when it is no longer valid. A client that falls behind is disconnected: reconnect and fetch again. Proxies must not buffer this route (the server sends `X-Accel-Buffering: no`). The message types are listed in [Events and audit trail](events.md).

```sh
curl -N http://127.0.0.1:8080/api/v1/stream
```

## Webhooks

Webhook routes need no session or token. A caller proves itself with a secret the project's configuration names (an environment variable or a file). A caller that cannot prove itself, a project without the hook, and an unknown project all get the same answer, `404 {"error": "not found"}`. Refused calls count towards the [failure limit](#too-many-failures) and are recorded in the Logbook.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /api/v1/projects/{name}/git-hook` | `public` | — | A Git host's push or pull-request webhook: makes the server poll the project at once. Recorded as `webhook.git`. |
| `POST /api/v1/projects/{name}/webhooks/{trigger}` | `public` | — | Starts a run of a pipeline's webhook trigger, with inputs mapped from the JSON body. Recorded as `webhook.run`. |

**Git hook.** Configure `git_webhook: { kind, secret_env }` on the project. The server checks each call the way its host signs it:

| `kind` | Checked header | Delivery ID |
|---|---|---|
| `github` (default) | `X-Hub-Signature-256: sha256=<HMAC-SHA256 of the body>` | `X-GitHub-Delivery` |
| `gitea`, `forgejo` | `X-Gitea-Signature` or `X-Forgejo-Signature`: hex HMAC-SHA256 of the body | `X-Gitea-Delivery` or `X-Forgejo-Delivery` |
| `gitlab` | `X-Gitlab-Token` equal to the secret | `X-Gitlab-Event-UUID` or `X-Gitlab-Webhook-UUID` |

It answers `202` with `{"status": "polling"}`; `{"status": "already received"}` for a delivery ID seen before; `{"status": "ignored", "event": "…"}` for an event that cannot move a ref. The body may be up to 25 MiB. The poll stays the source of truth: a call only makes it happen sooner.

**Webhook trigger.** The pipeline declares the trigger, and the project's configuration names its secret under `webhooks: { <trigger>: { token_env } }` or `{ hmac_secret_env }`. A caller sends either:

- `Authorization: Bearer <token>`, or
- `X-Launchway-Timestamp: <Unix seconds>` and `X-Launchway-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>">`, with a timestamp within five minutes. A signed request is accepted once; a repeat answers `409`.

The body is JSON of at most 64 KiB. `Idempotency-Key: <up to 200 printable characters>` makes a repeated call answer `200` with the run the first call started, and the header `Idempotent-Replayed: true`. A new Sortie answers `201` with the Sortie. See [Pipeline reference](pipeline.md).

## Station API {#runner-api}

The routes under `/api/runner/v1/` are for Launchway Stations only. People and scripts never call them. They are authenticated by `Authorization: Bearer <token>`: the shared Station token (`LAUNCHWAY_RUNNER_TOKEN`, or the token the server keeps in its database), or a launch Flight line's own token (`runner_pools` in the server configuration). Any other token answers `401 {"error": "a valid runner token is required"}`. All Station connections are outbound from the Station to the server.

In outline, a Station long-polls for work (`lease`), says the step started (`start`), reports while it runs (`heartbeat`, `logs`; the answer says whether the step was canceled), uploads and fetches Cargo in chunks, and reports how the step ended (`finish`). A Station that stops says so (`leave`), so that others can take over its work at once. Launch Stations, which hold a pool's token, take only launch and Return to base work, watch the deployments of their targets and report what they observe, and carry out people's deployment commands. The shared token cannot take launch work, and a pool's token can take nothing else (`403`).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /api/runner/v1/lease` | `runner` | — | Waits up to 50 seconds for a step to run; `204` if none. |
| `POST /api/runner/v1/start` | `runner` | — | The leased step has started. |
| `POST /api/runner/v1/heartbeat` | `runner` | — | The step is still running; answers whether it was canceled. A lost lease answers `410`. |
| `POST /api/runner/v1/logs` | `runner` | — | Appends log lines. With `offset` (how many lines of the step attempt came before the batch) it is safe to repeat: lines the server has are skipped, and a batch that starts beyond what the server has is answered `409` with `received`, the count it has. |
| `POST /api/runner/v1/finish` | `runner` | — | How the step ended, with its outputs and report files (`413` if larger than the server accepts). Safe to repeat: a result sent again for a lease that already finished answers success and records nothing more. |
| `POST /api/runner/v1/runs` | `runner` | — | The state of runs the Station holds work for (at most 5000 per call). |
| `POST /api/runner/v1/leave` | `runner` | — | The Station is shutting down. |
| `POST /api/runner/v1/artifacts/begin` | `runner` | — | Starts uploading a Cargo. A call carrying an `Idempotency-Key` header (as do the begin and commit calls of Cargo and caches, the finish call and `deploy/command-done`) that is made again with the same key gets the first answer, marked `Idempotent-Replayed: true`, and nothing is done twice; the answer is kept for seven days. |
| `PUT /api/runner/v1/artifacts/chunk` | `runner` | — | Uploads one chunk of a file. |
| `POST /api/runner/v1/artifacts/commit` | `runner` | — | Finishes a Cargo upload. |
| `POST /api/runner/v1/artifacts/download` | `runner` | — | Fetches a file of a Cargo for a step's `fetch:`. |
| `POST /api/runner/v1/caches/lookup` | `runner` | — | Asks for the entry a step with a `cache:` should restore: the exact key in the Sortie's own scope and then the default scope, or with `fallback: latest` the newest entry of the name and platform. Answers `{"entry"}` or nothing, with a `reason` when caching is off. |
| `POST /api/runner/v1/caches/begin` | `runner` | — | Declares an entry to save (name, key, platform, size, SHA-256). `{"exists": true}` when an entry with that key is kept already (entries are never changed) or being saved by another Sortie; `403` when the policy or the Sortie's scope forbids saving, `413` when the entry is over the limits or does not fit the project's quota. The entry's bytes count against the quota from here, ending the project's least recently used entries to make room (those of pull requests and branches first) and reported as `evicted`. |
| `PUT /api/runner/v1/caches/chunk` | `runner` | — | Uploads one chunk of an entry. It is its own route, not the Cargo', because an entry is one archive with rows and limits of its own. |
| `POST /api/runner/v1/caches/commit` | `runner` | — | Keeps an entry whose bytes have all arrived. |
| `POST /api/runner/v1/caches/download` | `runner` | — | Restores an entry the step's lookup may return, from an offset. |
| `POST /api/runner/v1/caches/discard` | `runner` | — | Ends an entry the Station proved damaged (its bytes are not what they were saved as, or not an archive Launchway made), so that the next Sortie saves a good one. Entries are never changed, so without it a damaged one would fail every restore until it fell idle. Only an entry the step's own lookup may return. |
| `POST /api/runner/v1/deploy/lease` | `runner` | — | A launch Station waits for launch or Return to base work for its targets. |
| `POST /api/runner/v1/deploy/watch` | `runner` | — | A launch Station learns which deployments to watch. |
| `POST /api/runner/v1/deploy/observe` | `runner` | — | A launch Station reports what it observes of them. |
| `POST /api/runner/v1/deploy/commands` | `runner` | — | A launch Station waits for people's deployment commands. |
| `POST /api/runner/v1/deploy/command-done` | `runner` | — | A launch Station reports how a command went. |
| `POST /api/runner/v1/deploy/verify/lease` | `runner` | — | A launch Station waits for a verification of its targets to run. |
| `POST /api/runner/v1/deploy/pull-request` | `runner` | — | A launch Station opens, or asks about, the pull request of a deployment that writes back by pull request, with the launch credential the server gave it for that deployment. |
| `POST /api/runner/v1/deploy/verify/interval` | `runner` | — | A launch Station reports one interval's readings of a verification; the answer says whether to go on. |
| `POST /api/runner/v2/join` | `public` | — | A Station enrols with a join token: `{"join_token", "public_key", "proof", "hostname", "facts"}`. Answers its `runner_id`, pool, kind and labels. The join token is checked in the handler; refusals are audited as `runner.refused`. |
| `POST /api/runner/v2/token` | `public` | — | An enrolled Station gets an access token (valid 15 minutes) for an assertion it signed with its key (valid 60 seconds, accepted once). A revoked or unknown Station, or a bad assertion, answers `401`; a skewed clock answers `401` with `server_time` and `skew: true`. |
| `POST /api/runner/v2/rotate` | `public` | — | An enrolled Station moves to a new key, proving it holds both. |

The request and response bodies of these routes are internal to Launchway and may change between releases; Stations and servers of mismatched versions are handled by the schema checks described in [Backup, restore and upgrade](../howto/backup-restore-upgrade.md).

## Configuration as code

Reports of Ansible and plan steps, the project's Configuration page, and the built-in Terraform/OpenTofu state. The tools run in step containers; these routes keep and serve what they produced. A step keeps its report files as the Cargo (artifact) `launchway-config`; the server reads them once, redacts them again, and stores the parsed report with the Sortie. State holds secrets in clear text by design of its format: it is sealed in the database, a server with no root key refuses it (`503` on the protocol, `409` elsewhere), and only `pull` returns it, with a reason.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/config-reports` | `run-viewer` | — | The Sortie's reports: `step`, `kind` (`ansible` or `plan`), `status`, `recap` (for Ansible `12 hosts: 9 changed, 3 ok, 0 failed`; in check mode `would change`), `check`, `environment`, `commit`. |
| `GET /api/v1/runs/{id}/config-reports/{step}` | `run-viewer` | — | One report with its `summary` (failure, hosts to run again, drift) and `document` (plays, tasks and hosts; or the plan's resources with attribute differences and risk). `?kind=` picks one when a step has both. No secret value is ever in it: `no_log` results, sensitive plan values and credential-like text are removed before it is stored. |
| `POST /api/v1/runs/{id}/config-reports/{step}/rerun-failed` | `run-operator` | `config.rerun_failed` | Starts a run of the same project and branch with the input `limit` set to the hosts that failed or were unreachable. `409` if none did. |
| `GET /api/v1/projects/{name}/configuration` | `project-viewer` | — | The Configuration page: per Airfield the last apply, the last check-mode run (drift), the last plan, and the state workspaces. |
| `GET /api/v1/projects/{name}/state` | `project-viewer` | — | The state workspaces: `name`, `serial`, `version`, `lock`; and whether this server can seal state (`sealed`). |
| `GET /api/v1/projects/{name}/state/{workspace}` | `project-viewer` | — | Resources (address, provider, instances) and output names (with `sensitive`) of the newest version, or of `?version=`. Never a value. |
| `GET /api/v1/projects/{name}/state/{workspace}/versions` | `project-viewer` | — | Every kept version (50 by default): serial, size, resource count, `source` (`write`, `push`, `rollback`), who, which run, when. |
| `GET /api/v1/projects/{name}/state/{workspace}/pull` | `project-operator` | `state.pull` | The raw state document. `?reason=` is required and goes to the Logbook with the caller; `?version=` picks a version. |
| `PUT /api/v1/projects/{name}/state/{workspace}/push` | `project-operator` | `state.push` | Stores a state document as the next version (an import or a repair). `409` for another lineage unless `?adopt=true`, or a serial older than the stored one. |
| `POST /api/v1/projects/{name}/state/{workspace}/lock` | `project-operator` | `state.lock` | Locks the workspace by hand: `{"reason"}`. `423` if it is locked. |
| `DELETE /api/v1/projects/{name}/state/{workspace}/lock` | `project-operator` | `state.unlock` | Releases a lock whoever holds it; `?reason=` is required. `409` while the Sortie that holds it is still going. |
| `POST /api/v1/projects/{name}/state/{workspace}/rollback` | `project-operator` | `state.rollback` | `{"version", "reason"}`: stores that version as the newest, with a higher serial. Changes the stored state only; no real resource changes. `423` while locked. |
| `GET /api/state/v1/{project}/{workspace}` | `public` | — | The Terraform/OpenTofu `http` backend: the current state, `404` if none. HTTP basic credentials: the password is a Sortie's state credential (`lwst_...`, one workspace of one project, until the Sortie ends) or an API token with the viewer role. |
| `POST /api/state/v1/{project}/{workspace}` | `public` | — | Stores the state (`?ID=` the lock id). `409` for an older serial, another lineage or the same serial with other content; `423` under another's lock; operator role or a read-write run credential. |
| `PUT /api/state/v1/{project}/{workspace}` | `public` | — | The same as `POST`. |
| `DELETE /api/state/v1/{project}/{workspace}` | `public` | — | Removes the workspace and its versions; refused while locked. |
| `LOCK /api/state/v1/{project}/{workspace}/lock` | `public` | — | Takes the lock with the client's lock document (an `ID`), audited as `state.lock`. `423` answers with the holder's document. A lock whose run has ended is released first. |
| `UNLOCK /api/state/v1/{project}/{workspace}/lock` | `public` | — | Releases the lock the client holds under its `ID`, audited as `state.unlock`. |
| `POST /api/runner/v1/config/state-credential` | `runner` | — | A Station (runner), under a lease, asks for the credential of one workspace of the lease's project: valid for that run only, read-only for pull request and draft runs. |

## What next

- [Roles and permissions](roles.md): who may call what.
- [Events and audit trail](events.md): the event log, the live stream and the audit actions.
- [Command-line reference](cli.md): the client built on this API.
