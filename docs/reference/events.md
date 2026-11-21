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

This page lists what Launchway records about runs and about people's actions: the Sortie (run) event log,
the messages of the live stream the web UI listens to, and the Logbook. It is for people who
read these records through the API, build tools on them, or review them for evidence.

> **Verified** on 2026-11-19 against commit f717337: a server started with `launchway server` on
> 127.0.0.1 without sign-in, running the demo project from `make demo`; a run was started, resumed
> after a failure and approved, and its event log, the live stream (all Sorties and one Sortie) and the
> audit trail were read with `curl`; refused calls to an unconfigured webhook and Git hook were made
> to see their audit entries; then a server with OpenID Connect sign-in (a test provider) recorded
> `auth.sign_in`, `auth.sign_out`, `token.create`, a refused `token.use`, and allowed, denied and
> failed `run.*` and `policy.*` entries. **Not verified:** the deployment events (`deploy.*`) and the
> `deployment` stream message, which need Argo CD; `step.lost`, `step.reused`, `run.cancel_requested` and
> `step.rejected` were not produced in this session. Their descriptions come from the code.
>
> The verification events and the Station (runner) and explain audit actions were added when those features
> were merged; their descriptions come from the code and its tests, and were not produced by a live run.

## Run events

Every run has an append-only log of events. Nothing changes or deletes an event: a Sortie's state is
whatever its events add up to, and every server works it out the same way. Each event has these
fields:

| Field | Meaning |
|---|---|
| `seq` | the event's number, increasing across the whole database (not per Sortie) |
| `run_id` | the Sortie, such as `run_06gzdc7n5e965qfptm1g` |
| `type` | one of the types below |
| `at` | when it was written, in Unix milliseconds |
| `data` | the payload; each type uses a few of its fields, and leaves the others out |

Each event type came with a database schema version (the **Schema** column). A server understands
the events of the schemas it knows; it leaves alone a Sortie that has an event it does not know (see
[database migrations](../development/migrations.md)). An event may also carry `requires`, the
oldest schema whose servers understand it.

