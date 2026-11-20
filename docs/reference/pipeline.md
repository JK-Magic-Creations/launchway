<!--
How this page is checked (read before editing).

Every key the parser accepts is listed in a table marked with an HTML comment on the line
before it:

  <!- - keys: NAME - ->        (written without the spaces between the dashes)
  | Key | ... |
  |---|---|
  | `key` | ... |

Each row of a marked table starts with the key in a code span. The Go test
internal/pipeline/docs_reference_test.go works out which keys the parser accepts at each NAME
(by adding an unknown key there and reading the parser's "expected one of" message) and fails
if a marked table lists a key the parser does not accept, or leaves one out. The table
step-common has a second column naming, in code spans, the kinds each row applies to; the keys
of a kind are those rows plus the table kind-<kind>. To document a new mapping, add a marked
table here and a probe for it in that test. Unmarked tables are not checked.

Examples: a ```yaml block preceded by a line <!- - validate - -> (without the spaces) must be a
complete pipeline that `launchway validate` accepts; one preceded by <!- - validate-template - ->
must be a complete template that reads without problems using its parameters' defaults. The
Go test parses both kinds, and scripts/docs/check-pipeline-examples.sh runs the real binary on
the pipelines. Fragments are not marked.
-->

# Pipeline file reference

This page lists every key of the pipeline file, `.launchway/pipeline.yaml`, and of the
template files of a [shared library](../glossary.md#library): its type, default, allowed values
and limits. It is for anyone who writes or reviews pipelines and wants the exact rules; for a
guided start, see [Your first pipeline](../tutorials/first-pipeline.md).

Everything here is checked by the same parser that the server, the editor and
[`launchway validate`](cli.md) use. A mistake is reported with its line number before anything
runs, and an unknown key is always an error: the message lists the keys allowed at that place.

## Conventions

- **Pattern** means a glob as Go's `path.Match` reads it: `*` matches any characters except
  `/`, `?` one character, and `[a-z]` a class. `release/*` matches `release/1.2` but not
  `release/1.2/fix`.
- **Duration** means a Go duration such as `30s`, `10m`, `1h30m` (units `s`, `m`, `h`).
- **One value or a list** means either `needs: build` or `needs: [build, lint]`.
- **Boolean** values are written `true`, `false`, `yes` or `no`.
- **Name** limits are given as regular expressions where they matter, for example
  `^[a-z0-9][a-z0-9-]{0,62}$` (lower-case letters, digits and hyphens, 1 to 63 characters).
- A file may be at most 256 KiB. The parser looks for `.launchway/pipeline.yaml`, then
  `.launchway/pipeline.yml`.
- There is no expression language. Values are written in place, or taken from a small closed set
  of [value forms](#value-forms).

## A complete example

<!-- validate -->
```yaml
pipeline: my-service
on:
  push: { branches: [main, "release/*"], tags: ["v*"] }
  pull_request: {}
supersede: true

steps:
  test:
    description: Run the unit tests
    image: golang:1.27
    run: go test ./...
    retry: 1
    timeout: 20m

  build:
    image: alpine:3.22
    needs: test
    run: |
      echo "building $LAUNCHWAY_COMMIT"
      echo digest=sha256:0123 >> "$LAUNCHWAY_OUTPUTS"

  publish:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "publishing $LAUNCHWAY_NEEDS_BUILD_DIGEST"
```

Steps with no unmet `needs` run in parallel. Each step's script runs with `sh -e` in the
repository checkout. A step passes values to the steps that need it by writing `key=value` lines
to the file named by `$LAUNCHWAY_OUTPUTS`; those steps receive them as
`LAUNCHWAY_NEEDS_<STEP>_<KEY>`. The [environment variables reference](environment-variables.md)
lists every variable a step receives.

## Top-level keys

<!-- keys: top -->
| Key | Type | Default | Description |
|---|---|---|---|
| `pipeline` | name | required | The pipeline's name. `^[a-z0-9][a-z0-9-]{0,62}$` |
| `on` | mapping | none | The events that start a run; see [Triggers](#triggers). Without it, runs start only by hand |
| `supersede` | boolean | `false` | A new run started by a push or a pull request cancels the older runs of the same ref still going, if they too were started by a push or a pull request. Runs started by hand, draft runs and re-runs never supersede and are never superseded. A run holding an open [deployment](../glossary.md#deployment) is never cancelled |
| `inputs` | mapping | none | The [run inputs](#inputs): values a run is started with. At most 50 |
| `libraries` | mapping | none | The [shared libraries](#libraries) whose templates steps use. At most 10 |
| `containers` | mapping | none | Named [containers and services](#containers) the steps run in or beside |
| `runner` | mapping | none | Which runners may run the pipeline's steps; see [Runner](#runner) |
| `lane` | mapping | none | Makes runs of the project take turns, and says what a run that arrives while another goes does; see [Lane](#lane). Not together with `supersede`. Needs schema 24 |
| `timeout` | duration | none | How long a run may stay open, from its creation: `1m` to `720h`, and no more than the installation's `max_run_timeout` where it sets one (`launchway run-limits`). A run past it is canceled, with the reason `run timeout of 2h reached`; a deployment it holds is handled as a cancel by hand handles it. **Time spent waiting for an approval does not count**, nor does time between a run's end and its resume. Needs schema 24 |
| `steps` | mapping | required | The [steps](#steps), by name; at least one. Written order is kept for display |

Keys that the design has but are not built yet are refused with a hint: `outputs` (write to
`$LAUNCHWAY_OUTPUTS` instead), `runs_on` (use [`runner`](#runner)) and `matrix` (use
[`for_each`](#for-each)).

## Triggers

`on` says which events start a run. A run can always be started by hand, from the UI, the API or
[`launchway runs start`](cli.md), whatever `on` says.

<!-- keys: on -->
| Key | Type | Default | Description |
|---|---|---|---|
| `push` | mapping | none | Start a run when a matching branch or tag is pushed |
| `pull_request` | mapping | none | Start a run when a pull request is opened or its head moves. Write `pull_request: {}` (or the key with no value) for every pull request, or filter by the branch they target with `into` (see [Pull request](#pull-request)) |
| `schedule` | list | none | Start runs at times written as cron expressions. At most 20 |
| `webhook` | mapping, or a list of mappings | none | Let other systems start runs by calling a URL. At most 20 |

The server polls each project's repository (every 30 seconds by default; see
[`projects[].poll`](server-config.md#projects)), so a push starts a run at the next poll, or at once
when the Git host calls the project's Git webhook.

### Push

<!-- keys: on-push -->
| Key | Type | Default | Description |
|---|---|---|---|
| `branches` | pattern, one value or a list | none | Branch names that start a run, such as `main` or `release/*` |
| `tags` | pattern, one value or a list | none | Tag names that start a run, such as `v*` |

At least one of `branches` or `tags` is required.

### Pull request

Pull-request runs run the pipeline at the pull request's head commit (`refs/pull/<n>/head`). They
run only if the pipeline on the project's default branch also has `pull_request`, so a pull request
cannot turn pull-request runs on for itself. A pull request has no branch or tag of its own:
`when: { branch: ... }` never matches a pull-request run, and `branches` and `tags` are not keys of
`pull_request`. What it has is the branch it would land on, and `into` filters by that:

<!-- keys: on-pull-request -->
| Key | Type | Default | Description |
|---|---|---|---|
| `into` | pattern, one value or a list | any branch | Run only for pull requests that target a branch matching one of the patterns, written as the `branches` of [`push`](#push). The step environment gets `LAUNCHWAY_PULL_REQUEST_INTO`. Needs schema 24 |

The server sees only each pull request's head, so it learns the target from the project's **forge
connection**, and does so when it first sees each head, reading the pipeline on the default branch
(the reviewed one) to know whether to filter. If the project has no forge connection, a pipeline
with `into` runs no pull requests, and the project's recent pushes say so: *pull request #12 not
run: the pipeline filters by target branch and the project has no forge connection*. If the forge
cannot be reached, the pull request is not run either (the server never runs unfiltered): the
decision says *waiting: forge unreachable*, and the next poll tries again for as long as the head
is the newest. The target is read once for each head; when a pull request is retargeted and its
head moves, it is read again and the decision says that the target changed. The target is kept on
the run (`pull_request_into` in the run JSON) and shown with the pull request. `pull_request: {}`
needs no forge and no newer server.

<!-- validate -->
```yaml
pipeline: checks
on:
  pull_request: { into: [main, "release/*"] }
steps:
  test:
    image: alpine:3.22
    run: echo "testing a change into $LAUNCHWAY_PULL_REQUEST_INTO"
```

Steps of pull-request runs may not read secrets unless the server's configuration allows it
([`pull_requests.secrets`](server-config.md#drafts-and-pull-requests)). See
[Pull requests](../tutorials/pull-requests.md).

### Schedule

<!-- keys: on-schedule -->
| Key | Type | Default | Description |
|---|---|---|---|
| `cron` | text | required | Five fields: minute (0-59), hour (0-23), day of the month (1-31), month (1-12 or `JAN`-`DEC`), day of the week (0-7 or `SUN`-`SAT`; 0 and 7 are Sunday). Each field is `*`, a value, a range `a-b`, a list `a,b,c`, or any of those with a step (`*/15`, `1-30/2`). `@hourly`, `@daily`, `@weekly`, `@monthly` and `@yearly` work too. When both day fields are restricted, a day matches if either does |
| `timezone` | IANA time zone | UTC | Such as `Europe/Berlin` or `America/Chicago`. `Local` is refused |
| `ref` | branch or tag | the default branch | The branch or tag whose pipeline runs |
| `inputs` | mapping | none | Values for the pipeline's [inputs](#inputs), by input name; the others take their defaults. A list input takes a YAML list |

Schedules are read only from the pipeline on the project's default branch. A time that does not
exist because the clocks go forward is skipped; a time that happens twice fires once. Two identical
entries are refused. Runs started by a schedule have the event `schedule`.

### Webhook

`webhook` is one mapping, or a list of them.

<!-- keys: on-webhook -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | name | required | The trigger's name, the last part of its URL: `POST /api/v1/projects/<project>/webhooks/<name>`. `^[a-z0-9][a-z0-9-]{0,62}$`, unique in the pipeline |
| `ref` | branch or tag | the default branch | The branch or tag whose pipeline runs |
| `inputs` | mapping | none | Input name to a field of the request's JSON body, written `body.field` or `body.a.b` (at most 10 parts, each `[A-Za-z0-9_-]{1,64}`). A missing or `null` field gives no value, so the input takes its default |

Webhook triggers are read only from the default branch. A trigger can be called only if the
server's configuration says what callers must prove
([`projects[].webhooks`](server-config.md#projects)). Runs started this way have the event
`webhook`.

<!-- validate -->
```yaml
pipeline: nightly
on:
  push: { branches: [main] }
  schedule:
    - { cron: "0 3 * * *", timezone: America/Chicago, ref: main, inputs: { environment: prod } }
    - { cron: "*/15 9-17 * * MON-FRI" }
  webhook:
    name: deploy-request
    ref: main
    inputs: { version: body.release.version, environment: body.environment }
inputs:
  environment: { type: choice, options: [dev, prod], default: dev }
  version: { type: string, default: latest }
steps:
  report:
    image: alpine:3.22
    run: echo "$LAUNCHWAY_EVENT $LAUNCHWAY_INPUT_ENVIRONMENT $LAUNCHWAY_INPUT_VERSION"
```

## Inputs

`inputs` declares the values a run is started with: a mapping of input names to settings. Names
match `^[a-z][a-z0-9_-]{0,39}$`, and two names may not become the same variable (`a-b` and `a_b`).
Each step receives every input as `LAUNCHWAY_INPUT_<NAME>` (upper case, `-` as `_`).

<!-- keys: inputs -->
| Key | Type | Default | Description |
|---|---|---|---|
| `type` | text | `string` | `string`, `choice`, `boolean`, `number` or `list` (a list of text values) |
| `description` | text | none | One line of at most 200 characters, shown in the form |
| `default` | value | none | Used when no value is given. A list's default is a YAML list. Checked against the other settings |
| `required` | boolean | `false` | A value must be given when there is no default; for a string, it may not be empty. A pipeline that pushes or pull requests start may not have a required input without a default, and a schedule or webhook must give one |
| `options` | one value or a list | none | A `choice`'s values: required for a choice, refused for other types. At most 200, no repeats |
| `pattern` | regular expression | none | `string` only: an RE2 expression the value must match. Write `^` and `$` to match the whole value. At most 500 characters |
| `min` | number | none | `number` only: the smallest value allowed |
| `max` | number | none | `number` only: the largest value allowed; not less than `min` |
| `options_from` | none | none | Not available yet (choices whose options come from a step or another system); refused. List the options under `options` |

Values travel as text in one canonical form: `true` or `false`; a number in its shortest decimal
form (`2`, `2.5`); a list as a JSON array (`["3.12","3.13"]`). A string value is at most 4096 bytes;
a list has at most 100 items and 16 KiB. Runs started by a push, a pull request or a draft use the
defaults. An input that is not required, has no default and is not given is empty (`false` for a
boolean, `[]` for a list).

An input is used in exactly these places: its variable; a step's
[`when: { input: name, equals: value }`](#when); a template's parameter
([`with: { name: { input: name } }`](#value-forms)); a loop's list
([`for_each: { key: { input: name } }`](#for-each)); and a deploy step's `version`, an image
tag or a written value, and a deploy step's `environment` ([value forms](#value-forms)).
Inputs are recorded with the run and shown to everyone who can see it: never use them for
secrets.

## Libraries

`libraries` names the shared libraries the pipeline uses templates from, by the name steps use in
`uses: <name>/<template>`. Names match `^[a-z][a-z0-9-]{0,39}$`.

<!-- keys: libraries -->
| Key | Type | Default | Description |
|---|---|---|---|
| `repo` | name | required | One of the libraries the server's configuration allows ([`libraries[].name`](server-config.md#libraries)). Never an address |
| `version` | tag, branch or full commit id | required | At most 128 letters, digits and `. _ / + -`, starting with a letter or digit, without `..` or `//`. Resolved when a run is created: a tag first, then a branch, then a commit id |

See [Templates](#template-files) and [Reuse steps with library templates](../tutorials/library-templates.md).

## Containers

`containers` names images once, so steps can use them by name, and declares services that run
beside the steps for the whole run (a database for tests, say). All of a run's containers share the
workspace; on Kubernetes they share one pod, so services are reachable at `localhost`.

Names match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`. `workspace` and names starting with `image-`
are reserved.

<!-- keys: containers -->
| Key | Type | Default | Description |
|---|---|---|---|
| `image` | image reference | required | The container image |
| `env` | mapping | none | Environment variables, name to value. Names match `^[A-Za-z_][A-Za-z0-9_]*$`; names starting with `LAUNCHWAY_` are reserved |
| `cpu` | quantity | the runner's | A CPU request on Kubernetes, such as `500m` or `2` |
| `memory` | quantity | the runner's | A memory request on Kubernetes, such as `512Mi` or `2Gi` |
| `service` | boolean | `false` | Run the container's own command for the whole run instead of running steps in it. Steps cannot use a service as their `container` |
| `command` | one value or a list | the image's own | Services only: the command to run |

<!-- validate -->
```yaml
pipeline: java-service
containers:
  maven: { image: "maven:3.9-eclipse-temurin-21", memory: 2Gi, cpu: "1" }
  postgres: { image: "postgres:17", service: true, env: { POSTGRES_PASSWORD: test } }
steps:
  build: { container: maven, run: mvn -B package }
  test:
    container: maven
    needs: build
    env: { DATABASE_URL: "postgres://postgres:test@localhost:5432/postgres" }
    run: mvn -B verify
```

## Steps

`steps` is a mapping of step names to steps. Names match `^[a-z0-9][a-z0-9_-]{0,62}$`. A step's
`kind` decides what it does and which keys it takes:

| Kind | What it does | Section |
|---|---|---|
| `step` (the default) | Runs a script in a container image | [Kind step](#kind-step) |
| `approval` | Waits for a person to approve or reject | [Kind approval](#kind-approval) |
| `image` | Builds, scans, signs and pushes a container image | [Kind image](#kind-image) |
| `deploy` | Deploys a version to an environment with Argo CD | [Kind deploy](#kind-deploy) |
| `rollback` | Reverts a deployment when a step guarding it did not succeed | [Kind rollback](#kind-rollback) |
| `rollout` | Promotes, aborts or retries a deployment's Argo Rollouts rollout | [Kind rollout](#kind-rollout) |
| `verify` | Watches a deployment's metrics and logs for a window of time and decides passed, failed or inconclusive | [Kind verify](#kind-verify) |
| `ai` | Asks a model the operator configured one question, with inputs from earlier steps | [Kind ai](#kind-ai) |

A step with `uses:` is replaced by a template's steps; it is not a kind, but it takes its own set of
keys ([A step that uses a template](#a-step-that-uses-a-template)). The kinds `release`, `promote`,
`input` and `analysis` are recognised and refused as not available yet.

### Step keys common to all kinds

The second column says which kinds take the key. `uses` stands for a step that uses a template.

<!-- keys: step-common -->
| Key | Kinds | Type | Default | Description |
|---|---|---|---|---|
| `kind` | `step` `approval` `image` `deploy` `rollback` `rollout` `verify` `ai` | text | `step` | What the step is. A step that uses a template takes no `kind` |
| `description` | `step` `approval` `uses` `image` `deploy` `rollback` `rollout` `verify` `ai` | text | none | One line of at most 200 characters, shown under the step's name and on the graph |
| `needs` | `step` `approval` `uses` `image` `deploy` `rollback` `rollout` `verify` `ai` | step names, one value or a list | none | Steps that must end first. By default a step runs only if all of them succeeded; see [`when.upstream`](#when). A step cannot need itself, and needs may not form a loop. Needing a step that uses a template, or a loop, means needing all of its steps |
| `when` | `step` `approval` `uses` `image` `deploy` `verify` `ai` | mapping | none | Conditions for the step to run; see [When](#when). Otherwise the step is skipped |
| `timeout` | `step` `approval` `uses` `image` `deploy` `rollback` `rollout` `verify` `ai` | duration | depends on the kind | How long the step may take. Defaults and bounds per kind are in the kind's section |
| `retry` | `step` `uses` `image` `ai` | whole number, or a mapping | `0` | Extra attempts after a failure, 0 to 10; the mapping also says how long to wait and for which causes (see [Retry](#retry)) |
| `on_failure` | `step` `image` | `stop` or `continue` | `stop` | What a failure that is final (after its retries) does to the run. With `continue` the step ends `failed (allowed)`: the run's outcome ignores it, `fail_fast` ignores it, and the steps that need it run as if it had succeeded, unless they say `when: { upstream: failed }`. Not for deploy, rollback, rollout, verify, ai or approval steps (each has its own failure handling), and not on a step that uses a template: set it inside the template. Needs schema 24 |

For a step that uses a template, `when`, `retry` and `timeout` apply to every step of the
template that does not set its own.

### Kind step

A step that runs a script. `timeout` is per attempt: default `1h`, from `1s` to `24h`.

<!-- keys: kind-step -->
| Key | Type | Default | Description |
|---|---|---|---|
| `image` | image reference | one of `image` or `container` is required | The container image the step runs in. Each image used this way gets a container of its own in the run |
| `container` | container name | | A container from [`containers`](#containers), instead of `image`. Not a service |
| `run` | text | required | The shell script, run with `sh -e` in the workspace (the repository checkout) |
| `env` | mapping | none | Environment variables, name to value. Names match `^[A-Za-z_][A-Za-z0-9_]*$`; `LAUNCHWAY_*` is reserved. In a looped step a value may be [`{ each: key }`](#value-forms) |
| `secrets` | mapping | none | Environment variables whose values a runner looks up: variable name to a [secret reference](#secret-references). A name may not also be under `env` |
| `reports` | mapping | none | Files the step writes that Launchway reads once it ends, passed or failed; see [Reports](#reports) |
| `resources` | mapping | the installation's default for the kind | The CPU and memory the step asks for; see [Resources](#resources) |
| `artifacts` | list | none | Files the step keeps once it has run; see [Artifacts](#artifacts) |
| `fetch` | list | none | Artifacts of earlier steps to put in the workspace first; see [Fetch](#fetch) |
| `for_each` | mapping | none | Repeat the step once for each combination of values; see [For each](#for-each) |
| `exclude` | list | none | With `for_each`: combinations to leave out |
| `max_parallel` | whole number | no bound | With `for_each`: how many members may be queued or running at once, 1 to 500 |
| `fail_fast` | boolean | `true` | With `for_each`: once a member fails for good, cancel the others not started and stop those running |

A step that fails keeps its last few lines of output with the step, so the reason shows where the
run says it failed. Secret values are masked in logs and outputs; values shorter than 4 characters
are not masked, and the log says so. See [Manage secrets](../howto/secrets.md).

### Kind approval

An approval pauses the run until a person approves or rejects it. It runs nothing. `timeout` is
how long to wait for a decision: default `24h`, from `1s` to `30d` (`720h`). Rejecting, or nobody
deciding in time, fails the step and the steps after it are skipped. An approval cannot be repeated
by `for_each`. See [Deploy with an approval](../tutorials/deploy-with-approval.md).

<!-- keys: kind-approval -->
| Key | Type | Default | Description |
|---|---|---|---|
| `message` | text | required | The question to decide, one line of at most 500 characters |
| `deployment` | step name | none | A deploy step this approval guards: the approval then shows the live rollout, is rejected by itself if the rollout is aborted, and needs the operator role on that environment. The approval must need the deploy step, directly or not, and takes no `when.upstream`. Not allowed in a template |

<!-- validate -->
```yaml
pipeline: approval-example
steps:
  smoke-test:
    image: alpine:3.22
    run: echo smoke
  approve-prod:
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy 2.14 to production?
    timeout: 48h
  deploy-prod:
    image: alpine:3.22
    needs: approve-prod
    run: ./deploy.sh
```

### A step that uses a template

A step with `uses` is replaced, when a run is created, by the steps of a template from one of the
pipeline's [libraries](#libraries). Its steps get the names `<step>/<template-step>`, such as
`ci/lint`. See [Template files](#template-files).

<!-- keys: kind-uses -->
| Key | Type | Default | Description |
|---|---|---|---|
| `uses` | `library/template` | required | The library, as named under `libraries`, and the template. In a template, a template of the same library is written by name alone |
| `with` | mapping | none | Values for the template's parameters, by name; at most 50, each at most 4096 bytes. A value is text, a YAML list (for a `list` parameter), or [`{ input: name }`](#value-forms) with an optional `format` |
| `for_each` | mapping | none | Repeat the whole template once for each combination; see [For each](#for-each) |
| `exclude` | list | none | With `for_each`: combinations to leave out |
| `max_parallel` | whole number | no bound | With `for_each`: members queued or running at once, 1 to 500 |
| `fail_fast` | boolean | `true` | With `for_each`: stop the other members once one fails |

`launchway validate` checks such a step without reading the template; the server's editor and
`POST /api/v1/validate?project=NAME` expand it.

### Kind image

An image step builds a container image from a Dockerfile in the workspace with a BuildKit daemon
the runner's operator runs, and scans, signs and pushes it. It runs no script and has no
container in the run. `timeout` defaults to `1h`, from `1m` to `24h`. It may be in a template, and
cannot be repeated by `for_each` yet. See [Build and sign an image](../tutorials/build-and-sign-image.md).

<!-- keys: kind-image -->
| Key | Type | Default | Description |
|---|---|---|---|
| `context` | path | `.` | The build context: a directory relative to the workspace, without `..` or a leading `/` |
| `file` | path | `<context>/Dockerfile` | The Dockerfile, relative to the workspace |
| `target` | text | none | A stage of the Dockerfile. `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$` |
| `platforms` | one value or a list | the builder's own | `os/arch` or `os/arch/variant`, such as `linux/amd64`, `linux/arm64`. At most 8, no repeats |
| `build_args` | mapping | none | Build arguments, name to a [value](#value-forms). At most 50. Recorded in the provenance: never put a secret in one |
| `secrets` | list | none | Build secrets, each `{ id, secret }`; see [Image build secrets](#image-build-secrets). At most 20. They reach the build only as `RUN --mount=type=secret,id=...` |
| `tags` | list | required unless `push: false` | Names to push under; see [Image tags](#image-tags). At least 1, at most 20 |
| `push` | boolean | `true` | Push the image. Pull-request and draft runs never push, whatever it says |
| `cache` | mapping | none | A repository the build cache is read from and, on runs that push there, written to; see [Image cache](#image-cache) |
| `sbom` | boolean | `false` | Make a CycloneDX SBOM of each platform's image, kept as the step's artifact `sbom` |
| `provenance` | boolean | `true` | Record where the image came from, kept as the step's artifact `provenance` |
| `sign` | boolean or mapping | none | `true` signs with the runner's own key; a mapping signs with a key from a secret store; see [Image signing](#image-signing) |
| `scan` | boolean or mapping | none | `true` scans for known vulnerabilities and records them; a mapping sets the policy; see [Image scan](#image-scan) |
| `fetch` | list | none | Artifacts of earlier steps to put in the workspace before the build; see [Fetch](#fetch) |

An image step's outputs are `digest`, `image` (`host/repository@sha256:...`, empty when nothing was
pushed), `tags` (comma-separated) and `pushed` (`true` or `false`). Steps that need it receive them as
`LAUNCHWAY_NEEDS_<STEP>_DIGEST` and so on; deploy steps read them as `{ output: <step>.digest }`.
Other output names are refused.

#### Image tags

<!-- keys: image-tag -->
| Key | Type | Default | Description |
|---|---|---|---|
| `registry` | name | required | A registry the runner's configuration names ([`images.registries`](runner-config.md#images)), in lower case |
| `repository` | text | required | The repository in that registry, without the host, such as `team/app` |
| `tag` | [value](#value-forms) | required | The tag, such as `latest` or `{ run: short_sha }` |

#### Image cache

<!-- keys: image-cache -->
| Key | Type | Default | Description |
|---|---|---|---|
| `registry` | name | required | A registry the runner's configuration names |
| `repository` | text | required | The cache repository, such as `team/cache/app` |

#### Image build secrets

<!-- keys: image-secret -->
| Key | Type | Default | Description |
|---|---|---|---|
| `id` | text | required | The ID the Dockerfile uses in `RUN --mount=type=secret,id=...`. `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`, unique in the step |
| `secret` | [secret reference](#secret-references) | required | Where the value is kept |

#### Image signing

<!-- keys: image-sign -->
| Key | Type | Default | Description |
|---|---|---|---|
| `key` | [secret reference](#secret-references) | required | A cosign private key (ECDSA, as `cosign generate-key-pair` makes it) or a PKCS#8 PEM key, in a secret store |
| `password` | [secret reference](#secret-references) | none | The key's password, if it is encrypted. Goes with `key` |
| `keyless` | none | none | Not available yet (keyless signing with an OIDC identity); refused. Sign with `sign: true` or a key |

#### Image scan

<!-- keys: image-scan -->
| Key | Type | Default | Description |
|---|---|---|---|
| `mode` | text | `report` | `report` records what is found; `enforce` fails the step, before anything is pushed, on a finding at `fail_on` or worse |
| `fail_on` | text | none | `critical`, `high`, `medium` or `low`. Required with `mode: enforce` |

<!-- validate -->
```yaml
pipeline: shop
on:
  push: { branches: [main] }
steps:
  test:
    image: golang:1.27
    run: go test ./... && echo version=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    needs: test
    context: .
    file: Dockerfile
    target: runtime
    platforms: [linux/amd64, linux/arm64]
    build_args: { VERSION: { output: test.version }, COMMIT: { run: sha } }
    secrets:
      - { id: npm_token, secret: { store: vault, key: ci/npm, field: token } }
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
      - { registry: harbor, repository: team/shop, tag: latest }
    cache: { registry: harbor, repository: team/cache/shop }
    sbom: true
    sign: true
    scan: { fail_on: high, mode: enforce }
```

### Kind deploy

A deploy step commits a version to a GitOps repository, asks Argo CD to sync if it must, and waits
until the application (and its Argo Rollouts rollout, if one is named) is healthy on that version.
A deploy runner serving the environment's target does the work. `timeout` covers the whole step:
default `30m`, from `1m` to `24h`. Deploy steps cannot be repeated by `for_each`, cannot be in a
template, take no `when.upstream`, and do not run in draft runs, pull-request runs or
`launchway run`. See [Deploy with an approval](../tutorials/deploy-with-approval.md).

<!-- keys: kind-deploy -->
| Key | Type | Default | Description |
|---|---|---|---|
| `environment` | name, or `{ input: name }` | required | One of the server's [environments](server-config.md#environments): it gives the lock and the record. `^[a-z0-9][a-z0-9-]{0,62}$`. `{ input: name }` takes it from a `choice` input whose options are all environment names |
| `service` | name | the project's name | What is deployed there. `^[a-z0-9][a-z0-9-]{0,62}$` |
| `version` | [value](#value-forms) | `{ run: short_sha }` | What the environment's "current version" shows |
| `argocd` | mapping | required | How to deploy with Argo CD; see [Argo CD](#argo-cd) |
| `rollout` | mapping | none | An Argo Rollouts rollout to wait for as well; see [Rollout](#rollout) |
| `on_cancel` | text | `leave` | `leave`, or `abort`: when the run is cancelled by hand while the deployment is open, abort the rollout. `abort` needs `rollout` |

The keys `revision` and `source` (syncing to a revision without a commit) are refused as not
available yet. Two deploy steps of the same service and environment must be ordered by `needs`.

#### Argo CD

<!-- keys: argocd -->
| Key | Type | Default | Description |
|---|---|---|---|
| `application` | Kubernetes name | required | The Argo CD application |
| `write_back` | mapping | required | The commit to make; see [Write back](#write-back) |
| `sync` | mapping | `request: auto`, `windows: wait` | Whether and how to ask Argo CD to sync; see [Sync](#sync) |
| `health` | text | `application`, or `resources` when `sync.resources` is set | `application`: the application's sync and health decide. `resources`: only the resources under `sync.resources` decide |

#### Write back

<!-- keys: write-back -->
| Key | Type | Default | Description |
|---|---|---|---|
| `repo` | name | required | A repository the deploy runner's configuration names ([`deploy.repos`](runner-config.md#deploy)). `^[a-z0-9][a-z0-9_-]{0,62}$` |
| `branch` | branch name | required | The branch to commit to |
| `changes` | list | required | The edits to make, at least 1 and at most 50; see [Changes](#changes) |

#### Changes

Each change edits one file: either every reference to an image gets a tag or a digest, or the
scalar at a YAML path gets a value. Comments and layout are kept.

<!-- keys: change -->
| Key | Type | Default | Description |
|---|---|---|---|
| `file` | path | required | The file, relative to the repository's root, without `..` or a leading `/` |
| `image` | image name | with `tag` or `digest` | The image whose references change, such as `team/shop`. A file with no reference to it is an error |
| `tag` | [value](#value-forms) | | The new tag |
| `digest` | [value](#value-forms) | | The new digest, such as `{ output: image.digest }`: references become `image@sha256:...`, which nobody can move. Not with `tag` |
| `set` | YAML path | with `value` | The scalar to change, such as `image.tag` |
| `value` | [value](#value-forms) | | Its new value |

#### Sync

<!-- keys: sync -->
| Key | Type | Default | Description |
|---|---|---|---|
| `request` | text | `auto` | `auto` (ask only if the application does not sync by itself), `always` or `never` |
| `resources` | list | the whole application | Sync, and judge, only these; at most 50; see [Sync resources](#sync-resources) |
| `prune` | boolean | `false` | Prune resources Git no longer has |
| `windows` | text | `wait` | While an Argo CD sync window does not allow the sync: `wait` (until the deadline) or `fail`. Windows are never overridden |

#### Sync resources

<!-- keys: sync-resource -->
| Key | Type | Default | Description |
|---|---|---|---|
| `kind` | text | required | As Kubernetes writes it, such as `Rollout` or `Deployment` |
| `name` | Kubernetes name | required | The resource's name |
| `namespace` | namespace | none | The resource's namespace |
| `group` | API group | known for common kinds | Required for kinds other than `Rollout`, `AnalysisTemplate` (`argoproj.io`), `Deployment`, `StatefulSet`, `DaemonSet`, `ReplicaSet` (`apps`), `Job`, `CronJob` (`batch`), `Ingress` (`networking.k8s.io`), and `Service`, `ConfigMap`, `ServiceAccount`, `PersistentVolumeClaim` (core, empty) |

#### Rollout

<!-- keys: rollout-ref -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | Kubernetes name | required | The rollout's name |
| `namespace` | namespace | required | Its namespace |
| `until` | text | `healthy` | `healthy`, or `paused`: end the step at the rollout's first indefinite pause. With `paused`, a [`kind: rollout`](#kind-rollout) step naming this deployment must carry it on |
| `pauses` | text | `wait` | What to do at an indefinite pause on the way to healthy: `wait` (a person promotes from the run page), `promote` (promote each such pause once) or `fail`. Not with `until: paused` |

The key `target` (a rollout on another target) is refused as not available yet.

### Kind rollback

A rollback step reverts what its deploy step committed, asks Argo CD to sync if it must, and waits
until the previous version is healthy. It runs if and only if its deployment was made and one of
the steps it needs did not succeed; otherwise it is skipped as not needed. `timeout` defaults to
`15m`, from `1m` to `24h`. It takes no `when`.

<!-- keys: kind-rollback -->
| Key | Type | Default | Description |
|---|---|---|---|
| `deployment` | step name | required | The deploy step it rolls back. It must need that step, directly or not; a deploy step has at most one rollback step |
| `abort_first` | boolean | `false` | Abort the deployment's rollout first, then revert. Needs a `rollout` on the deploy step |

`needs` is required: the steps that guard the deployment. None of them, directly or not, may run
after failures (`when.upstream: failed` or `always`), and a rollback cannot need another rollback.

### Kind rollout

A rollout step acts on the Argo Rollouts rollout of a deployment and then watches it. `timeout`
defaults to `30m`, from `1m` to `24h`. It takes no `when`: it runs when the steps it needs have
succeeded, and none of them, directly or not, may run after failures.

<!-- keys: kind-rollout -->
| Key | Type | Default | Description |
|---|---|---|---|
| `deployment` | step name | required | The deploy step whose rollout it acts on. It must need that step, directly or not, and that step must name a `rollout` |
| `action` | text | required | `promote` (past an indefinite pause), `promote-full` (skip the remaining steps and analyses), `abort` or `retry` (after an abort) |
| `until` | text | `healthy` | `healthy`, or `paused`: watch until the next indefinite pause. Not for `abort` |
| `pauses` | text | `wait` | As for a deploy step's [`rollout.pauses`](#rollout). Not for `abort`, nor with `until: paused` |

`needs` is required. A rollout step cannot come after a rollback step.

<!-- validate -->
```yaml
pipeline: shop
on:
  push: { branches: [main] }
steps:
  build:
    image: alpine:3.22
    run: echo tag=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  deploy-prod:
    kind: deploy
    needs: build
    when: { branch: main }
    environment: prod
    service: shop
    version: { output: build.tag }
    timeout: 30m
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop/rollout.yaml, image: team/shop, tag: { output: build.tag } }
          - { file: helm-values/shop.yaml, set: image.tag, value: { output: build.tag } }
      sync:
        request: auto
        resources:
          - { kind: Rollout, name: shop, namespace: shop }
        prune: false
        windows: wait
    rollout: { name: shop, namespace: shop, until: paused }
    on_cancel: abort
  canary-smoke:
    image: curlimages/curl:8.10.1
    needs: deploy-prod
    run: ./ci/smoke.sh http://shop-canary.shop.svc
  approve-full:
    kind: approval
    needs: canary-smoke
    deployment: deploy-prod
    message: "The canary passed its smoke test. Promote shop to 100%?"
  promote:
    kind: rollout
    needs: approve-full
    deployment: deploy-prod
    action: promote
    until: healthy
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true
```

### Kind verify

A verify step watches a deployment (or an environment) for a window of time, reads its metrics and
logs from [health sources](runner-config.md#health-sources) an operator configured, and decides
**passed**, **failed** or **inconclusive** with built-in statistics. It runs no script: a deploy
runner next to the monitoring systems reads the data. `timeout` defaults to the delay and the
window plus an hour, and must leave room for the delay, the window and a minute more; at most
`24h`. It cannot be in a template or in a loop. It needs a server that knows verify steps (schema
19). See [Verifying a deployment](../guide/verification.md) for how the verdict is reached.

<!-- keys: kind-verify -->
| Key | Type | Default | Description |
|---|---|---|---|
| `deployment` | step name | one of `deployment` or `environment` with `service` | The deploy step whose deployment to watch: its environment, service and pods |
| `environment` | name | | Instead of `deployment`: the environment to watch, with `service` |
| `service` | name | | The service to watch, with `environment` |
| `delay` | duration | `0s` | Before the first interval, to let the new version warm up. At most `1h` |
| `duration` | duration | `10m` | The window to watch: a whole number of intervals, at most `6h` |
| `interval` | duration | `1m` | How often readings are taken and judged, at least `10s`. At most 120 intervals fit in the window |
| `sensitivity` | text | `medium` | `low`, `medium` or `high`: how large a deviation must be to fail (high fails on smaller ones) |
| `baseline` | whole number or `false` | `3` | How many earlier passed verifications of the same environment and service to compare with, 0 to 10. `false` or `0` compares with none |
| `on_fail` | text | `fail` | `fail` (the step fails, and a rollback step that needs it then runs), `abort` (also abort the deployment's rollout at once) or `rollback` (abort the rollout if there is one, and leave the revert to the deployment's rollback step, which must need this step) |
| `on_inconclusive` | text | `fail` | `fail`, `pass` or `pause` (wait for a person to mark it passed or failed, like an approval) |
| `params` | mapping | none | Typed values the queries use as `{{name}}`, at most 30: `{ run: sha }`, `{ input: name }`, `{ output: step.key }`, or `{ deployment: fact }` with a fact of the deployment (`canary_hash`, `stable_hash`, `namespace`, `rollout`, `application`, `version`, `previous_version`, `environment`, `service`). Names start with a lower-case letter and use letters, digits and `_`; `start` and `end` are filled in by the deploy runner |
| `metrics` | list | none | At most 20 metrics to read and judge; see below. At least one of `metrics` and `logs` is required |
| `logs` | list | none | At most 10 log queries to read, cluster and compare; see below |
| `model` | text | none | A model of the server's `ai.models`, to explain the verdict. See [AI models](../guide/ai.md) |
| `decide` | text | `statistics` | `statistics`, or `model-assisted`: the model may turn a pass into inconclusive or a fail, never a fail into a pass. Needs `model` |

The step's outputs are `verdict` and `summary`. A placeholder in a query is checked and escaped for
the language it goes into, or sent as a typed parameter (SQL); one outside a quoted string is
refused when the pipeline is checked. Every placeholder a query uses must be under `params`.

Each metric in `metrics`:

<!-- keys: verify-metric -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | name | required | Lower-case letters, digits, `-` and `_`, starting with a letter; unique among metrics and logs |
| `source` | text | required | A health source of the server's `verification.sources` |
| `query` | text | | The query in the source's language (PromQL, or an Elasticsearch query as JSON), at most 8 KiB. Exactly one of `query` or `statement` |
| `stable_query` | text | none | The same for the stable version, read in the same interval to compare with |
| `statement` | text | | For ClickHouse, PostgreSQL and HTTP sources: the name of a statement the source's operator wrote |
| `index` | text | | For Elasticsearch and OpenSearch: the index to search |
| `direction` | text | `higher-is-worse` | `higher-is-worse`, `lower-is-worse` (throughput, say) or `either` |
| `max` | number | none | A fixed upper bound |
| `min` | number | none | A fixed lower bound. Not above `max` |
| `fail_fast` | mapping | none | `{ max, min }`: a reading beyond these fails the whole verification at once |
| `failures` | whole number | `2` (`1` when the window has one interval) | Failing intervals in a row that fail the metric, 1 to 120 |
| `min_intervals` | whole number | half the intervals | Intervals with data a metric needs, or the verification is inconclusive, 1 to 120 |
| `min_points` | whole number | `1` | Readings an interval needs to count as having data, 1 to 500 |
| `step` | duration | `30s` | The resolution of the readings, `5s` to `1h` |
| `baseline` | boolean | `true` | `false` turns the comparison with earlier verifications off for this metric |

Each log query in `logs`:

<!-- keys: verify-log -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | name | required | As for a metric |
| `source` | text | required | A health source, such as Loki, ClickHouse or Elasticsearch |
| `query` | text | | The query in the source's language (LogQL, or an Elasticsearch query as JSON). Exactly one of `query` or `statement` |
| `stable_query` | text | none | The same for the stable version |
| `statement` | text | | The name of a statement the source's operator wrote |
| `index` | text | | For Elasticsearch and OpenSearch: the index to search |
| `errors` | text | `auto` | `auto`: clusters whose lines have words such as error, exception or panic are errors; `all`: the query selects only errors |
| `max_lines` | whole number | `5000` | Lines read per interval, 1 to 50000, within the source's own limit |
| `baseline` | boolean | `true` | `false` turns the comparison with earlier verifications off |

<!-- validate -->
```yaml
pipeline: shop
on:
  push: { branches: [main] }
steps:
  build:
    image: alpine:3.22
    run: echo tag=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    service: shop
    version: { output: build.tag }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop/rollout.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop, until: paused }
  verify-canary:
    kind: verify
    needs: deploy-prod
    deployment: deploy-prod
    delay: 1m
    duration: 10m
    interval: 1m
    on_fail: abort
    on_inconclusive: pause
    params:
      ns: { deployment: namespace }
      canary: { deployment: canary_hash }
    metrics:
      - name: error-rate
        source: prometheus
        query: 'sum(rate(http_requests_total{namespace="{{ns}}",pod_template_hash="{{canary}}",code=~"5.."}[1m]))'
        max: 0.01
        fail_fast: { max: 0.2 }
    logs:
      - { name: app, source: loki, query: '{namespace="{{ns}}", pod_template_hash="{{canary}}"}' }
  promote:
    kind: rollout
    needs: verify-canary
    deployment: deploy-prod
    action: promote-full
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true
```

### Kind ai

An ai step asks a model one question: instructions, a prompt and inputs taken from earlier steps'
outputs and artifacts. It runs no script. The runner makes the call with a key from its own secret
stores (`ai: true` in its configuration); the key never enters a step's container. The model's
answer is untrusted text: it can become the step's outputs and an artifact, never run anything. The
model must be one of the server's [`ai.models`](server-config.md#ai-models) that the project may
use. `timeout` defaults to `10m`, from `10s` to `1h`. It cannot be in a template or in a loop, and
takes no `run`, `image`, `env`, `secrets`, `artifacts`, `reports` or `fetch`. It needs a server that
knows ai steps (schema 19). See [AI models](../guide/ai.md).

<!-- keys: kind-ai -->
| Key | Type | Default | Description |
|---|---|---|---|
| `model` | text | required | The model's name in the server's `ai.models` |
| `instructions` | text | none | The system prompt, at most 64 KiB |
| `prompt` | text | required | What to ask, at most 64 KiB |
| `inputs` | list | none | At most 20 inputs the model is given; see below |
| `schema` | mapping | none | A JSON schema, written as a mapping, the answer must follow; the top level is `type: object`. At most 16 KiB. Each top-level property of a string, number, integer or boolean type becomes an output |
| `max_tokens` | whole number | `2048` | The most the model may write, 1 to 128000 (the operator's own limit for the model may be lower) |
| `temperature` | number | the model's own | 0 to 2. Only for OpenAI-compatible models; Anthropic models refuse sampling settings |

An ai step's outputs are `text`, `model`, `input_tokens` and `output_tokens`, and with a `schema` its
properties. The whole answer is kept as the artifact `response` (`response.txt`, and
`response.json` with a schema). An answer that does not follow the schema fails the step.

Each input in `inputs`:

<!-- keys: ai-input -->
| Key | Type | Default | Description |
|---|---|---|---|
| `label` | text | the source's name | What the input is called in the prompt: letters, digits, spaces, `_`, `.` and `-`, at most 60 characters; unique in the step |
| `output` | `step.key` | | An output of a step this one needs. One of `output` or `artifact` |
| `artifact` | `step/name` | | An artifact a step this one needs kept |
| `file` | path | every file | With `artifact`: one file of it, without `..` or a leading `/` |
| `max_bytes` | whole number | `65536` | How much of it is sent, 1 to 1048576; more is cut, saying so |

<!-- validate -->
```yaml
pipeline: release-notes
steps:
  changes:
    image: alpine/git:2.47.2
    run: |
      mkdir -p out
      git log --format='%h %s' -n 50 > out/commits.txt
    artifacts:
      - { name: log, paths: [out/commits.txt] }
  notes:
    kind: ai
    needs: changes
    model: local
    instructions: You write short release notes for operators. Plain words, no marketing.
    prompt: Write the release notes for these commits, and say how risky the release looks.
    inputs:
      - { artifact: changes/log, file: out/commits.txt, label: commits, max_bytes: 32768 }
    schema:
      type: object
      properties:
        notes: { type: string }
        risk: { type: string, enum: [low, medium, high] }
      required: [notes, risk]
    max_tokens: 800
  publish:
    needs: notes
    image: alpine:3.22
    run: printf '%s\n' "$LAUNCHWAY_NEEDS_NOTES_NOTES" > RELEASE_NOTES.md
