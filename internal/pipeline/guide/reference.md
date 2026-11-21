# Launchway pipeline reference

A pipeline is one YAML file, `.launchway/pipeline.yaml`, in the repository it builds. It is a
graph of steps: each step runs a shell script in a container image, and `needs` orders them. Steps
with no unmet needs run in parallel. There is no expression language: values are written in place
or come from a few named sources (`{ output: … }`, `{ input: … }`, `{ param: … }`, `{ each: … }`,
`{ run: … }`), each allowed only where this page says.

Check a file before running it: problems are reported with their line, column and path (for
example `steps.build.needs`), and nothing starts while there are any.

## The top level

```yaml
# An outline; every part is described below.
pipeline: my-service        # required: lower-case letters, digits and hyphens
on:                         # what starts runs by itself (a run can always be started by hand)
  push: { branches: [main, "release/*"], tags: ["v*"] }
  pull_request: {}            # or { into: [main, "release/*"] } to run only for pull requests into those branches
  schedule: [ { cron: "0 3 * * *", timezone: Europe/Berlin, ref: main, inputs: { dry_run: false } } ]
  webhook: { name: deploy-request, ref: main, inputs: { version: body.release.version } }
supersede: true             # a new push or pull-request run cancels older ones of the same ref
timeout: 2h                 # a run open longer than this is canceled (waiting for approvals does not count)
lane: { per: [ref], when_busy: wait, queue: newest }   # runs of the project take turns; see Lane
inputs: { … }               # values a run is started with; see Inputs
libraries: { … }            # shared template libraries; see Templates
containers: { … }           # named toolboxes and services; see Containers
steps: { … }                # required: the steps, by name
```

