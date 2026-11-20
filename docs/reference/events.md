<!--
Machine-checked conventions (see internal/engine/docs_reference_test.go and
internal/api/docs_events_test.go):
- Every run event type appears as a table row starting "| `type` |" under "## Run events",
  for example "| `run.created` |". Every dotted name in that position must be a real event type.
- Every live stream message type appears as a table row starting "| `type` |" under "## Live stream".
- Every audit action appears as a table row starting "| `action` |" under "## Audit trail",
  for example "| `run.start` |". Every dotted name in that position must be a real action.
The tests fail when the code has a name this page does not list, or this page lists one the code
does not have. Keep each list in its own section, in that form.
-->

# Run events and audit trail

This page lists what Launchway records about runs and about people's actions: the run event log,
the messages of the live stream the web UI listens to, and the audit trail. It is for people who
read these records through the API, build tools on them, or review them for evidence.

> **Verified** on 2026-11-19 against commit f717337: a server started with `launchway server` on
> 127.0.0.1 without sign-in, running the demo project from `make demo`; a run was started, resumed
> after a failure and approved, and its event log, the live stream (all runs and one run) and the
> audit trail were read with `curl`; refused calls to an unconfigured webhook and Git hook were made
> to see their audit entries; then a server with OpenID Connect sign-in (a test provider) recorded
> `auth.sign_in`, `auth.sign_out`, `token.create`, a refused `token.use`, and allowed, denied and
> failed `run.*` and `policy.*` entries. **Not verified:** the deployment events (`deploy.*`) and the
> `deployment` stream message, which need Argo CD; `step.lost`, `step.reused`, `run.cancel_requested` and
> `step.rejected` were not produced in this session. Their descriptions come from the code.
>
> The verification events and the runner and explain audit actions were added when those features
> were merged; their descriptions come from the code and its tests, and were not produced by a live run.

## Run events

Every run has an append-only log of events. Nothing changes or deletes an event: a run's state is
whatever its events add up to, and every server works it out the same way. Each event has these
fields:

| Field | Meaning |
|---|---|
| `seq` | the event's number, increasing across the whole database (not per run) |
| `run_id` | the run, such as `run_06gzdc7n5e965qfptm1g` |
| `type` | one of the types below |
| `at` | when it was written, in Unix milliseconds |
| `data` | the payload; each type uses a few of its fields, and leaves the others out |

Each event type came with a database schema version (the **Schema** column). A server understands
the events of the schemas it knows; it leaves alone a run that has an event it does not know (see
[database migrations](../development/migrations.md)). An event may also carry `requires`, the
oldest schema whose servers understand it.

### Run lifecycle

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `run.created` | 1 | a run is created: by a push, a pull request, a schedule, a webhook, a person, a draft or a re-run | `project`, `number`, `pipeline` (the whole expanded pipeline), `trigger` (`event`, `ref`, `branch`, `tag`, `sha`, `actor`, `message`, `author`, `pull_request`, `inputs`, `schedule`, `scheduled_for`, `webhook`), `source` (`kind` git or dir, `url`, `libraries` with their commits), `draft` (`text`, `hash`, `secrets`) for a draft run, `rerun` (`of`, `of_number`, `from`) for a re-run, `pull_request_secrets` for a pull-request run |
| `run.cancel_requested` | 1 | someone asks to cancel the run, or a newer run supersedes it | `actor`; or `reason` (such as `superseded by run #12`) and `superseded_by` (`run`, `number`) |
| `run.resumed` | 1 | someone resumes a failed or cancelled run | `actor`; `draft_secrets` for a draft run (whether its steps may read secrets from now on) |
| `run.finished` | 1 | every step has ended | `status`: `succeeded`, `failed` or `canceled` |