```

## Runner

`runner` says which runners may run the pipeline's steps. It is written once, for the whole
pipeline, because a run's steps share the workspace of the runner that took its first step. It
needs a server that knows runner pools; see
[Runners on other machines and clusters](../guide/runner-identity.md). Name a pool, labels, or both.

<!-- keys: runner -->
| Key | Type | Default | Description |
|---|---|---|---|
| `pool` | name | any pool that serves the project | One of the server's [runner pools](server-config.md#runner-pools); it must serve the project. Lower-case letters, digits, `.`, `_` and `-` |
| `labels` | list | none | Labels a runner must all have, from its join token and its own [`labels`](runner-config.md). At most 16, each written as a pool name is |

A run that no runner may take says why on its page: no pool serves the project, the pool it asks for
does not, or none of the runners seen lately has the labels or accepts it.

<!-- validate -->
```yaml
pipeline: model-training
runner: { pool: gpu, labels: [cuda] }
steps:
  train:
    image: python:3.13
    run: python train.py
```

## Resources

`resources` says how much CPU and memory a step needs. Without it a step gets the installation's
default for its kind, which an administrator keeps in the database (see
[`launchway step-resources`](cli.md#step-resources) and `GET /api/v1/step-resources` in the
[HTTP API](http-api.md)); the defaults that a new installation starts from are in the table below.
Only a step that runs a script (kind `step`) takes it: an image step is built by the builder, whose
memory is set where the builder is deployed, deploy, rollout, rollback, verify and ai steps run in
the runner itself, and an approval runs nothing. A step that uses a template says nothing of its
own; the template's steps may.

Each of CPU and memory has an amount the step is **promised** (`cpu`, `memory`: the scheduler keeps
that much free for it) and a **limit** (`cpu_limit`, `memory_limit`: the most it may use). A limit
left out is the same as the amount promised, so `memory: 6Gi` is a step that is given 6 GiB and is
stopped, not slowed down, if it uses more. Where a step writes anything about CPU (or memory), the
default's CPU (or memory) is not used at all; what it leaves out of its own is filled in as the table
says.

<!-- keys: step-resources -->
| Key | Type | Default | Description |
|---|---|---|---|
| `cpu` | quantity | the default's | CPU promised, such as `500m`, `2` or `0.5`; at least `1m` |
| `cpu_limit` | quantity | `cpu` | The most CPU the step may use. Not below `cpu` |
| `memory` | quantity | the default's | Memory promised, such as `512Mi`, `6Gi` or `2G` (`Ki` `Mi` `Gi` `Ti` are powers of 1024, `k` `M` `G` `T` of 1000) |
| `memory_limit` | quantity | `memory` | The most memory the step may use; above it the step is killed. Not below `memory` |

<!-- validate -->
```yaml
pipeline: compile
steps:
  build:
    image: golang:1.27
    run: go build ./...
    resources: { cpu: 4, memory: 6Gi }
  test:
    image: golang:1.27
    needs: build
    run: go test ./...
    resources: { cpu: 1, cpu_limit: 8, memory: 2Gi, memory_limit: 6Gi }