| Key | Meaning |
|---|---|
| `pipeline` | The pipeline's name |
| `on` | `push` (with `branches` and `tags` patterns), `pull_request: {}` (or `{ into: [patterns] }`: only for pull requests that target such a branch, as the project's forge says), `schedule` (a list of `cron`, `timezone`, `ref`, `inputs`) and `webhook` (`name`, `ref`, `inputs` mapping fields of the request body). Schedules and webhooks are read from the default branch only |
| `supersede` | `true`: a run started by a push or pull request cancels the older such runs of the same branch, tag or pull request that are still going |
| `lane` | `name` (default the pipeline's), `per` (`ref`, `pull_request`, `input:<name>`: what splits it into separate lanes), `when_busy` (`wait`, `cancel` or `skip`: what a run that arrives while the lane is held does) and `queue` (`newest`: a waiting run is replaced by a newer one; or `all`). At most one run of a lane goes at a time. Not with `supersede` |
| `timeout` | How long a run may stay open, `1m` to `720h` (and no more than the installation's limit): a run past it is canceled with the reason `run timeout of 2h reached`. Time spent waiting for an approval does not count |
| `inputs` | Values a run is started with |
| `libraries` | Template libraries: `name: { repo: <library the server lists>, version: <tag, branch or full commit> }` |
| `containers` | Named containers steps run in, and services beside them |
| `steps` | The steps, by name. A step name is lower-case letters, digits, `-` and `_` |

Patterns use `*` (anything but `/`), `?` and `[…]`.

## Steps

```yaml
steps:
  test:
    description: Run the unit tests
    image: docker.io/library/golang:1.27
    run: go test ./...
    retry: 1
    timeout: 20m
  build:
    image: docker.io/library/alpine:3.22
    needs: test
    env: { GOFLAGS: -trimpath }
    run: |
      echo "building $LAUNCHWAY_COMMIT"
      echo version=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  publish:
    image: docker.io/library/alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "publishing $LAUNCHWAY_NEEDS_BUILD_VERSION"
```

| Key | Meaning |
|---|---|
| `kind` | `step` (the default), `approval`, `image`, `deploy`, `rollback` or `rollout` |
| `description` | One line saying what the step is for |
| `image` | The container image the step runs in; write it in full, such as `docker.io/library/alpine:3.22` |
| `container` | A named container from `containers`, instead of `image` |
| `run` | A shell script, run with `sh -e` in the repository checkout |
| `needs` | A step name or a list: the steps that must succeed first |
| `when` | Conditions; the step is skipped when they do not hold (below) |
| `env` | Extra environment variables: `NAME: value` |
| `secrets` | Environment variables whose values the runner looks up: `NAME: { store: vault, key: path, field: name }`. The server never sees the values; they are masked in logs |
| `retry` | Extra attempts after a failure (default 0). A mapping adds a pause and causes: `retry: { times: 3, wait: 30s, backoff: 2, on: [timeout, infrastructure] }`; `wait` is the pause before the first retry (`1s` to `1h`), `backoff` multiplies it for each retry after (1 to 10), `on` lists the causes retried (`exit`, `timeout`, `infrastructure`; default every cause) |
| `timeout` | Per attempt, such as `90s`, `10m`, `1h` (default 1h, at most 24h). For an approval, how long to wait (default 24h, at most 30 days) |
| `reports` | Files read when the step ends, pass or fail: `tests` (JUnit XML or `go test -json`), `findings` (SARIF 2.1.0), `sbom` (CycloneDX JSON). Each is a glob or a list of globs relative to the workspace |
| `resources` | CPU and memory for a step that runs a script: `{ cpu: 2, memory: 6Gi }`, with `cpu_limit` and `memory_limit` to let it use more than it is promised. A limit left out is the amount promised. Without it the step gets the installation's default for its kind (up to 4 CPUs and 6 GiB to start with). Tools are told to start only as many parallel jobs as fit (`GOMAXPROCS`, `MAKEFLAGS`, `CARGO_BUILD_JOBS`, `LAUNCHWAY_PARALLELISM`). A run is refused if a step asks for more than the installation or the cluster allows. Not for image, deploy, rollout, rollback, verify, ai or approval steps |
| `artifacts` | Files kept after the step: a list of `{ name, paths, retain, when }`; `when` is `success` (the default), `failure` or `always` |
| `fetch` | Artifacts of steps this step needs, put in the workspace first: a list of `{ from: step-or-loop, name: artifact, into: dir }`; from a loop, `into` is required and each member's files go in `<into>/<member>` (values joined by `_`), or add `member: { key: value, ... }` for one member |
| `cache` | Directories kept between runs, so that installs do not start from nothing: a list of `{ name, paths, key: { files, text }, fallback, save }`. Restored after `fetch` and before the script; saved after it. See "Caches" below. Only for steps that run a script (an image step's own `cache` is a registry for build layers) |
| `message` | For an approval: the question to decide, one line |
| `uses` | `library/template`: the step becomes that template's steps |
| `with` | Values for the template's parameters |
| `for_each` | Repeat the step over lists; with `exclude`, `add` (extra whole combinations), `continue_for` (members that may fail without failing the run), `max_parallel` and `fail_fast` |
| `on_failure` | `stop` (default) or `continue` (steps that run a script, and image steps): a final failure is shown as failed (allowed), the run's outcome ignores it and steps that need it run as if it had succeeded, unless they say `when: { upstream: failed }` |
| `deployment` | For a `rollback` or `rollout` step: the deploy step it acts on |

### Environment every step gets

`LAUNCHWAY_COMMIT`, `LAUNCHWAY_BRANCH` (empty for tags and pull requests), `LAUNCHWAY_REF`,
`LAUNCHWAY_EVENT` (`push`, `manual`, `draft`, `pull_request`, `schedule`, `webhook`),
`LAUNCHWAY_RUN_NUMBER`, `LAUNCHWAY_STEP`, `LAUNCHWAY_PULL_REQUEST` (pull-request runs),
`LAUNCHWAY_INPUT_<NAME>` for each input, and `LAUNCHWAY_OUTPUTS`, a file.

**Outputs.** A step writes `key=value` lines to `"$LAUNCHWAY_OUTPUTS"`; a step that needs it gets
`LAUNCHWAY_NEEDS_<STEP>_<KEY>` (upper case, `-` as `_`).

### when

```yaml
when: { branch: [main, "release/*"] }          # only on these branches
when: { tag: "v*" }
when: { event: [push, manual] }                # push, manual, draft, pull_request, schedule, webhook
when: { input: dry_run, equals: false }        # a run input; a boolean input alone means equals: true
when: { output: scan.changed, equals: true }   # an output of a step this one needs; never written: skipped
when: { upstream: failed }                     # run if a needed step failed (notifications)
when: { upstream: always }                     # run whatever happened (cleanup)
```

Conditions in one `when` must all hold. `upstream` is `succeeded` (the default), `failed` or
`always`; such a step waits until every step it needs has ended and never changes the run's
outcome. A pull-request run has no branch, so `branch` never matches it. In a **draft run** a step
with a `when` runs only if its `event` names `draft`, so guarded steps such as deployments never
run from unsaved text.

### Approvals

```yaml
  approve-prod:
    kind: approval
    needs: smoke-test
    message: Deploy to production?
    timeout: 24h
```

An approval takes only `needs`, `when`, `message`, `timeout`, `description`, `deployment` and `approvers`. `approvers: { groups: [release-managers], users: [zoe], count: 2 }` limits who may decide (it only narrows the operator role) and asks for `count` (1 to 5) distinct approvals; a protected environment's `min_approvals` is a floor the pipeline cannot lower; anyone allowed may reject. When its needs have
succeeded it waits for a person: approving lets the steps after it run; rejecting, or nobody
deciding before the timeout, fails it. With sign-in on, the person who started a run may not
approve it themselves (unless the server allows it).

## Containers

```yaml
containers:
  go:       { image: docker.io/library/golang:1.27, cpu: "1", memory: 2Gi }
  postgres: { image: docker.io/library/postgres:17, service: true, env: { POSTGRES_PASSWORD: test } }
steps:
  test: { container: go, run: go test ./... }
```

| Key | Meaning |
|---|---|
| `image` | The image |
| `env` | Environment variables of the container |
| `cpu`, `memory` | Requests, as Kubernetes quantities (`500m`, `2Gi`) |
| `service` | `true`: runs beside the steps for the whole run, reachable at `localhost` |
| `command` | For a service: the command to run instead of the image's own |
| `ready` | For a service: `{ run: "pg_isready", every: 2s, within: 60s }`, `{ tcp: 6379 }` or `{ http: { path: /health, port: 8080 } }`; the first step waits until it is ready |

Every container of a run shares the workspace. All of a run's steps go to one runner while it
keeps in touch; if another runner takes over, files earlier steps wrote are gone (outputs and
artifacts survive).

## Inputs

```yaml
inputs:
  environment: { type: choice, options: [dev, prod], default: dev, description: Where to deploy }
  version:     { type: string, pattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$", required: true }
  dry_run:     { type: boolean, default: true }
  replicas:    { type: number, min: 1, max: 20, default: 2 }
  pythons:     { type: list, default: ["3.12", "3.13"] }
```

| Key | Meaning |
|---|---|
| `type` | `string` (the default), `choice`, `boolean`, `number` or `list` |
| `default` | Used when no value is given |
| `required` | A value must be given when there is no default |
| `options` | A choice's values |
| `pattern` | An RE2 regular expression a string must match (write `^` and `$`) |
| `min`, `max` | A number's bounds |
| `description` | One line |
| `from` | The name of a variable set at a level (see below): the input takes its value when a run is given none, before `default` |
| `options_from` | Recognized but not supported yet |

Runs started by a push, pull request or draft use the defaults, so a pipeline started by pushes
may not have a required input without a default. Inputs are not secret: they are recorded with the
run. An input is used in `when: { input: … }`, in `with:` (`{ input: name }`), in `for_each` lists,
and in deploy values.

## Repeating a step: for_each

```yaml
  test:
    for_each:
      python: ["3.12", "3.13"]
      os: { input: systems }                   # a list input
    exclude: [ { python: "3.12", os: alpine } ]
    add: [ { python: "3.14", os: debian } ]   # a combination outside the lists
    continue_for: [ { python: "3.14" } ]       # may fail without failing the run
    max_parallel: 2
    fail_fast: false                           # default true
    image: { each: python, format: "docker.io/library/python:%s-slim" }
    env: { TARGET_OS: { each: os } }
    run: pytest
```

The step becomes one step per combination, named `test[python=3.12,os=debian]`. Each gets
`LAUNCHWAY_EACH_<KEY>`. A step that needs `test` waits for all of them. At most 64 combinations by
default; values are 1 to 40 letters, digits, `.`, `_`, `+` and `-`.

## Test reports, findings, SBOMs and artifacts

```yaml
  test:
    image: docker.io/library/python:3.13-slim
    run: pytest --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml
  scan:
    image: registry.example.com/scanners:1
    run: semgrep scan --sarif --output reports/semgrep.sarif || true
    reports:
      findings: reports/*.sarif
      sbom: reports/sbom.cdx.json
  build:
    image: docker.io/library/golang:1.27
    run: make dist
    artifacts:
      - { name: binary, paths: [bin/app], retain: 30d }
  smoke:
    image: docker.io/library/alpine:3.22
    needs: build
    fetch: [ { from: build, name: binary, into: out } ]
    run: ./out/bin/app --version
```

Globs use `*`, `?`, `[a-z]` and `**`; absolute paths and `..` are refused. Collecting reports or
artifacts never changes a step's outcome; keep the step failing yourself when tests fail
(`status=$?` … `exit $status`).

## Caches

```yaml
  test:
    image: docker.io/library/python:3.13-slim
    cache:
      - name: pip
        paths: [.cache/pip]
        key: { files: [requirements.txt, requirements-dev.txt], text: v1 }
        fallback: latest      # none (the default) or latest
        save: success         # success (the default), always or never
    env: { PIP_CACHE_DIR: .cache/pip }
    run: pip install -r requirements.txt && pytest
```

The key is the paths and SHA-256 contents of the files `key.files` matches, in sorted order, with
`key.text` mixed in and the runner's operating system and architecture; change `text` to start over.
An entry is never changed once saved. A pull-request run restores the default branch's entry and saves
only into its own scope; a draft run saves nothing; nothing is shared between projects. A cache is an
optimisation: a restore or save that fails is logged on the step and never changes its outcome. Only
regular files and directories are kept (no symbolic links). The names and the policy (10 GiB a
project, 2 GiB an entry, 7 days idle by default) are set by an administrator; `list_caches` shows
what a project keeps.

## Templates from shared libraries

```yaml
libraries:
  platform: { repo: ci-templates, version: v3 }    # repo: a library the server lists, by name
steps:
  ci:
    uses: platform/python-service
    with: { test_command: "pytest -q tests/" }
  deploy:
    image: docker.io/library/alpine:3.22
    needs: ci
    run: echo "coverage was $LAUNCHWAY_NEEDS_CI_COVERAGE"
```

A step that uses a template takes only `uses`, `with`, `description`, `needs`, `when`, `retry`,
`timeout` and the `for_each` keys. It becomes the template's steps, named `ci/lint`, `ci/test`, …
`with` values are checked against the template's typed parameters; unknown names are refused.
List a library's templates and their parameters before writing a `uses` step.

A template file (`templates/<name>.yaml` in the library) has `template`, `description`, `params`
(`name: { type: string|number|boolean|list, default, description, options, from }`), `containers`,
`steps` and `outputs` (`key: { from: step.key }`). In a template, `{ param: name }` (with `format:`
for text around it) stands for a parameter's value, and `if: { param: name }` or
`if: { param: name, equals: value }` on a step leaves the step out when false. The `store`, `key` and `field` of a step's secret reference may be `{ param: name }` of a string parameter, and an `ai` step may be in a template (its `model`, `instructions` and `prompt` may be parameters).

## Variables set at a level

`from: name` on an input or a template parameter takes the variable `name`, set at the Base, the
Wing, a Squadron or the project. The nearest setting wins: what is given when a run starts, then the
pipeline file's own `with:`, then the project, the Squadron, the Wing, the Base, then the
template's own default; at the same level a variable set for the environment being deployed to beats the
general one; and a lock set at a level beats everything below it. Lists replace, never merge. A variable
that is locked refuses a different value under `with:` or given at the start, naming the level that
locked it. A secret or a map variable cannot be an input; a pipeline names a secret with `secrets:`.
Nothing reaches a step just because a variable has its name: the binding is `from:`.

## A runner for one step

`runner: { labels: [arm64] }` or `{ pool: gpu }` on a step that runs a script gives it a runner and
a fresh workspace of its own (a checkout of the run's commit), shared with no other step; files
travel between such steps only through `artifacts` and `fetch`. In a loop a label may be
`{ each: arch }`; in a template the pool and labels may be parameters. Steps without it keep the
run's workspace and the top-level `runner`. Not on image, deploy, rollout, approval, verify or ai
steps.

## Container images: kind: image

```yaml
  image:
    kind: image
    needs: test
    context: .
    file: Dockerfile
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
    scan: { fail_on: high, mode: enforce }
```

Keys: `context`, `file`, `target`, `platforms`, `build_args` (values, `{ output: step.key }`,
`{ run: sha|short_sha|branch|tag|number }` or `{ input: name }`), `secrets` (a list of
`{ id, secret: { store, key, field } }`, reaching the build only as `RUN --mount=type=secret`),
`tags` (`registry`, `repository`, `tag`), `push` (default true; never in draft or pull-request
runs), `cache` (`registry`, `repository`), `sbom`, `provenance`, `sign`, `scan`, and `fetch`,
`needs`, `when`, `timeout`, `retry`, `description`. Registries are the runner's, by name. The step
outputs the image's `digest`.

## Deployments: kind: deploy, rollback and rollout

A deploy step commits a new version to a GitOps repository, has Argo CD sync it and waits until it
is healthy. It needs an `environment` configured on the server; the validator checks it.

```yaml
  deploy-prod:
    kind: deploy
    needs: image
    when: { branch: main }
    environment: prod
    service: shop
    version: { output: test.version }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop/rollout.yaml, image: team/shop, digest: { output: image.digest } }
  smoke:
    image: docker.io/curlimages/curl:8.10.1
    needs: deploy-prod
    run: ./ci/smoke.sh
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]
```

Deploy keys: `environment`, `service`, `version`, `argocd` (`application`, `write_back` with
`repo`, `branch` and `changes`, each `{ file, image, tag }`, `{ file, image, digest }` or
`{ file, set, value }` (`set` is a YAML path, `value` what to write there);
`sync` with `request`, `resources`, `prune`, `windows`, `leftovers` (fail or warn: security-relevant objects left by an earlier render when pruning is off); `health`), `rollout` (`name`, `namespace`,
`until`, `pauses`), `on_cancel`, plus `needs`, `when`, `timeout`, `description`. A `rollback` step
takes `deployment`, `needs`, `timeout`, `abort_first`, `description`; it runs only if its
deployment was made and a step it needs did not succeed. A `rollout` step takes `deployment`,
`action`, `until`, `pauses`, `needs`, `timeout`, `description`. Deploy steps never run in draft
runs, pull-request runs or local runs.

## Draft runs

A draft run runs pipeline text that is not committed, against the current commit of a branch. Its
event is `draft`: steps without `when` run; steps with a `when` run only if its `event` names
`draft`; steps that declare `secrets` fail unless the project allows drafts to read secrets; deploy
steps never run. Use one to try a pipeline before committing it.