### Step events

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `step.queued` | 1 | a step is ready and waits for a runner; again for each retry, after a lost runner, or when a lease is handed back | `step`, `attempt`, `reason` (`retry`, `requeued after: ...`, `handed back before it started`); for a deploy or rollback step `deploy` (`id`, `action` apply or revert) |
| `step.skipped` | 1 | a step will not run | `step`, `skip_kind` (`condition`: its `when` did not match; `upstream`: a step it needs failed; `not_needed` or `superseded` for deploy and rollback steps), `reason` |
| `step.leased` | 1 | a runner takes the step | `step`, `attempt`, `lease`, `runner`, and `inputs`: what the attempt was given apart from secret values (image, container, script, library commit, environment with where each value came from, template parameters, secret references) |
| `step.started` | 1 | the runner begins running it | `step`, `attempt`, `lease` |
| `step.finished` | 1 | the attempt ended | `step`, `attempt`, `lease`, `outcome` (`succeeded` or `failed`), `exit_code`, `outputs`, `reason` and `excerpt` (the last lines of output, masked) when it failed; summaries of what it reported: `tests`, `findings`, `sbom`, `artifacts`, and `image` for an image step |
| `step.lost` | 1 | the runner holding the step stopped answering and its lease expired | `step`, `attempt`, `reason`; it is then queued again, cancelled, or failed after too many tries |
| `step.canceled` | 1 | the run was cancelled while the step was waiting or running | `step`, `attempt`, `reason`; may carry the same report summaries as `step.finished` |
| `step.reset` | 1 | a resume makes a failed, cancelled or upstream-skipped step pending again | `step` |
| `step.reused` | 8 | a re-run does not run the step again, but takes its outcome and outputs from an earlier run | `step`, `outcome` (`succeeded` or `skipped`), `outputs`, `reused` (`run`, `number`, `attempt`, `decided_at`); for an approval also `actor` and `comment` |

### Approval events

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `step.waiting` | 4 | an approval step starts waiting for a person | `step`, `attempt`, `deadline` (when it is rejected if nobody decides, Unix milliseconds) |
| `step.approved` | 4 | a person approves it; the step succeeds | `step`, `attempt`, `actor`, `comment` |
| `step.rejected` | 4 | a person rejects it, nobody decides in time, or the deployment it names failed | `step`, `attempt`, `actor` (`launchway` when the server rejected it), `comment`, `reason` (such as `nobody decided within 24h`) |

### Deployment events

All of these are step events: `step` names the deploy, rollback or rollout step they belong to,
and `deploy` holds the details. None of them holds a credential.

| Type | Schema | Written when | Main fields in `data.deploy` |
|---|---|---|---|
| `deploy.waiting_lock` | 11 | a deploy step waits for its environment's lock | `environment`, `service`, `holder` (the deployment holding the lock: `deployment`, `run`, `number`, `step`, `version`) |
| `deploy.opened` | 11 | a deploy step takes the lock and opens a deployment | `id` (`dep_...`), `environment`, `service`, `target`, `application`, `mode`, `version`, `previous`, `deadline` |
| `deploy.action_done` | 11 | a deploy runner finished an action: commit and sync, or revert | `action` (`apply` or `revert`), `outcome`, `facts` (`repo`, `branch`, `commit`, `changed`, `files`, `revert`, `reverted`, `sync`, `abort`) |
| `deploy.observing` | 11 | the step waits for what deploy runners observe | `until` (such as `healthy`), `commit`, `deadline` |
| `deploy.phase` | 11 | what is observed moved to another phase | `phase`, `detail` |
| `deploy.closed` | 11 | every step of the deployment is done and the lock is released | `id`, `verdict`: `deployed`, `failed`, `rolled_back`, `rollback_failed` or `abandoned` |
| `deploy.command` | 14 | someone (or a step) asks for promote, promote-full, abort or retry on a rollout | `command` (`id`, `deployment`, `action`, `actor`, `comment`, `origin`, `expect`) |
| `deploy.command_withdrawn` | 14 | the command is withdrawn before a deploy runner took it | `command` (`id`, `actor`, `detail`) |
| `deploy.command_taken` | 14 | a deploy runner takes the command, until a time | `command` (`id`, `take`, `runner`, `until`); `runner` on the event |
| `deploy.command_done` | 14 | the command was applied, was already applied, was not applied, or failed | `command` (`id`, `result`: `applied`, `already_applied`, `not_applied` or `failed`, `detail`) |

### Policy events

| Type | Schema | Written when | Main fields in `data.policy` |
|---|---|---|---|
| `policy.evaluated` | 16 | policies are judged: at the run's start (also on a re-run or resume), before a deploy or rollout step acts, and before an image step that pushes is handed to a runner | `point` (`run`, `deploy` or `image`), `step`, `action`, `environment`, `at`, `verdict` (`pass`, `warn` or `block`), `policies` (each with the policy as judged, its verdict and its `rules`: `rule`, `verdict`, `found`, `want`, `evidence`, `waiver`), `input` (the facts judged), `waivers` (those considered) |

### Verification events

These are step events of a `kind: verify` step; see [Verifying a deployment](../guide/verification.md).
The step's `data.verify` holds the fields below.

