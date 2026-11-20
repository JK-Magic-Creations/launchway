# Launchway

A build and delivery platform for teams and enterprises, run as a shared service on Kubernetes or
on your own servers.

What Launchway is built around:

- **High availability from the ground up.** Several servers share one PostgreSQL database with no
  leader. Nothing is held in a server's memory, so losing one loses no work.
- **Durable runs.** Every change to a run is recorded as an event. A run survives crashes and
  restarts, and resumes from the step that failed.
- **Runners that only connect outbound**, in any network, cluster or cloud. Steps run in Kubernetes
  pods or containers, with named toolbox containers and services sharing a workspace.
- **Sign-in with your organisation's OpenID Connect provider**, roles per project, API tokens for
  scripts, and an audit trail of every change.
- **Secrets that never reach the server.** Pipelines hold references; the runner fetches values
  from Vault or OpenBao just before a step and masks them in output.
- **A delivery model, not only builds:** approvals today; environments, releases, promotion,
  verification and rollback are next.
- **Pipelines you can check and run anywhere**, with line-numbered errors before anything starts.
- **Shared pipelines as Git repositories.** Templates and the scripts they run live in one
  repository, released with Git tags; pipelines pin a tag, branch or commit, and every run records
  the commit it used.

It is early. What is described below works and is tested, including with several servers and with
real Kubernetes, PostgreSQL and OpenBao. Much of the rest is designed but not built yet (see
[Status](#status)). Turn on [sign-in](#sign-in-and-access) before anyone else can reach it.

## Running several servers

Point every server at the same PostgreSQL database and put them behind a load balancer:

```yaml
# launchway.yaml
listen: 0.0.0.0:8080
database: postgres://launchway:...@db.internal:5432/launchway
auth:
  mode: oidc   # see Sign-in and access
  # ...
```

`LAUNCHWAY_DATABASE` overrides the file. Each server exposes `/healthz` (the process is up) and
`/readyz` (the database answers, and the server is not shutting down). A starting server waits up
to `database_wait` (default `60s`) for the database. On SIGTERM a server reports not ready at
once, keeps serving for `server.drain_delay` (default `0s`) so that load balancers checking
`/readyz` move away, and then gives requests in flight up to 15 seconds. Runners may connect to
any server. With no `database` setting a
single server keeps its data in an embedded SQLite file, which suits development and small
installs. Sessions, API tokens and sign-ins in progress are kept in the database too, so a browser
or script may be sent to any server.

## Run it on Kubernetes

`make image` builds a container image, and the Helm chart in `deploy/helm/launchway` runs
several servers against your PostgreSQL database, with runners that execute steps in pods in a
namespace of your choice:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set database.existingSecret=launchway-database
```

See [deploy/README.md](deploy/README.md) for the required values, a high-availability example,
upgrades, and security notes.

## Try it on one machine

You need Go 1.27+, Node 24+, Git, and Podman or Docker.

```sh
make run
```

`make run` does four things:

- builds the UI and the binary;
- creates a demo repository at `../launchway-demo`, and a demo [shared library](#shared-libraries)
  at `../launchway-demo-library` with two repositories that use it;
- starts the server at **http://127.0.0.1:8080**;
- watches four projects: the demo repository, the two that use the library, and Launchway itself.

Things to try:

- **Projects → demo-app → Start a run.** The branch list is read from the repository as you open it.
- **Watch the run live.** The steps appear as a graph and the logs stream in.
- **Resume a failed run.** The demo's `smoke-test` fails on its first attempt on purpose while a
  file called `FLAKY` exists. Press *Resume from smoke-test*. Only that step and the ones after it
  run again; the earlier results are kept.
- **Re-run from a step.** On a finished run, open any step and press *Re-run from this step*. A new
  run starts at the same commit: that step and the ones after it run again, and the steps before
  it are reused with their outputs. Each step's *Inputs* say what it was given, and from where.
- **Approve a deployment.** On `main`, the demo waits for approval before `deploy-dev`. Open
  **Approvals**, or the run, and approve or reject it.
- **Push to trigger a run.** Commit to `../launchway-demo` on `main` or `feature/*` and push. A run
  starts within a few seconds, and *Recent pushes* on the project explains every decision.
- **Survive a restart.** Stop the server while a step is running and start it again. The step is
  run again, and nothing that had finished runs twice.
- **Edit a pipeline.** *Projects → demo-app → Edit pipeline* opens the file from a branch. Change
  it, press *Run this draft* to try the change without saving it, then *Save…* to commit it (the
  example configuration lets the demo be saved; see [Editing pipelines](#editing-pipelines)).
- **Open the Playground.** Write a pipeline and watch it validate and draw as you type.
- **Use a shared library.** *demo-lib-app* runs the library's `release` template at tag `v1`:
  its steps are framed as one group on the graph, and the run says which commit `v1` was. Start a
  run with *Advanced → library override* `platform` = `main` to try the library's unreleased work.
  *demo-plain* has no pipeline file at all; the server runs the library's `check` template for it.
- **Run locally.** In any repository with a `.launchway/pipeline.yaml`, run `bin/launchway run`. It
  uses the same engine, on your machine, against your working copy.

## A pipeline

```yaml
# .launchway/pipeline.yaml
pipeline: my-service
on:
  push: { branches: [main, "release/*"] }
  pull_request: {}
supersede: true

steps:
  test:
    image: golang:1.27
    run: go test ./...
    retry: 1

  build:
    image: alpine:3.22
    needs: test
    run: |
      echo "building $LAUNCHWAY_COMMIT"
      echo digest=sha256:... >> "$LAUNCHWAY_OUTPUTS"

  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "deploying $LAUNCHWAY_NEEDS_BUILD_DIGEST"
```

| Key | Meaning |
|---|---|
| `description` | One line saying what the step is for, shown under its name and on the graph |
| `image` | Container image the step runs in |
| `run` | Shell script, run with `sh -e` in the repository checkout |
| `needs` | Steps that must succeed first; steps with no unmet needs run in parallel |
| `when` | `branch`, `tag` and `event` (`push`, `manual`, `draft`, `pull_request`, `schedule`, `webhook`) patterns, and `input` with `equals` (see [Run inputs](#run-inputs)); the step is skipped if they do not match. `upstream` says how the steps it needs must have ended; see [Cleanup and notification steps](#cleanup-and-notification-steps). In a [draft run](#editing-pipelines), a step with a `when` runs only if its `event` names `draft`. A [pull-request run](#pull-requests) has no branch, so `when: { branch: main }` never matches it |
| `env` | Extra environment variables |
| `secrets` | Environment variables whose values a runner looks up; see [Secrets](#secrets) |
| `container` | A named container from the `containers` section, instead of `image` |
| `retry` | Extra attempts after a failure |
| `timeout` | Per attempt; default 1h, at most 24h. For an approval, how long to wait for a decision; default 24h, at most 30 days |
| `reports` | `tests:` one glob or a list, relative to the workspace: test result files read once the step ends; see [Test reports](#test-reports) |
| `kind` | `step` (the default), `approval` (see [Approvals](#approvals)), `image` (see [Container images](#container-images)), or `deploy`, `rollback` and `rollout` (see [Deployments](#deployments)) |
| `message` | For an approval: the question to decide, one line of at most 500 characters |
| `uses` | `library/template`: the step is replaced by a template's steps; see [Shared libraries](#shared-libraries) |
| `with` | Values for the template's parameters; a value may be `{ input: name }`, a [run input](#run-inputs) |
| `for_each` | Repeats the step over lists; with `exclude`, `max_parallel` and `fail_fast`. See [Repeating a step over lists](#repeating-a-step-over-lists) |

At the top level, beside `pipeline`, `on`, `containers` and `steps`, `libraries` names the
[shared libraries](#shared-libraries) the pipeline's templates come from, and `supersede: true`
lets a new run [cancel the older runs](#superseding-older-runs) of the same branch or pull
request, and `inputs` declares the [values a run is started with](#run-inputs). `on` takes `push`
(with `branches` and `tags` patterns), `pull_request: {}`, [`schedule`](#schedules) and
[`webhook`](#webhooks).

### Containers and services

Name the toolboxes a pipeline needs once, then use them by name. Every container in a run shares
the workspace. Services run beside the steps for the whole run and are reachable at `localhost`.

```yaml
containers:
  maven:    { image: maven:3.9-eclipse-temurin-21, memory: 2Gi, cpu: "1" }
  dotnet:   { image: mcr.microsoft.com/dotnet/sdk:9.0 }
  gcloud:   { image: gcr.io/google.com/cloudsdktool/google-cloud-cli:stable }
  postgres: { image: postgres:17, service: true, env: { POSTGRES_PASSWORD: test } }

steps:
  build:  { container: maven,  run: mvn -B package }
  test:   { container: dotnet, run: dotnet test, needs: build }
  deploy: { container: gcloud, run: gcloud run deploy ..., needs: test, when: { branch: main } }
```

On Kubernetes, a run is one pod: a container per toolbox plus the services, so steps start
instantly once the pod is up. Its containers run as an ordinary user, not root, unless the
runner's operator chose otherwise (see [Commands](#commands)). On Podman, services run in a pod
per run. Plain `image:` steps still work, and each image used that way gets a container of its
own.

The workspace lives on one runner, so all of a run's steps go to the runner that took its first
step, for as long as that runner keeps in touch with the server. The cost is that a run's parallel
steps share that one runner's concurrency; different runs still spread across runners. If the
runner stops responding for 90 seconds (three lease periods), or shuts down, another runner takes
the run over and carries on in a fresh checkout: files that earlier steps wrote are then gone, and
the step's log says so. Values passed as outputs (below) always survive.

Steps write `key=value` lines to `$LAUNCHWAY_OUTPUTS`. Steps that need them receive
`LAUNCHWAY_NEEDS_<STEP>_<KEY>`. Every step also gets `LAUNCHWAY_COMMIT`, `LAUNCHWAY_BRANCH`,
`LAUNCHWAY_EVENT`, `LAUNCHWAY_REF`, `LAUNCHWAY_RUN_NUMBER` and related variables, and the steps of
a pull-request run get `LAUNCHWAY_PULL_REQUEST`, the pull request's number.

Mistakes are reported with line numbers, before anything runs:

```
$ launchway validate
.launchway/pipeline.yaml:14: steps.build.needs: needs "tests", but there is no step with that name
```

When a step fails, its last few lines of output (on stderr if it wrote any there, else on stdout,
with secrets masked as in the log) are kept with the step and shown where the run says it failed,
so "exit code 1" comes with its reason.

### Pull requests

`on: { pull_request: {} }` runs the pipeline for pull requests. There are no webhooks yet:
Launchway polls, as it does for pushes. Git hosts such as GitHub, Gitea and Forgejo publish each
pull request's head commit as the ref `refs/pull/<number>/head`; the server's mirror fetches it
with everything else, and a run starts when one appears or moves.

- **What runs.** The pipeline at the pull request's head commit, at that commit. The run is
  marked *pull request #12* wherever runs are shown, its event is `pull_request`, and its steps
  get `LAUNCHWAY_EVENT=pull_request`, `LAUNCHWAY_REF=refs/pull/12/head` and
  `LAUNCHWAY_PULL_REQUEST=12`. `LAUNCHWAY_BRANCH` is empty.
- **Who decides.** A pull request runs only if the pipeline at the tip of the project's default
  branch asks for pull requests, and so does the pipeline at its head. The default branch decides
  because it has been reviewed: a pull request cannot turn pull-request runs on for itself. When
  a pull request asks but the default branch does not, *Recent pushes* says so.
- **Pull requests that already exist do not run.** Git hosts keep these refs after a pull
  request is closed or merged. The first time a server looks for a project's pull requests (a new
  project, or the first poll after upgrading), it records every pull request's head and runs
  none of them. After that only new pull requests, and ones pushed to, run. Heads are recorded
  even while the default branch does not ask for pull requests, so turning them on later does
  not run the old ones either. As with pushes, this is kept in the database, and exactly one
  server acts on each change.
- **No target branch.** The ref says which commit a pull request is at, not which branch it
  targets, so `pull_request` takes no `branches` filter, and `when: { branch: ... }` never matches
  a pull-request run. Use `when: { event: pull_request }` for steps only pull requests run, and
  `when: { event: [push, manual] }` to keep a step out of them.
- **No secrets, by default.** A pull request's code may come from anyone who can open one, so a
  step of a pull-request run that declares `secrets` fails before any runner sees it, unless the
  project allows it. The setting is read when the run is created; resuming the run and re-running
  it keep that decision.

  ```yaml
  pull_requests:
    secrets: false          # the default
  projects:
    - name: payments
      repo: https://git.example.com/team/payments.git
      pull_requests: { secrets: true }   # overrides, per project
  ```

- **Not there yet:** reporting a run's outcome back to the pull request as a commit status. A Git
  host's [webhook](#webhooks) makes the server look at once instead of at its next poll.

### Superseding older runs

With `supersede: true` at the top of the pipeline, a new run started by a push or a pull request
cancels the older runs of the same project and ref (the same branch, tag or pull request) that
are still queued, running or waiting for approval, if they too were started by a push or a pull
request. They are cancelled as if by hand (running steps are stopped by their runners), and say
why: *superseded by run #N*, in the run list, on the run page, and in the API (`cancel_reason`,
`superseded_by`, `superseded_by_number`). The push decision that started the new run names the
runs it superseded. Runs started by hand, draft runs and re-runs never supersede and are never
superseded, and the new run's own pipeline is what decides. The cancelling happens in the same
database transaction that creates the new run, so it is exact however many servers there are.
A superseded run can be resumed like any cancelled run.

### Run inputs

A pipeline may declare the values a run is started with:

```yaml
inputs:
  environment: { type: choice, options: [dev, prod], default: dev, description: Where to deploy }
  version:     { type: string, pattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$", required: true }
  dry_run:     { type: boolean, default: true }
  replicas:    { type: number, min: 1, max: 20, default: 2 }
  pythons:     { type: list, default: ["3.12", "3.13"] }
```

| Setting | Meaning |
|---|---|
| `type` | `string` (the default), `choice`, `boolean`, `number` or `list` (a list of text values) |
| `default` | Used when no value is given. A list's default is written as a list |
| `required` | A value must be given when there is no default; for a string, it may not be empty |
| `options` | A choice's values |
| `pattern` | A regular expression (RE2) a string must match; write `^` and `$` to match the whole value |
| `min`, `max` | A number's bounds |
| `description` | One line, shown in the form |

Starting a run by hand (**Start a run**, the API, or `launchway runs start -i name=value`) shows
a form made from the pipeline at the chosen branch or tag: a list of options for a choice, a
switch for a boolean, a number field with its bounds, and the pattern and description under each
field. The server checks every value, whoever sends it; a value that does not fit is refused, and
the form shows why next to the field. Runs started any other way use the defaults: a push, a pull
request, a draft run. So a pipeline that pushes or pull requests start may not have a required
input without a default (the file is refused, on that input's line); a
[schedule](#schedules) or [webhook](#webhooks) must give such an input a value.

The values a run was started with are recorded with it (shown on the run, under *Started with*,
and in the audit trail), and reach every step as `LAUNCHWAY_INPUT_<NAME>`: `true` or `false`, a
number in its shortest form (`2`, `2.5`), a list as a JSON array (`["3.12","3.13"]`). There is no
expression language; an input is used in exactly these places:

- a step's `when: { input: dry_run, equals: false }` (one value or a list; a boolean input alone,
  `when: { input: deploy }`, means `equals: true`);
- a template's parameter, `with: { target: { input: environment } }`, or for a string or a
  choice `with: { image: { input: version, format: "registry.example.com/app:%s" } }`;
- a loop's list, `for_each: { python: { input: pythons } }`.
- a deploy step's `version`, an image `tag` or a written `value` (`{ input: version }`, from a
  string, choice or number input), and its `environment` (`{ input: target }`, from a choice
  input whose options are all environment names). The environment is then checked against the
  server's environments when the run is created, like a written one.

A run that is resumed keeps its inputs. A re-run copies them and cannot change them: the steps it
reuses ran with those values, so a re-run with other values would mix two runs. To run with other
values, start a new run. Inputs are not for secrets: values are recorded with the run, shown to
everyone who can see it, and written to the audit trail. Use [secrets](#secrets) for those.
Choices whose options come from a step or another system (`options_from:`) are not supported yet.

### Repeating a step over lists

`for_each` repeats a step, or a step that uses a template, once for each combination of values:

```yaml
  test:
    needs: lint
    for_each:
      python: ["3.11", "3.12", "3.13"]
      os: { input: systems }             # a run input of type list
    exclude: [ { python: "3.11", os: alpine } ]
    max_parallel: 3
    fail_fast: false
    image: { each: python, format: "python:%s" }
    env: { TARGET_OS: { each: os } }
    run: pytest
```

When a run is created, the step becomes one step per combination, with names that say which:
`test[python=3.12,os=debian]`. The same lists always give the same names, in the order the lists
are written, so a resume or a re-run finds the same steps. Each one receives its values as
`LAUNCHWAY_EACH_<KEY>`, and `{ each: key }` (with `format:` for text around it) puts a value in
its `image`, an `env` value or a `with` value. A step that `needs: test` waits for all of them;
another step reaches one's outputs as `LAUNCHWAY_NEEDS_TEST_PYTHON_3_12_OS_DEBIAN_<OUTPUT>`.

- `exclude` leaves out the combinations that match an entry.
- `max_parallel` bounds how many are queued or running at once.
- `fail_fast` (on by default): once one fails for good (after its retries), the others that have
  not started are cancelled and those running are stopped, saying why. Resuming the run runs them
  again.
- Lists are written in place, come from a run input of type `list`, or, in a template, from a
  parameter of type `list` (`{ param: name }`). Values name steps, so they are 1 to 40 letters,
  digits, `.`, `_`, `+` and `-`; put other text around them with `format:`.
- Deploy and rollback steps cannot be repeated: each deployment holds its environment's lock and
  is recorded on its own, so write one deploy step for each environment.
- A loop may have at most 64 combinations (`loops: { max_members: N }` in the server's
  configuration, up to 500), and a run at most 500 steps. An empty list is refused when the run is
  created.

The run page shows a loop as one node whose status sums up its members; choosing it shows them as
a grid (two lists as rows and columns), and choosing a cell shows that step's log.

Lists produced by an earlier step while the run goes (dynamic fan-out) are not supported. They
would fit the engine this way: the producing step's outputs are recorded in its `step.finished`
event, so a new event (a loop's members, recorded when that step finishes) would add the member
steps to the run, and every server would rebuild them from the log as it does now; servers that
do not know the event would leave such runs alone, as they do any run they do not fully understand.

### Cleanup and notification steps

Normally a step runs only if every step it needs succeeded. `when: { upstream: … }` changes that:

```yaml
  notify-failure:
    needs: [build, deploy]
    when: { upstream: failed }    # only if one of them failed
    run: ./notify.sh
  cleanup:
    needs: [build, deploy]
    when: { upstream: always }    # whatever happened
    run: ./tear-down.sh
```

Such a step waits until every step it needs has ended. With `failed` it runs if one of them
failed, was cancelled, or was not run because of a failure, and is skipped otherwise; with
`always` it runs regardless; `succeeded` is the default. It does not change the run's outcome: a
run with a failed step has failed, even if its cleanup succeeded. A deploy step takes no `upstream`: it deploys
only after the steps it needs succeeded. A rollback step decides on the steps it needs, so none of
them, directly or not, may run after failures (its success would hide the failure the rollback
is for); let such a cleanup step need the rollback step instead. A cancel (by hand, or
[superseding](#superseding-older-runs)) means stop: steps not started, these included, are
cancelled. Resuming a run runs the failed steps again and then lets these steps decide again, so
a cleanup runs again after them, and `notify-failure` above is skipped if nothing fails the second
time. A [re-run](#re-running-from-a-step-and-what-each-step-was-given) from a step runs them again
like any step after it.

### Schedules

```yaml
on:
  schedule:
    - { cron: "0 3 * * *", timezone: America/Chicago, ref: main, inputs: { environment: prod } }
    - { cron: "*/15 9-17 * * MON-FRI" }
```

A schedule is a five-field cron expression (minute, hour, day of the month, month, day of the
week; `@hourly`, `@daily`, `@weekly`, `@monthly` and `@yearly` work too) in a time zone (an IANA
name; UTC if none). When the clocks go forward, a time that does not exist that day is skipped;
when they go back, a time that happens twice fires once. `ref` is the branch or tag whose pipeline
runs (the default branch if none) and `inputs` the run's values (the others take their defaults).
Schedules are read only from the pipeline on the project's default branch, so a branch cannot
schedule itself. Runs have event `schedule`.

Each firing is claimed in the database, so however many servers are running, it starts one run.
A schedule never fires for a time before it was first seen (adding or changing one does not run it
for the past). If no server was running when a schedule should have fired, the first server to
look afterwards starts one run, for the latest time that was missed, and none for the earlier
ones. A firing that cannot start a run (the branch is gone, the pipeline there has problems) is
recorded with why. The project page lists the schedules, when each fires next, and how its last
firing went (`GET /api/v1/projects/{name}/schedules`).

`schedules: { enabled: false }` in the server's configuration turns schedules off for every
project, and a project's own `schedules:` overrides it. While off, firings are recorded as
skipped, so turning them back on does not catch up.

### Webhooks

Two kinds of webhook reach the server. Both prove themselves with a secret that the server's
configuration names as an environment variable or a file (a Kubernetes Secret, for example); the
secret is never in the repository or the database. A caller that cannot prove itself gets the
same `404` whatever the reason (no such project, no webhook, a wrong secret), is recorded in the
audit trail, and counts towards the [limit on failed attempts](#api-tokens).

**A Git host's webhook** makes the server poll a project at once, instead of at its next poll. It
starts nothing by itself: the poll decides, as it always does, so a forged or repeated call could
at most cause a poll. Point the host's push and pull request webhook at
`https://launchway.example.com/api/v1/projects/<name>/git-hook`:

```yaml
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    git_webhook: { kind: github, secret_env: PAYMENTS_HOOK_SECRET }   # or secret_file
```

`kind` is `github` (checks `X-Hub-Signature-256`), `gitea` or `forgejo` (`X-Gitea-Signature` or
`X-Forgejo-Signature`), or `gitlab` (`X-Gitlab-Token`). Each delivery's ID is remembered for a
week; a delivery seen before is answered and ignored.

**A webhook trigger** lets another system start a run, with inputs taken from its request's JSON
body by a closed mapping (no expressions):

```yaml
on:
  webhook:
    name: deploy-request
    ref: main                        # whose pipeline runs; the default branch if none
    inputs: { version: body.release.version, environment: body.environment }
```

`webhook` may also be a list of these. Like schedules, they are read from the default branch. The
values are checked exactly like a person's: a missing field takes the input's default, and a value
that does not fit is refused with `422` and a message per input. Runs have event `webhook`. The
server's configuration says what callers must prove; a trigger it does not list cannot be called:

```yaml
projects:
  - name: payments
    webhooks:
      deploy-request: { hmac_secret_env: PAYMENTS_DEPLOY_HMAC }   # or hmac_secret_file,
                                                                   # token_env, token_file
```

Call it with `POST /api/v1/projects/payments/webhooks/deploy-request` and a JSON body of at most
64 KiB, either with `Authorization: Bearer <token>`, or signed: `X-Launchway-Timestamp: <Unix
seconds>` and `X-Launchway-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>">`. A signed
request is accepted within five minutes of its timestamp, and only once. Prefer signing: a token
can be replayed by anyone who sees one request. Send `Idempotency-Key: <up to 200 characters>` to
make retries safe: a repeat answers `200` with the run the first call started (and
`Idempotent-Replayed: true`), and the same key with a different body is refused with `409`. Keys
are remembered for a week. Every call is in the audit trail (`webhook.run`).

## Editing pipelines

Git is the only place a pipeline is kept. Launchway never holds a copy of its own: the editor
(*Projects → Edit pipeline*, or *Edit this pipeline* on a run's *Pipeline* tab) reads
`.launchway/pipeline.yaml` from the tip of a branch, checks it as you type with the same parser
the server uses, marks problems on their lines and draws the graph. It shows which branch and
commit the text came from and whether you have changed it. Unsaved edits stay in that browser tab
only. A branch without a pipeline opens with a starting point.

**Run this draft** runs the edited text against the branch's current commit without saving it.
The run is marked *Draft* everywhere; it records who started it and the SHA-256 of the text, and
keeps the text itself (which is in no commit) so that its *Pipeline* tab shows exactly what ran.
A draft is its own trigger event, `draft`:

- a step with no `when` runs as usual;
- a step with a `when` runs only if its `event` list names `draft`. `when: { branch: main }` or
  `when: { event: [push, manual] }` does not run in a draft of `main`, so a deployment guarded by
  `when` does not run from unsaved, unreviewed text;
- a step that declares `secrets` fails before any runner sees it, unless the project allows
  drafts to read secrets (below). Its runner never looks a secret up.

A draft lets someone run steps nobody has reviewed, with whatever the runners can reach, so who
may start one is a setting of its own, checked on the server with or without sign-in:

```yaml
drafts:
  allow: operator      # admin, operator (the default) or none: the role needed on a project
  secrets: false       # the default: steps of drafts may not read secrets
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    drafts: { allow: admin, secrets: true }   # overrides, per project
```

Resuming a failed draft run runs its text again, so it needs the same right, and whether its
steps may read secrets is decided again by the setting at that moment.

**Save…** commits the file as you: the author is your name and e-mail address as your sign-in
provider gave them (an address the provider says is unverified is not used; without one it is
`<you>@users.noreply.launchway`), the committer is Launchway, and only the pipeline file changes.
By default it goes to a new branch, `<branch_prefix><you>/<date-time>`, with a link to open a pull
request when the repository is on GitHub, GitLab, Gitea or Forgejo (judged by its host name;
Launchway calls no Git host API). A save is refused, and nothing is pushed, if the branch has
moved on since the text was opened, so two people cannot overwrite each other; or if the text has
problems, unless an admin of the project chooses to save it anyway. Saving needs the operator role
and a `push` section for the project:

```yaml
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    push:
      branch_prefix: launchway/          # the default
      allow_direct: ["release/*"]        # branches that may be committed to directly; default none
```

Pushing uses the server's Git credentials, exactly as fetching does: in the repository URL, or in
Git settings in the server's environment, such as `GIT_CONFIG_COUNT=1`,
`GIT_CONFIG_KEY_0=http.https://git.example.com/.extraHeader` and
`GIT_CONFIG_VALUE_0=Authorization: Bearer ...`. Give that credential only the rights it needs,
and protect the branches that matter on the Git host: Launchway refuses direct saves outside
`allow_direct`, but the credential itself could push anywhere it is allowed to. A project without
`push` cannot be saved to; the editor offers *Copy* and *Download* instead. Each save works in a
temporary clone of the server's mirror, which is removed whatever happens, and pushes with a
lease, so saves on several servers at once are safe. If the push fails, Git's own message is
shown, with any credentials in it removed.

Over the API: `GET /api/v1/projects/{name}/pipeline?ref=main` reads the file;
`POST /api/v1/projects/{name}/draft-runs` with `{"ref": "main", "pipeline": "..."}` starts a draft
run (problems answer `422` with their lines); `POST /api/v1/projects/{name}/pipeline` with
`{"base_ref", "base_sha", "pipeline", "message", "branch", "force"}` saves (`branch` empty for a
new branch, or `base_ref` itself to commit to it directly; a stale `base_sha` answers `409`). The
text may be at most 256 KiB.

## Shared libraries

A shared library is a Git repository of pipeline templates and the scripts they run, developed
like any application and released with Git tags. A pipeline names the library and a tag, branch
or commit; nothing is imported into Launchway or registered with it, and the template and its
scripts always come from the same commit, so there is no second version to keep in step.

```
templates/python-service.yaml   # a pipeline fragment with typed parameters
templates/release.yaml
scripts/lint.sh                 # any files; executable as committed
scripts/scan.sh
```

A template:

```yaml
template: python-service
description: Lint, test and scan a Python service
params:
  test_command: { type: string,  default: "python3 -m pytest -q", description: How to run the tests }
  scan:         { type: boolean, default: true }
  python:       { type: string,  default: "3.13", options: ["3.12", "3.13"] }
containers:
  py: { image: { param: python, format: "docker.io/library/python:%s-slim" } }
steps:
  lint: { container: py, run: "$LAUNCHWAY_LIB/scripts/lint.sh" }
  test: { container: py, needs: lint, run: "$PARAM_TEST_COMMAND" }
  scan: { container: py, needs: lint, if: { param: scan }, run: "$LAUNCHWAY_LIB/scripts/scan.sh" }
outputs:
  coverage: { from: test.coverage }
```

A pipeline that uses it:

```yaml
pipeline: mcp-weather
libraries:
  platform: { repo: ci-templates, version: v3 }   # a tag, a branch, or a full commit id
steps:
  ci:
    uses: platform/python-service
    with: { test_command: "pytest -q tests/" }
  deploy:
    image: alpine:3.22
    needs: ci
    run: echo "coverage was $LAUNCHWAY_NEEDS_CI_COVERAGE"
```

`examples/library` is a small working library (a `check` template, a `release` template that uses
it, and their scripts), and `make demo` makes a repository of it with two releases.

**Which repositories may be libraries** is server configuration. A pipeline's `repo:` must name
one of them; it can never be an address, so a pipeline cannot make the server fetch from anywhere
with its credentials. Libraries are fetched exactly as projects are, with the same
[credentials](#editing-pipelines) (in the URL, or `GIT_CONFIG_*` settings in the environment of
the server and the runners):

```yaml
libraries:
  - name: ci-templates
    repo: https://git.example.com/platform/ci-templates.git
```

**Versions.** When a run is created, the server fetches its mirror of each library the pipeline
names and resolves the version to a commit: a tag first, then a branch, then a full commit id (so
a tag and a branch with the same name give the tag). Moving tags and branches is allowed; the run
records the library, the version as written and the commit it pointed at, and its *run.created*
event, the run page (*Libraries* in the header) and every runner use that commit. Resuming a run
never resolves anything again. A version that does not exist, or a library the server does not
list, is reported at its line and starts nothing.

**Expansion.** A step that `uses` a template becomes the template's steps, with ids
`<step>/<template-step>` (`ci/lint`, `ci/test`), drawn on the run page as one group that can be
collapsed. Template steps that need nothing in the template need what the `uses` step needs; a
step that needs the `uses` step waits for all of its steps, and is skipped if any of them fails,
as usual. The `uses` step's `when`, `retry` and `timeout` apply to every step of the template that
does not set its own. The template's containers are added to the run under new names
(`ci-py`). A template can use templates of its own library, written by name alone
(`uses: check`), from the same commit; using another library's templates from a template is not
supported yet. A step whose `if` is false is left out, and what needed it needs what it needed.

**Parameters**, with no expression language. Each has a `type` (`string`, the default, `number` or
`boolean`), an optional `default` (without one it is required), `options` and a `description`.
`with:` values are checked against them, with line numbers; unknown names are refused. A
parameter reaches a template in exactly three ways, all resolved when the run is created, so the
graph a run keeps holds plain values only:

1. every step of the template receives it as the variable `PARAM_<NAME>` (upper case, `-` as
   `_`); in a template, names starting with `PARAM_` are reserved for this;
2. where a plain value is written, `{ param: name }` stands for it, and for strings
   `{ param: name, format: "text %s text" }`: in a container's or step's `image`, `env` values,
   `timeout`, `retry` and `when` lists (and in the `with` of a template the template uses);
3. `if: { param: name }` (a boolean) or `if: { param: name, equals: value }` on a template step
   leaves the step out when false.

**Outputs.** A template's `outputs:` names which step outputs it exposes; a step that needs the
`uses` step gets them as `LAUNCHWAY_NEEDS_<STEP>_<KEY>`. Other outputs of the template's steps do
not reach it. Inside a template, steps refer to each other by the names the template gives them,
so `LAUNCHWAY_NEEDS_LINT_...` works there as anywhere.

**Files at run time.** For every library a run uses, the runner puts the library's files at the
recorded commit in the workspace, at `.launchway/lib/<name>/`, so they reach steps the same way in
a container, a Kubernetes pod or on the host. A template's steps get `LAUNCHWAY_LIB`, the
directory of their own library (`/workspace/.launchway/lib/<name>` in a container), and every
step gets `LAUNCHWAY_LIB_<NAME>` for each library. The directory is listed in the checkout's
`.git/info/exclude`, so `git status` in a step does not report it; it replaces anything the
repository itself has at that path. A runner that takes a run over restores the same commit.

**Trying a library change before releasing it.** Starting a run by hand (*Start a run →
Advanced*, or `POST /api/v1/projects/{name}/runs` with `{"ref": "main", "libraries":
{"platform": "my-branch"}}`) and draft runs (the same `libraries` field, or *Advanced* in the
editor) may use another version of any library the pipeline names. The run records both versions
and is marked *Library override*. It needs only the right to start the run.

**One pipeline for many repositories.** A project may get its pipeline from a library instead of
a file in its repository:

```yaml
projects:
  - name: weather-service
    repo: https://git.example.com/team/weather-service.git
    pipeline:
      library: ci-templates
      version: v3
      template: python-service
      with: { test_command: "pytest -q tests/" }
      on: { push: { branches: [main, develop] }, pull_request: {} }
      supersede: true          # optional; see Superseding older runs
```

At a commit that has its own `.launchway/pipeline.yaml`, that file wins; otherwise the configured
template is the pipeline, named after the project. The push decisions under *Recent pushes* and
the run page say which was used, and the run keeps the configuration as the pipeline file it
amounts to, shown on its *Pipeline* tab. The editor opens that text for such a repository, so
saving it turns it into a file.

**In the editor**, pipelines are checked against the project's libraries as you type: the graph
shows the expanded template, parameter problems are marked on their lines, problems in a template
are listed with its file, and the resolved commit of each library is shown.
`POST /api/v1/validate?project=NAME` (or `?expand=1`) does the same over the API; without either,
and in `launchway validate`, a `uses` step is one node, marked as not expanded. `launchway run`
cannot run templates, which come from a server's libraries.

**Security.**

- Only configured libraries can be fetched, by name; versions and overrides are at most 128
  characters of letters, digits and `. _ / + -`, never start with `-`, and are compared with the
  names Git lists; only a full commit id, checked to be one, is ever passed to Git. Library names
  are at most 40 characters,
  parameter values at most 4 KiB, and a template may have at most 50 parameters.
- A template's steps run as part of the using project's run: a step's secrets are looked up for
  that project, so a secret store's `projects` allow-list applies to templates as to any step, and
  expansion cannot change whose job a step is. Draft runs that may not read secrets refuse
  template steps that ask for them, too.
- Manual and draft starts record in the audit trail each library's version, any override, the
  commit it resolved to, and the templates used.
- Whoever can push to a library, or move its tags, changes what every pipeline that uses it runs.
  Protect its tags and branches on the Git host as you would a deployment script, and pin
  pipelines to tags or commits rather than branches where that matters.

## Approvals

An approval step pauses the run until a person decides:

```yaml
steps:
  approve-prod:
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy 2.14 to production?
    timeout: 24h
  deploy-prod:
    image: alpine:3.22
    needs: approve-prod
    run: ./deploy.sh
```

An approval takes `needs`, `when`, `message` and `timeout`, and runs nothing, so it has no image or
script. When its needs have succeeded it waits, and every pending approval is listed on the
**Approvals** page. Approving lets the steps after it run. Rejecting, or nobody deciding before the
timeout, fails it: the steps after it are skipped, and resuming the run asks again. Each decision
is kept with the run: who made it, when, and their comment.

Decide in the web UI, or over the API with `POST /api/v1/runs/{id}/steps/{step}/approve` (or
`/reject`) and an optional `{"comment": "..."}`. `launchway run` asks in the terminal, and rejects
the approval if there is no terminal to ask in.

With sign-in on, the person who started a run may reject it but not approve it: someone else must.
Without sign-in everyone is the same configured `user`, so they may. Either default can be changed:

```yaml
approvals:
  allow_self: false   # true lets people approve runs they started
```

A run started by a push records the commit's author name, which anyone can set in Git; it is not
checked against anyone's sign-in.

## Re-running from a step, and what each step was given

Every attempt of every step records what it was given when a runner took it: its environment, with
where each value came from (the step's own `env`, its container's, a template parameter, the
standard `LAUNCHWAY_*` values, or an output of a step it needs, named as `build · image`), its image
and container, its script, the commit of the library it came from, and which secrets it asked for.
Secrets are recorded by reference (`vault:ci/registry#token`), never by value: the server never sees
values. Very long values are shortened in the record (4 KiB a value, 16 KiB of script, 64 KiB an
attempt), and marked so; the step itself receives them in full. The run page shows them under
*Inputs* beside a step's outputs, attempt by attempt, and the API returns them with each step
(`inputs`).

A finished run can be **re-run from any step**: `POST /api/v1/runs/{id}/rerun` with
`{"from": "deploy"}`, or *Re-run from this step* in the UI, which first lists what will run and what
will be reused. The re-run is a new run, linked to the original (`rerun_of`, `rerun_from`), at the
same commit, with the same pipeline, the same library commits (even if a tag has moved since) and
the same trigger, started by whoever asked for it. The chosen step and every step after it run
again. Every other step is **reused**: it is recorded in the new run with the outcome and outputs
it had, marked as reused from the original, and is never run, so the steps after it receive exactly
the values they would have. Its log stays in the run where it ran, which the UI links to.

- Only steps that succeeded, or were skipped by their `when` condition, can be reused. If a step the
  chosen one needs did not succeed, re-run from an earlier step, or resume.
- Approvals after the step wait for a decision again. Approvals before it are reused with the
  decision made in the original, shown with who made it.
- A draft run's re-run is a draft run of the same text, with the same rules.
- Re-running needs the operator role on the project (and, for a draft, the right to start drafts),
  and is recorded in the audit trail as `run.rerun`, with the original run, the step and the new
  run.
- **Files are not reused.** A re-run starts from a fresh checkout, so files that reused steps wrote
  to the workspace are not there. Outputs are kept; steps that need files an earlier step made
  should be included in the re-run (re-run from that earlier step).

**Resume** is different: it continues the same run and runs again only the steps that did not
succeed. Use it after a failure that has been put right; use a re-run to go again from a chosen
step, for example to redeploy what a run built.

## Test reports

A step can say which test result files it writes, and Launchway reads them when the step ends,
**whether it passed or failed** (a failing test step is the case that matters), was canceled or
timed out:

```yaml
steps:
  test:
    image: python:3.12
    run: pytest --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml          # one glob or a list; relative to the workspace

  go-test:
    image: golang:1.27
    run: |
      mkdir -p reports
      status=0
      go test -json ./... > reports/go.json || status=$?
      exit $status                  # keep the step failing when a test fails
    reports:
      tests: [reports/go.json]
```

- **Formats**, worked out from each file's content, not its name: JUnit XML, as pytest, Maven
  Surefire, Gradle, Jest and Vitest reporters, gotestsum and most other tools write it (nested
  suites, failures, errors, skipped tests and captured output included; a test Surefire ran again
  until it passed counts as flaky), and the output of `go test -json`. A file that is neither is
  shown as such, with the reason ("reports/x.xml: not a test report: ..."); it never fails the step.
- **Globs** use `*`, `?` and `[a-z]` within a path part, and `**` for any number of directories
  (`**/TEST-*.xml`); `**` does not enter `.git` or `node_modules`. Absolute paths, `..`, and empty
  globs are refused with the line they are on. In a template, a glob may come from a parameter
  (`{ param: dir, format: "%s/*.xml" }`).
- **What is collected**: regular files only, from the run's pod on Kubernetes and from the workspace
  directory otherwise. Symbolic links and pipes are not followed or read, so a step cannot make a
  runner read anything outside its workspace. The step's log says what was collected, and when a
  glob matched nothing or a file was cut short. Collecting never changes the step's outcome.
- **The Tests tab** of a run shows the totals, a pass rate and a bar, then every result, failures
  first and then the slowest, each opening to its message, stack trace and captured output with a
  button to copy them. Filters by status, a search box, and grouping by step or suite. A test that
  failed and then passed on a retry of the step is marked **flaky**; one that passed in the previous
  run of the same branch and fails now is a **new failure**. A failed step links to its failed tests,
  and run lists show each run's counts. A re-run shows the results of the steps it reused, linked
  to the run where they ran.
- **A project's Tests page** shows, for a branch (its default branch first), the totals and the time
  the tests took over its last runs, the tests that fail most, flaky tests (they passed and failed on
  the same commit, or passed only on a retry), the slowest, and each test's history: its last results
  as a strip of links to their runs.
- **API** (anyone who may see the run or project): `GET /api/v1/runs/{id}/tests` (summary, step by
  step), `GET /api/v1/runs/{id}/tests/cases?status=failed|passed|skipped|flaky&q=&step=&limit=&offset=`,
  `GET /api/v1/runs/{id}/tests/cases/{case}` (one result in full),
  `GET /api/v1/projects/{name}/tests?ref=&runs=` and
  `GET /api/v1/projects/{name}/tests/history?key=&ref=`. Run summaries carry `tests` totals.

**What is kept, and limits.** Each step attempt's event records a short summary: the totals, the
files and what was wrong with them. The individual results are stored beside the event log, in the
same transaction: messages cut to 2 KiB, traces to 8 KiB, and captured output (only for tests that
failed) to its last 4 KiB. A test is known by its suite or class, its name and, when the report
gives one, its file, so its history follows it from run to run. Launchway keeps runs forever (there
is no run retention yet), so individual results are bounded per project: when a project has more
than `max_cases_per_project`, the oldest runs' results are removed first; their totals stay, and the
run says so.

| Setting | Where | Default | Meaning |
|---|---|---|---|
| `tests.max_upload_mib` | server | 24 | MiB of report files accepted per step attempt; runners are told and send no more |
| `tests.max_cases_per_run` | server | 50000 | individual results one run keeps; beyond it, only the totals |
| `tests.max_cases_per_project` | server | 1000000 | individual results a project keeps; the oldest runs' go first |
| `runner.reports.max_files` | runner | 100 | files collected per step attempt (the first, in name order) |
| `runner.reports.max_file_mib` | runner | 10 | MiB read of one file; a larger one is cut, and what was read is still used |
| `runner.reports.max_total_mib` | runner | 20 | MiB read of all of an attempt's files |

With the Helm chart these are `config.tests.*` and `runner.reports.*` in the values. A server
older than the one that brought test reports cannot read a pipeline with `reports:`; use the key
once every server is upgraded (see [docs/development/migrations.md](docs/development/migrations.md)).

## Artifacts

A step can keep files once it has run: people download them from the run, and later steps of
the run fetch them into their workspace, on whichever runner they run.

```yaml
steps:
  build:
    image: golang:1.27
    run: make dist
    artifacts:
      - name: binary
        paths: [bin/app, "dist/*.tar.gz"]   # globs as for reports; a directory keeps all below it
        retain: 30d                         # optional: 2w, 12h…; the server has a default and a maximum
        when: success                       # the default; or failure (screenshots, logs), or always

  deploy:
    image: alpine:3.22
    needs: [build]                          # the step must have run first
    fetch:
      - { from: build, name: binary, into: out }   # into is optional: default, where the files were
    run: ./out/bin/app --version
```

- **What is kept**: regular files only, copied out of the workspace by the runner and checked by
  SHA-256 end to end. Symbolic links are not followed. An artifact is kept whole or not at all: a
  file over the size limit, too many files, or paths that match nothing mean none is kept, and the
  step's log says why. Keeping files never changes the step's outcome.
- **Fetching** puts the files in the workspace before the step runs, on any executor, also when
  the step runs on another runner than the one that kept them, or in a re-run that reused the
  step. A fetch of an artifact that was not kept, has expired or was deleted fails the step and
  says so. A step can fetch only from steps it needs, directly or through others; in a template,
  from a step of the same template.
- **With the other features**: each member of a loop (`for_each`) keeps its own artifacts, so
  members may use the same name; fetching from a loop's members is not supported yet (fetch from
  a step that is not repeated). A step that runs after a failure (`when: { upstream: failed }` or
  `always`) may fetch from a step that failed what that step keeps with `when: failure` or
  `always`; one that runs only after a failure and fetches what is kept only on success is
  refused. Deploy and rollback steps take no `artifacts`, `fetch` or `reports`. Artifact names,
  paths and `into` are plain text: `{ input: … }` and `{ each: … }` are not allowed there (the
  script sees those values as `LAUNCHWAY_INPUT_<NAME>` and `LAUNCHWAY_EACH_<KEY>`).
- **Secrets are not masked in artifacts.** Secret values are hidden in a step's log, but files a
  step keeps are kept exactly as written. Do not put secrets in them.
- **The Artifacts tab** of a run lists each artifact with its files, sizes, SHA-256 and how long it
  is kept, with a download for each file and for all of them as a zip. Operators of the project may
  delete one; the audit trail records who did. Files are always served as downloads, never shown
  as pages.
- **Storage**: a directory (the default, under the data directory) for one server, or
  S3-compatible object storage, which several servers need. A file kept twice is stored once.
  Uploads go in 8 MiB chunks that any server can take, and carry on after a broken connection.
  Each server checks that it shares the store with the others (a small marker object), and keeps
  no artifacts, saying why, if it does not.
- **API**: `GET /api/v1/runs/{id}/artifacts`, `GET /api/v1/runs/{id}/artifacts/{artifact}/file?path=`
  (ranges supported), `GET /api/v1/runs/{id}/artifacts/{artifact}/archive` (zip),
  `DELETE /api/v1/runs/{id}/artifacts/{artifact}` (operators). Run summaries carry `artifacts`,
  the number kept.

| Setting | Default | Meaning |
|---|---|---|
| `artifacts.store` | filesystem | `filesystem` or `s3` |
| `artifacts.dir` | `<data>/artifacts` | the filesystem store's directory |
| `artifacts.s3.endpoint`, `bucket`, `region`, `prefix`, `path_style`, `ca_file` | – | the S3 store; credentials come from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or `*_FILE`), never from the file |
| `artifacts.max_file_mib` | 1024 | the largest file an artifact may hold |
| `artifacts.max_run_mib` | 4096 | how much one run may keep |
| `artifacts.max_project_gib` | 50 | how much a project may keep over all its runs |
| `artifacts.retain` | 30d | how long artifacts are kept when the pipeline does not say |
| `artifacts.max_retain` | 90d | the longest any is kept, whatever the pipeline says |

Expired artifacts are removed once their run has finished; contents nothing refers to any more are
removed an hour later. Every server does this; they never get in each other's way. A runner needs
disk space for a copy of what a step keeps or fetches. Runners older than this feature are not
given steps that keep or fetch artifacts. `make test-s3` checks the S3 store against a real
S3-compatible server.

## Security findings and SBOMs

Steps can declare the results of security scanners and software bills of materials:

```yaml
steps:
  scan:
    image: registry.example.com/scanners:1
    run: |
      gitleaks detect --report-format sarif --report-path reports/security/gitleaks.sarif || true
      semgrep scan --sarif --output reports/security/semgrep.sarif || true
      trivy fs --format cyclonedx --output reports/sbom.cdx.json .
    reports:
      findings: reports/security/*.sarif   # SARIF 2.1.0, from any scanner
      sbom: reports/sbom.cdx.json           # CycloneDX JSON
```

- **Findings** are read from SARIF 2.1.0, as gitleaks, semgrep, CodeQL, bandit, hadolint, trivy,
  govulncheck and most other scanners write it, whether the step passed or failed. A file that is
  not SARIF is shown as such, with the reason; it never fails the step.
- **Severity** is one of critical, high, medium, low or unknown, taken from the first of: a
  `security-severity` score on the result or its rule (9.0 and over critical, 7.0 high, 4.0 medium,
  0.1 low); the tool's own severity word (`issue_severity`, `severity`, or a rule tag such as
  `HIGH`); the SARIF level (error high, warning medium, note low, none unknown). A finding about a
  secret is never below high.
