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
| `when` | `branch`, `tag` and `event` (`push`, `manual`, `draft`, `pull_request`) patterns; the step is skipped if they do not match. In a [draft run](#editing-pipelines), a step with a `when` runs only if its `event` names `draft`. A [pull-request run](#pull-requests) has no branch, so `when: { branch: main }` never matches it |
| `env` | Extra environment variables |
| `secrets` | Environment variables whose values a runner looks up; see [Secrets](#secrets) |
| `container` | A named container from the `containers` section, instead of `image` |
| `retry` | Extra attempts after a failure |
| `timeout` | Per attempt; default 1h, at most 24h. For an approval, how long to wait for a decision; default 24h, at most 30 days |
| `kind` | `step` (the default) or `approval`; see [Approvals](#approvals) |
| `message` | For an approval: the question to decide, one line of at most 500 characters |
| `uses` | `library/template`: the step is replaced by a template's steps; see [Shared libraries](#shared-libraries) |
| `with` | Values for the template's parameters |

At the top level, beside `pipeline`, `on`, `containers` and `steps`, `libraries` names the
[shared libraries](#shared-libraries) the pipeline's templates come from, and `supersede: true`
lets a new run [cancel the older runs](#superseding-older-runs) of the same branch or pull
request. `on` takes `push` (with `branches` and `tags` patterns) and `pull_request: {}`.

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
instantly once the pod is up. On Podman, services run in a pod per run. Plain `image:` steps still
work, and each image used that way gets a container of its own.

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

- **Not there yet:** reporting a run's outcome back to the pull request as a commit status, and
  webhooks.

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
| `admin` | everything an operator may, read the audit trail, start draft runs if `drafts.allow` is `admin`, and save a pipeline that has problems |

Each entry in `access` grants a role to one `group` (from `groups_claim`) or one `user` (from
`username_claim`), on the projects matching one of `projects`, or on every project if there are
none. Patterns use `*` (any characters but `/`), `?` (one character) and `[...]`. Where several entries
match, the highest role wins. Someone matching none can sign in but sees nothing. The audit trail
needs `admin` from an entry without `projects`.

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

Scripts use API tokens: `Authorization: Bearer lw_...`. Make one on the **API tokens** page with a
name and an expiry (default 90 days, at most a year). It is shown once; only its SHA-256 is stored.
A token acts as you, with your rights as of your latest sign-in, and can be revoked at any time.
Tokens can only be made from a signed-in browser, not with another token. Over the API:
`GET /api/v1/tokens`, `POST /api/v1/tokens` (`{"name": "...", "expires_in_days": 30}`) and
`DELETE /api/v1/tokens/{id}`.

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
tokens) by someone signed in, allowed or refused, and every sign-in, failed sign-in and sign-out is
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
launchway runner   --server URL --token T     a runner on another machine (outbound connections only)
                   [--config FILE] [--secrets-config FILE] [--health-listen ADDR]
launchway run      [--secrets-config FILE]    this directory's pipeline, on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     the graph a pipeline produces, as JSON
```

`launchway runner --config FILE` reads the same settings as `runner:` in the server's
configuration, plus where the server is. Unknown settings are refused. Flags given on the command
line win, then `LAUNCHWAY_SERVER` and `LAUNCHWAY_RUNNER_TOKEN`, then the file. The token is never
in the file.

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
secrets: {}                   # see Secrets
```

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
- Git polling, for pushes and pull requests, and superseding older runs;
- the pipeline editor: draft runs of unsaved text, and saving to Git as the person who saved;
- shared libraries of templates and scripts, pinned by Git tag, branch or commit, with
  per-run overrides and pipelines configured on the server for repositories without a file;
- sign-in with OpenID Connect, roles per project, API tokens and an audit trail;
- the web UI.

Not built yet, in the order planned:

- Image builds
- Releases and promotion
- The Argo CD driver
- Inputs
- Policy
- Infrastructure runs
- VM fleets

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