| Type | Schema | Written when | Main fields in `data.verify` |
|---|---|---|---|
| `verify.started` | 19 | the verification's window begins | `environment`, `service`, `target`, `deployment`, `version`, `start`, `deadline`, `spec` (what is checked), `params` (as resolved), `baseline` (what it is judged against) |
| `verify.interval` | 19 | a deploy runner read one interval's metrics and logs | `interval`: each metric's readings and the log clusters seen |
| `verify.verdict` | 19 | the verification is decided: passed, failed or inconclusive | `verdict` (with the reason for each check), `explanation` (a model's, if one was asked; untrusted text), `pause` (it now waits for a person) |
| `verify.override` | 19 | a person marked a running or paused verification passed or failed | `outcome` (`passed` or `failed`); the person and the reason are the payload's `actor` and `comment` |

Every interval's data and the verdict are events, so any server derives the same verdict from the
log alone.

The decision is written in the same database transaction as what it allows, so it can be
reproduced from the log alone. `launchway policy run <run-id>` prints a run's decisions; the
[policy rules](policy-rules.md) page explains the rules.

### Reading the event log

`GET /api/v1/runs/{id}/events` returns a run's whole log as a JSON array, oldest first, with no
paging. Anyone who may see the run may read it (see [roles](roles.md)). Before sending it, the
server takes out what the UI shows elsewhere or what must not leave it:

- `pipeline` is removed from `run.created` (read the run's graph with `GET /api/v1/runs/{id}`);
- a draft run's `draft.text` is removed, leaving `hash` and `secrets` (the text is at
  `GET /api/v1/runs/{id}/pipeline-file`);
- `inputs` is removed from `step.leased` (the step view of `GET /api/v1/runs/{id}` has them);
- credentials are removed from addresses in `source`.

```sh
curl -s http://127.0.0.1:8080/api/v1/runs/run_06gzdc7n5e965qfptm1g/events
```

An excerpt of a real run of the demo project (a retry, a failed step, a resume and an approval;
lines shortened):

```json
[
  {"seq": 38, "run_id": "run_06gzdc7n5e965qfptm1g", "type": "run.created", "at": 1795140154667,
   "data": {"project": "demo-app", "number": 2,
            "trigger": {"event": "manual", "ref": "refs/heads/main", "branch": "main",
                        "sha": "fa0b04cb0a708ee44a88f003723019c8675c95ae", "actor": "dana",
                        "message": "Initial demo app", "inputs": {"scan": "quick", "alpine": "[\"3.21\",\"3.22\"]"}},
            "source": {"kind": "git", "url": "/srv/demo/launchway-demo"}}},
  {"seq": 39, "type": "policy.evaluated", "data": {"policy": {"point": "run", "verdict": "pass", "policies": ["..."]}}},
  {"seq": 40, "type": "step.queued", "data": {"step": "lint", "attempt": 1}},
  {"seq": 43, "type": "step.leased", "data": {"step": "lint", "attempt": 1, "lease": "lease_06gzdc7n8mkb3cm0bb00", "runner": "builtin-build-1-8080"}},
  {"seq": 44, "type": "step.started", "data": {"step": "lint", "attempt": 1, "lease": "lease_06gzdc7n8mkb3cm0bb00"}},
  {"seq": 53, "type": "step.finished", "data": {"step": "lint", "attempt": 1, "outcome": "succeeded", "exit_code": 0}},
  {"seq": 67, "type": "step.finished", "data": {"step": "integration-tests", "attempt": 1, "outcome": "failed", "exit_code": 1,
                                               "excerpt": "test_cache_survives_restart failed; the step will be retried"}},
  {"seq": 68, "type": "step.queued", "data": {"step": "integration-tests", "attempt": 2, "reason": "retry"}},
  {"seq": 80, "type": "step.skipped", "data": {"step": "approve-dev", "skip_kind": "upstream", "reason": "not run because smoke-test failed"}},
  {"seq": 82, "type": "run.finished", "data": {"status": "failed"}},
  {"seq": 89, "type": "run.resumed", "data": {"actor": "dana"}},
  {"seq": 90, "type": "step.reset", "data": {"step": "smoke-test"}},
  {"seq": 98, "type": "step.waiting", "data": {"step": "approve-dev", "attempt": 1, "deadline": 1795226620236}},
  {"seq": 99, "type": "step.approved", "data": {"step": "approve-dev", "attempt": 1, "comment": "looks good", "actor": "dana"}},
  {"seq": 104, "type": "run.finished", "data": {"status": "succeeded"}}
]
```

`seq` has gaps within a run because it counts every run's events. Step output is not in the event
log: read it with `GET /api/v1/runs/{id}/steps/{step}/logs`, or follow it on the live stream.

## Live stream

`GET /api/v1/stream` sends live changes as server-sent events (`Content-Type: text/event-stream`).
The web UI uses it to update pages without polling. It needs the same access as the rest of the
API: a session or an API token when sign-in is on.

- Without parameters it sends `run` messages for every run of the projects you may see.
- With `?run=<run-id>` it sends every message about that one run: `run`, `step`, `log` and
  `deployment`. A run you may not see answers `404`.

The stream starts with a reconnect delay and a comment, sends a `: ping` comment every 15 seconds,
and one `event:`/`data:` pair per message, the data being JSON:

| Type | Sent when | `data` |
|---|---|---|
| `run` | a run or any of its steps changed | the run summary, as in `GET /api/v1/runs`: `id`, `project`, `number`, `status`, `event`, `ref`, `branch`, `tag`, `sha`, `actor`, `created_at`, `started_at`, `finished_at`, `steps` (`id` and `status` each), `waiting` (approval steps waiting), and more |
| `step` | a step changed | the step view, as in `step_list` of `GET /api/v1/runs/{id}`: `run_id`, `id`, `status`, `attempt`, `exit_code`, `error`, `outputs`, `excerpt`, `inputs` and the step's definition |
| `log` | a step wrote output | `step`, `attempt`, `lines` (each `seq`, `at`, `stream` (`stdout`, `stderr` or `system`), `text`, already masked) |
| `deployment` | a deployment changed | the deployment, as in `GET /api/v1/deployments`: `id`, `run_id`, `step`, `environment`, `service`, `version`, `phase`, `verdict`, and more |

```sh
curl -sN 'http://127.0.0.1:8080/api/v1/stream?run=run_06gzdc7n5e965qfptm1g'
```

```text
retry: 2000
: connected

event: log
data: {"step":"lint","attempt":1,"lines":[{"seq":7,"at":1795140158024,"stream":"system","text":"Succeeded in 3.3s"}]}

event: step
data: {"run_id":"run_06gzdc7n5e965qfptm1g","id":"lint","position":0,"status":"succeeded","attempt":1,"exit_code":0,...}

event: run
data: {"id":"run_06gzdc7n5e965qfptm1g","project":"demo-app","number":2,"status":"running",...,"steps":[{"id":"lint","status":"succeeded"},...]}

: ping
```

A client that falls behind is cut off rather than slowing the server down: reconnect and fetch the
current state again (`retry: 2000` asks browsers to reconnect after two seconds). With sign-in on,
the session or token is checked again at every ping, and the stream ends when it is no longer
valid. When servers share a PostgreSQL database, each server publishes the changes the others make
as well, so a client may connect to any of them. A reverse proxy in front of Launchway must not
buffer this path.

## Audit trail

The audit trail records who did what: every call that changes something, allowed or refused, every
refused API token, every webhook call, and every sign-in and sign-out. Launchway only ever adds to
it. It never holds pipeline text, secrets, tokens or session IDs: a draft run is recorded with the
SHA-256 of its text, and a save with its branch and commit.

Calls are recorded with sign-in off too, as the configured `user`.

| Action | Recorded by |
|---|---|
| `run.start` | `POST /api/v1/projects/{name}/runs`: starting a run by hand (also `launchway runs start`) |
| `run.draft` | `POST /api/v1/projects/{name}/draft-runs`: starting a draft run from the pipeline editor |
| `pipeline.save` | `POST /api/v1/projects/{name}/pipeline`: saving a pipeline from the editor to a branch |
| `run.cancel` | `POST /api/v1/runs/{id}/cancel` |
| `run.resume` | `POST /api/v1/runs/{id}/resume` |
| `run.rerun` | `POST /api/v1/runs/{id}/rerun`: re-running from a step |
| `approval.approve` | `POST /api/v1/runs/{id}/steps/{step}/approve` |
| `approval.reject` | `POST /api/v1/runs/{id}/steps/{step}/reject` |
| `deployment.promote` | `POST /api/v1/runs/{id}/deployments/{dep}/promote` |
| `deployment.promote_full` | `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` |
| `deployment.abort` | `POST /api/v1/runs/{id}/deployments/{dep}/abort` |
| `deployment.retry` | `POST /api/v1/runs/{id}/deployments/{dep}/retry` |
| `deployment.withdraw` | `POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw` |
| `artifact.delete` | `DELETE /api/v1/runs/{id}/artifacts/{artifact}` |
| `token.create` | `POST /api/v1/tokens` |
| `token.revoke` | `DELETE /api/v1/tokens/{id}`, and an admin's `DELETE /api/v1/admin/tokens/{id}` |
| `token.use` | a request with an API token that is refused: malformed, unknown, revoked or expired, or not allowed by its limits or its owner's role on a call that has no action of its own |
| `step_resources.set_limits` | `PUT /api/v1/step-resources/limits` (also `launchway step-resources set-limits`); the detail holds the limits before and after |
| `step_resources.set_default` | `PUT /api/v1/step-resources/defaults/{kind}` (also `launchway step-resources set-default`); the detail holds the default before and after |
| `policy.waive` | `POST /api/v1/projects/{name}/waivers`: granting a waiver (also `launchway waivers grant`) |
| `policy.revoke_waiver` | `POST /api/v1/projects/{name}/waivers/{waiver}/revoke` |
| `webhook.git` | `POST /api/v1/projects/{name}/git-hook`: a Git host's call that made the project poll (`ok`) or could not prove itself (`denied`); ignored events and repeated deliveries are not recorded |
| `webhook.run` | `POST /api/v1/projects/{name}/webhooks/{trigger}`: a webhook trigger's call that started a run (`ok`), failed, or was refused |
| `step.explain` | `POST /api/v1/runs/{id}/steps/{step}/explain`: asking a model to explain a failed step; the detail names the model |
| `verification.override` | `POST /api/v1/runs/{id}/steps/{step}/verify/override`: marking a verification passed or failed, with the reason |
| `runner.join_token.create` | `POST /api/v1/runner-join-tokens`: making a join token (the detail says the pool, the uses and the expiry; the value is never recorded) |
| `runner.join_token.revoke` | `POST /api/v1/runner-join-tokens/{id}/revoke` |
| `runner.join` | `POST /api/runner/v2/join`: a runner enrolled with a join token |
| `runner.rotate` | a runner moved to a new key (`POST /api/runner/v2/rotate`, or `POST /api/v1/runners/{id}/rotate` asking it to) |
| `runner.revoke` | `POST /api/v1/runners/{id}/revoke`, or an ephemeral runner that left |
| `runner.refused` | a runner refused a step before running it, because its own `accept:` section does not allow it; the detail says why |
| `runner.lease_refused` | a runner asked for a lease it does not hold: the logs, results, artifacts or heartbeat of a step taken by another runner |
| `runner.auth` | a runner's request refused: an unknown, revoked or expired token or identity; the target and detail say which and why |
| `auth.sign_in` | `GET /auth/callback`: a sign-in that succeeded (with the groups the provider gave) or failed (with why) |
| `auth.sign_out` | `POST /auth/logout`, for a session that existed |

Each entry has these fields:

| Field | Meaning |
|---|---|
| `id` | the entry's number; newer entries have larger numbers |
| `at` | when, in Unix milliseconds |
| `user` | who: the user name, `git:<host kind>` for a Git hook, `webhook:<trigger>` for a webhook trigger, or empty for a sign-in that failed before anyone was known |
| `source` | the client's address (behind a reverse proxy, the one the proxy saw, with `server.trust_proxy: true`) |
| `action` | one of the actions above |
| `project` | the project, or empty |
| `target` | what was acted on: a run ID, `run/step`, a token's name, a waiver, or a webhook delivery |
| `outcome` | `ok`; `denied` when the permission check or the handler refused it with `403`; `failed` when the call was allowed but did not succeed |
| `detail` | a short description, at most 1000 bytes, such as `run #2 of main at fa0b04cb, inputs scan=quick`, or why it was refused or failed |

### Reading the audit trail

Admins of every project read the trail on the **Audit trail** page, or with
`GET /api/v1/audit`. Every parameter is optional:

- `user`, `project`, `action`: only entries with exactly this value;
- `from`, `to`: only entries with `from <= at < to`, in Unix milliseconds;
- `limit`: entries per page, 100 by default and at most 500 (a larger value gives 100);
- `before`: only entries with a smaller `id`; pass the previous page's `next_before`.

Entries come newest first, as `{"entries": [...], "next_before": N}`. `next_before` is `0` when the
page held fewer than `limit` entries; otherwise ask again with `before=N` (the next page may then be
empty).

```sh
curl -s 'http://127.0.0.1:8080/api/v1/audit?limit=2'
```

```json
{
  "entries": [
    {"id": 15, "at": 1795140269853, "user": "git:github", "source": "127.0.0.1", "action": "webhook.git",
     "project": "demo-app", "target": "", "outcome": "denied", "detail": "a Git webhook call that could not prove itself"},
    {"id": 14, "at": 1795140269848, "user": "webhook:nope", "source": "127.0.0.1", "action": "webhook.run",
     "project": "demo-app", "target": "", "outcome": "denied",
     "detail": "a call to a webhook the server's configuration does not list"}
  ],
  "next_before": 14
}
```

The trail is kept until you remove old rows from the `audit` table yourself. See the
[HTTP API](http-api.md) for the other endpoints and [roles](roles.md) for who may call them.
