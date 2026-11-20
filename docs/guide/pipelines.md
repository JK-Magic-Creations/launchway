# Pipelines

The pipeline file: steps, containers and services, outputs, run inputs, loops, and cleanup steps. Every key is listed in the [pipeline reference](../reference/pipeline.md).

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
| `when` | `branch`, `tag` and `event` (`push`, `manual`, `draft`, `pull_request`, `schedule`, `webhook`) patterns, and `input` or `output` with `equals` (see [Run inputs](#run-inputs) and [Tolerating a failure](#tolerating-a-failure-and-testing-an-output)); the step is skipped if they do not match. `upstream` says how the steps it needs must have ended; see [Cleanup and notification steps](#cleanup-and-notification-steps). In a [draft run](editor.md), a step with a `when` runs only if its `event` names `draft`. A [pull-request run](triggers.md#pull-requests) has no branch, so `when: { branch: main }` never matches it |
| `env` | Extra environment variables |
| `secrets` | Environment variables whose values a runner looks up; see [Secrets](secrets.md) |
| `container` | A named container from the `containers` section, instead of `image` |
| `retry` | Extra attempts after a failure; a mapping adds a wait and the causes worth retrying, see [Retrying after a wait](#retrying-after-a-wait-and-limiting-a-run) |
| `on_failure` | `stop` (the default) or `continue`: with `continue` a step that fails for good is shown as failed (allowed) and the run goes on; see [Tolerating a failure](#tolerating-a-failure-and-testing-an-output) |
| `timeout` | Per attempt; default 1h, at most 24h. For an approval, how long to wait for a decision; default 24h, at most 30 days |
| `reports` | `tests:` one glob or a list, relative to the workspace: test result files read once the step ends; see [Test reports](test-reports.md) |
| `kind` | `step` (the default), `approval` (see [Approvals](approvals-and-reruns.md#approvals)), `image` (see [Container images](images.md)), or `deploy`, `rollback` and `rollout` (see [Deployments](deployments.md)) |
| `message` | For an approval: the question to decide, one line of at most 500 characters |
| `uses` | `library/template`: the step is replaced by a template's steps; see [Shared libraries](libraries.md) |
| `with` | Values for the template's parameters; a value may be `{ input: name }`, a [run input](#run-inputs) |
| `for_each` | Repeats the step over lists; with `exclude`, `max_parallel` and `fail_fast`. See [Repeating a step over lists](#repeating-a-step-over-lists) |

At the top level, beside `pipeline`, `on`, `containers` and `steps`, `libraries` names the
[shared libraries](libraries.md) the pipeline's templates come from, and `supersede: true`
lets a new run [cancel the older runs](#superseding-older-runs) of the same branch or pull
request, and `inputs` declares the [values a run is started with](#run-inputs). `on` takes `push`
(with `branches` and `tags` patterns), `pull_request: {}`, [`schedule`](triggers.md#schedules) and
[`webhook`](triggers.md#webhooks).

## Containers and services

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
runner's operator chose otherwise (see [Commands](commands.md)). On Podman, services run in a pod
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

## Superseding older runs

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

## Run inputs

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
[schedule](triggers.md#schedules) or [webhook](triggers.md#webhooks) must give such an input a value.

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
everyone who can see it, and written to the audit trail. Use [secrets](secrets.md) for those.
Choices whose options come from a step or another system (`options_from:`) are not supported yet.

## Repeating a step over lists

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

## Retrying after a wait, and limiting a run

`retry: 3` tries a failed step again at once, whatever went wrong. A flaky test or a service that
needs a moment to come back wants a pause, and a failure that no retry can fix (a compile error)
should not be tried again:

```yaml
  integration:
    image: alpine:3.22
    run: ./integration.sh
    retry: { times: 3, wait: 20s, backoff: 2, on: [timeout, infrastructure] }
```

`wait` is the pause before the first retry and `backoff` multiplies it for each retry after (20 s,
40 s, 80 s here; never more than an hour). While it lasts the step is queued but no runner is
given it, and the run page says `retrying in 20s (attempt 2 of 4)`. `on` names the causes that are
retried: `exit` (the script ended non-zero), `timeout` (the step ran past its `timeout`) and
`infrastructure` (the image could not be pulled, the workspace could not be prepared, or a runner
refused the step). Without `on`, every cause is retried, as with a whole number, except that a
runner's refusal is retried only when you list `infrastructure`. A lost runner is always run again, a
bounded number of times, whatever `retry` says.

A pipeline can also bound a whole run: `timeout: 2h` at the top. A run still open after that is
canceled with the reason `run timeout of 2h reached`; running steps are stopped as for any cancel,
and a deployment the run holds is handled as when someone cancels it (`on_cancel` is honoured). **Time
spent waiting for an approval does not count**, and neither does the time between a run's end and
its resume. An administrator can cap what pipelines may ask for with `launchway run-limits set
--max-run-timeout 48h`; a pipeline that asks for more is refused, with the limit in the message.

## Tolerating a failure and testing an output

A step that fails fails the run, and a step that needs it is not run. Two keys change that for
the steps where it makes sense.

`on_failure: continue` lets a step fail without failing the run, for a check that informs but does
not gate (a linter, an optional scan):

```yaml
  lint:
    image: alpine:3.22
    on_failure: continue
    run: ./lint.sh
  build:
    needs: lint            # runs after lint, whether lint passed or failed for good
    run: make
```

A step that fails for good (after its `retry` attempts) ends **failed (allowed)** in the run page,
the API, the MCP tools and `launchway runs show`. The run's outcome ignores it, `fail_fast` of a
loop ignores it, and the steps that need it **run as if it had succeeded**, unless they say
`when: { upstream: failed }`, which runs them after any failure. Take care with steps that need
files the failed step should have written: they run anyway. A step that fails for a reason that
is not tolerated still fails the run, and a step that needs it is skipped. A
[resume](approvals-and-reruns.md) does not run an allowed failure again, and a re-run reuses it.
`on_failure` is for steps that run a script and image steps; deploy, rollback, rollout, verify, ai
and approval steps have their own failure handling, and a step that uses a template takes it from
inside the template.

`when: { output: step.key, equals: value }` skips a step because of what an earlier step found.
The step must be one this step needs, directly or through others. Outputs compare as text (a
boolean is `true` or `false`), a list of values matches any of them, and an output that was never
written never matches: the skip says `scan.changed was not set`. The step shows as skipped, not
as passed, and a re-run reuses the skip.

```yaml
  scan:
    run: ./scan.sh          # writes changed=true to "$LAUNCHWAY_OUTPUTS" when it finds something
  fix:
    needs: scan
    when: { output: scan.changed, equals: true }
    run: ./fix.sh
```

A step that repeats (`for_each`) cannot be named, because which member is not known when the
pipeline is written; add a step that needs it and writes the output to test. Both keys need
schema 24: older servers leave runs that use them alone.

## Cleanup and notification steps

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
time. A [re-run](approvals-and-reruns.md#re-running-from-a-step-and-what-each-step-was-given) from a step runs them again
like any step after it.
