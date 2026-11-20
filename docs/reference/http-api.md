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
| Sign-in on | a script | `Authorization: Bearer lw_…`, an [API token](#api-tokens). A request that sends an `Authorization` header is judged by the token alone, never by a cookie. |
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
| `503` | the server is shutting down, or the database did not answer | — |

## Pagination

Lists return at most a fixed number of items. Ask for fewer or more with `limit`, up to the maximum:

| Route | Parameter | Default | Maximum | A larger value gives |
|---|---|---|---|---|
| `GET /api/v1/runs` | `limit` | 50 | 500 | the default |
| `GET /api/v1/runs/{id}/steps/{step}/logs` | `limit`, `after` | 5000 | 10000 | the default |
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
| `GET /readyz` | `public` | — | The server can do useful work: the database answered within 2 seconds and the server is not shutting down. |

`GET /readyz` answers `200` with the database schema:

```json
{"schema": {"code": 21, "database": 21, "minimum": 7}, "status": "ok"}
```

It answers `503` with `status` `stopping` while the server drains before it stops, `unavailable` when the database did not answer, and `incompatible` when the database was migrated by a newer server that this one cannot work with. A runner started with `--health-listen` serves its own `/healthz` and `/readyz`; see [the command-line reference](cli.md#launchway-runner).

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
| `GET /api/v1/runs/{id}/rerun` | `run-viewer` | — | What re-running from `?from=<step>` would run again and reuse. |
| `POST /api/v1/runs/{id}/rerun` | `run-operator` | `run.rerun` | Starts a new run that re-runs a finished one from a step. Answers `201` with the new run. |

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

**Re-run.** `GET …/rerun?from=smoke-test` answers `{"run": ["smoke-test", …], "reuse": ["lint", …]}`, or `409` with why it cannot be re-run from there. `POST …/rerun` takes `{"from": "smoke-test"}`.

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
| `POST /api/v1/validate` | `any-viewer` | — | Checks pipeline text without running it. |

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
| `GET /api/v1/runs/{id}/deployments/{dep}/controls` | `run-viewer` | — | Which commands the caller could give a deployment now, and why not the others. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote` | `run-operator` | `deployment.promote` | Promotes a paused rollout one step. |
| `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` | `run-operator` | `deployment.promote_full` | Promotes a rollout fully, skipping its remaining steps. |
| `POST /api/v1/runs/{id}/deployments/{dep}/abort` | `run-operator` | `deployment.abort` | Aborts a rollout. |
| `POST /api/v1/runs/{id}/deployments/{dep}/retry` | `run-operator` | `deployment.retry` | Retries an aborted rollout. |
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
| `POST /api/runner/v1/logs` | `runner` | — | Appends log lines. |
| `POST /api/runner/v1/finish` | `runner` | — | How the step ended, with its outputs and report files (`413` if larger than the server accepts). |
| `POST /api/runner/v1/runs` | `runner` | — | The state of runs the runner holds work for (at most 5000 per call). |
| `POST /api/runner/v1/leave` | `runner` | — | The runner is shutting down. |
| `POST /api/runner/v1/artifacts/begin` | `runner` | — | Starts uploading an artifact. |
| `PUT /api/runner/v1/artifacts/chunk` | `runner` | — | Uploads one chunk of a file. |
| `POST /api/runner/v1/artifacts/commit` | `runner` | — | Finishes an artifact upload. |
| `POST /api/runner/v1/artifacts/download` | `runner` | — | Fetches a file of an artifact for a step's `fetch:`. |
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
