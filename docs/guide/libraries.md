# Shared libraries

Templates and the scripts they run, kept in a Git repository and pinned by tag, branch or commit.

Besides the libraries you list, one library needs no listing: [`launchway`](starters.md) ships inside Launchway, with Flight plans
and step templates for common kinds of repository (`uses: launchway/go-service`). The name `launchway` is reserved for it.

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
pipeline: weather-service
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

## Steps in a library

Besides templates, a library may hold **custom steps**: a directory `steps/<name>` with a manifest and code in
any language, used as `uses: <library>/<name>` and pinned like a template, by tag or commit. The step's
inputs and outputs are typed, the pipeline's `with:` is checked against them before a Sortie starts, and what
the step may do (network hosts, secrets, writable paths) is shown to whoever adds it and called out when a newer
version changes it. See [Write your own step](write-your-own-step.md); `launchway steps list LIBRARY` lists
a library's steps.

## One delivery definition for every service

A template may hold the whole path from a tested commit to a running, verified version: an image
step, an approval, a deploy step, a verify step and a Return to base (rollback) step. `examples/library` has the
pieces of such a library, and the thin pipeline files that use them under `pipelines/`:

| Template | What it does |
|---|---|
| `service-ci` | Installs dependencies (cached), lints, runs tests, and scans a Python or Node service; declares the test, coverage and findings reports once for every repository that uses it. `working_directory` makes it usable once for each directory of a monorepo |
| `service-ci-postgres` | The same with a PostgreSQL service beside the tests, whose address the tests get as `DATABASE_URL` |
| `service-cd` | Builds, scans and pushes the image, optionally asks a person, deploys to one Airfield (environment) with GitOps, verifies, and rolls back on failure |
| `service-cd-rollout` | The same for a workload that is an Argo Rollouts rollout |

The Airfield and the application are typed parameters (`type: environment`, `type: application`),
so a mistyped name is refused where it is written. A service with two Airfields uses `service-cd`
twice, each use with a `when` on its branch, and a service of a different shape changes parameters,
not the template. The repository's file stays short:

```yaml
libraries:
  platform: { repo: platform, version: v1 }
steps:
  ci:   { uses: platform/service-ci, with: { coverage_minimum: 70 } }
  prod:
    uses: platform/service-cd
    needs: ci
    when: { branch: main }
    with: { environment: prod, application: shop, image_repository: team/shop,
            manifest: apps/shop/deployment.yaml, workload: shop, namespace: shop,
            health_query: 'min(probe_success{instance="http://shop.shop.svc:8080/health"})' }
```