```

**Defaults.** A new installation starts with these for kind `step`, and an administrator changes
them without a restart; a change applies to the next pod a run starts (a run's pod is made once, for
its first step, and kept for its other steps). The values live in the database, so every server of
an installation agrees.

**Upgrading does not change your pipelines.** An installation that is upgraded from a release before
step resources starts with nothing sized: every kind's default is empty, so a step that writes no
`resources` gets what the namespace gives, as it did before (a step that does write `resources` is
sized as written). To adopt the recommended values below, an administrator runs
`launchway step-resources set-default step --recommended` (or `PUT /api/v1/step-resources/defaults/step`
with `{"recommended": true}`, or the MCP tool `set_step_resource_default`), or sets values of their
own with `--cpu`, `--cpu-limit`, `--memory` and `--memory-limit`. `launchway step-resources get`
shows what an installation has; `updated_by` says `seed` for a new installation and `upgrade` for an
upgraded one until someone changes it. The recommended values are:

| Setting | Recommended | Meaning |
|---|---|---|
| `cpu`, `cpu_limit` | `1`, `4` | A step is promised one CPU and may use four |
| `memory`, `memory_limit` | `2Gi`, `6Gi` | A step is promised 2 GiB and may use six |
| `max_cpu`, `max_memory` | not set | The most any step may ask for, on the installation |
| `memory_per_thread` | `1Gi` | The memory one parallel job is assumed to need (see below) |

**Steps that share an image share a container.** A run's steps that use the same image run in one
container, so on Kubernetes the container gets the largest of what its steps ask for, and every
step in it may use that much. A container written under `containers` with its own `cpu` or `memory`
counts as asking for them.

**Parallelism follows the memory.** A container that may use 2 GiB but sees 32 CPUs makes `go build`
start about 32 compilations and run out of memory. So alongside the limits, Launchway tells the
tools how many jobs to start: the smaller of the CPUs it may use (rounded up) and its memory limit
divided by `memory_per_thread`, never fewer than one. It sets `GOMAXPROCS` (which `go build` also
uses for how many packages it compiles at once), `MAKEFLAGS=-jN`, `CARGO_BUILD_JOBS`,
`CMAKE_BUILD_PARALLEL_LEVEL`, `UV_THREADPOOL_SIZE` and, for your own scripts,
`LAUNCHWAY_PARALLELISM`. A variable the step sets under `env` is left as it wrote it. With the
starting defaults that is 4 jobs; `memory: 6Gi` with no CPU written is 4 as well, and
`cpu: 16, memory: 6Gi` is 6.

**Where it is applied.** Kubernetes sets the pod's requests and limits. Podman and Docker run each
step with `--memory`, `--memory-reservation` and `--cpus`. The host executor cannot limit anything:
a step that writes `resources` there is refused with a message saying so. A step that does not
(another step that uses the same image may) runs, the defaults are not applied, and its log has one
line, "resources not enforced on the host executor", saying what it would have been given. The runner log says what each container was given.

**Checks.** `launchway validate` checks the quantities. A run is refused when it is created if a
step asks for more than the installation's `max_cpu` or `max_memory`, the message naming the step,
the value and the limit. On Kubernetes the executor checks again before it creates the pod, against
the namespace's LimitRange (the container's and the pod's maximum) and ResourceQuota, and against
the runner's own `kubernetes.max_cpu` and `kubernetes.max_memory`; the step then fails at once with
a message naming which of them it exceeds. That applies to what the step, or the container under
`containers`, asked for. A default that the installation chose never fails a step: if it is above
what the runner or the namespace allows one container, the runner lowers it to the most allowed
(the runner's `kubernetes.max_cpu` and `kubernetes.max_memory`, and each LimitRange's container
maximum) and writes one line to the step's log saying what it lowered and why. A step that asks for
memory only is held to its memory, and its CPU default is lowered if need be, and the other way
round.

## Lane

A lane makes runs take turns: at most one run of a lane goes at a time. A run that arrives while the
lane is held waits its turn, replaces the run that holds it, or is skipped. A lane belongs to one
project; two pipelines of a project that give their lanes the same `name` (and `per`) share one.

<!-- keys: lane -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | name | the pipeline's name | The lane's name: lower-case letters, digits, hyphens and underscores, as a step's |
| `per` | list | none | What splits the name into separate lanes, so that runs of different branches do not wait for each other: `ref` (each branch, tag or pull-request ref), `pull_request` (each pull request) or `input:<name>` (each value of a `string` or `choice` [input](#inputs)) |
| `when_busy` | `wait`, `cancel` or `skip` | `wait` | What a run that arrives while the lane is held does. `wait`: it is created and queued, with no step queued, and starts when the run before it ends. `cancel`: the run that holds the lane and every run waiting for it are canceled, saying `replaced in lane … by run #N`, and the new run goes; a holder with an open deployment finishes it first, and the new run waits. `skip`: the new run is created and canceled at once, saying `skipped: lane busy (run #N)`; it appears in lists and status posts and runs no step |
| `queue` | `newest` or `all` | `newest` | For `wait`. `newest`: a newer run replaces an older one that is still waiting, which ends canceled saying `replaced in lane … by run #N`, so at most one run waits. `all`: every run waits, and they start in order |