### Run lifecycle

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `run.created` | 1 | a run is created: by a push, a pull request, a schedule, a webhook, a person, a draft or a re-run | `project`, `number`, `pipeline` (the whole expanded pipeline), `trigger` (`event`, `ref`, `branch`, `tag`, `sha`, `actor`, `message`, `author`, `pull_request`, `pull_request_into`, `inputs`, `schedule`, `scheduled_for`, `webhook`), `source` (`kind` git or dir, `url`, `libraries` with their commits), `draft` (`text`, `hash`, `secrets`) for a draft run, `rerun` (`of`, `of_number`, `from`, and `froms` when it starts from several steps; then the event carries `requires` 24) for a re-run, `pull_request_secrets` for a pull-request run |
| `run.cancel_requested` | 1 | someone asks to cancel the Sortie, or a newer run supersedes it | `actor`; or `reason` (such as `superseded by run #12`) and `superseded_by` (`run`, `number`) |
| `run.resumed` | 1 | someone resumes a failed or cancelled run | `actor`; `draft_secrets` for a draft run (whether its steps may read secrets from now on) |
| `run.finished` | 1 | every step has ended | `status`: `succeeded`, `failed` or `canceled` |
| `run.reviewed` | 32 | an approver acknowledges an Abort (emergency Return to base (rollback)) that needed review (see [Return to base (rollback) after success](../guide/rollback-after-success.md)) | `actor`, `comment` |
| `run.trusted` | 38 | a maintainer approved a restricted run (a pull request from an automation account or a fork): "Run with secrets" | `actor`, `comment` |
| `run.held` | 38 | a limit on concurrent Sorties (or an operator) holds the Sortie back when it is created, or an operator holds it: no step is queued | `queue` (`code`, `reason`, and `manual` for a hold an operator put on), `actor` |
| `run.admitted` | 38 | a held run may go: its lane is joined and its steps are queued; the time it waited does not count against its timeout | `actor` (set when an operator released or moved it) |
| `run.bumped` | 38 | an operator moved a held run to the front of the Holding pattern (`reason` `release` when a person's hold was lifted instead) | `actor`, `reason` |
| `run.lane_waiting` | 24 | a run of a pipeline with a [lane](pipeline.md#lane) arrives while another Sortie holds the lane and waits its turn: no step is queued | `lane` (`name`, `key`, `label`, and `run`, `number` of the run it waits behind) |
| `run.lane_started` | 24 | the Sortie took a free lane, or the lane was handed to it when the run before it ended (in the same transaction), or it took the lane again on a resume | `lane` (`name`, `key`, `label`) |
| `run.lane_replaced` | 24 | a newer run took this Sortie's place: a waiting run under `queue: newest`, or the holder and waiting runs under `when_busy: cancel`; followed by `run.cancel_requested` | `lane` (…, and `run`, `number` of the newer run) |
| `run.lane_skipped` | 24 | the lane was busy and says `when_busy: skip`: the run is canceled at once; followed by `run.cancel_requested` | `lane` (…, and `run`, `number` of the holder) |

### Step events

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `step.queued` | 1 | a step is ready and waits for a Station; again for each retry, after a lost Station, or when a lease is handed back | `step`, `attempt`, `not_before` (a retry that waits: when a Station may be given it, Unix ms), `reason` (`retry`, `retry in 20s`, `requeued after: ...`, `handed back before it started`); for a deploy or Return to base step `deploy` (`id`, `action` apply or revert) |
| `step.skipped` | 1 | a step will not run | `step`, `skip_kind` (`condition`: its `when` did not match; `upstream`: a step it needs failed; `not_needed` or `superseded` for deploy and Return to base steps), `reason` |
| `step.leased` | 1 | a Station takes the step | `step`, `attempt`, `lease`, `runner`, and `inputs`: what the attempt was given apart from secret values (image, container, script, library commit, Airfield (environment) with where each value came from, template parameters, secret references) |
| `step.started` | 1 | the Station begins running it | `step`, `attempt`, `lease` |
| `step.finished` | 1 | the attempt ended | `step`, `attempt`, `lease`, `outcome` (`succeeded` or `failed`), `exit_code`, `outputs`, `reason`, `excerpt` (the last lines of output, masked) and `cause` (`exit`, `timeout` or `infrastructure`) when it failed, and `allowed` when its pipeline tolerates the failure (`on_failure: continue`); summaries of what it reported: `tests`, `findings`, `sbom`, `artifacts`, `caches` (for each cache of a step with `cache:`: `name`, `key`, `result` of `hit`, `fallback`, `miss`, `saved` or `not_saved`, `restored`, `bytes`, `scope`, `reason`, `seconds`), and `image` for an image step |
| `step.lost` | 1 | the Station holding the step stopped answering and its lease expired | `step`, `attempt`, `reason`; it is then queued again, cancelled, or failed after too many tries |
| `step.canceled` | 1 | the run was cancelled while the step was waiting or running | `step`, `attempt`, `reason`; may carry the same report summaries as `step.finished` |
| `step.reset` | 1 | a resume makes a failed, cancelled or upstream-skipped step pending again | `step` |
| `step.reused` | 8 | a re-run does not run the step again, but takes its outcome and outputs from an earlier run | `step`, `outcome` (`succeeded` or `skipped`), `outputs`, `reused` (`run`, `number`, `attempt`, `decided_at`); for an approval also `actor` and `comment` |

### Approval events

| Type | Schema | Written when | Main fields in `data` |
|---|---|---|---|
| `step.waiting` | 4 | an approval step starts waiting for a person | `step`, `attempt`, `deadline` (when it is rejected if nobody decides, Unix milliseconds) |
| `step.approval_recorded` | 27 | one of several people approves an approval that asks for more than one approval (`approvers.count`, or an Airfield's `min_approvals`); the step goes on waiting until enough have | `step`, `attempt`, `actor`, `comment` |
| `step.approved` | 4 | a person approves it; the step succeeds | `step`, `attempt`, `actor`, `comment` |
| `step.rejected` | 4 | a person rejects it, nobody decides in time, or the deployment it names failed | `step`, `attempt`, `actor` (`launchway` when the server rejected it), `comment`, `reason` (such as `nobody decided within 24h`) |

### Deployment events

All of these are step events: `step` names the deploy, Return to base or rollout step they belong to,
and `deploy` holds the details. None of them holds a credential.

| Type | Schema | Written when | Main fields in `data.deploy` |
|---|---|---|---|
| `deploy.waiting_lock` | 11 | a deploy step waits for its Airfield's lock | `environment`, `service`, `holder` (the deployment holding the lock: `deployment`, `run`, `number`, `step`, `version`) |
| `deploy.opened` | 11 | a deploy step takes the lock and opens a deployment | `id` (`dep_...`), `environment`, `service`, `target`, `application`, `mode`, `version`, `previous`, `deadline` |
| `deploy.action_done` | 11 | a deploy Station finished an action: commit and sync, or revert | `action` (`apply` or `revert`), `outcome`, `facts` (`repo`, `branch`, `commit`, `changed`, `files`, `revert`, `reverted`, `sync`, `abort`) |
| `deploy.observing` | 11 | the step waits for what deploy Stations observe | `until` (such as `healthy`), `commit`, `deadline` |
| `deploy.phase` | 11 | what is observed moved to another phase | `phase`, `detail` |
| `deploy.closed` | 11 | every step of the deployment is done and the lock is released | `id`, `verdict`: `deployed`, `failed`, `rolled_back`, `rollback_failed` or `abandoned` |
| `deploy.command` | 14 | someone (or a step) asks for promote, promote-full, abort or retry on a rollout | `command` (`id`, `deployment`, `action`, `actor`, `comment`, `origin`, `expect`) |
| `deploy.command_withdrawn` | 14 | the command is withdrawn before a deploy Station took it | `command` (`id`, `actor`, `detail`) |
| `deploy.command_taken` | 14 | a deploy Station takes the command, until a time | `command` (`id`, `take`, `runner`, `until`); `runner` on the event |
| `deploy.command_done` | 14 | the command was applied, was already applied, was not applied, or failed | `command` (`id`, `result`: `applied`, `already_applied`, `not_applied` or `failed`, `detail`) |

### Policy events

| Type | Schema | Written when | Main fields in `data.policy` |
|---|---|---|---|
| `policy.evaluated` | 16 | policies are judged: at the Sortie's start (also on a re-run or resume), before a deploy or rollout step acts, and before an image step that pushes is handed to a Station | `point` (`run`, `deploy` or `image`), `step`, `action`, `environment`, `at`, `verdict` (`pass`, `warn` or `block`), `policies` (each with the policy as judged, its verdict and its `rules`: `rule`, `verdict`, `found`, `want`, `evidence`, `waiver`), `input` (the facts judged), `waivers` (those considered) |

### Verification events

These are step events of a `kind: verify` step; see [Verifying a deployment](../guide/verification.md).
The step's `data.verify` holds the fields below.

| Type | Schema | Written when | Main fields in `data.verify` |
|---|---|---|---|
| `verify.started` | 19 | the verification's window begins | `environment`, `service`, `target`, `deployment`, `version`, `start`, `deadline`, `spec` (what is checked), `params` (as resolved), `baseline` (what it is judged against) |
| `verify.interval` | 19 | a deploy Station read one interval's metrics and logs | `interval`: each metric's readings and the log clusters seen |
| `verify.verdict` | 19 | the verification is decided: passed, failed or inconclusive | `verdict` (with the reason for each check), `explanation` (a model's, if one was asked; untrusted text), `pause` (it now waits for a person) |
| `verify.override` | 19 | a person marked a running or paused verification passed or failed | `outcome` (`passed` or `failed`); the person and the reason are the payload's `actor` and `comment` |

Every interval's data and the verdict are events, so any server derives the same verdict from the
log alone.

The decision is written in the same database transaction as what it allows, so it can be
reproduced from the log alone. `launchway policy run <run-id>` prints a Sortie's decisions; the
[policy rules](policy-rules.md) page explains the rules.

### Reading the event log

`GET /api/v1/runs/{id}/events` returns a Sortie's whole log as a JSON array, oldest first, with no
paging. Anyone who may see the run may read it (see [roles](roles.md)). Before sending it, the
server takes out what the UI shows elsewhere or what must not leave it:

- `pipeline` is removed from `run.created` (read the Sortie's graph with `GET /api/v1/runs/{id}`);
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

`seq` has gaps within a run because it counts every Sortie's events. Step output is not in the event
log: read it with `GET /api/v1/runs/{id}/steps/{step}/logs`, or follow it on the live stream.

## Live stream

`GET /api/v1/stream` sends live changes as server-sent events (`Content-Type: text/event-stream`).
The web UI uses it to update pages without polling. It needs the same access as the rest of the
API: a session or an API token when sign-in is on.

- Without parameters it sends `run` messages for every run of the projects you may see.
- With `?run=<run-id>` it sends every message about that one Sortie: `run`, `step`, `log` and
  `deployment`. A Sortie you may not see answers `404`.

The stream starts with a reconnect delay and a comment, sends a `: ping` comment every 15 seconds,
and one `event:`/`data:` pair per message, the data being JSON:

| Type | Sent when | `data` |
|---|---|---|
| `run` | a Sortie or any of its steps changed | the Debrief (run summary), as in `GET /api/v1/runs`: `id`, `project`, `number`, `status`, `event`, `ref`, `branch`, `tag`, `sha`, `actor`, `created_at`, `started_at`, `finished_at`, `steps` (`id` and `status` each), `waiting` (approval steps waiting), and more |
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

The Logbook records who did what: every call that changes something, allowed or refused, every
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
| `run_trigger.create` | `POST /api/v1/projects/{name}/run-triggers`: a project now starts a run when another project's run or launch ends |
| `run_trigger.delete` | `DELETE /api/v1/projects/{name}/run-triggers/{trigger}`: a trigger on another project's results was removed |
| `run_trigger.allow` | `POST /api/v1/projects/{name}/run-trigger-allows`: the upstream project allowed another to trigger on its results (the detail says whether it may read step outputs) |
| `run_trigger.revoke` | `DELETE /api/v1/projects/{name}/run-trigger-allows/{downstream}`: that permission was withdrawn |
| `event_source.create` | `POST /api/v1/projects/{name}/event-sources`: an incoming event source was made; the signing secret was shown once and is never recorded |
| `event_source.change` | `POST /api/v1/projects/{name}/event-sources/{source}`: its settings changed, or its secret was replaced |
| `event_source.test` | `POST /api/v1/projects/{name}/event-sources/{source}/test`: a body was tried against a source; nothing started |
| `event_source.delete` | `DELETE /api/v1/projects/{name}/event-sources/{source}`: an event source was removed |
| `event_source.call` | a call to an event source's address: the user is `event-source:<name>`; a call that cannot prove itself is recorded as denied, and one that did is recorded with what became of it (started, filtered, duplicate...) |
| `event.replay` | `POST /api/v1/events/{id}/replay`: an event started again by an operator; the target is the event, the detail says what started |
| `run.queue` | `POST /api/v1/runs/{id}/queue`: run next, hold or release a Sortie that waits in a Holding pattern (the action is in the detail) |
| `run.trust` | `POST /api/v1/runs/{id}/trust`: a maintainer approving a restricted run, *Run with secrets* (the comment is in the detail) |
| `approval.approve` | `POST /api/v1/runs/{id}/steps/{step}/approve` |
| `approval.reject` | `POST /api/v1/runs/{id}/steps/{step}/reject` |
| `deployment.promote` | `POST /api/v1/runs/{id}/deployments/{dep}/promote`: the request only; the outcome is `deployment.command_outcome` |
| `deployment.promote_full` | `POST /api/v1/runs/{id}/deployments/{dep}/promote-full` |
| `deployment.abort` | `POST /api/v1/runs/{id}/deployments/{dep}/abort` |
| `deployment.retry` | `POST /api/v1/runs/{id}/deployments/{dep}/retry` |
| `deployment.command_outcome` | what a deploy Station did with a command it took (promote, promote full, abort, retry), written when it reports, apart from the entry for the request: `ok` for a command that got to the rollout, `failed` for one that did not, with the reason; the user is the Station |
| `deployment.rollback` | `POST /api/v1/projects/{name}/rollbacks`: the request, with the reason, and then the run it made |
| `rollback.review` | `POST /api/v1/runs/{id}/review`: an approver acknowledging an Abort, with the comment |
| `deployment.withdraw` | `POST /api/v1/runs/{id}/deployments/{dep}/commands/{cmd}/withdraw` |
| `artifact.delete` | `DELETE /api/v1/runs/{id}/artifacts/{artifact}` |
| `token.create` | `POST /api/v1/tokens` |
| `token.revoke` | `DELETE /api/v1/tokens/{id}`, and an admin's `DELETE /api/v1/admin/tokens/{id}` |
| `token.use` | a request with an API token that is refused: malformed, unknown, revoked or expired, or not allowed by its limits or its owner's role on a call that has no action of its own |
| `config.create` | `POST /api/v1/config/{kind}`: an object was created (the detail names it and its version) |
| `config.put` | `PUT /api/v1/config/{kind}/{obj}`: an object was created or replaced; `unchanged` when the same settings were sent again |
| `config.delete` | `DELETE /api/v1/config/{kind}/{obj}`: an object was deleted (it can be restored) |
| `config.restore` | `POST /api/v1/config/{kind}/{obj}/restore` |
| `config.check_repository` | `POST /api/v1/config/check/repository`: the server contacted (or refused to contact) an address a caller gave; the target is the address without credentials, the detail says whether it was readable or refused |
| `config.check_signin` | `POST /api/v1/config/check/signin`: the same for an identity provider's address |
| `config.apply` | `POST /api/v1/config/apply`: several changes made together; the detail says how many changed |
| `config.import` | `POST /api/v1/config/import` with `apply` (also `launchway config import --apply`); the detail holds the import's ID and the number of changes. The first start's import of the configuration file is recorded as an import, not in the Logbook |
| `step_resources.set_limits` | `PUT /api/v1/step-resources/limits` (also `launchway step-resources set-limits`); the detail holds the limits before and after |
| `step_resources.set_default` | `PUT /api/v1/step-resources/defaults/{kind}` (also `launchway step-resources set-default`); the detail holds the default before and after |
| `run_limits.set` | `PUT /api/v1/run-limits` (also `launchway run-limits set`); the detail holds the limits before and after |
| `cache.delete` | `DELETE /api/v1/projects/{name}/caches/{id}` and `DELETE /api/v1/projects/{name}/caches?name=` (also `launchway caches delete`); the target is the entry or the cache name and the detail says how much was ended |
| `standards.settings_set` | `PUT /api/v1/standards/settings` (also `launchway standards set`); the target is the level and scope, the detail says the version saved, and says plainly in capitals when Standing orders were switched off, with the reason |
| `finding.accept` | `POST /api/v1/projects/{name}/findings/{fingerprint}/accept` (also `launchway findings accept` and the MCP tool `accept_finding`); the target is the finding's fingerprint and the detail holds the decision, the end date and the reason |
| `finding.reopen` | `POST /api/v1/projects/{name}/findings/{fingerprint}/reopen` (also `launchway findings reopen`); the target is the finding's fingerprint |
| `cache_policy.set` | `PUT /api/v1/cache-policy` (also `launchway caches policy set`); the detail holds the policy before and after |
| `cache_policy.set_project` | `PUT /api/v1/projects/{name}/cache-policy`; the detail holds the policy before and after |
| `variable.set` | `PUT /api/v1/variables/{name}` (also `launchway variables set`); the target names the level, scope, name and Airfield. The variable's own history (who, old to new, a secret only as its reference) is kept apart from this trail |
| `variable.delete` | `DELETE /api/v1/variables/{name}` (also `launchway variables delete`) |
| `variable.lock` | `PUT /api/v1/variables/{name}/lock` (also `launchway variables lock`); the detail is the reason |
| `variable.unlock` | `DELETE /api/v1/variables/{name}/lock` (also `launchway variables unlock`) |
| `toggle.apply` | A toggles file applied; the detail is the reason |
| `toggle.approve` | A waiting change approved |
| `toggle.create` | A toggle made; the detail is the reason |
| `toggle.delete` | A toggle removed (it can be restored by reverting the change) |
| `toggle.evaluate` | A context tried against a toggle; the context is not recorded |
| `toggle.key_create` | An SDK key made; the target names the Airfield and the detail the kind and name, never the key |
| `toggle.key_revoke` | An SDK key revoked |
| `toggle.key_rotate` | An SDK key rotated; the old one works until its expiry |
| `toggle.lock` | A shared toggle locked at its level |
| `toggle.plan` | A toggles file compared with the live state |
| `toggle.reject` | A waiting change turned down |
| `toggle.revert` | A change reverted |
| `toggle.revert_since` | The toggle changes of an Airfield since a time reverted together, as when a Return to base is asked to take them back |
| `toggle.safeguards_set` | The safeguards changed at a level; turning one down is recorded with its reason |
| `toggle.schedule_cancel` | A scheduled change canceled |
| `toggle.segment_delete` | A segment removed |
| `toggle.segment_set` | A segment made or replaced |
| `toggle.set` | A toggle changed in an Airfield (made, or held for approval); the detail is the reason. The toggle's own history holds before and after |
| `toggle.unlock` | A lock removed |
| `toggle.update` | A toggle's definition changed |
| `toggle.withdraw` | A waiting change taken back by the person who asked |
| `cache_policy.clear_project` | `DELETE /api/v1/projects/{name}/cache-policy` |
| `registry.push` | a manifest was pushed to the built-in Depot (registry) over `/v2/` (the target is `repository:tag` or `repository@digest`, the detail names the run when a step pushed it); a push refused for lack of the operator role is recorded with outcome `denied` |
| `registry.pull` | a pull or read refused over `/v2/` (outcome `denied`); allowed pulls are not recorded one by one |
| `registry.delete` | an image was deleted (over `/v2/`, the API, the CLI or an MCP tool); the detail says how many manifests went with it (signatures, SBOMs); a refused delete is recorded with outcome `denied` |
| `registry.untag` | a tag was deleted and the image stays |
| `registry.retention` | a retention rule deleted images (the actor is the person who ran it, or `retention` for the Patrol (scheduled run)) |
| `registry.gc` | garbage collection removed blobs; the detail has the counts and bytes |
| `registry.quota` | a push was refused because the project or Wing is over its quota (outcome `denied`) |
| `registry.gc_run` | `POST /api/v1/registry/gc` (also `launchway registry gc`); the detail says dry run or collection |
| `registry.policy` | `PUT /api/v1/registry/policy` and `PUT /api/v1/projects/{name}/registry/policy` (also `launchway registry policy set`); the target is the level and its name |
| `registry.image_delete` | `DELETE /api/v1/projects/{name}/registry/image` (also `launchway registry delete`); the target is `repository@reference` |
| `registry.tag_delete` | `DELETE /api/v1/projects/{name}/registry/tag` (also `launchway registry delete --tag`) |
| `registry.retention_run` | `POST /api/v1/projects/{name}/registry/retention` (also `launchway registry retention --run`) |
| `repository.create` | `POST /api/v1/repositories`; the target is the repository's path, the detail its visibility |
| `repository.update` | `PATCH /api/v1/repositories/{owner}/{name}`; the detail lists what changed (visibility, default branch, archived, a rename) |
| `repository.delete` | `DELETE /api/v1/repositories/{owner}/{name}`; the repository moves to the trash |
| `mirror.create` | `POST /api/v1/repositories/{owner}/{name}/mirrors` (also `launchway repos mirror add`); the detail says the direction, the address and where the credential is read from, never the credential |
| `mirror.update` | `PATCH /api/v1/repositories/{owner}/{name}/mirrors/{id}`: paused, resumed, a new interval or prune setting |
| `mirror.delete` | `DELETE /api/v1/repositories/{owner}/{name}/mirrors/{id}`: a push mirror removed |
| `mirror.sync` | `POST /api/v1/repositories/{owner}/{name}/mirrors/{id}/sync`: a mirror run on demand; a failed run is recorded with its redacted error |
| `mirror.promote` | `POST /api/v1/repositories/{owner}/{name}/stop-mirroring` (also `launchway repos mirror stop`): the repository stopped being a mirror and takes pushes; the detail says whether a final sync ran first |
| `connection.create` | `POST /api/v1/connections` (also `launchway repos connection add`): a server to import from; the detail says its kind, address and where the credential is read from, never the credential |
| `signin.test_begin` | `POST /api/v1/signin/test` (the setup guide): an administrator started a test sign-in with an identity provider; the target is the provider's address. Nothing about how people sign in changes |
| `signin.test_result` | the second window's return from the provider (`GET /auth/signin-test`): the detail says whether the test sign-in worked, who the provider said signed in and which groups it sent; recorded for the administrator who started it |
| `encryption.root_key_copy` | `POST /api/v1/encryption/root-key-copy` (the setup guide): an administrator, after typing their password and a code again, took a recovery copy of the root key wrapped with a passphrase. The copy itself is never kept; this entry is how Launchway knows that one was taken, and when |
| `connection.delete` | `DELETE /api/v1/connections/{name}` |
| `repository.import` | `POST /api/v1/imports` (also `launchway repos import`); the target is the connection, the detail lists the repositories made or resumed and how each ended |
| `repository.verify` | `POST /api/v1/imports/verify` (also `launchway repos verify`); the detail says whether every ref was equal |
| `repository.file_write` | `PUT /api/v1/repositories/{owner}/{name}/files` (also `launchway repos put-file`): one commit made by the server; the detail names the branch, the path and the commit range |
| `repository.file_delete` | `DELETE /api/v1/repositories/{owner}/{name}/files` (also `launchway repos delete-file`): one commit made by the server that removes a file; the detail names the branch, the path and the commit range |
| `repository.branch_create` | `POST /api/v1/repositories/{owner}/{name}/branches` (also `launchway repos branches create`): a branch made by the server; the detail names the branch and the commit |
| `repository.branch_delete` | `DELETE /api/v1/repositories/{owner}/{name}/branches` (also `launchway repos branches delete`): a branch removed; the detail names the branch and the commit it was at, which brings it back |
| `archive.import` | `POST /api/v1/repositories/{owner}/{name}/archive/issues` (also `launchway repos import-archive`); the target is the repository, the detail the counts of issues and comments stored |
| `ssh_key.add` | `POST /api/v1/user/ssh-keys`; the target is the person, the detail the key's name and fingerprint |
| `ssh_key.delete` | `DELETE /api/v1/user/ssh-keys/{id}` |
| `deploy_key.add` | `POST /api/v1/repositories/{owner}/{name}/deploy-keys`; the detail is the key's name, fingerprint and whether it may push |
| `deploy_key.delete` | `DELETE /api/v1/repositories/{owner}/{name}/deploy-keys/{id}` |
| `repository.protect` | `PUT /api/v1/repositories/{owner}/{name}/protected-branches`; the detail lists the patterns |
| `pull.create` | `POST /api/v1/repositories/{owner}/{name}/pulls`; the detail: pull request #N from a branch into another |
| `pull.update` | `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}`; the detail: pull request #N and what changed |
| `pull.merge` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/merge`; the detail: pull request #N, the method, the new commit |
| `pull.revert` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/revert`; the detail: pull request #N and the pull request that reverts it |
| `pull.comment` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments`; the detail: pull request #N |
| `pull.comment_update` | `PATCH /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}`; the detail: pull request #N and the comment |
| `pull.comment_delete` | `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}`; the detail: pull request #N and the comment |
| `pull.suggestion_apply` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/comments/{id}/apply`; the detail: pull request #N and the new commit |
| `pull.review` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews`; the detail: pull request #N and the verdict |
| `pull.review_dismiss` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/reviews/{id}/dismiss`; the detail: pull request #N, the review and the reason |
| `pull.review_request` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests`; the detail: pull request #N and the people asked |
| `pull.review_unrequest` | `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/review-requests/{reviewer}`; the detail: pull request #N and the person taken off the list |
| `pull.queue` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/queue`; the detail: pull request #N, who queued it and its state |
| `pull.dequeue` | `DELETE /api/v1/repositories/{owner}/{name}/pulls/{number}/queue`; the detail: pull request #N |
| `pull.update_branch` | `POST /api/v1/repositories/{owner}/{name}/pulls/{number}/update-branch`; the detail: pull request #N and the new head |
| `pull.owners_check` | `POST /api/v1/repositories/{owner}/{name}/owners/check`; checks the text of an owners file and stores nothing |
| `repository.pull_settings` | `PUT /api/v1/repositories/{owner}/{name}/pull-settings`; the detail: the allowed methods and the default |
| `repository.merge_rules` | `PUT /api/v1/repositories/{owner}/{name}/merge-rules`; the detail: the patterns and what they require |
| `pull.auto_merge` | A pull request merged by auto-merge once its rules passed; made by the server, for the person who turned it on |
| `webhook.add` | A webhook added to a repository or an owner; the detail is its name, the address's host and the events, never the secret |
| `webhook.update` | A webhook changed, turned on or off, or its secret replaced (the detail says which, never the secret) |
| `webhook.delete` | A webhook deleted |
| `webhook.test` | A test delivery sent; the detail is the outcome |
| `webhook.redeliver` | A delivery sent again; the detail is the event and the outcome |
| `webhook.disable` | A webhook turned off automatically after its deliveries kept failing; the user is `system` and the detail carries the notice shown on the hook |
| `git.push` | A push over HTTP, to `/git/{owner}/{name}.git` or `/{owner}/{name}.git`, or over SSH; the user is the person (or `run <id>`, or `deploy key <name>`), the detail the token's name or `ssh key SHA256:...` and each ref with its old and new commit, the outcome `ok`, `failed` (it lost a race or did not commit) or `denied` (no push rights, or the repository is archived) |
| `policy.waive` | `POST /api/v1/projects/{name}/waivers`: granting a waiver (also `launchway waivers grant`) |
| `policy.revoke_waiver` | `POST /api/v1/projects/{name}/waivers/{waiver}/revoke` |
| `webhook.git` | `POST /api/v1/projects/{name}/git-hook`: a Git host's call that made the project poll (`ok`) or could not prove itself (`denied`); ignored events and repeated deliveries are not recorded |
| `webhook.run` | `POST /api/v1/projects/{name}/webhooks/{trigger}`: a webhook trigger's call that started a Sortie (`ok`), failed, or was refused |
| `step.explain` | `POST /api/v1/runs/{id}/steps/{step}/explain`: asking a model to explain a failed step; the detail names the model |
| `verification.override` | `POST /api/v1/runs/{id}/steps/{step}/verify/override`: marking a verification passed or failed, with the reason |
| `runner.join_token.create` | `POST /api/v1/runner-join-tokens`: making a join token (the detail says the pool, the uses and the expiry; the value is never recorded) |
| `runner.join_token.revoke` | `POST /api/v1/runner-join-tokens/{id}/revoke` |
| `runner.join` | `POST /api/runner/v2/join`: a Station enrolled with a join token |
| `runner.rotate` | a Station moved to a new key (`POST /api/runner/v2/rotate`, or `POST /api/v1/runners/{id}/rotate` asking it to) |
| `runner.revoke` | `POST /api/v1/runners/{id}/revoke`, or an ephemeral Station that left |
| `runner.refused` | a Station refused a step before running it, because its own `accept:` section does not allow it; the detail says why |
| `runner.lease_refused` | a Station asked for a lease it does not hold: the logs, results, Cargo (artifacts) or heartbeat of a step taken by another Station |
| `runner.auth` | a Station's request refused: an unknown, revoked or expired token or identity; the target and detail say which and why |
| `runner.identity_refused` | a Station with the shared or a pool token said it is an enrolled Station; only that Station, with its own identity, may say so. The request is refused with 403 and the detail names the claimed Station and the path |
| `auth.sign_in` | `GET /auth/callback`: a sign-in that succeeded (with the groups the provider gave) or failed (with why) |
| `auth.sign_out` | `POST /auth/logout`, for a session that existed |
| `auth.setup_now` | `POST /api/v1/auth/setup-now`: sign-in was asked for on an installation that ran with none; the servers' next start uses built-in accounts |
| `auth.local.password_changed` | `POST /api/v1/account/password`: someone changed their own password (their other sessions end) |
| `auth.local.second_factor_begin` | `POST /api/v1/account/second-factor`: someone started adding an authenticator |
| `auth.local.second_factor_added` | `POST /api/v1/account/second-factor/confirm`: the authenticator was proven with a code |
| `auth.local.second_factor_removed` | `DELETE /api/v1/account/second-factor` |
| `auth.local.recovery_codes_renewed` | `POST /api/v1/account/recovery-codes`: a new set; the old codes stopped working |
| `auth.sign_out_everywhere` | `POST /api/v1/account/sign-out-everywhere` |
| `user.invite` | `POST /api/v1/users`: an administrator invited a person (the target is the user name; the link is never recorded) |
| `user.reset` | `POST /api/v1/users/{name}/reset`: a one-time reset link was issued |
| `user.change` | `POST /api/v1/users/{name}`: a role change, an account turned off or on |
| `user.delete` | `DELETE /api/v1/users/{name}` |
| `user.sign_out` | `POST /api/v1/users/{name}/sign-out`: every session of that person ended |

Each entry has these fields:

| Field | Meaning |
|---|---|
| `id` | the entry's number; newer entries have larger numbers |
| `at` | when, in Unix milliseconds |
| `user` | who: the user name, `git:<host kind>` for a Git hook, `webhook:<trigger>` for a webhook trigger, or empty for a sign-in that failed before anyone was known |
| `source` | the client's address (behind a reverse proxy, the one the proxy saw, with `server.trust_proxy: true`) |
| `action` | one of the actions above |
| `project` | the project, or empty |
| `target` | what was acted on: a Sortie ID, `run/step`, a token's name, a waiver, or a webhook delivery |
| `outcome` | `ok`; `denied` when the permission check or the handler refused it with `403`; `failed` when the call was allowed but did not succeed |
| `detail` | a short description, at most 1000 bytes, such as `run #2 of main at fa0b04cb, inputs scan=quick`, or why it was refused or failed |

### Reading the Logbook

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

## Audit stream on standard output

Every audit entry is also written, right after it is saved to the database, as one JSON line on the
server's standard output, so a log collector can alert on it (for example on repeated sign-in failures)
without polling the API. It is on by default; `audit.stdout: false` in the configuration file turns it
off. The stream carries the same entries as the Logbook, in the same order, and nothing else.

| Field | Meaning |
|---|---|
| `time` | UTC time, RFC 3339 with milliseconds |
| `kind` | the audit action, for example `auth.local.sign_in` |
| `actor` | who acted; `system` when nobody did |
| `actor_kind` | `person`, `runner` or `system` |
| `organization` | the Wing the entry belongs to (`default`) |
| `project` | the project, or an empty string |
| `target` | what was acted on: a Sortie, a token name, a repository path |
| `outcome` | `ok`, `denied` or `failed` |
| `detail` | a short note |
| `source_addr` | the client's address |
| `request_id` | the `X-Request-Id` of the request, which the response also carries |

Secret values never appear: addresses lose their credentials, bearer tokens and values after
`password=`, `token=` or similar are replaced by `***`, and the secret and variable operations record
names only. Three sample lines:

```json
{"time":"2026-11-21T09:14:03.512Z","kind":"auth.local.sign_in","actor":"ada","actor_kind":"person","organization":"default","project":"","target":"","outcome":"failed","detail":"wrong password (3 in a row)","source_addr":"203.0.113.9","request_id":"req_5f0c2a91b7d3e4a6c81f0d22"}
{"time":"2026-11-21T09:15:41.077Z","kind":"user.change","actor":"ada","actor_kind":"person","organization":"default","project":"","target":"bob","outcome":"ok","detail":"bob: viewer to admin","source_addr":"198.51.100.20","request_id":"req_a1d9e07c3b5f46280de1c4b7"}
{"time":"2026-11-21T09:16:12.940Z","kind":"repository.delete","actor":"runner:builder-1","actor_kind":"runner","organization":"default","project":"","target":"platform/old-service","outcome":"denied","detail":"only an administrator may delete a repository","source_addr":"10.0.4.17","request_id":"req_0b6e83f2a4c15d97e3a8b610"}
```

## Audit entries of the key operations

These are written to the same audit trail by the keyring, not by an API route, so they have no route in the table above: by the server at start (as user `system`, source `server`) and by `launchway encryption` on the machine that has the root key. They name keys by version and never hold key material.

| Action | When |
|---|---|
| `encryption.key_created` | The installation's first key-encryption key was made under the root key, at the first start with one. Written by the server, as `system`; never holds key material |
| `encryption.migrated` | Stored secrets that were in the clear were sealed (and, after a data key rotation, re-sealed); the detail has the counts |
| `encryption.kek_rotated` | `launchway encryption rotate`: a new key-encryption key was made and the data keys re-wrapped; the target names its version |
| `encryption.dek_rotated` | `launchway encryption rotate --data`: new data key versions were made |
| `encryption.rewrap` | `launchway encryption rewrap`: the key-encryption keys are now wrapped by the configured root key (or a re-wrap failed) |
| `encryption.unseal` | A server could not unseal its keys with the configured root key and refused to start: outcome `failed`. The detail says which key and why, never the key |

## Servers' configuration state

Besides the actions above, each server writes an audit entry, action `config.server_state`, when it applies a stored configuration revision or cannot: the user is the server's name, the target is `revision N`, the outcome is `ok` or `failed`, and the detail is the reason. `GET /api/v1/config` shows each server's newest one, so an administrator sees which servers have applied the stored revision and which have not.