**A template authorizes nothing.** When a Sortie (run) is created the template's steps become ordinary
steps and are checked as if the pipeline had written them: the Airfield must exist and list the
Flight (codebase), the deploy Station's repository settings must allow the files, and policies see the
deployment. A library cannot be used to reach an Airfield the Flight may not use. A library
author checks a change with `examples/library/check-library.sh`, which also runs the tests that
expand every example pipeline; a repository that has no pipeline file can get one of these
templates as a [flight plan](../reference/config-objects.md#a-flight-plan-for-every-repository).

**Which repositories may be libraries** is server configuration. A pipeline's `repo:` must name
one of them; it can never be an address, so a pipeline cannot make the server fetch from anywhere
with its credentials. Libraries are fetched exactly as Flights are, with the same
[credentials](editor.md) (in the URL, or `GIT_CONFIG_*` settings in the Airfield of
the server and the Stations (runners)):

```yaml
libraries:
  - name: ci-templates
    repo: https://git.example.com/platform/ci-templates.git
```

**Versions.** When a Sortie is created, the server fetches its mirror of each library the pipeline
names and resolves the version to a commit: a tag first, then a branch, then a full commit id (so
a tag and a branch with the same name give the tag). Moving tags and branches is allowed; the Sortie
records the library, the version as written and the commit it pointed at, and its *run.created*
event, the Sortie page (*Libraries* in the header) and every Station use that commit. Resuming a Sortie
never resolves anything again. A version that does not exist, or a library the server does not
list, is reported at its line and starts nothing.

**Expansion.** A step that `uses` a template becomes the template's steps, with ids
`<step>/<template-step>` (`ci/lint`, `ci/test`), drawn on the Sortie page as one group that can be
collapsed. Template steps that need nothing in the template need what the `uses` step needs; a
step that needs the `uses` step waits for all of its steps, and is skipped if any of them fails,
as usual. The `uses` step's `when`, `retry` and `timeout` apply to every step of the template that
does not set its own. The template's containers are added to the Sortie under new names
(`ci-py`). A template can use templates of its own library, written by name alone
(`uses: check`), from the same commit; using another library's templates from a template is not
supported yet. A step whose `if` is false is left out, and what needed it needs what it needed.

**Parameters**, with no expression language. Each has a `type` (`string`, the default, `number`,
`boolean` or `list`), an optional `default` (without one it is required), `options` and a `description`.
`with:` values are checked against them, with line numbers; unknown names are refused. A
parameter reaches a template in exactly three ways, all resolved when the Sortie is created, so the
graph a Sortie keeps holds plain values only:

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

**Files at run time.** For every library a Sortie uses, the Station puts the library's files at the
recorded commit in the workspace, at `.launchway/lib/<name>/`, so they reach steps the same way in
a container, a Kubernetes pod or on the host. A template's steps get `LAUNCHWAY_LIB`, the
directory of their own library (`/workspace/.launchway/lib/<name>` in a container), and every
step gets `LAUNCHWAY_LIB_<NAME>` for each library. The directory is listed in the checkout's
`.git/info/exclude`, so `git status` in a step does not report it; it replaces anything the
repository itself has at that path. A Station that takes a Sortie over restores the same commit.

**Trying a library change before releasing it.** Starting a Sortie by hand (*Start a Sortie →
Advanced*, or `POST /api/v1/projects/{name}/runs` with `{"ref": "main", "libraries":
{"platform": "my-branch"}}`) and draft runs (the same `libraries` field, or *Advanced* in the
editor) may use another version of any library the pipeline names. The Sortie records both versions
and is marked *Library override*. It needs only the right to start the Sortie.

**One pipeline for many repositories.** A Flight may get its pipeline from a library instead of
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
template is the pipeline, named after the Flight. The push decisions under *Recent pushes* and
the Sortie page say which was used, and the Sortie keeps the configuration as the pipeline file it
amounts to, shown on its *Pipeline* tab. The editor opens that text for such a repository, so
saving it turns it into a file.

**In the editor**, pipelines are checked against the Flight's libraries as you type: the graph
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
- A template's steps run as part of the using Flight's run: a step's secrets are looked up for
  that Flight, so a secret store's `projects` allow-list applies to templates as to any step, and
  expansion cannot change whose job a step is. Draft runs that may not read secrets refuse
  template steps that ask for them, too.
- Manual and draft starts record in the Logbook each library's version, any override, the
  commit it resolved to, and the templates used.
- Whoever can push to a library, or move its tags, changes what every pipeline that uses it runs.
  Protect its tags and branches on the Git host as you would a deployment script, and pin
  pipelines to tags or commits rather than branches where that matters.

## Defaults that follow other parameters, and parts that can be left out

A service's names usually follow its name, so a template can say so instead of every file repeating
them:

```yaml
template: service-cd
params:
  workload:    { type: string }
  application: { type: string, default: { param: workload, format: "%s-dev" } }
  manifest:    { type: string, default: { param: workload, format: "apps/%s-dev/base.yaml" } }
  verify:      { type: boolean, default: true }
  moving_tag:  { type: boolean, default: true }
steps:
  image:
    kind: image
    tags:
      - { registry: harbor, repository: team/app, tag: { output: tag.tag } }
      - { registry: harbor, repository: team/app, tag: ci-latest, if: { param: moving_tag } }
  verify:
    kind: verify
    if: { param: verify }              # a deploy, verify, rollback or rollout step may be left out too
    ...
```

A use that gives `workload: mcp-weather` gets `application: mcp-weather-dev` and `manifest:
apps/mcp-weather-dev/base.yaml` unless it writes its own. With `verify: false` the verify step is
left out, and the Return to base (rollback) step that needed it needs what it needed; `moving_tag: false` leaves the
second tag out. A default that derives from another parameter is checked where the template is used:
if the result is not a valid value ("the default of mode, taken from name ("mcp-weather"), does not
fit: mode is one of a, b…"), the message says which parameter to give a value for. Defaults derive
only for string parameters, from string parameters, and not in a circle.

## Listing templates from the command line

```sh
launchway templates list                         # the libraries the server lists
launchway templates list platform --version v3   # its templates, with each parameter's type
```

Each parameter shows its type, whether it is required, and `[names a secret]` when a step takes the
store, key or field of its secret reference from it (`secrets: { TOKEN: { store: vault, key:
{ param: app, format: "apps/%s/token" }, field: token } }`). The same data is in
`GET /api/v1/libraries/{library}/templates` and the MCP tool `list_templates`.