Runs started by a push, a pull request, a schedule, a webhook or by hand are all subject to the lane.
A draft run never joins one, and a re-run does not either. A run that waits for its lane does not
count that time against its [`timeout`](#top-level-keys). Cancel a waiting run with the cancel button
(or `launchway runs cancel`): it leaves the line. Resuming a finished run of a lane is refused while
another run holds it, and takes the lane otherwise. A lane is released whenever its holder ends, however
it ends: a run that fails, is canceled or is lost to a server that died frees the lane, and a server that
finds a lane with runs waiting and no live holder starts the first of them.

<!-- validate -->
```yaml
pipeline: release
lane:
  name: release
  per: [ref]        # one lane for each branch
  when_busy: wait
  queue: all        # every run goes, in turn
steps:
  ship:
    image: alpine:3.22
    run: ./ship.sh
```

`supersede: true` stays as the short form of `lane: { per: [ref], when_busy: cancel }` for runs
started by a push or a pull request; writing both is an error.

## Retry

`retry: 2` retries a failed step twice at once, whatever the cause. The mapping form adds a pause and
the causes worth retrying:

<!-- keys: retry -->
| Key | Type | Default | Description |
|---|---|---|---|
| `times` | whole number | required | Extra attempts after a failure, 0 to 10 |
| `wait` | duration | none | The pause before the first retry, `1s` to `1h`. The step is queued but not handed to a runner until it is over; the run page says `retrying in 20s (attempt 2 of 4)` |
| `backoff` | number | `1` | What the pause is multiplied by for each retry after the first, 1 to 10; no pause is longer than `1h`. Goes with `wait` |
| `on` | list of causes | every cause | The causes that are retried: `exit` (the script ended non-zero, and any failure that is none of the others), `timeout` (the step ran past its `timeout`) and `infrastructure` (the machinery failed: the image could not be pulled, the workspace or the fetched artifacts could not be prepared, or, only when `infrastructure` is listed here, a runner refused the step). A cause not listed fails the step at once |

A step whose runner is lost, or whose secret store is unavailable, is run again whatever `retry` says,
a bounded number of times, as before. A step that fails its coverage threshold, is refused by a
policy or is rejected is never retried. In a template, `times`, `wait`, `backoff` and `on` may be
`{ param: name }`, and a `retry` written on the step that uses the template applies to the
template's steps that write none of their own. The failed attempt's cause is shown on the step, in
the API as `failure_cause`.

<!-- validate -->
```yaml
pipeline: flaky-integration
timeout: 2h
steps:
  integration:
    image: alpine:3.22
    run: ./integration.sh
    retry: { times: 3, wait: 20s, backoff: 2, on: [timeout, infrastructure] }
```

## When

`when` holds conditions; every condition written must hold for the step to run, and within one,
any listed value may match. It is a closed set of tests, not an expression language.

<!-- keys: when -->
| Key | Type | Default | Description |
|---|---|---|---|
| `branch` | pattern, one value or a list | none | The run's branch matches one. A tag run, a pull-request run and a run with no branch never match |
| `tag` | pattern, one value or a list | none | The run's tag matches one |
| `event` | one value or a list | none | The run's event is one of `push`, `manual`, `draft`, `pull_request`, `schedule`, `webhook` |
| `input` | input name | none | The step runs only if this [input](#inputs)'s value is one of `equals`. A `boolean` input alone means `equals: true`. A `list` input cannot be tested. Not in a template |
| `output` | `step.key` | none | The step runs only if this output of a step it needs (directly or through others) is one of `equals`. The step must be in `needs`, may not repeat (`for_each`) and may not be a step that uses a template. An output that was never written never matches, and the skip says `scan.changed was not set`. Needs schema 24 |
| `equals` | one value or a list | `true` for a boolean `input` | The values `input` or `output` is compared with, as text: a boolean is written `true` or `false`. A condition tests an input or an output, not both |
| `upstream` | text | `succeeded` | How the steps it needs must have ended: `succeeded` (all of them), `failed` (at least one failed, was cancelled, or was not run because of a failure) or `always` (whatever happened). The step then waits until every step it needs has ended |

In a [draft run](../glossary.md#draft-run) a step with a `when` runs only if its `event` names
`draft`, so `when: { branch: main }` alone does not run in a draft of `main`. A `when` that only says
`upstream` does not keep a step out of drafts. A deploy step, and an approval that names a
deployment, take no `upstream`. In a template, the lists may be `{ param: name }`.

A condition on an output reads what a step it needs wrote to `$LAUNCHWAY_OUTPUTS`; the step is
skipped (a skip by condition, which a re-run reuses) when the output differs, and the skip says what
the value was:

<!-- validate -->
```yaml
pipeline: output-example
steps:
  scan:
    image: alpine:3.22
    run: ./scan.sh   # writes changed=true or changed=false to "$LAUNCHWAY_OUTPUTS"
  fix:
    image: alpine:3.22
    needs: scan
    when: { output: scan.changed, equals: true }
    run: ./fix.sh
  lint:
    image: alpine:3.22
    on_failure: continue   # a failing lint is reported but does not fail the run
    run: ./lint.sh
  build:
    image: alpine:3.22
    needs: [fix, lint]
    when: { upstream: always }
    run: make
```

<!-- validate -->
```yaml
pipeline: cleanup-example
inputs:
  dry_run: { type: boolean, default: true }
steps:
  build:
    image: alpine:3.22
    run: make
  deploy:
    image: alpine:3.22
    needs: build
    when: { event: [push, manual], branch: main, input: dry_run, equals: false }
    run: ./deploy.sh
  notify-failure:
    image: alpine:3.22
    needs: [build, deploy]
    when: { upstream: failed }
    run: ./notify.sh
  cleanup:
    image: alpine:3.22
    needs: [build, deploy]
    when: { upstream: always }
    run: ./tear-down.sh
```

## For each

`for_each` repeats a step, or a step that uses a template, once for each combination of values.
It maps up to 5 keys to lists; key names match `^[a-z][a-z0-9_-]{0,39}$`. A list is written in
place, comes from a run input of type `list` (`{ input: name }`), or in a template from a parameter
of type `list` (`{ param: name }`). Lists produced by an earlier step while the run goes are not
available yet.

When a run is created the step becomes one step per combination, named
`<step>[<key>=<value>,...]` such as `test[python=3.12,os=debian]`, in the order the lists are
written. Each member receives its values as `LAUNCHWAY_EACH_<KEY>`, and
[`{ each: key }`](#value-forms) puts a value in its `image`, an `env` value or a `with` value. A
step that needs the loop needs every member.

- Values are 1 to 40 characters: letters, digits, `.`, `_`, `+` and `-`, starting with a letter or
  digit. Put other text around a value with `format`.
- `exclude` is a list of mappings from keys to values; a combination matching every key of an
  entry is left out. With every list written in place, an entry that matches nothing is an error.
- A loop has at most 64 combinations unless the server allows more
  ([`loops.max_members`](server-config.md#loops), at most 500), and a run at most 500 steps.
  An expanded step name may be at most 200 bytes.
- Approval, image, deploy, rollback and rollout steps cannot be repeated. Fetching from a loop's
  members is not available yet.

The loop list forms are mappings with one key:

<!-- keys: loop-list-input -->
| Key | Type | Default | Description |
|---|---|---|---|
| `input` | input name | required | A run input of type `list`. Not in a template |

<!-- keys: loop-list-param -->
| Key | Type | Default | Description |
|---|---|---|---|
| `param` | parameter name | required | Templates only: a parameter of type `list` |

<!-- validate -->
```yaml
pipeline: python-matrix
inputs:
  systems: { type: list, default: [debian, alpine] }
steps:
  lint:
    image: python:3.13
    run: ruff check .
  test:
    needs: lint
    for_each:
      python: ["3.11", "3.12", "3.13"]
      os: { input: systems }
    exclude: [ { python: "3.11", os: alpine } ]
    max_parallel: 3
    fail_fast: false
    image: { each: python, format: "python:%s" }
    env: { TARGET_OS: { each: os } }
    run: pytest
  summary:
    image: alpine:3.22
    needs: test
    run: echo "every combination passed"
```

## Value forms

Some values may be written in place or as a mapping with one of these closed forms.

### Output run and input values

Used by a deploy step's `version`, a change's `tag`, `digest` and `value`, an image step's
`build_args` and its tags' `tag`. Write a plain value, or exactly one of:

<!-- keys: value -->
| Key | Type | Default | Description |
|---|---|---|---|
| `output` | `step.key` | | An output of a step this step needs (directly, for a deploy step), such as `{ output: build.tag }` |
| `run` | text | | A fact about the run: `sha`, `short_sha`, `branch`, `tag` or `number` |
| `input` | input name | | A run input of type `string`, `choice` or `number`. Not in a template |

A deploy step's `environment` may be a mapping with one key:

<!-- keys: value-environment -->
| Key | Type | Default | Description |
|---|---|---|---|
| `input` | input name | required | A `choice` input whose options are all environment names |

### Input values in with

A value under `with` may come from a run input. Not in a template.

<!-- keys: value-with-input -->
| Key | Type | Default | Description |
|---|---|---|---|
| `input` | input name | required | The run input, such as `{ input: environment }` |
| `format` | text | none | Text with exactly one `%s` where the value goes, and no other `%`. For `string` and `choice` inputs |

### Loop values

In a step with `for_each`, the `image`, an `env` value or a `with` value may be the member's value:

<!-- keys: value-each -->
| Key | Type | Default | Description |
|---|---|---|---|
| `each` | loop key | required | A key under the step's `for_each` |
| `format` | text | none | Text with exactly one `%s`, and no other `%`, such as `"python:%s"` |

### Parameter values

In a template, a value written in place (a container's or step's `image`, `env` values, `timeout`,
`retry`, `when` lists, report and artifact globs, image step values, and the `with` of a template
it uses) may be a parameter's value:

<!-- keys: value-param -->
| Key | Type | Default | Description |
|---|---|---|---|
| `param` | parameter name | required | A parameter the template declares |
| `format` | text | none | Text with exactly one `%s`, and no other `%`. For `string` parameters |

## Secret references

A secret reference says where a value is kept; it never holds the value. The runner looks it up
just before the step runs, from its own [secret stores](runner-config.md#secrets), and masks it in
the step's output. Used by a step's `secrets`, an image step's build secrets and signing keys.

<!-- keys: secret-ref -->
| Key | Type | Default | Description |
|---|---|---|---|
| `store` | name | required | A store configured on the runner, such as `vault`. `^[a-z][a-z0-9_-]{0,62}$` |
| `key` | text | required | The secret's name or path in that store |
| `field` | text | none | A field within the secret, for stores that have them (required for `vault`) |

Steps of [draft runs](../glossary.md#draft-run) and pull-request runs that ask for secrets fail
before any runner sees them, unless the server's configuration allows it.

## Reports

`reports` names files a step writes that Launchway reads once the step ends, whether it passed or
failed. Each key takes one glob or a list of at most 20, relative to the workspace. A path part `**`
matches any number of directories; absolute paths, `..` and empty globs are refused; a glob is at
most 256 bytes. Reading reports never changes the step's outcome. See
[Tests and reports](../tutorials/tests-and-reports.md) and [Security scans](../tutorials/security-scans.md).

<!-- keys: reports -->
| Key | Type | Default | Description |
|---|---|---|---|
| `tests` | glob, one value or a list; or a mapping | none | Test results in any of the [13 formats](../guide/test-reports.md#formats), such as JUnit XML, `go test -json`, Playwright or Cucumber JSON. A mapping adds attachments: see [`tests` as a mapping](#tests-as-a-mapping) |
| `findings` | glob, one value or a list | none | Security scanner results in SARIF 2.1.0 |
| `sbom` | glob, one value or a list | none | Software bills of materials in CycloneDX JSON, kept as the step's artifact `sbom` |
| `coverage` | glob, one value or a list; or a mapping | none | Coverage reports: Cobertura XML, JaCoCo XML, LCOV or a Go coverprofile. A mapping adds a threshold: see [Coverage](#coverage) |
| `html` | mapping, or a list of them | none | Directories of HTML reports the tools wrote, hosted sandboxed on the server: see [HTML reports](#html-reports). At most 10 per step |

### `tests` as a mapping

<!-- keys: reports-tests -->
| Key | Type | Default | Description |
|---|---|---|---|
| `paths` | glob, one value or a list | required | The test result files, as for the plain form |
| `attachments` | directory | none | A directory below the workspace holding the screenshots, videos and traces that the reports name. The workspace itself is refused. Kept whole or not at all, within the server's [`reports.max_attachment_files`](server-config.md#reports) and `max_attachment_mib` |

### Coverage

`coverage` may be one glob or a list, or a mapping that also sets a threshold.

<!-- keys: reports-coverage -->
| Key | Type | Default | Description |
|---|---|---|---|
| `paths` | glob, one value or a list | required | The coverage files |
| `fail_under` | percentage, or a mapping | none | The step fails when coverage is below it: a percentage of lines (`80`, `72.5`, `"80%"`, above 0 and at most 100), or the mapping below |

<!-- keys: reports-fail-under -->
| Key | Type | Default | Description |
|---|---|---|---|
| `lines` | percentage | none | The least percentage of lines covered |
| `branches` | percentage | none | The least percentage of branches covered |
| `functions` | percentage | none | The least percentage of functions covered |

### HTML reports

<!-- keys: reports-html -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | text | the last part of `path` | Lower-case letters, digits, `.`, `-` and `_`, starting with a letter or digit, unique in the step |
| `path` | directory | required | The directory the tool wrote its report to, such as `playwright-report`. A directory below the workspace; the workspace itself cannot be published |
| `index` | file | `index.html` | The page the report opens at, relative to `path` |

Hosted reports are served sandboxed, behind a signed link that only people who may see the run are
given; see [Test reports](../guide/test-reports.md#html-reports).

## Artifacts

`artifacts` is a list of named sets of files a step keeps once it has run, for people to download
and later steps to [fetch](#fetch). At most 10 per step.

<!-- keys: artifacts -->
| Key | Type | Default | Description |
|---|---|---|---|
| `name` | text | required | `^[a-z0-9][a-z0-9._-]{0,63}$`, unique in the step. `sbom` is taken when the step has `reports.sbom` |
| `paths` | glob, one value or a list | required | Files to keep, relative to the workspace, as for reports; a directory keeps everything below it. At most 20 |
| `retain` | text | the server's `artifacts.retain` | How long to keep the files: whole days (`30d`), weeks (`2w`) or a duration (`12h`). From `1h` to `3650d`; never longer than the server's `artifacts.max_retain` |
| `when` | text | `success` | After which outcomes the files are kept: `success`, `failure` (a failed attempt, including a timeout) or `always` |

Names, paths and `into` are plain text: `{ input: ... }` and `{ each: ... }` are refused there. Secret
values are not masked in artifacts.

## Fetch

`fetch` is a list of artifacts of earlier steps to put in the workspace before the step runs. At
most 20 per step.

<!-- keys: fetch -->
| Key | Type | Default | Description |
|---|---|---|---|
| `from` | step name | required | The step that keeps the artifact. The fetching step must need it, directly or not. Not a step that uses a template, nor a loop |
| `name` | text | required | The artifact's name |
| `into` | directory | the workspace | A directory relative to the workspace to put the files in, keeping their paths below it |

A step that runs only after a failure (`when.upstream: failed`) cannot fetch an artifact kept only
on success.

<!-- validate -->
```yaml
pipeline: artifacts-example
steps:
  build:
    image: golang:1.27
    run: make dist
    reports:
      tests: [reports/*.xml]
      findings: reports/security/*.sarif
      sbom: reports/sbom.cdx.json
    artifacts:
      - name: binary
        paths: [bin/app, "dist/*.tar.gz"]
        retain: 30d
        when: success
      - { name: logs, paths: [logs], when: failure }
  check:
    image: alpine:3.22
    needs: [build]
    fetch:
      - { from: build, name: binary, into: out }
    run: ./out/bin/app --version
```

## Template files

A shared library is a Git repository with templates in `templates/<name>.yaml` and any other files
(scripts) they use. Template names match `^[a-z0-9][a-z0-9-]{0,62}$`. A template file has the same
step syntax as a pipeline, plus typed parameters and the outputs it exposes. See
[Reuse steps with library templates](../tutorials/library-templates.md).

<!-- keys: template-top -->
| Key | Type | Default | Description |
|---|---|---|---|
| `template` | name | required | The template's name; must match its file name |
| `description` | text | none | One line of at most 200 characters |
| `params` | mapping | none | The parameters it takes; at most 50 |
| `containers` | mapping | none | As in a pipeline's [`containers`](#containers). They are added to the run as `<step>-<name>` |
| `steps` | mapping | required | As in a pipeline's [`steps`](#steps), with the differences below |
| `outputs` | mapping | none | Step outputs it exposes to steps that need the using step |

### Parameters

Each entry of `params` maps a name (`^[a-z][a-z0-9_-]{0,39}$`) to its settings.

<!-- keys: template-param -->
| Key | Type | Default | Description |
|---|---|---|---|
| `type` | text | `string` | `string`, `number`, `boolean` or `list` |
| `default` | value | none: the parameter is required | Used when `with` gives no value. A list's default is a YAML list |
| `description` | text | none | One line of at most 200 characters |
| `options` | one value or a list | none | The values allowed. For `string` and `number` only |

A parameter reaches the template in exactly three ways, all resolved when the run is created:
every step of the template receives it as `PARAM_<NAME>` (names starting with `PARAM_` are reserved
in templates); a value written in place may be [`{ param: name }`](#parameter-values); and
[`if`](#template-steps) leaves a step out. Values are at most 4096 bytes.

### Outputs

Each entry of `outputs` maps an output name (`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`) to:

<!-- keys: template-output -->
| Key | Type | Default | Description |
|---|---|---|---|
| `from` | `step.key` | required | An output of one of the template's steps, not a looped one. A step that needs the using step receives it as `LAUNCHWAY_NEEDS_<USING_STEP>_<NAME>` |

### Template steps

Steps in a template take every key a pipeline's steps take, with these differences: they may not
be deploy, rollback or rollout steps; an approval may not name a `deployment`; `{ input: ... }` and
`when.input` are refused (pass the input to a parameter instead); `uses` names a template of the
same library by name alone; and they take one more key:

<!-- keys: template-step-extra -->
| Key | Type | Default | Description |
|---|---|---|---|
| `if` | mapping | none | Leave the step out unless the test holds; what needed it then needs what it needed. Steps of kind `step`, `approval` and `image`, and steps that use a template |

<!-- keys: template-if -->
| Key | Type | Default | Description |
|---|---|---|---|
| `param` | parameter name | required | A `boolean` parameter, alone; or any parameter with `equals` |
| `equals` | value | none | Keep the step when the parameter has this value |

Inside a template, steps refer to each other by the names the template gives them; files of the
library are at `$LAUNCHWAY_LIB`. Templates may use templates down to 4 levels.

<!-- validate-template -->
```yaml
template: python-service
description: Lint, test and scan a Python service
params:
  test_command: { type: string, default: "python3 -m pytest -q", description: How to run the tests }
  scan: { type: boolean, default: true }
  python: { type: string, default: "3.13", options: ["3.12", "3.13"] }
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

<!-- validate -->
```yaml
pipeline: weather-service
libraries:
  platform: { repo: ci-templates, version: v3 }
inputs:
  target: { type: choice, options: [dev, prod], default: dev }
steps:
  ci:
    uses: platform/python-service
    with: { test_command: "pytest -q tests/", python: "3.12" }
  report:
    image: alpine:3.22
    needs: ci
    run: echo "coverage was $LAUNCHWAY_NEEDS_CI_COVERAGE"
```

## Limits

| What | Limit |
|---|---|
| Pipeline or template file | 256 KiB |
| Inputs per pipeline | 50 |
| Libraries per pipeline | 10 |
| Schedules, webhooks per pipeline | 20 each |
| Step `retry` | 0 to 10 |
| Step `timeout` | `1s` to `24h` (default `1h`) |
| Approval `timeout` | `1s` to `720h` (default `24h`) |
| Image step `timeout` | `1m` to `24h` (default `1h`) |
| Deploy, rollout `timeout` | `1m` to `24h` (default `30m`) |
| Rollback `timeout` | `1m` to `24h` (default `15m`) |
| `description` | one line, 200 characters |
| Approval `message` | one line, 500 characters |
| Report globs | 20 per kind, 256 bytes each |
| Artifacts, fetches per step | 10, 20 |
| Loop keys, combinations | 5; 64 by default, at most 500 |
| Steps in a run once expanded | 500 |
| Template parameters, `with` values | 50, 4096 bytes each |
| Template nesting | 4 levels |
| Deploy `changes`, `sync.resources` | 50 each |
| Image tags, platforms, build arguments, build secrets | 20, 8, 50, 20 |

## Schema versions

Some keys need a server that knows them. A pipeline that uses them is refused, not misread, by an
older server. Use them only once every server and runner is upgraded; see
[Database migrations](../development/migrations.md).

| Schema | Keys |
|---|---|
| 9 | `on.pull_request`, `supersede` |
| 10 | `reports` |
| 11 | deploy and rollback steps |
| 12 | `artifacts`, `fetch`, `reports.findings`, `reports.sbom` |
| 13 | `inputs`, `when.input`, `when.upstream`, `for_each`, `on.schedule`, `on.webhook` |
| 14 | rollout steps, `rollout.until: paused`, `rollout.pauses`, an approval's `deployment`, `abort_first`, `on_cancel: abort` |
| 15 | image steps, a change's `digest` |
| 22 | a step's `resources` |
| 24 | `when.output`, `on_failure`, `retry` with `wait`, `backoff` or `on`, the pipeline's `timeout`, `lane`, `on.pull_request.into` |

## What next

- [Your first pipeline](../tutorials/first-pipeline.md) walks through writing and running one.
- [Server configuration](server-config.md) and [runner configuration](runner-config.md) list what
  operators set: environments, secret stores, registries and limits.
- [Environment variables](environment-variables.md) lists what each step receives.
