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

> **Verified** on 2026-11-19 against commit f717337: a representative call of every group was made with `curl` against a local server on `127.0.0.1` without sign-in (health, projects, runs, logs, events, tests, findings, artifacts, policies, waivers, validate, the live stream, the audit trail, the CSRF refusal, the unknown-endpoint answer); then, with OpenID Connect sign-in on (a test provider), sign-in and sign-out, making API tokens (and the refusal to make one with a token, or with a role above the maker's), and the `401`, `403` and `404` refusals of each kind with tokens and session cookies; the route list is checked against the server's route table by a test. **Not verified:** deployment commands (they need Argo CD), image search with real images, the Git-host webhooks, and the runner API beyond a refused call.

The command line (`launchway runs`, `policy`, `waivers`) is a client of this API. See [the command-line reference](cli.md).

## Conventions

- **Base URL.** Every path is relative to the server, such as `https://launchway.example.com`. Only these paths exist: anything else under `/api/` answers `404` with `{"error": "no such API endpoint"}`, and any other path serves the web UI.
- **JSON.** Request and response bodies are JSON (`Content-Type: application/json`), except where a route says otherwise. Request bodies are limited to 16 MiB unless stated. A body that is not valid JSON answers `400`.
- **Times** are Unix milliseconds (`created_at`, `finished_at`, `expires_at`, …).
- **IDs** have a prefix: `run_…` for runs, `tok_…` for API tokens, `art_…` for artifacts, `wvr_…` for waivers, `dep_…` for deployments, `cmd_…` for deployment commands.
- **Path values** are URL-escaped. A step ID that contains `/` (a step from a template, such as `vars/show`) must be sent as `vars%2Fshow`; a looped step's brackets as `compat%5Balpine%3D3.21%5D` (or as they are, if your client allows).
- **Security headers.** Every response carries `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`.

## Authentication

How a request is authenticated depends on the server's `auth.mode` and on the route's [access class](#access-classes).

| Server | Caller | How |
|---|---|---|
| Sign-in off (`auth.mode: none`) | anyone | No credentials. Every request acts as the configured `user`, with every right. An `Authorization` header is ignored. |
| Sign-in on (`auth.mode: oidc`) | a browser | The `launchway_session` cookie set by [`GET /auth/callback`](#sign-in). |
| Sign-in on | a script | `Authorization: Bearer lw_…`, an [API token](#api-tokens); `Authorization: token lw_…` and HTTP Basic with the token as the password are accepted too, for clients written for other Git hosts. A request that sends an `Authorization` header is judged by the token alone, never by a cookie. |
| Either | a runner | `Authorization: Bearer <runner token>`: the shared runner token, a deploy runner pool's token, or the short-lived access token of an enrolled runner. Only the [runner API](#runner-api) accepts these. |
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

Every route belongs to one access class. The server checks it before the handler runs. A run or project the caller may not see answers `404`, exactly as if it did not exist; one the caller may see but not change answers `403`. See [Roles and permissions](roles.md) for how roles are worked out.

| Class | Who may call | Refusal |
|---|---|---|
| `public` | Anyone. The handler decides (health checks, sign-in, webhooks). | — |
| `runner` | A runner, with the shared runner token, a runner pool's token or an enrolled runner's access token. | `401` |
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

Some handlers check more after the class: deployment commands and approvals that name a deployment need the role on the environment, waivers need the role `policy.waivers.allow` names, saving with `force` needs admin on the project, and protected environments refuse the run's starter. Those refusals are `403`.

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
| `409` | the request conflicts with the current state (such as re-running a run that is still going) | an artifact upload may add `received` |
| `410` | a runner's lease has ended | — |
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
| `GET /api/v1/projects/{name}/tests` | `runs` (how many runs back) | 30 | 100 | the maximum |
| `GET /api/v1/projects/{name}/security` | `runs` (how many runs back) | 30 | — | — |
| `GET /api/v1/components` | `limit` | 100 | 500 | the maximum |
| `GET /api/v1/images` | `limit` | 50 | 500 | the maximum |
| `GET /api/v1/waivers` | `limit` | 200 | 500 | the default |
| `GET /api/v1/audit` | `limit`, `before` | 100 | 500 | the default |

`GET /api/v1/runs/{id}/events` has no paging: it returns the whole log.

Logs page by sequence number: pass the `seq` of the last line you have as `after`. The audit trail pages by entry ID: pass the `next_before` of the previous page as `before`. `next_before` is `0` when a page is shorter than `limit`; when the last page happens to be exactly `limit` long, the next call returns an empty page. Test cases and findings page by `offset`.

## Health

For load balancers and orchestrators. Neither needs credentials.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /healthz` | `public` | — | The process is up: `200 {"status": "ok"}`. |
| `GET /readyz` | `public` | — | The server can do useful work: the database answered within 2 seconds, it can be written (not only reached), and the server is not shutting down. |

`GET /readyz` answers `200` with the database schema:

```json
{"schema": {"code": 21, "database": 21, "minimum": 7}, "status": "ok"}
```

The answer also has `database`: its `kind`, `writable`, `standby` (PostgreSQL in recovery) and, on PostgreSQL when the database user may see them (the `pg_monitor` or `pg_read_all_stats` role), `replication` (standbys streaming and synchronous, lag; on a standby the WAL receiver and replay lag) and `archiving`.

It answers `503` with `status` `stopping` while the server drains before it stops, `unavailable` when the database did not answer, `read_only` when the database answers but cannot be written (a standby, or set read-only), so that a load balancer drains a server attached to it, and `incompatible` when the database was migrated by a newer server that this one cannot work with. A runner started with `--health-listen` serves its own `/healthz` and `/readyz`; see [the command-line reference](cli.md#launchway-runner).

## Sign-in

These routes exist only with `auth.mode: oidc`; with sign-in off they answer `404` with `sign-in is off on this server (auth.mode: none)`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /auth/login` | `public` | — | Starts sign-in: redirects (`302`) to the provider. `?next=/path` is where to return afterwards; only paths on this server are accepted. |
| `GET /auth/callback` | `public` | — | Where the provider returns. Checks the answer, sets the `launchway_session` cookie and redirects to `next`. A failure redirects to `/signin?error=<code>`. |
| `POST /auth/logout` | `public` | — | Ends the session on every server. Needs the [CSRF header](#csrf-header). Answers `204`. |

Sign-ins, failed sign-ins and sign-outs are recorded in the audit trail as `auth.sign_in` and `auth.sign_out`. See [Events and audit trail](events.md).

## Server and caller

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/info` | `public` | — | The server's version and schema, and whether sign-in is on. |
| `GET /api/v1/me` | `signed-in` | — | Who the caller is and what they may do. |
| `GET /api/v1/setup` | `public` | — | Whether this installation still waits for its first administrator, and when the setup code expires. |
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

`via` is `none` (sign-in off), `session` or `token`. `roles` has the caller's role on each project they have one on, after any [token limits](#api-tokens). `admin` says whether they may read the audit trail; `runners` whether they may see the runners.

## Projects

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects` | `signed-in` | — | The projects the caller may see. |
| `GET /api/v1/projects/{name}` | `project-viewer` | — | One project. |
| `GET /api/v1/projects/{name}/refs` | `project-viewer` | — | The project's branches and tags. |
| `GET /api/v1/projects/{name}/inputs` | `project-viewer` | — | The inputs the pipeline declares at `?ref=` (default: the default branch), for a start form. |
| `GET /api/v1/projects/{name}/schedules` | `project-viewer` | — | The schedules read from the default branch, when each fires next, recent firings, and the project's webhook triggers. |
| `GET /api/v1/projects/{name}/lanes` | `project-viewer` | — | The project's [lanes](pipeline.md#lane) that have a run going or runs waiting: for each, its `name`, `key` and `label`, the run that holds it (`holder`) and the runs waiting, in the order they start (`waiting`, each with its `position`). A run's own place is its `lane` block in the run JSON: `name`, `key`, `label`, `state` (`holding`, `waiting`, `replaced` or `skipped`), `position`, and `behind` (the run it waits behind, or that replaced it). `GET /api/v1/runs?lane=NAME` lists only runs in that lane. A waiting run is canceled with the run's cancel route. |

A project object has the configuration (`name`, `url` with credentials removed, `default_branch`, `poll_seconds`, `push`), the latest poll (`last_fetch`, `decisions`), its newest run as `last_run`, and `editor`, what the caller may do in the pipeline editor:

```json
"editor": {"drafts_allow": "operator", "may_draft": true, "draft_secrets": false, "may_save": true, "may_force": true}
```

A ref is `{"name", "kind" (branch or tag), "sha", "subject", "author", "committed_at"}`.

## Libraries

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/libraries` | `any-viewer` | — | The shared libraries pipelines may use, by `name`, with the time of the last fetch (`last_fetch`). |
| `GET /api/v1/libraries/{library}/templates` | `any-viewer` | — | A library's templates at `?version=` (default: its newest tag, else its default branch), with their parameters and outputs. `404` when no libraries are configured. |

## Runs

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /api/v1/projects/{name}/runs` | `project-operator` | `run.start` | Starts a run. Answers `201` with the run. |
| `GET /api/v1/runs` | `signed-in` | — | Runs of the projects the caller may see, newest first. |
| `GET /api/v1/runs/{id}` | `run-viewer` | — | One run in full. |
| `GET /api/v1/runs/{id}/events` | `run-viewer` | — | The run's event log. |
| `GET /api/v1/runs/{id}/pipeline-file` | `run-viewer` | — | The pipeline text the run ran. |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `run-viewer` | — | A step's log lines. |
| `POST /api/v1/runs/{id}/cancel` | `run-operator` | `run.cancel` | Cancels the run. |
| `POST /api/v1/runs/{id}/resume` | `run-operator` | `run.resume` | Resumes a failed or canceled run, from the steps that did not succeed. |
| `GET /api/v1/runs/{id}/rerun` | `run-viewer` | — | What re-running from `?from=<step>` (given once for each step) or `?failed=true` would run again and reuse. |
| `POST /api/v1/runs/{id}/rerun` | `run-operator` | `run.rerun` | Starts a new run that re-runs a finished one from a step, several steps or the failed ones. Answers `201` with the new run. |

**Start a run.** The body:

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

**A run.** `GET /api/v1/runs/{id}` adds to the summary: `step_list` (each step's `status`, `attempt`, `error`, `exit_code`, `outputs`, `runner`, times, and `inputs`: what the attempt was given, without secret values), `graph` and `levels` (the pipeline as run), `trigger_detail` (inputs, schedule, webhook), `tests`, `findings`, `artifacts`, `deployments`, `policy`, `reruns`, `source` (credentials removed from addresses) and `draft`.

**Events.** `GET /api/v1/runs/{id}/events` is the run's log of what happened, in order: `[{"seq", "run_id", "type", "at", "data"}, …]`. The first event's pipeline, each `step.leased` event's inputs, and a draft run's text are left out (other routes return them). See [Run events](events.md#run-events).

**Pipeline file.** `{"text": "...", "draft": false}`; for a draft run also `hash`; for a pipeline the server configured, `origin`.

**Logs.** `GET /api/v1/runs/{id}/steps/{step}/logs?attempt=1&after=0&limit=1000`. `attempt` `0` (the default) is the latest:

```json
{"attempt": 1, "lines": [{"seq": 1, "at": 1795140132325, "stream": "system", "text": "Runner build-1, executor container (podman)"}]}
```

`stream` is `stdout`, `stderr` or `system` (what Launchway itself says). Secrets are masked before lines reach the server.

**Cancel and resume.** No body. Answers `{"ok": true}`. Resuming a draft run needs the right to start draft runs.

**Re-run.** `GET …/rerun?from=smoke-test` answers `{"from": ["smoke-test"], "run": ["smoke-test", …], "reuse": ["lint", …]}`, or `409` with why it cannot be re-run from there. `from=` may be repeated, and `failed=true` asks for the smallest set of steps that covers every failed and canceled step. `POST …/rerun` takes `{"from": "smoke-test"}`, `{"from": ["lint", "unit"]}` or `{"failed": true}`; the new run's JSON has `rerun_from` (the first step) and, for several, `rerun_froms`. The command line has `launchway runs resume` and `launchway runs rerun`, and the MCP tool `retry_run` takes `from_step`, `from_steps` and `failed_only`.

## Approvals

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/approvals` | `signed-in` | — | Approval steps waiting for a decision, in the projects the caller may see. |
| `POST /api/v1/runs/{id}/steps/{step}/approve` | `run-operator` | `approval.approve` | Approves a waiting approval step. |
| `POST /api/v1/runs/{id}/steps/{step}/reject` | `run-operator` | `approval.reject` | Rejects it. |

The body is optional: `{"comment": "Looks good"}`. Answers `{"ok": true}`. An approval that names a deployment also needs the operator role on that environment. With sign-in on, the person who started the run may not approve it unless `approvals.allow_self` is `true` (`403`).

## Pipeline editor

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/pipeline` | `project-viewer` | — | The pipeline file at the tip of `?ref=` (default: the default branch). |
| `POST /api/v1/projects/{name}/pipeline` | `project-operator` | `pipeline.save` | Commits pipeline text to the project's repository, as the caller. |
| `POST /api/v1/projects/{name}/draft-runs` | `project-draft` | `run.draft` | Runs pipeline text that is not in Git against a branch. |
| `POST /api/v1/validate` | `any-viewer` | — | Checks pipeline text without running it. With `?project=NAME` it checks it for that project too, and a valid pipeline may come back with `warnings` (for example, a pull-request filter by target branch in a project with no forge connection). |

**Read.** `{"ref", "kind", "sha", "subject", "author", "committed_at", "path", "exists", "configured", "text"}`.

**Save.** The body is `{"base_ref", "base_sha", "pipeline", "message", "branch", "force"}`. The project needs `push:` in the configuration. `force: true` saves text that has problems and needs the admin role on the project. Answers `201` with where it was saved (`path`, `branch`, `sha`, …); `409` if the branch moved since `base_sha`; `502` if the push failed.

**Draft run.** `{"ref": "main", "pipeline": "<YAML text>", "inputs": {…}, "libraries": {…}}`. Answers `201` with the run. Its steps may read secrets only if `drafts.secrets` is `true`. The pipeline text may be at most the size a pipeline file may be (`413` otherwise).

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
| `GET /api/v1/runs/{id}/tests` | `run-viewer` | — | A run's test totals, step by step, with what stands out. |
| `GET /api/v1/runs/{id}/tests/cases` | `run-viewer` | — | A run's test results, failures first, then the slowest. |
| `GET /api/v1/runs/{id}/tests/cases/{case}` | `run-viewer` | — | One result in full, with its trace and captured output. |
| `GET /api/v1/projects/{name}/tests` | `project-viewer` | — | A project's tests over its last runs of a ref: totals, slowest, most failing, flaky. |
| `GET /api/v1/projects/{name}/tests/history` | `project-viewer` | — | One test's results in the project's last runs. |
| `GET /api/v1/runs/{id}/tests/compare` | `run-viewer` | — | A run's tests beside the previous run of the same ref: new failures, fixed tests and tests still failing. |
| `GET /api/v1/runs/{id}/tests/cases/{case}/attachments/{n}` | `run-viewer` | — | One attachment of a test result (a screenshot, video or trace), number `n` from 0. Supports `Range`, so videos can be played and sought. Served as a download or an image, never as a page. |
| `GET /api/v1/runs/{id}/coverage` | `run-viewer` | — | A run's coverage, step by step, with the previous run of its ref and the project's default branch to compare. |
| `GET /api/v1/runs/{id}/coverage/files` | `run-viewer` | — | A run's per-file coverage: `?step=`, `?q=`, `?sort=` (`path`, `lines` or `missed`), `?limit=` and `?offset=`. |
| `GET /api/v1/projects/{name}/coverage` | `project-viewer` | — | A project's coverage over its last runs of a ref (`?ref=`, `?runs=`). |
| `GET /api/v1/runs/{id}/reports` | `run-viewer` | — | A run's hosted HTML reports, each with a fresh signed link. |
| `GET /hosted/{link}/{path...}` | `public` | — | A file of a hosted HTML report. It takes no session: the signed `link`, scoped to one report for a few hours and given only to people who may see the run, is the permission. Served with a sandboxing `Content-Security-Policy`, and from the separate origin `reports.origin` when one is configured. |

`…/tests/cases` takes `?status=failed|passed|skipped|flaky`, `?q=` (searched in the name, suite, file and message; at most 200 bytes), `?step=` (with `?attempt=`), `?limit=` and `?offset=`. `{case}` is a result's numeric `id`. The project routes take `?ref=` (a branch, a full ref, or `all`; default the default branch) and `?runs=`. `…/tests/history` needs `?key=`, the `key` of a result. See [Test reports](../tutorials/tests-and-reports.md).

## Artifacts findings and SBOMs

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/artifacts` | `run-viewer` | — | The artifacts a run kept, with their files. |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/file` | `run-viewer` | — | Downloads one file, `?path=`. Supports `Range`. |
| `GET /api/v1/runs/{id}/artifacts/{artifact}/archive` | `run-viewer` | — | Downloads every file of the artifact as a zip archive. |
| `DELETE /api/v1/runs/{id}/artifacts/{artifact}` | `run-operator` | `artifact.delete` | Deletes a kept artifact. Answers with what was deleted. |
| `GET /api/v1/artifact-store` | `any-viewer` | — | Where this server keeps artifacts (`database`, `filesystem` or `s3`), why, and how full the store is. |
| `GET /api/v1/runs/{id}/findings` | `run-viewer` | — | A run's security findings. |
| `GET /api/v1/projects/{name}/security` | `project-viewer` | — | A project's findings over its last runs of a ref (`?ref=`, `?runs=`). |
| `GET /api/v1/components` | `signed-in` | — | The runs whose SBOMs list a component: `?name=` (exact, ignoring case), `?version=`. |

Downloads are sent as `application/octet-stream` (or `application/zip`) with `Content-Disposition: attachment`, `Cache-Control: private, no-store`, a sandboxing `Content-Security-Policy`, and, for a file, `ETag` and `X-Launchway-SHA256` with its SHA-256.

`…/findings` takes `?severity=` (comma-separated: `critical`, `high`, `medium`, `low`, `unknown`), `?tool=`, `?status=new|existing|fixed`, `?q=`, `?step=`, `?limit=` and `?offset=`. Its answer has `counts`, `statuses`, `steps` and the findings. Secrets found by scanners are never returned.

## Images

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/images` | `run-viewer` | — | What a run's image steps built: digest, tags, platforms, signature, provenance, SBOM and scan. `{"images": […]}` |
| `GET /api/v1/images` | `signed-in` | — | Which run built a digest (`?digest=`), the latest image of a repository (`?repository=host/path`, with `?tag=`), or a project's images (`?project=`). `{"hits": […]}` |

`GET /api/v1/images` needs one of `digest`, `repository` or `project` (`400` otherwise). See [Build and sign an image](../tutorials/build-and-sign-image.md).

## Deployments

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/environments` | `signed-in` | — | The configured environments that list a project the caller may see, with what is deployed in each. |
| `GET /api/v1/deployments` | `signed-in` | — | Deployments, newest first: `?environment=`, `?project=`, `?service=`, `?limit=`. |
| `GET /api/v1/launch-lines` | `signed-in` | — | Where each recent change is on its way, as stations worked out from a run's steps and deployments (commit, checks, tests, package, each environment, verified), each with a state and one plain sentence; a station that stands for several steps also has `parallel` (how many steps, how many can run together, how many are done, running, waiting, failed or skipped, and each step's state): `?project=`, `?limit=` (12, at most 50), or `?run=` for the line of one run (404 for a run of a project the caller may not see). Only projects the caller may see. |
| `GET /api/v1/settings/approvals` | `signed-in` | — | Whether the person who started a run may approve it here (`allow_self`), and whether people sign in. The approval cards use it to say exactly why someone cannot approve. |
| `GET /api/v1/runs/{id}/deployments/{dep}/controls` | `run-viewer` | — | Which commands the caller could give a deployment now, and why not the others. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote` | `run-operator` | `deployment.promote` | Promotes a paused rollout one step. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` | `run-operator` | `deployment.promote_full` | Promotes a rollout fully, skipping its remaining steps. |
| `POST /api/v1/runs/{id}/deployments/{dep}/abort` | `run-operator` | `deployment.abort` | Aborts a rollout. |
| `POST /api/v1/runs/{id}/deployments/{dep}/retry` | `run-operator` | `deployment.retry` | Retries an aborted rollout. |
| `GET /api/v1/projects/{name}/rollback` | `project-viewer` | — | What a service can be rolled back to in an environment after its deployment succeeded: `?environment=` (required), `?service=`. Answers the plan: what runs now (`current`), the lock holder if any (`holder`), and the `rollback` action as the server reports it (`available`, or `reason`, `instead` and `would_help`; `targets`, the earlier successful deployments, each with the deployments it reverts, its `consequence` and `suggestions`; `queueable`, `emergency`, `requires_approval`). Without the operator role on the project (and the environment) the action says so and offers no targets. See [rollback after success](../guide/rollback-after-success.md). |
| `POST /api/v1/projects/{name}/rollbacks` | `project-operator` | `deployment.rollback` | Rolls a service back: `{"environment", "service", "to", "reason", "queue"}`. `to` is a deployment ID or a version (default the previous successful deployment); `reason` is required; `queue` waits behind a deployment that holds the environment's lock. Answers `201` with the rollback run. Refused with the action's own `reason` (`409`) when the action is not available. Needs the operator role on the environment too. |
| `GET /api/v1/rollbacks/review` | `signed-in` | — | Emergency rollbacks of protected environments that no approver has acknowledged, newest first, each with `acknowledge`: whether the caller may, and if not why. Only projects the caller may see. |
| `POST /api/v1/runs/{id}/review` | `run-operator` | `rollback.review` | Acknowledges an emergency rollback: `{"comment"}` (required). Refused for the person who asked unless `approvals.allow_self` is `true`, and for anyone the environment's `reviewers` leave out. Answers `{"ok": true}`. |
| `POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw` | `run-operator` | `deployment.withdraw` | Withdraws a command that no deploy runner has taken yet. Answers `{"ok": true}`. |

The command routes take an optional `{"comment": "..."}` and answer `201` with the command; a deploy runner carries it out. They need the operator role on the deployment's environment too. On a `protected: true` environment, the person who started the run may not promote or retry (unless `approvals.allow_self` is `true`); anyone with the role may abort.

`…/controls` answers `{"environment", "protected", "controls": [{…, "available", "forbidden", "reason"}], "withdraw", "withdraw_reason"}`. See [Deploy with approval](../tutorials/deploy-with-approval.md).

## Policies and waivers

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/policies` | `any-viewer` | — | The rule catalogue, the policies in force with their sources and versions, and recent verdicts (`?recent=N` per policy, at most 100) of runs the caller may see. |
| `GET /api/v1/runs/{id}/policy` | `run-viewer` | — | A run's policy decisions, rule by rule, the waivers that could apply, and whether the caller may waive. |
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
| `GET /api/v1/config` | `any-viewer` | — | The kinds, the names of the levels, the units, the revision this server runs with, and whether the caller is an administrator. `available` is false on a server limited to a schema before 23, which uses its file |
| `GET /api/v1/config/history` | `admin` | — | The changes of every object, newest first: who, when, the action, what changed. `kind`, `name`, `import`, `before` and `limit` narrow and page it |
| `GET /api/v1/config/export` | `admin` | — | Every object as configuration text (YAML), for backup, review or Git |
| `POST /api/v1/config/import` | `admin` | `config.import` | `{"text", "prune", "apply", "digest"}`. Without `apply`, plans the import: what would be created, changed or (with `prune`) deleted. With `apply` and the plan's `digest`, makes exactly those changes, recorded as one import |
| `GET /api/v1/config/imports` | `admin` | — | The imports so far, the first start's import of the configuration file among them |
| `POST /api/v1/config/validate` | `admin` | — | `{"changes": [...]}`: checks changes as `apply` would make them and stores nothing. The answer is `{"valid", "problems"}` and lists every problem. The wizards call it as people answer |
| `POST /api/v1/config/check/repository` | `admin` | `config.check_repository` | `{"repo", "branch"}`: reads a repository with the server's own Git credentials and keeps nothing. Internal addresses and local paths are refused (`blocked`, `blocked_host`; see Egress in the server settings). Audited and limited to a few a minute per caller. Answers `ok`, a plain `problem`, the `default_branch`, the `branches` and whether the branch has a pipeline file |
| `POST /api/v1/config/check/signin` | `admin` | `config.check_signin` | `{"issuer", "client_id", "redirect_url", "client_secret_env", "groups_claim", "admin_groups", "admin_users"}`: checks the answers against the identity provider and returns the `auth:` block for the configuration file (`snippet`). Sign-in stays in the file |
| `POST /api/v1/config/apply` | `admin` | `config.apply` | `{"changes": [{"kind", "name", "action", "unit", "spec", "version", "comment"}]}`, at most 200: all or none. `action` is `put` (the default), `delete` or `restore`; `version` -1 means the object must not exist yet |
| `GET /api/v1/config/effective/{project}` | `admin` | — | What applies to a project and where each part comes from: the installation, the organization, a unit or the project |
| `GET /api/v1/config/{kind}` | `admin` | — | The objects of a kind; `?deleted=1` lists the deleted ones |
| `POST /api/v1/config/{kind}` | `admin` | `config.create` | `{"name", "unit", "spec", "comment"}`: creates an object; `409` if it exists |
| `GET /api/v1/config/{kind}/{obj}` | `admin` | — | One object: `spec`, `version` (also the `ETag`), `managed_by`, who created and last changed it, and `deleted` |
| `PUT /api/v1/config/{kind}/{obj}` | `admin` | `config.put` | Creates or replaces the object, declaratively: sending the same body twice changes nothing the second time (`changed` says so). `400` lists every problem the same checks as the configuration file find |
| `DELETE /api/v1/config/{kind}/{obj}` | `admin` | `config.delete` | Deletes the object; it is kept and can be restored. Refused if something still depends on it, if active runs use it (a project, an environment or a runner pool: the answer lists them; `{"force_in_use": true}` deletes it anyway, recorded in the object's history and the audit trail) or if it would leave the configuration invalid |
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
whose timeout is longer is refused when a run starts, when it is re-run or resumed (the limit may have been lowered since), and by `POST /api/v1/validate`, with the limit
in the message.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/encryption` | `admin` | — | Encryption at rest: `{"encrypted", "dump_alone_cannot_reveal_secrets", "root_key_holder": {"kind", "where", "strength", "reachable"}, "keys", "columns", "secrets_sealed", "secrets_remaining", "migration": {"state", ...}, "warnings", "advice", "not_yet_encrypted", "summary"}`. Never any key material. Read-only: the root key is a bootstrap setting held outside the database, and `launchway encryption` rotates or replaces it where the key is. `409` with code `no_root_key` is what a request for something that stores secrets (a webhook with a secret) gets on a server with no root key |
| `GET /api/v1/run-limits` | `signed-in` | — | `{"max_run_timeout", "updated_at", "updated_by"}`. An empty `max_run_timeout` is not set: pipelines may ask for up to `720h`. |
| `PUT /api/v1/run-limits` | `admin` | `run_limits.set` | Replaces the limits: `{"max_run_timeout": "48h"}`; empty or left out removes the limit. `400` for a duration that does not read or is outside `1m` to `720h`. |
## Build caches

A step's `cache:` key (see [the pipeline reference](pipeline.md#cache)) keeps directories between runs.
Runners look entries up, send them in chunks and restore them under a lease, with the routes in the
runner API below. The request never says which scope an entry is in: the server works the scope out
from the run the lease belongs to (`default` for the default branch, `ref:<ref>` for another branch or
tag, `pull:<n>` for a pull request), so a runner cannot read or write an entry its run's scopes do not
reach. People and agents see a project's entries with the rights they have on the project and delete them
as an operator; the policy (whether caches are kept, and how much) is an administrator's, in the
database, as the installation's default with an override for a project.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/projects/{name}/caches` | `project-viewer` | — | The project's entries, most recently used first: `id`, `name`, `key`, `scope`, `platform`, `bytes`, `files`, `status` (`kept` or `uploading`), `created_at`, `last_used_at`, `hits`, `pipeline`, `generation`, `sensitive`, and `run_id` and `run_number` of the run that wrote it; `totals` (`entries`, `bytes`, `quota_bytes`, `max_entry_bytes`, `idle_days`) and the `policy` that applies. |
| `DELETE /api/v1/projects/{name}/caches/{id}` | `project-operator` | `cache.delete` | Ends one entry; the next run that wanted it starts without it. Answers `{"ended", "bytes"}`; `404` for an entry that does not exist or is another project's. |
| `DELETE /api/v1/projects/{name}/caches` | `project-operator` | `cache.delete` | Ends every entry of the cache named by `?name=pip`, in every scope, key and platform. `404` if there is none. |
| `GET /api/v1/cache-policy` | `signed-in` | — | The installation's policy: `enabled`, `max_project_gib`, `max_entry_gib`, `idle_days`, `max_age_days`, and `source` (`default` until an administrator sets one). |
| `PUT /api/v1/cache-policy` | `admin` | `cache_policy.set` | Changes the installation's policy; a field left out keeps its value. `400` for a value out of range (an entry limit above the project's quota, idle days outside 1 to 3650). Audited with the policy before and after. |
| `GET /api/v1/projects/{name}/cache-policy` | `project-viewer` | — | The policy that applies to the project, with `source` `project` (its own override), `installation` or `default`. |
| `PUT /api/v1/projects/{name}/cache-policy` | `admin` | `cache_policy.set_project` | Overrides the policy for one project; a field left out keeps the value that applies to it now. |
| `DELETE /api/v1/projects/{name}/cache-policy` | `admin` | `cache_policy.clear_project` | Removes the override: the installation's policy applies again. |

## Variables

A variable is a name with a type, a value, an optional environment and an optional lock, set at a level: `installation`, `organization`, `unit` or `project`. A level and its scope (the unit's or the project's name) are given as `?level=&scope=` or in the body. The rule, the same sentence everywhere: **The nearest setting wins: what is given when a run starts, then the pipeline file's own `with:`, then the project, the squadron (unit), the wing (organization), the base (installation), then the template's own default; at the same level a variable set for the environment being deployed to beats the general one; and a lock set at a level beats everything below it.** Lists replace, never merge. Variables and locks never cross organizations. See the [guide](../guide/variables.md).

| Route | Who | Audit action | What it does |
|---|---|---|---|
| `GET /api/v1/variables` | `signed-in` | — | The variables at a level (`?level=&scope=&environment=&names=`) with what each inherits from above and whether this level may override it. `400` without a level; `404` for a project the caller cannot see |
| `PUT /api/v1/variables/{name}` | `signed-in` | `variable.set` | Sets a variable: `{level, scope, environment, type, of, value, description}`. Types: `string`, `number`, `boolean`, `list`, `map`, `secret` (a reference `{store, key, field}`, never text) and `connection` (a name, with `of`). A name keeps one type everywhere. Who: an operator of the project, or an admin of the unit's projects, or of every project for the organization and the installation. `409` when a lock above forbids it, naming the level and who can change it |
| `DELETE /api/v1/variables/{name}` | `signed-in` | `variable.delete` | Deletes a variable at a level (`?level=&scope=&environment=`); what it overrode applies again |
| `PUT /api/v1/variables/{name}/lock` | `signed-in` | `variable.lock` | Locks it: `{level, scope, environment, reason}`; a reason is required. Needs an admin at that level (of the project, at a project). Answers the variable and the nearer settings now ignored |
| `DELETE /api/v1/variables/{name}/lock` | `signed-in` | `variable.unlock` | Removes the lock |
| `GET /api/v1/projects/{name}/variables` | `project-viewer` | — | Every variable in effect for a project, optionally `?environment=`, each with its whole chain: the winning value, its level, each overridden value in order, the lock and why |
| `GET /api/v1/runs/{id}/variables` | `run-viewer` | — | The variables the run used, as recorded when it was created; a re-run shows the original's |

## Repositories (Git hosting)

Only with the installation setting `forge.hosting` on (Configuration, Installation settings; a change applies when each server next restarts); otherwise every route answers `404` with `{"error": "feature_off: ...", "code": "feature_off"}` and the repositories are kept. A repository is `<owner>/<name>`; a bindings' project patterns match that path (`jkhunter/*`).

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
| `POST /api/v1/repositories/{owner}/{name}/mirrors/{id}/sync` | `repo-operator` | `mirror.sync` | Runs the mirror now and answers with its status. `409` when it is already running; `502` with `{"error", "mirror"}` when the run failed (the mirror's `last_error` says why). A pull mirror whose source suddenly lists no branches or tags refuses to delete everything. |
| `POST /api/v1/repositories/{owner}/{name}/stop-mirroring` | `repo-admin` | `mirror.promote` | Makes the repository the primary: the pull mirror is deleted and pushes are accepted. `{"final_sync": true}` runs the mirror once more first, and changes nothing if that fails. `409` when it is not a mirror. |

### Importing repositories (Git hosting)

Getting existing repositories into Launchway, safely, before anything is switched over. A **connection** names the server they are on: its `kind` (`gitea` or `forgejo`, whose REST API lists the repositories; or `plain`, a list of addresses you give), its `base_url` and a credential reference (`{"env": "NAME"}` or `{"file": "/path"}`, never a value). An **import** makes each repository here with the same owner, name and visibility, copies every branch, tag and note, checks the copy ref by ref, and by default leaves it a pull mirror (the staged move). It can be run again: a repository already made as a mirror of the same address is resumed, one that exists in any other state is left alone, so an import never overwrites a repository that has content of its own. The connection's credential is sent only to the connection's own host; an internal address is refused unless `forge.mirror_allow` lists it (the refusal says so). All of these need the admin role on every project, and a server with `forge.hosting` off answers `404 feature_off`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/connections` | `admin` | — | The connections to import from: `name`, `kind`, `base_url`, `credential` (where it is read from) and `enabled`. |
| `POST /api/v1/connections` | `admin` | `connection.create` | `{"name", "kind", "base_url", "credential"}`. `400` for another kind, an address that is internal and not allowed, or a credential that is a value. |
| `DELETE /api/v1/connections/{name}` | `admin` | `connection.delete` | Removes a connection that no repository uses; Launchway's own is refused. |
| `POST /api/v1/imports` | `admin` | `repository.import` | `{"connection", "urls", "owner", "repository", "target_owner", "mirror" (default true), "interval_seconds", "username", "dry_run", "wait"}`. `owner` limits the listing to one user or organization of the source, `repository` (`owner/name`) to one; without them it brings in everything the credential can see. `dry_run` lists what would be created and changes nothing. `wait` copies and checks each repository before answering; without it the repositories are made and their mirrors left to run in the background. `mirror: false` makes each an ordinary repository once copied and checked (needs `wait`). Answers `{"dry_run", "items", "summary"}`; each item has `source`, `repository`, `url`, `visibility`, `action` (`create`, `resume`, `skip`), `state` (`planned`, `queued`, `verified`, `skipped`, `failed`), `reason`, `error` (credentials removed), `source_refs`, `hosted_refs`, `verified`, `differences` and `mirror_id`. One repository failing does not stop the others. |
| `POST /api/v1/imports/verify` | `admin` | `repository.verify` | `{"connection", "urls", "owner", "repository", "target_owner"}`. Proves, or fails to prove, that every repository of the source exists here with exactly the branches, tags and notes at exactly the commits the source has: `{"equal", "items", "summary"}`, each item `equal`, `different` (with the refs that differ), `missing` or `error`. It reads only. |

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
| `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests` | `repo-operator` | `pull.review_request` | Asks people to review: `{"reviewers": [...]}`. |
| `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests/{reviewer}` | `repo-operator` | `pull.review_unrequest` | Takes one person off the list of those asked to review (a review they already gave stays); `404` if they were not asked, `409` if the pull request is not open. Answers with the pull request. |
| `GET /api/v1/repositories/{owner}/{name}/compare` | `repo-viewer` | — | What a pull request from `?head=` into `?base=` (default: the default branch) would contain: commits, files, ahead and behind, mergeability, and the number of an open pull request between them. |
| `GET /api/v1/repositories/{owner}/{name}/pull-settings` | `repo-viewer` | — | The repository's merge settings: which methods are allowed, the default, delete the branch after a merge, dismiss stale approvals, the message templates. |
| `PUT /api/v1/repositories/{owner}/{name}/pull-settings` | `repo-admin` | `repository.pull_settings` | Sets them; what the request leaves out stays. At least one method must stay allowed. |
| `GET /api/v1/repositories/{owner}/{name}/merge-rules` | `repo-viewer` | — | The merge rules of the protected branches: approvals (count, role), required checks, up to date, resolved threads, no self-approval, administrator bypass. Also `actions`: `edit_rules` and `edit_settings` (available to administrators of the repository, with the reason when not) and `require_signed_commits` (never available yet), so an interface offers only what can work. |
| `PUT /api/v1/repositories/{owner}/{name}/merge-rules` | `repo-admin` | `repository.merge_rules` | Sets rules: `{"rules": [{"pattern", "required_approvals", "approval_role", "required_checks", ...}]}`. A rule protects its branch pattern. Signed commits are reserved and refused. |

### Webhooks

[Webhooks and events](../guide/webhooks.md) of one repository (`repositories/{owner}/{name}/hooks`, its administrators) or of an owner (`owners/{owner}/hooks`, an administrator): the same seven routes under each base. The secret is write-only: it is never in an answer, except one Launchway made because none was given, which the answer that creates the hook carries once as `secret`. A hook of another repository or owner answers `404`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/repositories/{owner}/{name}/hooks` | `repo-admin` | — | `{"items": [{"id", "scope", "repository", "name", "url", "events", "active", "has_secret", "failures", "disabled_reason", "created_at"}], "events": [...]}`. |
| `POST /api/v1/repositories/{owner}/{name}/hooks` | `repo-admin` | `webhook.add` | Adds one: `{"name", "url", "events", "active", "secret"}`; `events` are `push`, `create`, `delete`, `repository` (`pull_request` and `pull_request_review` are reserved). `201` with `{"hook", "secret", "secret_note"}`; `400` for an address that is not `http(s)://`, carries a password, or an unknown event. |
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

These routes do not use cookies. They take HTTP Basic (the password is a Launchway API token, the user name is ignored; a token alone as the user name works too), `Authorization: Bearer <token>`, `Authorization: token <token>`, or a run's read-only credential. A token in the query string is refused with `400`. Basic over plain HTTP is refused with `403` unless the listener is loopback or `forge.allow_insecure_http` is on. A failure answers `401` with `WWW-Authenticate: Basic`; a repository that does not exist, one the caller may not read and, without credentials, a private one are told apart by nobody. Anonymous clones work only on a repository whose visibility is `public`. A push to an archived or mirror repository answers `403`. With Git hosting off every route answers `404 feature_off`.

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /git/{owner}/{name}/info/refs` | `repo-viewer` | — | The ref advertisement (`?service=git-upload-pack`, or `git-receive-pack`, which needs push rights). The dumb protocol is not served. |
| `POST /git/{owner}/{name}/git-upload-pack` | `repo-viewer` | — | Fetch and clone. The request body is limited to 32 MiB. |
| `POST /git/{owner}/{name}/git-receive-pack` | `repo-operator` | `git.push` | Push. The body is limited by `forge.max_push_mib`; the audit entry names the refs and the token. |
| `GET /{owner}/{name}/info/refs` | `repo-viewer` | — | The same, at the address other Git hosts use. |
| `POST /{owner}/{name}/git-upload-pack` | `repo-viewer` | — | The same. |
| `POST /{owner}/{name}/git-receive-pack` | `repo-operator` | `git.push` | The same. |

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

## Runners

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runners` | `any-viewer` | — | The runners that have contacted the server: `id`, `name`, `executor`, `version`, `last_seen`, `online`, their pool, labels and whether they are enrolled (with the key's fingerprint and age), and for deploy runners their targets. |
| `GET /api/v1/runner-pools` | `any-viewer` | — | The runner pools (`runner_pools` in the configuration, and the default pool): name, kind, projects, events and whether the pool requires identity. |
| `GET /api/v1/runner-join-tokens` | `signed-in` | — | The join tokens that are still usable, never their values. Needs the role `runner_admin` names. |
| `POST /api/v1/runner-join-tokens` | `signed-in` | `runner.join_token.create` | Makes a join token for a pool: `{"pool", "labels", "description", "max_uses", "ttl", "ephemeral"}` (`ttl` such as `1h`, the default; at most 7 days). Answers `201` with the value (`lwj_…`) in `token`, shown once: only its hash is stored. Needs the role `runner_admin` names. |
| `POST /api/v1/runner-join-tokens/{id}/revoke` | `signed-in` | `runner.join_token.revoke` | Revokes a join token. |
| `POST /api/v1/runners/{id}/revoke` | `signed-in` | `runner.revoke` | Revokes an enrolled runner: every server refuses it from then on, and the steps it held are queued again. |
| `POST /api/v1/runners/{id}/rotate` | `signed-in` | `runner.rotate` | Asks an enrolled runner to move to a new key at its next request. |

Making and revoking join tokens, and revoking and rotating runners, need the role that `runner_admin` names on every project (admin by default); the handlers check it after the access class. See [Runners on other machines and clusters](../guide/runner-identity.md).

## Verifications and AI

| Route | Access | Audit | What it does |
|---|---|---|---|
| `GET /api/v1/runs/{id}/verifications` | `run-viewer` | — | A run's verifications: each interval's readings, the verdict and its reason in words, log clusters, and any explanation. |
| `GET /api/v1/verifications` | `signed-in` | — | Recent verifications of the projects the caller may see: `?environment=`, `?project=`, `?service=`, `?limit=`. |
| `GET /api/v1/runs/{id}/steps/{step}/verify/controls` | `run-viewer` | — | Whether the caller could mark the verification passed or failed now, and why not, in plain words. |
| `POST /api/v1/runs/{id}/steps/{step}/verify/override` | `run-operator` | `verification.override` | Marks a running or paused verification passed or failed, with a reason: `{"outcome", "reason"}` where `outcome` is `passed` or `failed`. On a protected environment the person who started the run may not mark it passed. |
| `GET /api/v1/health-sources` | `signed-in` | — | The health sources the caller's projects may use, by name, type and projects; never where they are or how they are reached. |
| `GET /api/v1/ai/models` | `signed-in` | — | The configured models the caller may use: name, limits and today's use, never endpoints or keys; and `server_can_explain`. |
| `GET /api/v1/runs/{id}/steps/{step}/explanations` | `run-viewer` | — | The explanations models gave of a failed step. |
| `POST /api/v1/runs/{id}/steps/{step}/explain` | `run-operator` | `step.explain` | Asks a configured model (`{"model"}`) to explain a failed step from its log tail, failed tests and findings, redacted. `409` when the server has no model or no key store for it. |

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
| `GET /api/v1/audit` | `admin` | — | The audit trail, newest first. |

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

Without parameters it sends `run` events (run summaries) of the projects the caller may see. With `?run=<id>` it sends only that run's changes, including its log lines. With sign-in on, the session or token is checked again at every ping, and the stream ends when it is no longer valid. A client that falls behind is disconnected: reconnect and fetch again. Proxies must not buffer this route (the server sends `X-Accel-Buffering: no`). The message types are listed in [Events and audit trail](events.md).

```sh
curl -N http://127.0.0.1:8080/api/v1/stream
```

## Webhooks

Webhook routes need no session or token. A caller proves itself with a secret the project's configuration names (an environment variable or a file). A caller that cannot prove itself, a project without the hook, and an unknown project all get the same answer, `404 {"error": "not found"}`. Refused calls count towards the [failure limit](#too-many-failures) and are recorded in the audit trail.

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

The body is JSON of at most 64 KiB. `Idempotency-Key: <up to 200 printable characters>` makes a repeated call answer `200` with the run the first call started, and the header `Idempotent-Replayed: true`. A new run answers `201` with the run. See [Pipeline reference](pipeline.md).

## Runner API

The routes under `/api/runner/v1/` are for Launchway runners only. People and scripts never call them. They are authenticated by `Authorization: Bearer <token>`: the shared runner token (`LAUNCHWAY_RUNNER_TOKEN`, or the token the server keeps in its database), or a deploy runner pool's own token (`runner_pools` in the server configuration). Any other token answers `401 {"error": "a valid runner token is required"}`. All runner connections are outbound from the runner to the server.

In outline, a runner long-polls for work (`lease`), says the step started (`start`), reports while it runs (`heartbeat`, `logs`; the answer says whether the step was canceled), uploads and fetches artifacts in chunks, and reports how the step ended (`finish`). A runner that stops says so (`leave`), so that others can take over its work at once. Deploy runners, which hold a pool's token, take only deploy and rollback work, watch the deployments of their targets and report what they observe, and carry out people's deployment commands. The shared token cannot take deploy work, and a pool's token can take nothing else (`403`).

| Route | Access | Audit | What it does |
|---|---|---|---|
| `POST /api/runner/v1/lease` | `runner` | — | Waits up to 50 seconds for a step to run; `204` if none. |
| `POST /api/runner/v1/start` | `runner` | — | The leased step has started. |
| `POST /api/runner/v1/heartbeat` | `runner` | — | The step is still running; answers whether it was canceled. A lost lease answers `410`. |
| `POST /api/runner/v1/logs` | `runner` | — | Appends log lines. With `offset` (how many lines of the step attempt came before the batch) it is safe to repeat: lines the server has are skipped, and a batch that starts beyond what the server has is answered `409` with `received`, the count it has. |
| `POST /api/runner/v1/finish` | `runner` | — | How the step ended, with its outputs and report files (`413` if larger than the server accepts). Safe to repeat: a result sent again for a lease that already finished answers success and records nothing more. |
| `POST /api/runner/v1/runs` | `runner` | — | The state of runs the runner holds work for (at most 5000 per call). |
| `POST /api/runner/v1/leave` | `runner` | — | The runner is shutting down. |
| `POST /api/runner/v1/artifacts/begin` | `runner` | — | Starts uploading an artifact. A call carrying an `Idempotency-Key` header (as do the begin and commit calls of artifacts and caches, the finish call and `deploy/command-done`) that is made again with the same key gets the first answer, marked `Idempotent-Replayed: true`, and nothing is done twice; the answer is kept for seven days. |
| `PUT /api/runner/v1/artifacts/chunk` | `runner` | — | Uploads one chunk of a file. |
| `POST /api/runner/v1/artifacts/commit` | `runner` | — | Finishes an artifact upload. |
| `POST /api/runner/v1/artifacts/download` | `runner` | — | Fetches a file of an artifact for a step's `fetch:`. |
| `POST /api/runner/v1/caches/lookup` | `runner` | — | Asks for the entry a step with a `cache:` should restore: the exact key in the run's own scope and then the default scope, or with `fallback: latest` the newest entry of the name and platform. Answers `{"entry"}` or nothing, with a `reason` when caching is off. |
| `POST /api/runner/v1/caches/begin` | `runner` | — | Declares an entry to save (name, key, platform, size, SHA-256). `{"exists": true}` when an entry with that key is kept already (entries are never changed) or being saved by another run; `403` when the policy or the run's scope forbids saving, `413` when the entry is over the limits or does not fit the project's quota. The entry's bytes count against the quota from here, ending the project's least recently used entries to make room (those of pull requests and branches first) and reported as `evicted`. |
| `PUT /api/runner/v1/caches/chunk` | `runner` | — | Uploads one chunk of an entry. It is its own route, not the artifacts', because an entry is one archive with rows and limits of its own. |
| `POST /api/runner/v1/caches/commit` | `runner` | — | Keeps an entry whose bytes have all arrived. |
| `POST /api/runner/v1/caches/download` | `runner` | — | Restores an entry the step's lookup may return, from an offset. |
| `POST /api/runner/v1/caches/discard` | `runner` | — | Ends an entry the runner proved damaged (its bytes are not what they were saved as, or not an archive Launchway made), so that the next run saves a good one. Entries are never changed, so without it a damaged one would fail every restore until it fell idle. Only an entry the step's own lookup may return. |
| `POST /api/runner/v1/deploy/lease` | `runner` | — | A deploy runner waits for deploy or rollback work for its targets. |
| `POST /api/runner/v1/deploy/watch` | `runner` | — | A deploy runner learns which deployments to watch. |
| `POST /api/runner/v1/deploy/observe` | `runner` | — | A deploy runner reports what it observes of them. |
| `POST /api/runner/v1/deploy/commands` | `runner` | — | A deploy runner waits for people's deployment commands. |
| `POST /api/runner/v1/deploy/command-done` | `runner` | — | A deploy runner reports how a command went. |
| `POST /api/runner/v1/deploy/verify/lease` | `runner` | — | A deploy runner waits for a verification of its targets to run. |
| `POST /api/runner/v1/deploy/verify/interval` | `runner` | — | A deploy runner reports one interval's readings of a verification; the answer says whether to go on. |
| `POST /api/runner/v2/join` | `public` | — | A runner enrols with a join token: `{"join_token", "public_key", "proof", "hostname", "facts"}`. Answers its `runner_id`, pool, kind and labels. The join token is checked in the handler; refusals are audited as `runner.refused`. |
| `POST /api/runner/v2/token` | `public` | — | An enrolled runner gets an access token (valid 15 minutes) for an assertion it signed with its key (valid 60 seconds, accepted once). A revoked or unknown runner, or a bad assertion, answers `401`; a skewed clock answers `401` with `server_time` and `skew: true`. |
| `POST /api/runner/v2/rotate` | `public` | — | An enrolled runner moves to a new key, proving it holds both. |

The request and response bodies of these routes are internal to Launchway and may change between releases; runners and servers of mismatched versions are handled by the schema checks described in [Backup, restore and upgrade](../howto/backup-restore-upgrade.md).

## What next

- [Roles and permissions](roles.md): who may call what.
- [Events and audit trail](events.md): the event log, the live stream and the audit actions.
- [Command-line reference](cli.md): the client built on this API.