- **Secrets that scanners find are never stored or shown**: only where they are and which rule
  found them. Code snippets in SARIF files are never kept for any finding, the message of a
  finding about a secret is replaced by its rule's description, and every message is cleared of
  anything shaped like a key or token. SARIF files themselves are not kept.
- **New, still there, fixed.** Each finding gets a fingerprint (its tool, rule and file, with the
  scanner's own fingerprints when it gives real ones, else the line and message), and a run's
  findings are compared with the previous run of the same branch: new, still there, or fixed.
  Only scanners that ran both times are compared. A finding whose code moved up or down a file
  shows as fixed and new again, unless its scanner gives line-independent fingerprints, as
  CodeQL does.
- **The Security tab** of a run shows a bar of counts by severity, then every finding, the most
  severe first, with filters by severity, tool and status and a search box; each opens to the
  rule, its description, the location and a link to the rule's documentation. Run lists show the
  counts. **A project's Security page** shows the counts over a branch's recent runs, and the
  findings still open, each with the run where it was first seen.
- **SBOMs** are kept whole as the step's artifact `sbom` and summarised: the number of components
  and the licences. **Components** can be searched across projects (`/components` in the UI): which
  runs and projects contain a package, at a version.
- **API** (anyone who may see the run or project):
  `GET /api/v1/runs/{id}/findings?severity=&tool=&status=new|existing|fixed&q=&step=&limit=&offset=`,
  `GET /api/v1/projects/{name}/security?ref=&runs=`,
  `GET /api/v1/components?name=&version=&limit=` (only projects the caller may see). Run summaries
  carry `findings`, the counts by severity, for checks that later act on them.

| Setting | Default | Meaning |
|---|---|---|
| `findings.max_upload_mib` | 32 | MiB of SARIF files accepted per step attempt; runners are told |
| `findings.max_per_run` | 20000 | findings one run keeps; beyond it, only the counts |
| `findings.max_per_project` | 500000 | findings a project keeps; the oldest runs' go first |
| `findings.max_components_per_run` | 20000 | SBOM components one run keeps for searching |
| `findings.max_components_per_project` | 1000000 | SBOM components a project keeps |
| `findings.max_sbom_mib` | 64 | MiB of a step's SBOM files read to summarise them |

With the Helm chart these are `config.artifacts.*` and `config.findings.*` in the values, unset by
default. A server older than the one that brought these keys cannot read a pipeline that uses
`artifacts`, `fetch`, `reports.findings` or `reports.sbom`; use them once every server is upgraded.

## Container images

An image step builds a container image from a Dockerfile in the workspace, scans it, pushes it and
signs it, and records what it made:

```yaml
steps:
  test:
    image: golang:1.27
    run: go test ./... && echo version=1.4.2 >> "$LAUNCHWAY_OUTPUTS"

  image:
    kind: image
    needs: test
    context: .                        # default .
    file: Dockerfile                  # default <context>/Dockerfile; relative to the workspace
    target: runtime                   # a stage of the Dockerfile; optional
    platforms: [linux/amd64, linux/arm64]   # optional; see below
    build_args: { VERSION: { output: test.version }, COMMIT: { run: sha } }
    secrets:                          # RUN --mount=type=secret,id=npm_token in the Dockerfile
      - { id: npm_token, secret: { store: vault, key: ci/npm, field: token } }
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
      - { registry: harbor, repository: team/shop, tag: latest }
    cache: { registry: harbor, repository: team/cache/shop }   # optional
    sbom: true                        # default false
    provenance: true                  # the default
    sign: true                        # with the runner's key; or { key: {store,key,field}, password: {...} }
    scan: { fail_on: high, mode: enforce }   # or scan: true (report only)

  deploy-prod:
    kind: deploy
    needs: image
    environment: prod
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop/rollout.yaml, image: team/shop, digest: { output: image.digest } }
```

| Key | Meaning |
|---|---|
| `context`, `file`, `target` | What to build: a directory and a Dockerfile in the workspace, as earlier steps left it, and a stage. Neither may lead outside the workspace, through a symbolic link or otherwise |
| `platforms` | `os/arch` values; default the builder's own. See *Several platforms* |
| `build_args` | Values as for deploy steps: written in place, `{ output: step.key }`, `{ run: sha \| short_sha \| branch \| tag \| number }` or `{ input: name }`. They are recorded in the provenance: never put a secret in one |
| `secrets` | Build secrets by ID, read from the runner's secret stores like any step's secrets. They reach the build only as `RUN --mount=type=secret` mounts: never in a layer, the image's history, the provenance, the log (masked like any secret), an event or the database |
| `tags` | Names to push under: a registry as the runner's configuration names it, a repository in it, and a tag (a value, as above) |
| `push` | Default true. Pull-request and draft runs never push, whatever it says |
| `cache` | A repository the build cache is read from and, on runs that push and may push there, written to |
| `sbom` | Make a CycloneDX SBOM of each platform's image: kept as the step's `sbom` artifact and summarised, as for `reports.sbom`, and searchable under *Components* |
| `provenance` | Record where the image came from (see *Provenance*); kept as the step's `provenance` artifact |
| `sign` | Sign the image, its platforms' images, the provenance and the SBOMs, in the form cosign verifies |
| `scan` | Look for known vulnerabilities in each platform's image. `mode: report` (the default) records them; `mode: enforce` fails the step, before anything is pushed, when one is at `fail_on` or worse. They are findings like any scanner's: the run's Security tab, the project's Security page |

Its outputs are `digest` (the image index's, or the one image's), `image` (`host/repository@sha256:…`,
empty when nothing was pushed), `tags` (comma-separated `host/repository:tag`) and `pushed` (`true`
or `false`). Later steps receive them as `LAUNCHWAY_NEEDS_IMAGE_DIGEST` and so on, and deploy steps
as `{ output: image.digest }`. A deploy change with `digest:` instead of `tag:` pins every reference
to the image as `repository@sha256:…`, which nobody can move; references already pinned are moved
to the new digest. A tag cannot move a reference pinned by digest, and says so.

An image step runs no script and has no container in the run's pod: the runner builds it. It may
`fetch` artifacts into the workspace first, is retried with `retry`, may be in a template, and
cannot be repeated by `for_each` yet. A run with image steps runs, from its first step, on a runner
that can build images, since its steps share that runner's workspace.

**How it is built.** Images are built by [BuildKit](https://github.com/moby/buildkit) daemons the
runner's operator runs apart from the steps, rootless (see
[deploy/buildkit/README.md](deploy/buildkit/README.md): nothing that builds a Dockerfile runs under
Pod Security "restricted", so the daemon has a namespace of its own with exactly two relaxations,
and step pods stay restricted). The runner is the daemon's client, over mutual TLS: it sends the
build context (copied out of the run's pod on Kubernetes), answers the daemon's requests for build
secrets and registry credentials over the build's session, and receives the image. For registries
that use tokens, an honest daemon asks the runner for a token, which the runner fetches itself, and
never sees the password; for registries that take a user name and password directly (basic
authentication), the daemon is given the password. The session answers both kinds of request, so a
daemon that someone has taken over can obtain the user name and password of every registry the
current build may use. That is why a daemon is one trust boundary (see
[deploy/buildkit/README.md](deploy/buildkit/README.md)) and why the credentials a build may use are
scoped (see *Who gets which credentials* below). It then has it scanned, by a pinned
[Trivy](https://trivy.dev) image run inside the daemon on exactly what was built, enforces the
scan's policy, and pushes and signs it itself. Base images, the build cache and the scanner's
database are the daemon's business; it keeps them between builds.

**Registries and who may push** are the runner's configuration, never the pipeline's:

```yaml
# runner.yaml (or runner: in the server's configuration)
images:
  builders:
    - name: shared
      address: tcp://buildkitd.launchway-build.svc:1234
      ca_file: /var/run/launchway/buildkit/ca.crt      # the daemon's CA
      cert_file: /var/run/launchway/buildkit/tls.crt   # the runner's client certificate
      key_file: /var/run/launchway/buildkit/tls.key
      projects: ["*"]                 # which projects may build here
    - { name: arm, address: tcp://buildkitd-arm.launchway-build.svc:1234, platforms: [linux/arm64], ... }
  registries:
    harbor:
      host: harbor.example.com
      credentials: { username: "robot$ci", password: { store: vault, key: ci/harbor, field: password } }
      pull_credentials: { username: "robot$pull", password: { store: vault, key: ci/harbor-pull, field: password } }
      pull_projects: [shop, "web-*"]          # who gets pull_credentials; empty: every project
      ca_file: /etc/launchway/harbor-ca.crt   # optional
      push:
        - { projects: [shop], repositories: [team/shop, "team/cache/*"] }
        - { projects: ["platform-*"], repositories: ["platform/**"] }
  signing: { key_file: /var/run/launchway/signing/cosign.key, password_file: /var/run/launchway/signing/cosign.password }
  scanner: { db_repository: harbor.example.com/mirror/trivy-db }   # optional
  max_context_mib: 2048
  max_image_mib: 20480
```

- **Push rules.** Nothing is pushed without a rule naming the project and the repository (`*`
  within one level, a final `/**` any depth below). The runner checks that the registry accepts its
  credentials for each repository before building, so a wrong password or a registry that cannot be
  reached fails the step in seconds, saying which.
- **Pull requests and drafts** never push, never write the cache and never read `credentials`:
  the server tells the runner they may not push, and the runner refuses again on its own. They do
  build, scan and make the SBOM, so a pull request shows what its image would be. Their build
  secrets and `pull_credentials` are refused unless the project allows secrets for pull requests;
  without them, they pull anonymously, and the log says once that private base images cannot be
  pulled. The server decides this and tells the runner; a runner that is not told treats a
  pull-request or draft run as not allowed.
- **Who gets which credentials.** A Dockerfile can send anything it can pull to anywhere (a `RUN`
  line with network access), so any credentials a build gets are as good as the project's. The
  builder gets `pull_credentials` only for the projects in `pull_projects`; with `pull_projects`
  empty, **every project's builds, and pull requests that may read secrets, can pull whatever that
  account can read**, from any repository. Give each registry's pull account read access to what
  those projects need and nothing more, and name the projects. Push `credentials` are given to the
  builder only for the registries a build pushes to (for the build cache), and they pull too: a
  project allowed to push to one repository can pull whatever that account can read. Use a registry
  account per project or team (robot accounts scoped to their repositories), not one account for
  everything.
- **Token services.** A registry names where the runner should send its credentials for a token;
  the runner sends them only over HTTPS, unless the registry is marked `insecure: true`.
- **No plain HTTP** to a registry unless it is marked `insecure: true` (for a registry on the
  runner's own machine): the registry client would otherwise fall back to HTTP for private
  addresses when TLS fails, sending the credentials in the clear.

**Signing.** `sign: true` signs with the runner's own key (`images.signing`), a file only the runner
reads: steps never see it, so a signature says the image came from this runner's build. `sign: {
key: …, password: … }` signs with a key from a secret store, which steps of projects that store
allows can read too: such a signature is only as trustworthy as access to that key. Keys are ECDSA
keys as `cosign generate-key-pair` makes them (encrypted, with their password) or PKCS#8 PEM. The
signature, the provenance and the SBOMs are written to the image's repository the way cosign keeps
them (`sha256-<digest>.sig` and `.att`); nothing is sent to a public transparency log. Check them
with:

```sh
cosign verify --key cosign.pub --insecure-ignore-tlog harbor.example.com/team/shop@sha256:…
cosign verify-attestation --key cosign.pub --insecure-ignore-tlog --type slsaprovenance1 harbor.example.com/team/shop@sha256:…
```

Keyless signing (a short-lived certificate for an OIDC identity, and a transparency log entry) is
designed for but not built; `sign: { keyless: … }` is refused for now. Checking signatures at deploy
time (a deploy step that refuses an image not signed by a given key) is a later feature: the image
step records the key that signed (its fingerprint) with the image, which is where that check will
start.

**Provenance.** Two statements, both in-toto with SLSA provenance v1 predicates:

- *Launchway's*, written by the runner from what it knows first hand: the repository, ref and commit
  it checked out and the event that started the run; the libraries and the commits they were used
  at; the Dockerfile, target, platforms, build arguments and the IDs (never the values) of the build
  secrets; the runner, the builders, and the run and step attempt; and the base images by digest.
  Kept as the `provenance` artifact; attached to the image (signed) when the step signs.
- *The builder's*, which BuildKit attaches to the image's index: what it observed while building,
  including whether the build had network access.

What it does not vouch for: a Dockerfile runs code, which may download anything, so a build is not
hermetic, and the builder's statement says so. The run's earlier steps run code too and may change
the workspace: the source is "this commit, as the run's earlier steps left it".

**Several platforms.** A builder builds for its own architecture, and for others only through
emulation, which needs `binfmt_misc` handlers installed on its machine (a privileged change to the
node, which Launchway does not make). Give each architecture a builder of its own, with
`platforms:`: a step that asks for several platforms is built on each builder at once, and the
images are pushed as one index, with each builder's provenance. A platform no builder of the
project builds is refused before anything starts, naming what the builders build.

**On the run page**, the Images tab shows each image: its digest, the pushed name and tags (or why
nothing was pushed), size, platforms, whether and with which key it was signed, the provenance and
the SBOM (links to the artifacts), the scan's counts and policy, and how much of the build came from
the cache. **The Images page** finds the runs that built a digest (the index's or a platform's), and
the latest images of a repository or tag.

- **API**: `GET /api/v1/runs/{id}/images`, and
  `GET /api/v1/images?digest=sha256:…` or `?repository=host/path&tag=…` or `?project=…` (only projects
  the caller may see).
- **Runner settings** are `images:` in a runner's configuration; with the Helm chart,
  `runner.images` (unset by default: runners then take no image steps). `launchway run
  --images-config FILE` builds image steps on your own machine, with an `images:` section of your own.

Image steps and digest changes need schema 15; use them once every server and runner is upgraded.

## Deployments

A deploy step changes what runs in an environment. It commits the new version to a GitOps
repository, asks Argo CD to sync if it must, and waits until the application (and its Argo
Rollouts rollout, if it has one) is healthy on that version. A rollback step reverts that commit
when a step guarding the deployment does not succeed.

```yaml
steps:
  build:
    container: kaniko
    run: ./ci/build.sh                 # writes tag=1.4.2 to $LAUNCHWAY_OUTPUTS

  deploy-prod:
    kind: deploy
    needs: build
    when: { branch: main }
    environment: prod                  # configured on the server: the lock and the record
    service: shop                      # default: the project's name
    version: { output: build.tag }     # what "current version" shows; default { run: short_sha }
    timeout: 30m                       # the whole step, commit to healthy; default 30m
    argocd:
      application: shop
      write_back:
        repo: gitops                   # a repository the deploy runner's configuration names
        branch: main
        changes:
          - file: apps/shop/rollout.yaml
            image: team/shop           # every reference to this image; none at all is an error
            tag: { output: build.tag }
          - file: helm-values/shop.yaml
            set: image.tag             # a YAML path; comments and layout are kept
            value: { output: build.tag }
      sync:
        request: auto                  # auto (only if the application does not sync by itself) | always | never
        resources:                     # sync, and judge, only these; default: the whole application
          - { kind: Rollout, name: shop, namespace: shop }
        prune: false
        windows: wait                  # wait | fail, while a sync window holds the sync; never overridden
      health: resources                # application | resources; default resources when listed
    rollout: { name: shop, namespace: shop }   # also wait until this rollout is healthy

  smoke:
    needs: deploy-prod
    image: curlimages/curl:8.10.1
    run: ./ci/smoke.sh https://shop.example

  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]                     # the steps that guard the deployment
    timeout: 15m                       # default 15m
```

- **Pin by digest**: `digest: { output: image.digest }` in place of `tag:` writes
  `repository@sha256:…` (see [Container images](#container-images)).
- **Values** are written in place, or are `{ output: step.key }` (an output of a step this step
  needs), `{ run: sha | short_sha | branch | tag | number }` or `{ input: name }` (a
  [run input](#run-inputs); `environment` may be `{ input: name }` too, of a choice input). There is
  no expression language.
- **"Deployed" means**, in one observation: the revision Argo CD has synced, for the source from
  the write-back repository, includes the commit; the judged resources (or the whole application)
  are Synced and Healthy; and the rollout, if one is named, is Healthy with its stable ReplicaSet on
  the new pod template and its current spec observed. Missing or stale observations are never a
  pass. A failed sync of the commit, a Degraded application or resource, or an aborted or Degraded
  rollout (named with its failed analysis run and metric) fails the step; so does the deadline,
  saying what was last observed. The rollout's own analysis runs are the verdict, shown read-only.
- **A deployment that changes nothing** (Git already holds the version) commits nothing, and is
  still synced and verified.
- **Sync windows** are never overridden. The deploy runner reads the application's AppProject and
  never asks for a sync a window does not allow: with `windows: wait` the step waits (until its
  deadline) for a window to allow it; with `windows: fail` it fails, saying which window. Argo CD
  itself would hold such a sync and then, when the window ends, mark it done without applying
  anything, so asking anyway is never safe. An application that syncs by itself and is held by a
  window shows as held.
- **Argo CD learns of the commit** from the refresh annotation the deploy runner sets right after
  it (Argo CD does not look at Git again by itself for an application nothing has changed in), or
  from a Git webhook to Argo CD if you have one; `refresh: false` on a target turns the annotation
  off.
- **The rollback rule**: a rollback step runs if and only if its deployment was made and one of the
  steps it needs did not succeed (it failed, or was skipped or canceled because something failed);
  otherwise it is skipped as not needed. It reverts the deployment's commit (never a forced reset),
  asks Argo CD to sync if it must, and waits until the revert is synced and healthy. A revert that
  does not apply (later commits changed the same lines) fails the step and leaves Git alone; the
  deployment then says it needs a person. A deployment that changed nothing has nothing to revert.
- **One deployment at a time**: each service of a project has one lock per environment, taken when
  its deployment opens and released when the deploy step, its rollback step and every step on a
  needs path between them are done. A deploy step that finds it held waits. With `queue: newest`
  (the default), a run waiting for the lock is skipped as superseded when a newer run of the same
  service starts waiting; with `queue: all` they deploy in turn. `supersede: true` never cancels a
  run that holds an open deployment: it finishes, and the newer run waits.
- **Where deploy steps do not run**: draft runs, pull-request runs and `launchway run`. A deploy step
  is never reused by a re-run, a re-run cannot start inside a deployment (re-run from its deploy
  step), and a run that made a deployment is not resumed.
- **Validation** says what is wrong and on which line: a value from a step that is not needed, a
  rollback that does not need its deploy step, two deploy steps of one service and environment that
  could run at once, and, against the server's configuration (in `/api/v1/validate` and when a run is
  created), an unknown environment or a project the environment does not allow.
- **Not yet**: syncing to a revision without a commit, a "roll back to…" button that starts a run,
  pausing a rollout, and `kind: analysis` arrive in later releases.

### Controlling a rollout: promote, abort, retry

A canary can stop at a pause and wait: for a smoke test of the canary, for a person, or both.

```yaml
  deploy-prod:
    kind: deploy
    # ... as above ...
    rollout: { name: shop, namespace: shop, until: paused }   # end at the first indefinite pause
    on_cancel: abort                   # leave (default) | abort: abort the rollout if the run is canceled

  canary-smoke:
    needs: deploy-prod
    image: curlimages/curl:8.10.1
    run: ./ci/smoke.sh http://shop-canary.shop.svc

  approve-full:
    kind: approval
    needs: canary-smoke
    deployment: deploy-prod            # shows the live rollout; rejected by itself if it is aborted
    message: "The canary passed its smoke test. Promote shop to 100%?"

  promote:
    kind: rollout
    needs: approve-full
    deployment: deploy-prod
    action: promote                    # promote | promote-full | abort | retry
    until: healthy                     # healthy | paused (the next indefinite pause); not for abort
    timeout: 30m

  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true                  # abort the rollout (seconds), then revert (what lasts)
```

- **An indefinite pause** is a pause step without a duration, a blue-green pause without automatic
  promotion, or an analysis that ended inconclusive. A deploy step watching until healthy says what
  to do there with `rollout.pauses`: `wait` (the default: someone presses Promote on the run page),
  `promote` (Launchway promotes each such pause once; never an inconclusive analysis, which waits
  for a person) or `fail`. With `until: paused` the step ends at the first one, and a `kind: rollout`
  step naming the deployment must carry it on.
- **A rollout step** acts on its deployment's rollout and then watches it: until healthy, until the
  next indefinite pause after the one it promoted, or, for `abort`, until the rollout is aborted. It
  gives its command from the first fresh observation, so the command expects what the rollout was
  really doing then.
- **Commands.** Promote, Promote full, Abort and Retry are commands, given by a person on the run
  page (or `POST /api/v1/runs/{run}/deployments/{deployment}/promote`, `promote-full`, `abort`,
  `retry`, with an optional `{"comment": "..."}`), by a rollout step, by `pauses: promote`, or by
  `on_cancel: abort`. Each records what it expects of the rollout: the deployment's pod template and
  the canary step it was at. A deploy runner serving the target applies it only if the rollout is
  still there; a rollout already past that point records "already applied", and one now on another
  pod template (a change outside Launchway replaced it) records "not applied". So a second click on
  the same pause, on any server, is refused naming who already asked, and an abort only ever stops
  this deployment's version. A command no deploy runner has taken can be withdrawn
  (`POST .../commands/{command}/withdraw`); an abort withdraws the ones not taken yet and goes first.
  Commands given while no deploy runner reaches the cluster wait, and are applied when one does.
  Every command, withdrawal and refusal is in the audit trail (`deployment.promote` and so on).
- **Who may.** A command needs the operator role on the run's project from a binding that covers
  the deployment's environment (see `environments:` under [Roles](#roles)). On an environment marked
  `protected: true`, the person who started the run may not promote or retry it (unless
  `approvals.allow_self` lets them approve their own runs); anyone with the operator role there may
  abort it. An approval that names a deployment needs the same role on its environment.
- **The deployment** stays open, holding the environment's lock, until the deploy step, its rollout,
  rollback and linked approval steps, and every step on a needs path between them are done. It is
  "deployed" only if its rollout got to healthy; a deployment left at a pause, or aborted, is
  "failed" unless a rollback reverted it. A deploy step that stops at a pause does not change the
  environment's current version; the rollout step that gets it healthy does.
- **Rolling back with `abort_first`** aborts the rollout first if it is still on the deployment's pod
  template, then reverts the commit and waits until the previous version is healthy. Git still
  names the bad version until the revert lands, which is why the revert is never skipped.
- **`on_cancel: abort`** gives an abort command when the run is canceled by hand while the
  deployment is open; the deployment ends as abandoned. Git still names the version: an application
  that syncs by itself keeps it, aborted, until someone reverts it.
- **With run inputs, loops and cleanup steps.** The deploy step's version and tag may be run inputs.
  Steps inside the deployment may be loops (`for_each`, a smoke test per region, say); rollout steps
  and approvals that name a deployment are never repeated, keep or fetch no artifacts and report
  nothing. Neither may need, directly or not, a step that runs after failures
  (`when: { upstream: failed | always }`), and such an approval takes no upstream rule itself: a
  cleanup step's success would let a failed check be promoted past. Make the cleanup step need them
  instead.
- **Not yet**: promoting past an analysis step that is still running, and promoting a rollout paused
  through its spec (`spec.paused`), which needs a change to the spec Launchway does not make.

**Who does the work.** Deploy steps are done by a **deploy runner**: `launchway runner` with a
`deploy:` section, in or near the cluster, with the token of a **runner pool**. Only a pool's token
can take deploy work, and only for the targets the pool may serve; the shared runner token, which
build runners running anyone's code hold, cannot claim a target or report that a deployment is
healthy. The deploy runner talks to the cluster through the Kubernetes API with its own identity;
it never runs a script, and the servers hold no cluster credentials. It reads Applications,
Rollouts and AnalysisRuns; it changes only an Application's operation (to request a sync) and its
refresh annotation, and, for commands, the status of the rollouts it is allowed to control. One
deploy runner observes each target at a time; another takes over within a lease period when it
stops. It also observes, every 30 seconds and at most 100 per target, the services nothing is
deploying, so the Environments page shows when what runs differs from the last deployment.

```yaml
# server: launchway.yaml
environments:
  - { name: prod, target: homelab, projects: [shop, "mcp-*"], queue: newest }
runner_pools:
  - { name: deploy-homelab, token_env: LAUNCHWAY_POOL_DEPLOY_HOMELAB_TOKEN, targets: [homelab] }

# deploy runner: runner.yaml (launchway runner --config runner.yaml, with the pool's token)
server: https://launchway.example
deploy:
  targets:
    homelab:
      kubernetes: { in_cluster: true }     # or { kubeconfig: ..., context: ... }; never the current context
      argocd_namespace: argocd
      applications: [ { projects: [shop], names: [shop] } ]
      rollout_namespaces: [shop]
  repos:
    gitops:
      url: https://git.example/team/gitops.git
      argocd_url: http://gitea-http.gitea.svc:3000/team/gitops.git   # as Applications write it, if different
      credentials: { store: vault, key: ci/gitops, field: token }     # a name, never a value
      paths: [ { projects: [shop], files: ["apps/shop/**", "helm-values/shop.yaml"] } ]
secrets:
  vault: { type: vault, address: https://vault.example:8200, role: launchway-deploy }
```

Pool tokens are at least 32 characters, and every server must have the same environments and
pools. With the Helm chart, `delivery.environments` and `delivery.deployRunner.*` run a deploy
runner in the cluster with the least access it needs: get, list and watch on the named
Applications (and patch, which a ValidatingAdmissionPolicy limits to the operation and the refresh
annotation), get on AppProjects (for their sync windows), get, list and watch on rollouts and
analysis runs in the listed namespaces, and nothing on pods or secrets. To let it apply commands,
name the rollouts in `delivery.deployRunner.rbac.controlRollouts` (`{ shop: [shop] }`): it then
gets patch on `rollouts/status` for exactly those, and a second admission policy holds the patch to
the fields promote, promote full, abort and retry set.

**The run page** shows a deployment panel for deploy and rollback steps: environment, application,
version and previous version, the commit and its revert, sync and health, the rollout's steps with
the current one, weights and pauses, its analysis runs with each metric's counts, replicas, and the
timeline of what was observed, with "observed 14s ago by runner X" (stale data is greyed, never
green). **The Environments page** shows each service by environment: the current version, since
when and from which run, the lock holder and the runs waiting, the last deployment's state, and its
history; protected environments; and, for a service nothing is deploying, what was last seen of
it and whether it drifted: "OutOfSync" (Argo CD says what runs differs from Git), "live differs
from the last deployment" (another commit or pod template runs), or the application is gone. When
nothing can be observed the page says why: no deploy runner serves the target, the runner cannot
reach the cluster (with its error), or Argo CD has not compared the application for minutes. The
deployment panel shows the buttons a person may use, the reasons for those they may not, and the
commands given, with their state. API: `GET /api/v1/environments`,
`GET /api/v1/deployments?environment=&project=&service=`, `deployments` in a run, and
`GET /api/v1/runs/{run}/deployments/{deployment}/controls`.

Deploy steps need schema 11; rollout steps, `until: paused`, `pauses`, an approval's
`deployment`, `abort_first`, `on_cancel: abort`, commands, `protected:` and `environments:` on access
bindings need schema 14. Use them only once every server and runner is upgraded, and take them out
before rolling back (see [docs/development/migrations.md](docs/development/migrations.md)).

## Policies

Policies are rules an organisation sets for every pipeline, which the pipeline's authors cannot
switch off: they live in the server's configuration, or in a Git repository the server reads at a
pinned tag, never in the pipeline file. Each policy is a name, a scope, a mode and rules taken from
a fixed catalogue. There is no policy language: a rule is a typed setting with a fixed meaning,
checked when the configuration (or the policy file) is read, with its file and line.

```yaml
# launchway.yaml
policy:
  policies:
    - name: prod-evidence
      description: What reaches production shows passing tests, a clean scan and a second pair of eyes.
      mode: enforce                        # or report
      scope: { projects: [shop, "pay-*"], environments: [prod] }
      rules:
        tests: { min: 1 }                  # the steps before the deploy step reported tests, none failed
        findings: { max_severity: medium, scanners: [semgrep, gitleaks] }
        sbom: {}                           # a step before it made an SBOM
        deployed_images: { digest: true, built_by_run: true, signed: true, scanned: true }
        approvals: { min: 1 }              # by someone other than the person who started the run
        refs: { branches: [main], tags: ["v*"] }
    - name: pipeline-shape
      mode: report
      rules:
        library_versions: { pinned: tag }
        required_templates: [ { library: ci-templates, template: security-scan, versions: ["v3", "v3.*"] } ]
        step_images: { digest: true, registries: ["harbor.example.com/**"] }
        max_timeout: 2h
        forbid_secrets: { events: [pull_request, draft] }
        image_push: { scan: high, sign: runner, sbom: true, provenance: true, registries: [harbor] }
```

A run of `shop` on `main` that reaches `deploy-prod` without test results fails there, before
anything is committed or synced, and says why, on the graph, in the step and in the run's
**Policy** tab:

```
deploy-prod failed. blocked by policy: policy prod-evidence (enforce), rule tests: no step before
deploy-prod reported test results; needs at least 1 test result(s) from the steps before
deploy-prod, none failed. Grant a waiver or change what was found, then resume the run (or re-run
it from deploy-prod)
```

**Where policies are judged.** Each rule is judged at one point, in the same database transaction
as what the decision allows, so a decision and the step it lets through are stored together or not
at all, whatever server or restart comes between:

| Point | When | What a blocking decision does |
|---|---|---|
| run start | a run is created, re-run or resumed | each step of the run fails as it would start, saying why; nothing reaches a runner |
| before deploying | a deploy step is about to take its environment's lock; a rollout step is about to promote, promote fully or retry | the step fails before anything reaches the cluster or Git |
| before an image push | an image step that would push is about to be handed to a runner | the step fails before any runner sees it |

Rollback steps and aborts are never judged: they return to what ran before, which is what a
failure needs. A step whose policies pass, and a run no policy governs, run exactly as before.

**The rule catalogue.**

| Rule | Judged | Passes when |
|---|---|---|
| `library_versions: { pinned: tag \| commit }` | run start | every shared library the run uses (and every override) resolved to a tag or a full commit id (`commit`: only a commit), never a branch |
| `required_templates: [ { library, template, versions } ]` | run start | a step uses each template of each library (the server's name for it), at a version matching `versions` that is a tag or a commit |
| `step_images: { digest, registries }` | run start | every image a step or container runs in is pinned by digest (`name@sha256:…`) and/or its repository matches `registries` (`*` within a part, a final `/**` any depth; `alpine` is `docker.io/library/alpine`) |
| `max_timeout: 2h` | run start | no step's timeout is longer (approvals' waits are not counted) |
| `forbid_secrets: { events: [...] }` | run start | a run of one of these events (`pull_request`, `draft`) that may read secrets has no step that asks for one |
| `tests: { min }` | before deploying | the steps the deploy step needs (directly or not) reported at least `min` test results (default 1), none failed or errored |
| `findings: { max_severity, per_tool, scanners }` | before deploying | those steps reported findings (no report is not "no findings"), none worse than `max_severity` (`none`, `low`, `medium`, `high`, `critical`; `unknown` counts only against `none`), none from a tool worse than its `per_tool` severity, and every tool in `scanners` reported |
| `sbom: { min_components }` | before deploying | one of those steps made an SBOM of at least `min_components` (default 1) |
| `deployed_images: { digest, built_by_run, signed, scanned }` | before deploying | every image the deployment writes is pinned by digest, was built by an image step before it in this run (its digest or a platform's), was signed, and was scanned without its scan failing it. A deployment that writes no image reference fails the rule |
| `approvals: { min }` | before deploying | at least `min` people (default 1) other than the person who started the run approved an approval step before it |
| `refs: { branches, tags }` | before deploying | the run is of a branch or tag matching the patterns (a pull-request or draft run is of neither) |
| `image_push: { scan, sign, sbom, provenance, registries }` | before an image push | the step scans with `mode: enforce` and a `fail_on` at least as strict as `scan`, signs (`runner`: with the runner's own key; `any`), makes an SBOM and provenance, and pushes (and writes its cache) only to the listed registries. A step that pushes nothing passes |

Evidence is what the run's events say: the summaries kept with each step attempt (test totals,
findings by severity and by tool, the SBOM, what an image step built and signed), approvals and who
gave them, and the resolved values of the deploy step's changes. A step a re-run reused is judged
by what it reported in the run where it ran. Evidence that is missing never passes a rule.

**Scope and modes.** `scope` narrows a policy to `projects`, `environments` (deploy rules only: a
policy scoped to environments may not have rules of other points), `branches` and `events`
(patterns as elsewhere; an empty list matches everything; a run without a branch does not match a
policy scoped to branches). A policy in `enforce` mode stops the step or the run; in `report` mode
it is judged, recorded and shown the same way, as a warning, and never changes an outcome. Start
new policies in report mode, watch their verdicts on the Policy page, then enforce them.

**Every decision is recorded.** Each decision is a `policy.evaluated` event in the run's log, with
the policies as judged (their source and version), the facts judged, the waivers considered and
the time, so it can be reproduced from the log alone; `launchway policy run <run-id>` prints it.
The run's **Policy** tab shows each decision rule by rule: what was found, what would satisfy the
rule, links to the evidence (the Tests, Security, Images and Artifacts tabs, approval steps), and
the waiver that passed a rule. The **Policy** page lists the policies in force, where each comes
from and at which version, their scope, mode and rules, and recent verdicts of the runs you may see.

**Waivers.** A waiver lets one rule of one policy pass for a project, optionally only in one run,
one environment, or (for `findings`) for one finding by its fingerprint, until it expires. A reason
and an expiry are required (at most `policy.waivers.max_days`, default 30). Waivers are a log of
grants and revocations, written to the audit trail (`policy.waive`, `policy.revoke_waiver`), shown
wherever the verdict they changed is, and expire on their own: a decision made after expiry does
not count them, even in a run that started before. A rule is judged again when the run is resumed,
so the usual path is: grant a waiver, then resume. Who may waive:

```yaml
policy:
  waivers:
    allow: admin      # admin (the default), operator or none: the role needed on the project
    max_days: 30
```

A deploy rule needs that role on the environment too (`environments:` on access bindings): a
waiver that names no environment needs it from a binding not limited to environments. A limited
API token never grants more than its limits. On an environment marked `protected: true`, the person
who started a run may not waive its deploy gate (unless `approvals.allow_self` lets people approve
their own runs): a waiver of theirs for the run is refused, and a project-wide waiver they granted is
not applied to their runs there. A policy source that cannot be loaded cannot be waived.

**Policies from Git.** A source is one of the server's [libraries](#shared-libraries), pinned to a
tag or a full commit id (a branch is refused), and a directory of `*.yaml` files in it, each with a
`policies:` list as above:

```yaml
libraries:
  - { name: org-policies, repo: https://git.example.com/platform/policies.git }
policy:
  sources:
    - name: org
      library: org-policies
      version: v3            # a tag, or a full commit id
      path: policies         # the default
      projects: ["*"]        # the projects it may govern; the default is every project
      refresh: 1m            # how often servers look at the tag again
      stale_after: 1h        # how long a version loaded earlier is used while the repository cannot be reached
```

Changing policies is then a reviewed commit and a new tag, and moving `version` in the
configuration. Every server loads each source and stores what it loaded in the database; every
server judges from what is stored, so all of them reach the same verdict from the same inputs.
Policy names must be unique across the configuration and the sources.

**Failing closed.** A source governs the projects it covers whatever is in it, so when its
policies cannot be known, every run of those projects is refused at every policy point (run start,
deploying, pushing an image), naming the source and the reason, and the Policy page shows the
source as failing closed. That happens when no server has loaded it yet (just after it is added),
when the version does not exist or is a branch, when a file does not parse or uses a rule this
server does not know (the message names the file and line), when two sources define the same
policy, when the version stored is not the one this server is configured with (during a change of
`version`, until a server with the new setting loads it), or when it has not been confirmed for
longer than `stale_after` because the repository cannot be reached. A short outage of the
repository does not refuse anything: the version loaded before stays in use until then. Policies in
the server's own configuration are checked when the file is read: a mistake stops the server from
starting, with the file and line.

**What policies do not protect against.** They judge what Launchway does: a person with access to
the cluster, the GitOps repository or the registry can still change what runs without Launchway,
and Argo CD can sync a commit Launchway did not make. Facts come from what steps report: a step
that writes a test report claiming tests passed, or a scanner configured to find nothing, satisfies
the rules as far as the report goes (keep scanning steps in shared templates that
`required_templates` asks for). A run started by a push records the commit's author as its starter,
which anyone can set in Git. Running steps unrestricted is the runner's setting, never the
pipeline's, so it is not a rule. Signatures are recorded by the image step, not verified again at
deploy time.

**API and commands.** `GET /api/v1/policies` (the policies in force, their sources, recent verdicts
of runs you may see), `GET /api/v1/runs/{id}/policy` (a run's decisions and the waivers that apply),
`GET /api/v1/waivers?project=&active=1`, `POST /api/v1/projects/{name}/waivers` with `{"policy",
"rule", "reason", "expires_in_hours", "run", "environment", "finding"}`, and
`POST /api/v1/projects/{name}/waivers/{waiver}/revoke` with an optional `{"reason"}`. On the command
line: `launchway policy list`, `launchway policy run <run-id>`, `launchway waivers list
[--project P] [--active]`, `launchway waivers grant <project> --policy P --rule R --reason "..."
--expires 72h [--run R] [--environment E] [--finding F]` and `launchway waivers revoke <project> <id>`.
With the Helm chart, policies are `config.policy` in the values (unset by default). Policies need
schema 16: configure them only once every server runs a release that knows them, and take them out
before rolling back (see [docs/development/migrations.md](docs/development/migrations.md)).

## Runners on other machines and clusters

A runner only ever connects out to the server, over HTTPS, so it can sit in any network that can
reach the server: another machine, another cluster, a customer's data centre. Each runner belongs
to a **pool**, and a pool says which projects' work its runners may take.

### Connecting a runner

Someone who manages runners (the role `runner_admin` names on every project; admin by default)
makes a **join token** for a pool, on the Runners page (**Add a runner**), with
`launchway runners join-token --pool web`, or with `POST /api/v1/runner-join-tokens`. It is shown
once, stored only as a hash, lasts an hour by default (at most 7 days) and enrols one runner unless
it says more. On the runner's machine:

```sh
launchway runner --server https://ci.example.com --join lwj_… --config /etc/launchway/runner.yaml
```

The runner makes an Ed25519 key, which never leaves it (`<data>/identity/key`, readable by the
runner's user only), registers the public half, and gets a runner ID from the server. From then on
it starts without the join token, using its identity; the join token is used up. In a container,
pass `LAUNCHWAY_JOIN` instead of `--join`. In Kubernetes with the chart, put a join token in a
Secret and set `runner.join.existingSecret`: each pod enrols when it starts, so make the token
ephemeral (each runner is revoked when it leaves) with enough uses for every pod start. The page
shows the commands for each, ready to copy.

The Runners page shows each runner's pool, identity (key fingerprint and age, who made its join
token), version, OS and architecture, labels, what its `accept:` section allows, and what it is
running. **Revoke** refuses it at once on every server; the steps it held are queued again for
other runners. **Rotate key** makes it move to a new key at its next request (runners also rotate
every 30 days). Every join, rotation, revocation and refusal is in the audit trail.

The shared runner token (`LAUNCHWAY_RUNNER_TOKEN`, `--token`) and deploy pools' tokens keep
working as before, and runners using them are marked on the Runners page: a shared token gives no
identity of its own, so the server cannot tell such runners apart, trusts the runner ID each one
gives, and anyone who has the token can take that pool's work. Enrol them, then set
`require_identity: true` on the pool to refuse the shared token.

### Pools

```yaml
# launchway.yaml
runner_pools:
  - name: web                # build runners for the web team's projects
    kind: build
    projects: ["web-*"]
    events: [push, manual]   # optional: only runs started this way (not pull requests, say)
  - name: default            # the shared token's pool; listed only to limit it
    kind: build
    projects: ["*"]
    require_identity: true   # refuse the shared token (turn the built-in runner off first)
  - name: deploy-prod        # a deploy pool, as in Deployments
    token_env: LAUNCHWAY_DEPLOY_POOL_TOKEN
    targets: [prod-cluster]
```

Without the default pool listed, it serves every project no other build pool serves. A runner is
only ever given steps of runs its pool serves; the server checks it when it looks for work and
again when it grants it, whatever the runner says. A pipeline may choose among the pools that serve
its project, and ask for labels the runner must have:

```yaml
runner: { pool: gpu, labels: [cuda] }   # the whole pipeline: a run's steps share one workspace
```

Labels come from the join token and from the runner (`labels:` in `runner.yaml`, or `--labels`).
A run that no runner may take says why on its page: no pool serves the project, the pool it asks
for does not, or none of the runners seen lately has the labels or accepts it.

### Proxies and private certificates

The runner uses `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, or `proxy:` and `no_proxy:` in
`runner.yaml`, which win. `ca_file:` adds certificates to the system's, for a server or a proxy
whose certificate a private authority signed, such as one that inspects TLS. Runners prove who they
are inside the request, not with client certificates, so this works through proxies that inspect
TLS. A runner waits for work in requests of up to 25 seconds; a proxy must allow idle requests of
at least 30.

### What a runner trusts the server with

Whoever controls the server decides what runs on the runners: which commit, which script, which
image, which secrets a step asks for. A runner's **`accept:`** section is its own limit on that,
checked on the runner before anything runs (no clone, no secret looked up, no container started).
The server cannot change it, because it lives only in the runner's file. A job it does not allow is
refused: the step fails without retries, saying why, and the refusal is audited.

```yaml
# runner.yaml
accept:
  projects: [shop, "web-*"]
  repositories: ["https://git.example.com/shop/*"]   # the run's and every library's
  images: ["registry.example.com/*", "docker.io/library/golang@sha256:*"]
  images_by_digest: false
  executors: [podman, buildkit]                      # buildkit for image steps, deploy for deploy runners
  environments: [staging]                            # deploy runners
  pull_requests: false
  drafts: false                                      # pipeline text that is not in Git
  max_timeout: 2h
  env_deny: [LD_PRELOAD, BASH_ENV, ENV, "GIT_*"]
```

Every runner also refuses, whatever its settings: IDs from the server that do not look like the
server's own (they become paths); on a runner on another machine, a run whose source is a directory
of that machine (only `launchway run` does that); and an enrolled runner does not fall back to
running steps directly on its host when no Podman or Docker is found, unless `executor: host` is
chosen.

What `accept:` does **not** stop: a server that has been taken over can still run any script it
likes for a project, repository and image the runner accepts, with that runner's network reach and
the secrets its stores give those projects; it can choose which accepted commit runs, and replay an
old one. Keep each runner's `accept:`, secret stores and network as narrow as its pool's work, and
give production its own pool.

What the server checks of runners: each enrolled runner's requests carry a short-lived access
token (15 minutes) that it gets by signing a 60-second assertion with its key; each assertion is
accepted once, by any server. A step's lease belongs to the runner that took it: its logs, results,
artifacts and heartbeats are refused from any other runner. A runner can only leave as itself, and
only hears about runs of its pool's projects.

## Secrets

A step names the secrets it needs. Each one becomes an environment variable:

```yaml
steps:
  push:
    image: alpine:3.22
    secrets:
      REGISTRY_PASSWORD: { store: vault, key: apps/harbor/robot, field: password }
      NPM_TOKEN:         { store: env,   key: NPM_TOKEN }
    run: echo "$REGISTRY_PASSWORD" | some-login
```

**The server never sees a secret value.** The pipeline, the server and the UI hold only the
reference (`vault:apps/harbor/robot#password`); the runner looks the value up just before the step
runs, inside the network that owns the secret, and replaces it with `***` in the step's logs and
outputs. Values shorter than 4 characters are not masked, and the log says so.

Each runner has its own stores:

| Type | `key` is | Settings |
|---|---|---|
| `env` | an environment variable of the runner process (not `LAUNCHWAY_*`) | none |
| `file` | a file name in a directory, such as a mounted Kubernetes secret; nothing outside it can be read | `dir` |
| `vault` | a path in a KV version 2 engine of HashiCorp Vault or OpenBao; `field` is required | `address`, `mount` (default `secret`), `namespace`, `ca_file`, and one of `token_env`, `token_file`, or `role` for Kubernetes auth (with `auth_path`, default `kubernetes`, and `jwt_file`, default the pod's service account token) |

A runner has only the stores you configure. The one exception is `launchway run` on your own
machine, which also has an `env` store so a pipeline can read your own environment. A server or
shared runner gets an `env` store only if you define one, because it lets pipelines read the
runner's environment.

Any pipeline can ask for any key, so every store takes allow-lists, checked on the runner:
`projects` and `keys`, lists of patterns where `*` does not cross a `/`. An empty list allows
everything.

On Kubernetes, a step's environment (secrets included) is held in memory inside the run's pod
while the step runs, where the run's other containers could read it. Do not put containers you do
not trust in a pipeline that uses secrets.

The built-in runner reads its stores from `runner.secrets` in the server's configuration; `launchway
run` reads the same map from `--secrets-config FILE`, and `launchway runner` from either
`--secrets-config FILE` or the `secrets:` section of its `--config` file (see [Commands](#commands)):

```yaml
runner:
  secrets:
    vault:
      type: vault
      address: https://vault.example:8200
      role: launchway          # Kubernetes auth
      projects: [payments, "web-*"]
      keys: ["apps/*/*"]
    mounted:
      type: file
      dir: /var/run/launchway-secrets
```

A secret that does not exist or may not be read fails the step, naming the store and key. A store
that cannot be reached (no answer, sealed, 5xx) is asked again for 30 seconds, and then the step is
handed back to be run again later. `launchway run` uses the project name `local`.

## Sign-in and access

Out of the box Launchway has no sign-in (`auth.mode: none`): everything is done as the configured
`user`, with full rights. That suits a server on your own machine and nothing else, so **a server
without sign-in refuses to start unless it listens only on a loopback address** (`127.0.0.1`, `::1`,
`localhost`). If something in front of it already decides who may reach it, say so with
`auth.allow_anonymous: true`. The UI shows "No sign-in" on every page while this is the case.

### Signing in with OpenID Connect

Any OpenID Connect provider works (Authentik, Keycloak, Okta, Entra ID, Dex and others), through
discovery, without anything specific to one of them:

```yaml
listen: 0.0.0.0:8080
server:
  trust_proxy: true          # only behind a reverse proxy; see below
auth:
  mode: oidc
  oidc:
    issuer: https://sso.example.com/realms/acme   # exactly as in the provider's discovery document
    client_id: launchway
    client_secret_env: LAUNCHWAY_OIDC_SECRET      # or client_secret_file: /run/secrets/oidc-secret
    redirect_url: https://launchway.example.com/auth/callback
    # scopes: [openid, profile, email, groups]    # the default
    # username_claim: preferred_username          # the default; then email, then sub
    # groups_claim: groups                        # the default
  idle_timeout: 8h        # the default: a session unused this long ends
  session_lifetime: 24h   # the default: every session ends this long after sign-in
  access:
    - { group: platform-admins, role: admin }
    - { group: payments,        role: operator, projects: ["payments-*"] }
    - { group: everyone,        role: viewer }
    - { user: dana,             role: operator, projects: [web] }
```

Register Launchway with the provider as a confidential web application using the authorization
code flow, with `redirect_url` as its redirect URI. Launchway sends PKCE (S256), `state` and
`nonce`, and checks the ID token's signature, issuer, audience, expiry and nonce. The client secret
is read from an environment variable or a file, never from the configuration file.

The provider must put the person's groups in the ID token, or in its userinfo response, under
`groups_claim`; most providers need a claim mapping for that. Entra ID sends group object IDs
rather than names (use the IDs in `access`), and leaves groups out for people in more than 200.
Choose a `username_claim` that people cannot change themselves at the provider: `user:` bindings,
and every record of who did what, use it.

The endpoints are `GET /auth/login?next=/path` (where to return afterwards; only paths on this
server are accepted), `GET /auth/callback`, `POST /auth/logout`, and `GET /api/v1/me`, which says
who you are and what you may do. `GET /api/v1/info` then reports only the version and that sign-in
is on.

### Roles

| Role | May |
|---|---|
| `viewer` | see a project and its runs, logs, events, pipeline files and approvals; see the runners; use the Playground; open the pipeline editor |
| `operator` | everything a viewer may, and start, cancel, resume and re-run runs, approve and reject approvals, save pipelines, and start draft runs if `drafts.allow` is `operator` |
| `admin` | everything an operator may, read the audit trail, start draft runs if `drafts.allow` is `admin`, save a pipeline that has problems, and waive a policy's rule if `policy.waivers.allow` is `admin` (the default; see [Policies](#policies)) |

Each entry in `access` grants a role to one `group` (from `groups_claim`) or one `user` (from
`username_claim`), on the projects matching one of `projects`, or on every project if there are
none. Patterns use `*` (any characters but `/`), `?` (one character) and `[...]`. Where several entries
match, the highest role wins. Someone matching none can sign in but sees nothing. The audit trail
needs `admin` from an entry without `projects`.

An entry may also list `environments` (patterns, as for projects). It then grants its role on
deployments (the commands of [Controlling a rollout](#controlling-a-rollout-promote-abort-retry),
and approvals that name a deployment) to those environments only; on its projects otherwise it
grants as usual. An entry without `environments` covers every environment. An API token limited to
a role or projects never grants more there than its maker has:

```yaml
auth:
  access:
    - { group: developers, role: operator, projects: [shop], environments: [staging] }
    - { group: sre, role: operator, environments: [prod] }
```

Rights are worked out on every request from the configuration and the groups the provider gave at
the person's latest sign-in, and checked by the server whatever the UI shows. A run, project or
approval you may not see answers `404`, exactly as if it did not exist; one you may see but not
change answers `403`. Lists and the live stream contain only what you may see.

### Sessions

Signing in sets a session cookie (`HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` when the
browser uses HTTPS). The session is a random 256-bit value; the database keeps only its SHA-256, so
every server accepts it and a copy of the database does not contain it. Signing out ends it on
every server, and live streams it opened close within 15 seconds. What the UI fetches by itself,
such as refreshing a list, does not count as activity for `idle_timeout`.

### API tokens

Scripts and the [command line](#commands) use API tokens: `Authorization: Bearer lw_...`. Make one
on the **API tokens** page with a name and an expiry. Every token expires: by default after 90
days, at most after `auth.tokens.max_days` (default 365, up to 3650). It is shown once; only its
SHA-256 is stored. A token acts as you, with your rights as of your latest sign-in, and can be
revoked at any time. It may be limited further:

- to a **role** (viewer, operator or admin), no higher than the highest you have: a viewer token
  of an admin reads and changes nothing;
- to **projects**, by names or patterns such as `pay-*`: elsewhere it is as if the projects did not
  exist. A token limited to projects is never an admin of every project, so it cannot read the
  audit trail.

The page shows when and from which address each token was last used. Admins of every project also
see everyone's tokens there and may revoke any (`GET /api/v1/admin/tokens`,
`DELETE /api/v1/admin/tokens/{id}`), for example one that leaked. Tokens can only be made from a
signed-in browser, not with another token. Over the API: `GET /api/v1/tokens`,
`POST /api/v1/tokens` (`{"name": "...", "expires_in_days": 30, "role": "viewer", "projects":
["pay-*"]}`), `DELETE /api/v1/tokens/{id}`, and `GET /api/v1/tokens/limits` for the longest a token
may last and the highest role you may give it.

A token that is refused (unknown, revoked, expired, or not allowed by its limits) is recorded in
the audit trail as `token.use`. An address that fails to authenticate 20 times within a minute
(with tokens or webhook secrets) is refused with `429` and `Retry-After` for the rest of that
minute; each server counts on its own and remembers at most 10,000 addresses. Behind a reverse
proxy, the address is the one the proxy saw only with `server.trust_proxy: true`; otherwise
`X-Forwarded-For` is ignored, so a client cannot dodge the limit by making addresses up.

A limited token works only on servers that know limits: an older server, during a rollback,
refuses it rather than grant its owner's full rights. Tokens without limits work on both.

Launchway cannot tell when someone leaves your organisation: their tokens keep the groups of their
last sign-in until they expire. Remove their tokens when they leave, with
`DELETE FROM api_tokens WHERE username = '...'` in the database.

### Requests from other sites

A request that changes something and is authenticated by the session cookie must carry the header
`X-Launchway-CSRF: 1`, and if it has an `Origin` header, it must be this server; otherwise it is
refused with `403`. Browsers do not let another site add that header, so no web page can act with
a visitor's session. The UI sends it; scripts using a token need not. Without sign-in the header is
required all the same, so a web page cannot drive a Launchway on `localhost`. Launchway's pages
cannot be shown in a frame on another site.

### Behind a reverse proxy

Set `server.trust_proxy: true` only if every client reaches Launchway through one reverse proxy.
Launchway then believes its `X-Forwarded-Proto` (to mark cookies `Secure`) and takes the client's
address from the last entry of `X-Forwarded-For`, the one the proxy added. The proxy must pass the
`Host` header through unchanged, and must not buffer `/api/v1/stream`. Cookies are also `Secure`
whenever `redirect_url` starts with `https://`.

### Audit trail

Every call that changes something (starting, canceling and resuming runs, starting draft runs
(`run.draft`), saving pipelines (`pipeline.save`), approving and rejecting, making and revoking
tokens) by someone signed in, allowed or refused, every token that is refused (`token.use`), every
webhook call (`webhook.git`, `webhook.run`), allowed or refused, every waiver granted or revoked (`policy.waive`,
`policy.revoke_waiver`), allowed or refused, and every sign-in, failed sign-in and sign-out is
recorded with its time, who, their address, the action, project, target (run, step or token name),
outcome (`ok`, `denied` or `failed`) and a short detail. A draft run is recorded with the SHA-256
of its text and a save with its branch and commit; the pipeline text itself is never in the
trail, nor are secrets, tokens and session ids. Launchway only ever adds to it. Admins read it on the **Audit trail** page or with
`GET /api/v1/audit?user=&project=&action=&from=&to=&before=&limit=` (times in Unix milliseconds;
`before` is the `next_before` of the previous page). It is kept until you remove old rows from the
`audit` table yourself.

## Commands

```
launchway server   [--config launchway.yaml]   the server, web UI and a built-in runner
launchway runner   --server URL --join J      enrol a runner on another machine, then take work (outbound only)
                   [--config FILE] [--secrets-config FILE] [--health-listen ADDR] [--labels a,b]
launchway runner   --server URL --token T     a runner with the shared token (no identity of its own)
launchway run      [--secrets-config FILE] [--images-config FILE]   this directory's pipeline, on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     the graph a pipeline produces, as JSON
launchway login    --server URL               store an API token for a server
launchway runs     start <project> [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
launchway runs     list [--project P] [--status S] | show <run-id> | logs <run-id> [--step S] [--follow]
launchway runs     cancel <run-id>
launchway policy   list | run <run-id>        the policies in force; a run's policy decisions
launchway waivers  list | grant | revoke      waivers of policy rules (see Policies)
launchway runners  list | join-token --pool P | revoke-token ID | revoke ID | rotate ID
```

`launchway login` reads a token (from `LAUNCHWAY_TOKEN`, or typed, or piped in), checks it with the
server, and keeps the server and token in `credentials.json` in your configuration directory
(`~/.config/launchway`, or `LAUNCHWAY_CONFIG_DIR`), readable by you alone. It refuses a plain
`http://` address that is not on this machine unless given `--allow-http`. `LAUNCHWAY_SERVER` and
`LAUNCHWAY_TOKEN` override what it stored, for scripts. `runs start --wait` follows the run and
exits with its outcome: `0` succeeded, `1` failed, `3` canceled; `2` means the command was written
wrongly, `4` that the server refused (such as an input value that does not fit, named) or could
not be reached, and `5` that `--timeout` passed (the run goes on). `launchway run -i name=value`
gives inputs to a pipeline run on this machine.

`launchway runner --config FILE` reads the same settings as `runner:` in the server's
configuration, plus where the server is. Unknown settings are refused. Flags given on the command
line win, then `LAUNCHWAY_SERVER`, `LAUNCHWAY_JOIN` and `LAUNCHWAY_RUNNER_TOKEN`, then the file.
Tokens are never in the file. `labels:`, `accept:`, `proxy:`, `no_proxy:` and `ca_file:` are
described in [Runners on other machines and clusters](#runners-on-other-machines-and-clusters).

```yaml
# runner.yaml
server: https://launchway.example.com
name: build-2                 # default: the host name
data: /var/lib/launchway-runner
concurrency: 4
executor: kubernetes          # auto, container, podman, docker, host or kubernetes
kubernetes:
  namespace: builds
  helper_image: registry.example.com/busybox:1.37
  service_account: builder    # for step pods; default none
  node_selector: { pool: builds }
  cpu: 250m                   # request per container, unless the pipeline asks
  memory: 256Mi
  memory_limit: 4Gi
  image_pull_secrets: [registry-credentials]
  keep_failed: 2h             # keep a failed run's pod this long, for a resume
  start_timeout: 10m
  restricted: true            # the default: steps run as run_as_user, not root (see below)
  run_as_user: 1000
  run_as_group: 1000
secrets: {}                   # see Secrets
reports:                      # test report files collected after each step; see Test reports
  max_files: 100
  max_file_mib: 10
  max_total_mib: 20
images: {}                    # builders and registries for image steps; see Container images
```

With the Kubernetes executor, every container of a step pod runs as `run_as_user` (default
1000, never root) with no privileges, so the step namespace can enforce Pod Security
`restricted`. `HOME` is a writable `/home/launchway`, and `/tmp` and the checkout are writable.
Steps that need root, such as `apt-get install`, fail; use images that already have their tools.
`restricted: false` (or `--k8s-unrestricted`) runs images as their own users, often root, and
needs a namespace that allows that. Pipelines cannot change this setting.
[deploy/README.md](deploy/README.md#step-pods-and-pod-security) has the details, including
service images that expect root.

`--health-listen :8081` serves `/healthz` (the process is up) and `/readyz` (the server answered
within the last two minutes) for probes.

## How it is built

| Package | What it does |
|---|---|
| `internal/pipeline` | Parses and validates the pipeline file into a typed graph |
| `internal/engine` | The durable run engine. Every change is an event in the database (PostgreSQL, or SQLite for one server) and state is rebuilt from events. Runners hold steps on leases; a lost runner's step is run again. Handles retries, cancel, resume and approvals. |
| `internal/executor` | Runs a step in a Podman or Docker container, in a Kubernetes pod, or on the host |
| `internal/runner` | Takes steps from the server over HTTP, streams logs, sends heartbeats |
| `internal/secrets` | Secret stores (env, file, Vault/OpenBao) and masking, used only by runners |
| `internal/source` | Mirrors Git repositories, starts runs on matching pushes and pull requests, records why |
| `internal/auth` | Roles and access bindings, OpenID Connect sign-in, and the cryptography sessions and tokens need |
| `internal/api` | Runner API, UI API, sign-in, API tokens, access checks, the audit trail and the live event stream |
| `internal/fakeidp` | An OpenID Connect provider for tests, which can be made to misbehave |
| `internal/blobstore` | Where artifacts are kept: a directory, or S3-compatible object storage |
| `internal/findings`, `internal/sbom` | Read SARIF scanner results and CycloneDX SBOMs, treating them as hostile input |
| `internal/policy` | Delivery policies: the rule catalogue, policy files read with their lines, and the pure function that judges a run |
| `internal/imagebuild` | Image steps on the runner: BuildKit's client, pushing, scanning, signing and provenance |
| `web/` | The React UI, compiled into the binary |

`make test` runs the Go tests with the race detector and type-checks the UI. The tests cover:

- crash recovery;
- lost and killed runners;
- many runners competing for the same steps;
- retries, cancel and resume;
- re-runs from a step: what is reused and never leased, outputs reaching the steps run again, refusals,
  approvals, drafts, library commits after a tag moved, two servers, and a real runner on the host;
- upgrades that can be rolled back: a build one schema older running against the newer database,
  runs it does not understand left alone, and servers migrating at the same moment;
- approvals, including two servers deciding or expiring the same one at once;
- pull requests from real repositories: closed ones not run, the default branch deciding, no
  secrets by default, and two servers polling the same pull requests;
- superseding: what is and is not superseded, all or nothing, and runs of one branch created at
  once on two servers while runners work on them;
- real Git repositories, including saves racing from two servers, stale saves and pushes the
  repository refuses;
- shared libraries: version resolution against real repositories, moving tags, overrides, a
  private library behind credentials, and a library script run at its pinned commit on the host,
  in containers and on Kubernetes;
- secrets kept out of the database, events, logs and API (`make test-vault` adds a real OpenBao);
- sign-in against a fake OpenID Connect provider: forged state, stolen codes (PKCE), bad ID tokens,
  open redirects, CSRF, session expiry, API tokens, a permission matrix over every route, the live
  stream, the audit trail, and sign-in across two servers;
- artifacts: chunked uploads that break off and carry on, checksum mismatches, quotas, pruning by
  two servers at once, fetching in a fresh workspace, and downloads that are never shown as pages
  (`make test-s3` adds a real S3-compatible server);
- security findings: real scanner output, hostile and fuzzed files, and secrets never stored;
- image steps with a real rootless BuildKit and registries with and without TLS and passwords: images
  pulled back by digest, the registry cache, build secrets in no layer, log, event or column, pull
  requests never pushing, readable failures, cosign 2 and 3 verifying signatures and attestations,
  two builders making one multi-platform index, and on Kubernetes building from a restricted step
  pod with the daemon deployed from `deploy/buildkit` (`make test-containers`, `make test-kubernetes`);
- policies: every rule pass and fail, modes, scope, waivers (expired, protected environments, one finding), decisions
  replayed from what they record, malformed and unknown rules refused with their lines, Git sources broken,
  unreachable and stale, two servers racing on one gate, a restart between a decision and the step, an older
  server leaving governed runs alone, and a deploy gate in front of real Argo CD (`make test-argo`);
- real containers (`make test-containers`);
- a real Kubernetes cluster (`make test-kubernetes`).

## Status

Built so far:

- the pipeline model;
- the durable engine;
- the Podman, Docker, host and Kubernetes executors;
- named containers and services;
- secrets (environment, files, Vault and OpenBao);
- approvals;
- re-runs from any step, and a record of what each step attempt was given;
- test reports: JUnit XML and `go test -json`, with flaky and new-failure detection, history and
  trends;
- artifacts, kept in a directory or S3-compatible storage and fetched by later steps on any runner;
- security findings from SARIF, new and fixed across runs, with secrets never stored; SBOMs and a
  component search;
- Git polling, for pushes and pull requests, and superseding older runs;
- the pipeline editor: draft runs of unsaved text, and saving to Git as the person who saved;
- shared libraries of templates and scripts, pinned by Git tag, branch or commit, with
  per-run overrides and pipelines configured on the server for repositories without a file;
- sign-in with OpenID Connect, roles per project, API tokens and an audit trail;
- deployments with Argo CD and Argo Rollouts: GitOps write-back, sync requests, health and rollout
  watching, rollback by revert, environments with a lock each, deploy runner pools;
- container images: rootless BuildKit builds, pushes by digest, SBOMs, vulnerability scans with a
  policy, provenance, cosign-compatible signatures, several platforms on several builders;
- delivery policies: typed rules at run start, before deployments and before image pushes, from the
  configuration or a Git repository pinned by tag, enforced or reported, with waivers;
- runner identities: join tokens, a key per runner, leases bound to the runner, revocation and key
  rotation; build runner pools by project and event; the runner's own `accept:` limits;
- the web UI.

Not built yet, in the order planned:

- Releases and promotion
- Deployment control: promote, abort and retry, and rollout steps
- Inputs
- Infrastructure runs
- VM fleets

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
