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
| `secrets` | Environment variables whose values a Station (runner) looks up; see [Secrets](secrets.md) |
| `container` | A named container from the `containers` section, instead of `image` |
| `retry` | Extra attempts after a failure; a mapping adds a wait and the causes worth retrying, see [Retrying after a wait](#retrying-after-a-wait-and-limiting-a-run) |
| `on_failure` | `stop` (the default) or `continue`: with `continue` a step that fails for good is shown as failed (allowed) and the Sortie (run) goes on; see [Tolerating a failure](#tolerating-a-failure-and-testing-an-output) |
| `timeout` | Per attempt; default 1h, at most 24h. For an approval, how long to wait for a decision; default 24h, at most 30 days |
| `reports` | `tests:` one glob or a list, relative to the workspace: test result files read once the step ends; see [Debriefs (test reports)](test-reports.md) |
| `kind` | `step` (the default), `approval` (see [Approvals](approvals-and-reruns.md#approvals)), `image` (see [Container images](images.md)), or `deploy`, `rollback` and `rollout` (see [Deployments](deployments.md)) |
| `message` | For an approval: the question to decide, one line of at most 500 characters |
| `uses` | `library/template`: the step is replaced by a template's steps; see [Shared libraries](libraries.md) |
| `with` | Values for the template's parameters; a value may be `{ input: name }`, a [run input](#run-inputs) |
| `for_each` | Repeats the step over lists; with `exclude`, `add`, `continue_for`, `max_parallel`, `fail_fast` and `serial`. See [Repeating a step over lists](#repeating-a-step-over-lists) |

At the top level, beside `pipeline`, `on`, `containers` and `steps`, `libraries` names the
[shared libraries](libraries.md) the pipeline's templates come from, and `supersede: true`
lets a new Sortie [cancel the older Sorties](#superseding-older-runs) of the same branch or pull
request, and `inputs` declares the [values a run is started with](#run-inputs). `on` takes `push`
(with `branches` and `tags` patterns), `pull_request: {}`, [`schedule`](triggers.md#schedules) and
[`webhook`](triggers.md#webhooks).

## The picture of a pipeline

The Sortie page, the pipeline's **Live** tab and the editor's preview draw the steps as a graph, built from `needs`.
A step sits one **stage** to the right of the deepest step it needs, so steps that do not wait for each other share a
stage and are stacked in one column: they run at the same time. A connector runs from each step to the steps that
need it; one that skips stages goes around the steps between. Each step shows its name, its state in words and an
icon (never color alone), and how long it has run or took. Every step that is running at the moment has the running
treatment, so several can be seen at once; skipped, not started and waiting-for-a-person look different from one another.

- Choose a step with the pointer or the keyboard. Arrow right and left follow the connectors to what a step needs and
  what needs it; arrow up and down move between the steps of a stage; Home and End go to the first and last stage.
- A pipeline wider than its box scrolls sideways, with a small map to jump along it. In a pipeline of more than 14 steps,
  a long run of steps one after another folds into one node while none of them needs a look; use the button above the
  graph to open it. The steps of a template can be folded into one node, named after the template.
- On a narrow screen (under 640 px) the same picture is a vertical list: each stage is a heading, and the steps that run
  together are indented under it.
- For a screen reader the graph is a list of stages: "Stage 2 (runs together): test, security-go, security-web", and each
  step says what it follows and what runs beside it. Motion respects the reduced-motion setting.
- `launchway runs show RUN_ID` prints the same stages as text, and the Sortie's `levels` and each step's `needs` are in
  `GET /api/v1/runs/{id}` and the MCP tool `get_run`.

On Home, in the Sortie lists and on pull requests the compact [launch line](../learn/concepts/launch-line.md) stays one line.
A station that stands for several steps shows a small stack with their number, and the station's text says it: "3 at once:
2 passed, 1 running". Hover over it (or focus the row) to see each step.

## Containers and services

Name the toolboxes a pipeline needs once, then use them by name. Every container in a Sortie shares
the workspace. Services run beside the steps for the whole Sortie and are reachable at `localhost`.

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

On Kubernetes, each step runs in a pod of its own: the step's toolbox plus the helper that holds the workspace and
the services, made when the step starts and removed when it ends. A step so has the memory and CPU of its own toolbox, even beside a step that uses the same one, and
the Sortie holds quota only while a step runs. The cost is a pod start for each step, and each service starts afresh with each step. Its containers run as an ordinary user, not root, unless the
Station's operator chose otherwise (see [Commands](commands.md)). On Podman, services run in a pod
per Sortie. Plain `image:` steps still work, and each image used that way gets a container of its
own.

The workspace lives on one Station, so all of a Sortie's steps go to the Station that took its first
step, for as long as that Station keeps in touch with the server. The cost is that a Sortie's parallel
steps share that one Station's concurrency; different runs still spread across Stations. If the
Station stops responding for 90 seconds (three lease periods), or shuts down, another Station takes
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
with secrets masked as in the log) are kept with the step and shown where the Sortie says it failed,
so "exit code 1" comes with its reason.

## Superseding older Sorties {#superseding-older-runs}

With `supersede: true` at the top of the pipeline, a new Sortie started by a push or a pull request
cancels the older Sorties of the same Flight (codebase) and ref (the same branch, tag or pull request) that
are still queued, running or waiting for approval, if they too were started by a push or a pull
request. They are cancelled as if by hand (running steps are stopped by their Stations), and say
why: *superseded by run #N*, in the Sortie list, on the Sortie page, and in the API (`cancel_reason`,
`superseded_by`, `superseded_by_number`). The push decision that started the new Sortie names the
runs it superseded. Runs started by hand, draft runs and re-runs never supersede and are never
superseded, and the new Sortie's own pipeline is what decides. The cancelling happens in the same
database transaction that creates the new Sortie, so it is exact however many servers there are.
A superseded run can be resumed like any cancelled run.

## Lanes: one run at a time

`supersede` only ever cancels. A **lane** makes the Sorties of a Flight take turns, and says what a
run that arrives while another goes does:

```yaml
lane:
  name: release          # default: the pipeline's name
  per: [ref]             # a lane for each branch, tag or pull request; also pull_request, input:<name>
  when_busy: wait        # wait (the default), cancel or skip
  queue: all             # for wait: all, or newest (a waiting run is replaced by a newer one)
```

- **wait** puts the run in line: it is created, shown as *waiting for lane release/ref main (run #41
  is running)*, and none of its steps is queued until the run before it ends. With `queue: newest`
  (the default) a Sortie that is still waiting is replaced by a newer one, and says so; with `all`
  every Sortie goes, in order.
- **cancel** cancels the Sortie that holds the lane and every Sortie waiting for it, and the new Sortie goes
  at once. A holder with an open deployment finishes the deployment first, and the new Sortie waits.
- **skip** creates the new Sortie and cancels it at once, so it appears in lists and status posts
  but runs no step.

Every kind of run is subject to a lane: pushes, pull requests, schedules (a schedule's firing
records *skipped* or *waiting*), webhooks and runs started by hand. Draft runs and re-runs never join
one. A lane is released when its holder ends, however it ends, and the next Sortie starts in the same
database transaction, so it is exact however many servers there are; a server that finds a lane
with runs waiting and no live holder starts the first of them. Resuming a finished run is
refused while another Sortie holds its lane. A Sortie that waits for an approval keeps its lane until the approval is decided or expires, so give approvals in a laned pipeline a short `timeout`. See the runs a Flight has in line with `launchway runs
lanes Flight`, on the run page, or with the MCP tool `list_lanes`. `lane` and `supersede` cannot be written together:
`supersede: true` is the short form of `lane: { per: [ref], when_busy: cancel }` for pushes and
pull requests.

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

Starting a run by hand (**Start a Sortie**, the API, or `launchway runs start -i name=value`) shows
a form made from the pipeline at the chosen branch or tag: a list of options for a choice, a
switch for a boolean, a number field with its bounds, and the pattern and description under each
field. The server checks every value, whoever sends it; a value that does not fit is refused, and
the form shows why next to the field. Runs started any other way use the defaults: a push, a pull
request, a draft run. So a pipeline that pushes or pull requests start may not have a required
input without a default (the file is refused, on that input's line); a
[schedule](triggers.md#schedules) or [webhook](triggers.md#webhooks) must give such an input a value.

The values a run was started with are recorded with it (shown on the Sortie, under *Started with*,
and in the Logbook), and reach every step as `LAUNCHWAY_INPUT_<NAME>`: `true` or `false`, a
number in its shortest form (`2`, `2.5`), a list as a JSON array (`["3.12","3.13"]`). There is no
expression language; an input is used in exactly these places:

- a step's `when: { input: dry_run, equals: false }` (one value or a list; a boolean input alone,
  `when: { input: deploy }`, means `equals: true`);
- a template's parameter, `with: { target: { input: environment } }`, or for a string or a
  choice `with: { image: { input: version, format: "registry.example.com/app:%s" } }`;
- a loop's list, `for_each: { python: { input: pythons } }`.
- a deploy step's `version`, an image `tag` or a written `value` (`{ input: version }`, from a
  string, choice or number input), and its `environment` (`{ input: target }`, from a choice
  input whose options are all Airfield (environment) names). The Airfield is then checked against the
  server's Airfields when the run is created, like a written one.

A Sortie that is resumed keeps its inputs. A re-run copies them and cannot change them: the steps it
reuses ran with those values, so a re-run with other values would mix two Sorties. To run with other
values, start a new Sortie. Inputs are not for secrets: values are recorded with the Sortie, shown to
everyone who can see it, and written to the Logbook. Use [secrets](secrets.md) for those.
Choices whose options come from a step or another system (`options_from:`) are not supported yet.

## Repeating a step over lists

`for_each` repeats a step, or a step that uses a template, once for each combination of values. This
is the **matrix**: a step is written once, the lists of values are the axes, and each combination is a step
of its own. (There is no key called `matrix`; a file that writes one is told to use `for_each`.)

```yaml
  test:
    for_each: { go: ["1.26", "1.27"], os: [linux] }
    image: { each: go, format: "golang:%s" }
    run: go test ./...
```

What each combination gets, and what the matrix as a whole shows:

- **Its own step**, with a stable name (`test[go=1.27,os=linux]`), its own log, its own
  [cache](build-caches.md) when the cache key says `{ each: go }`, and its own [Debrief](test-reports.md) (write
  reports to a directory per combination: the starters use `reports/<values>/`).
- **A single "passed"** for steps that `needs: test`, which wait for every combination.
- **A roll-up** on the launch line: `3 of 4 passed`, and with a failure `3 of 4 passed: test[go=1.26] ended with exit code 1`.
- **One check** on a pull request: requiring `ci/test` requires every combination; requiring `ci/test[go=1.26]` names one.
- **Re-running one combination**: re-run from its step (*Re-run from here*), and the others keep what they did.
- **The Sortie page** draws the matrix as one node that sums its combinations up, and a grid when you open it.
- **A failure stops the others by default** (`fail_fast: true`): CI minutes are not spent on combinations of a Sortie that already
  failed. Write `fail_fast: false` when each combination's answer matters, as it does for a library on several versions; the
  [library starters](starters.md) do.

The full example, with all the keys:

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
- `add: [ { python: "3.14", os: debian } ]` appends whole combinations that are not in the cross
  product (every key named), after the others and in the order written. Existing members keep
  their names.
- `continue_for: [ { python: "3.14" } ]` selects members, matched like `exclude` entries, that may
  fail without failing the Sortie: they end as failed (allowed), steps that need the loop go on, and
  `fail_fast` ignores them.
- `max_parallel` bounds how many are queued or running at once.
- `fail_fast` (on by default): once one fails for good (after its retries), the others that have
  not started are cancelled and those running are stopped, saying why. Resuming the Sortie runs them
  again.
- Lists are written in place, come from a Sortie input of type `list`, or, in a template, from a
  parameter of type `list` (`{ param: name }`). Values name steps, so they are 1 to 40 letters,
  digits, `.`, `_`, `+` and `-`; put other text around them with `format:`.
- Deploy and Return to base (rollback) steps cannot be repeated: each deployment holds its Airfield's lock and
  is recorded on its own, so write one deploy step for each Airfield.
- A loop may have at most 64 combinations (`loops: { max_members: N }` in the server's
  configuration, up to 500), and a run at most 500 steps. An empty list is refused when the run is
  created.

The Sortie page shows a loop as one node whose status sums up its members; choosing it shows them as
a grid (two lists as rows and columns), and choosing a cell shows that step's log.

Lists produced by an earlier step while the Sortie goes (dynamic fan-out) are not supported. They
would fit the engine this way: the producing step's outputs are recorded in its `step.finished`
event, so a new event (a loop's members, recorded when that step finishes) would add the member
steps to the Sortie, and every server would rebuild them from the log as it does now; servers that
do not know the event would leave such runs alone, as they do any Sortie they do not fully understand.

## Defaults for every step

Steps that share an image, a variable, a timeout or a cache say it once:

```yaml
defaults:
  image: docker.io/library/node:22
  env: { CI: "true" }
  timeout: 15m
  cache:
    - { name: npm, paths: [.cache/npm], key: { files: [package-lock.json] } }
steps:
  lint: { run: npm run lint }
  test: { needs: lint, run: npm test }
  e2e:  { image: mcr.microsoft.com/playwright:v1.50.0, needs: test, run: npm run e2e }   # its own image, the rest from defaults
```

A step's own value wins, `env` is merged key by key, and approvals, images, launches (deploys), verifications and steps that use
templates take none of it (see [Defaults](../reference/pipeline.md#defaults)).

## Retrying after a wait, and limiting a Sortie {#retrying-after-a-wait-and-limiting-a-run}

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
40 s, 80 s here; never more than an hour). While it lasts the step is queued but no Station is
given it, and the Sortie page says `retrying in 20s (attempt 2 of 4)`. `on` names the causes that are
retried: `exit` (the script ended non-zero), `timeout` (the step ran past its `timeout`) and
`infrastructure` (the image could not be pulled, the workspace could not be prepared, or a Station
refused the step). Without `on`, every cause is retried, as with a whole number, except that a
Station's refusal is retried only when you list `infrastructure`. A lost Station is always run again, a
bounded number of times, whatever `retry` says.

A pipeline can also bound a whole Sortie: `timeout: 2h` at the top. A Sortie still open after that is
canceled with the reason `run timeout of 2h reached`; running steps are stopped as for any cancel,
and a deployment the Sortie holds is handled as when someone cancels it (`on_cancel` is honoured). **Time
spent waiting for an approval does not count**, and neither does the time between a Sortie's end and
its resume. An administrator can cap what pipelines may ask for with `launchway run-limits set
--max-run-timeout 48h`; a pipeline that asks for more is refused, with the limit in the message.

## Tolerating a failure and testing an output

A step that fails fails the Sortie, and a step that needs it is not run. Two keys change that for
the steps where it makes sense.

`on_failure: continue` lets a step fail without failing the Sortie, for a check that informs but does
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

A step that fails for good (after its `retry` attempts) ends **failed (allowed)** in the Sortie page,
the API, the MCP tools and `launchway runs show`. The Sortie's outcome ignores it, `fail_fast` of a
loop ignores it, and the steps that need it **run as if it had succeeded**, unless they say
`when: { upstream: failed }`, which runs them after any failure. Take care with steps that need
files the failed step should have written: they run anyway. A step that fails for a reason that
is not tolerated still fails the Sortie, and a step that needs it is skipped. A
[resume](approvals-and-reruns.md) does not run an allowed failure again, and a re-run reuses it.
`on_failure` is for steps that run a script and image steps; deploy, Return to base, rollout, verify, ai
and approval steps have their own failure handling, and a step that uses a template takes it from
inside the template.

`when: { output: step.key, equals: value }` skips a step because of what an earlier step found.
The step must be one this step needs, directly or through others. Outputs compare as text (a
boolean is `true` or `false`), a list of values matches any of them, and an output that was never
written never matches: the skip says which output was missing and what the step wrote instead: `the output "changed" of step scan was not set: scan did not write it (it wrote: other)`. The step shows as skipped, not
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
`always` it runs regardless; `succeeded` is the default. It does not change the Sortie's outcome: a
run with a failed step has failed, even if its cleanup succeeded. A deploy step takes no `upstream`: it launches
only after the steps it needs succeeded. A Return to base step decides on the steps it needs, so none of
them, directly or not, may run after failures (its success would hide the failure the Return to base
is for); let such a cleanup step need the Return to base step instead. A cancel (by hand, or
[superseding](#superseding-older-runs)) means stop: steps not started, these included, are
cancelled. Resuming a Sortie runs the failed steps again and then lets these steps decide again, so
a cleanup runs again after them, and `notify-failure` above is skipped if nothing fails the second
time. A [re-run](approvals-and-reruns.md#re-running-from-a-step-and-what-each-step-was-given) from a step runs them again
like any step after it.
