# Starters: the Hangar

Pipelines that ship with Launchway, so that most repositories need a pipeline file of a few lines, or none.

The **Hangar** (template library) is the library that comes inside Launchway; what is in it are the **starters**. It needs no registration and no version: it is the release's own, so
the server and every Station (runner) of the same release hold identical files. A pipeline uses a starter as `launchway/<name>`:

```yaml
pipeline: shop-api
steps:
  ci:
    uses: launchway/go-service
```

That is a whole pipeline: it checks the format, vets, tests with a report and a coverage profile, builds, scans the
dependencies and the files for secrets, and builds, scans, signs and (when you say where) pushes the image from the
`Dockerfile`. **Library → Starters** in the interface lists every starter with its inputs, an example file and what it runs, and
adds the file to a repository as a pull request. `launchway validate --resolved` prints the same list of steps for your file,
and `launchway templates list launchway` prints the inputs.

There are two kinds, both used with `uses:`:

- **Flight plans** are whole pipelines for a kind of repository. Most files use only one.
- **Maneuvers** (step templates) are single building blocks, for a pipeline you write yourself, such as `image-build` and `deliver`.


## A file of two lines

A file that is only one Flight plan needs no `steps:`, no name and no triggers:

```yaml
uses: launchway/python-service
with: { port: 8001 }
```

It stands for a pipeline named `ci` that runs on pushes to `main` and on pull requests (a change that touches only `docs/**` or Markdown is skipped), cancelling older Sorties (runs). What the file writes itself (`pipeline`, `on`, `supersede`, `containers`) is kept. `launchway validate FILE --resolved` shows every step it makes.

## What a service Flight plan runs, and what blocks

`go-service`, `python-service` and `node-app` run, by default: a format and style check, the unit tests with a parsed report and coverage, an audit of the dependencies for known vulnerabilities, static analysis where a free tool exists (`bandit` for Python, `staticcheck` for Go), a list of the dependencies' licenses, a secret scan, the build, and the image: built, scanned, with its bill of materials, signed, and pushed when the project has Airfields (environments) to deliver to. Provenance and signing use what the Station has configured. Coming: findings of the analysis in the Security tab, a coverage floor from the base branch.

Safeguards are on, and the team is in charge of each:

| Finding | By default | Input |
|---|---|---|
| A failed test | blocks | |
| A high or critical vulnerability in a dependency or the image | blocks | `audit_mode: report`, `scan_mode: report` |
| High severity static analysis (bandit) | blocks | `analysis_mode: report` |
| Style and formatting | advice (printed, never fails) | `lint_mode: enforce` |
| Coverage | advice | `coverage_minimum: 80` blocks below it |
| A license on your deny list | blocks | `license_deny: [GPL-3.0]` (empty: only listed) |

## Delivery

When the project has Airfields (in the server's configuration, in the order they are listed), the Flight plans for services that ship an image deliver to **each of them, in that order**, after the image is built, from `main`. The Airfield's own setting decides where a person is asked first: a **protected** Airfield gets a Clearance step. Then, for each Airfield: the **launch (deploy)** (a commit of the image's tag and digest to the GitOps repository that Argo CD applies), a **health proof** (the launch Station calls `http://<workload>.<namespace>.svc.cluster.local:<port><health_path>` for a **watch period**, 5 minutes by default), and an automatic **return to base** (the deployment is reverted) if the launch or the proof fails. The next Airfield starts only after the one before has been proven. People are told by the server's notification rules: `approval.requested` when a decision waits, `deployment.failed` and `deployment.rolled_back` when it goes wrong.

Everything the delivery needs is worked out, and each value can be changed:

| Value | Worked out as | Change it |
|---|---|---|
| The service's name | the project's name | `service` |
| The image | `<image_namespace>/<service>` | `image_repository`, or set the variable `image_namespace` once |
| The Argo CD application | `<service>-<environment>` | `overrides: [prod.application=shop]` |
| The file in the GitOps repository | `apps/<application>/base.yaml` | `overrides: [prod.manifest=...]` |
| The namespace, the workload | `<service>-<environment>`, `<service>` | `overrides: [prod.namespace=...]`, `prod.workload` |
| The health address | the workload's service on `port` (8080) and `health_path` (`/healthz`) | `port`, `health_path`, `overrides: [prod.health_url=...]` |
| How it is written | the image's references in the manifest | `method: helm-values` sets `image.tag` and `image.digest` in a values file |
| An Argo Rollouts Pathfinder (canary) | off | `overrides: [prod.rollout=true]` |
| No proof, no decision for one Airfield | on | `overrides: [dev.verify=false, prod.approve=false]` |
| Which Airfields | the project's | `deliver: [staging, prod]`, or `deliver: []` for none |
| The watch period | 5 minutes | `verify_for: 10m` |

The project's name, Airfields and protection reach the Flight plan as variables (`launchway_project`, `launchway_environments`, `launchway_protected`), so the Sortie records them like any variable it used. Values that are the same for many projects are variables too: `image_namespace`, `registry`, `gitops_repo`, `gitops_branch`, `health_source`, `health_path` and `delivery_overrides` can be set once at a level. Plain Kubernetes manifests and Helm values are what the launch step writes today; a chart repository and other deploy methods are coming.

## Switching stages off, adding steps and taking stages over

Three patterns, in order of how often you need them:

```yaml
uses: launchway/python-service
with: { port: 8001, coverage_minimum: 80, lint_mode: enforce }   # the common knobs are inputs
skip: [licenses, analysis]                                        # leave stages out, by name
extend:                                                           # add steps next to a stage
  - after: test
    steps:
      docs: { image: alpine:3.22, run: ./check-docs.sh }
replace:                                                          # take a stage over wholesale
  check: { image: python:3.13, run: ruff check . }
```

The stages are the step names in the Flight plan's list (`launchway templates list launchway` and the gallery show them). A misspelled stage is refused with the stages there are. Steps that need a skipped stage need what it needed; steps added `after` a stage are waited for by what waited for it. See [Changing what a template runs](../reference/pipeline.md#changing-what-a-template-runs).

## A repository with no pipeline file

A level can let a repository with no `.launchway/pipeline.yaml` run the Flight plan that fits what it holds:

```yaml
# launchway config set organisation default -f organisation.yaml
default_pipeline:
  detect: true
```

(or `pipeline: { detect: true }` on one project in the server's configuration). At each commit without a file the server looks at the
repository's top level (`go.mod`, `pyproject.toml`, `package.json`, `pom.xml`, `build.gradle`, `Chart.yaml`, a
`Dockerfile`, an `index.html`), chooses a Flight plan, and reads the versions it names (the Go version in `go.mod`, `.python-version`,
`.nvmrc`, the Java release in `pom.xml`). A repository where nothing matches has no pipeline, as before: nothing is guessed.
It is off until you turn it on, because it changes what a repository with no file does.

The Sortie page says it plainly: **No pipeline file: using `launchway/go-service` because `go.mod` was found (and a Dockerfile)**,
with a button, *Add this file to the repository*, that saves the text on a new branch for a pull request, so that the file
becomes the repository's own and can be changed. A file in the repository always wins.

From the command line, `launchway init` proposes the same file for the directory you are in (and `--write` saves it).

## Flight plans

Each Flight plan has the same shape: look at the repository's own files for the tool and its version, restore and save the
dependency cache under a key made of the lock file and the tool version, check, test, build, and the image when there is a
`Dockerfile`. Every input has a default, so `uses: launchway/<name>` alone runs.

| Flight plan | For | What it runs |
|---|---|---|
| `go-service` | A Go program with a `Dockerfile` | `gofmt` and `go vet`; `go test -json` with a Tests report and a coverage profile; `go build`; `govulncheck`; secret scan; the image |
| `go-library` | A Go module that is not launched | the same without the image, with the tests repeated for each Go version in `test_versions` |
| `python-service` | A Python program with a `Dockerfile` | dependencies by `uv.lock` (uv), `poetry.lock` (Poetry) or `requirements*.txt` and `pyproject.toml` (pip); `ruff`; `pytest` with a JUnit report and a Cobertura coverage report; secret scan; the image |
| `python-library` | A Python package | the same, tested on each version in `test_versions`, and a wheel built |
| `node-app` | A Node or TypeScript program with a `Dockerfile` | install by `pnpm-lock.yaml`, `yarn.lock` or `package-lock.json`; the `lint`, `format:check` and `typecheck` scripts the package defines (and `tsc --noEmit` for TypeScript); tests with a report for vitest, jest (with `jest-junit`) and `node --test`; the `build` script; secret scan; the image |
| `node-library` | An npm package | the same, tested on each version in `test_versions` |
| `java-maven` | A Maven project | `mvn verify` (or `./mvnw`) with the Surefire and Failsafe results in Tests and JaCoCo in Coverage; the jars kept; secret scan; the image when `image: true` |
| `java-gradle` | A Gradle project | `gradle build` (or `./gradlew`) with the test results and JaCoCo; the jars kept; secret scan; the image when `image: true` |
| `container-image` | A repository whose product is an image | secret scan; build, scan, bill of materials, sign and push |
| `static-site` | HTML, written by hand or built by a command | the build command if you give one; checks for `index.html` and for links to files that are not there; the pages kept as the Cargo (artifact) `site` |
| `helm-chart` | A Helm chart | `helm lint --strict`, `helm template`, `helm package` (kept as the Cargo `chart`); secret scan |
| `build-only` | Anything that only has to build | the build of the `language` you name, kept as the Cargo `build` |
| `library-only` | Anything that is only checked | the checks and tests of the `language` you name, with their reports; no build, no image |

### The image

Flight plans with a `Dockerfile` build the image on the branch named by `image_branch` (default `main`), scan it, write its
software bill of materials and sign it, **and push it only when `push: true`**. The Depot (registry) is `registry` (default `launchway`, the [built-in Depot](registry.md) of this server, which names an
image under the project; or a name your Stations' configuration lists) and the repository `image_repository`:

```yaml
steps:
  ci:
    uses: launchway/go-service
    with: { push: true, image_repository: team/shop, scan_mode: enforce }
```

Pull-request and draft runs never push. Launching, verifying and returning to base are not part of the Flight plans yet; add them
with the delivery templates of your own library, after `ci` (`needs: ci`). **Coming:** the same steps built in, for a project
that has an Airfield configured, asking for approval where the Airfield is protected.

### Versions

The tool's version is an input (`go_version`, `python_version`, `node_version`, `java_version`) that sets the image the steps run in.
Detection reads the repository's own file and writes it into the file `launchway init` proposes. Go fetches the newer toolchain
a `go.mod` asks for, and the Node step warns when `.nvmrc` names another major version. The cache key always includes the version
as well as the lock files, so a new version starts a new cache.

### Matrix

The library Flight plans take a list: `test_versions: ["1.26", "1.27"]` runs the tests once for each, as `test[go=1.26]` and
`test[go=1.27]`, each with its own log, cache and Debrief (test report). They do not stop each other when one fails (`fail_fast: false`: a
library wants every version's answer). See [Repeating a step over lists](pipelines.md#repeating-a-step-over-lists).

### A database beside the tests

A service container beside the steps is written in your own file, and the test settings pass its address in `test_env`:

```yaml
containers:
  db: { image: docker.io/library/postgres:17, service: true, env: { POSTGRES_PASSWORD: test }, ready: { tcp: 5432 } }
steps:
  ci:
    uses: launchway/python-service
    with:
      test_env: "DATABASE_URL=postgresql://postgres:test@localhost:5432/postgres"
```

## Step templates

| Step template | One step that |
|---|---|
| `go-check`, `go-test`, `go-build` | formats and vets, tests with reports, builds Go |
| `python-check`, `python-test` | installs and lints, installs and tests Python |
| `node-check`, `node-test`, `node-build` | installs and checks, tests, builds Node |
| `maven-verify`, `gradle-build` | builds and tests Java |
| `helm-check` | lints, renders and packages a chart |
| `site-check` | builds and checks a static site |
| `secret-scan` | looks for private keys and provider tokens in the files |
| `image-build` | tags from the commit, builds, scans, signs, pushes (two steps; outputs `tag`, `digest`, `image`) |
| `collect-docs` | keeps a folder of documentation as the Cargo `docs` |

Assembled in a pipeline, they are the building blocks of a custom shape:

```yaml
pipeline: shop
steps:
  test: { uses: launchway/go-test, with: { race: true } }
  scan: { uses: launchway/secret-scan }
  image: { needs: [test, scan], uses: launchway/image-build, with: { image_repository: team/shop, push: true } }
```

**Coming:** separate cache steps are not needed, because a step carries its cache; publishing documentation to a site and notifying
from a step wait for the notification work.

## Changing a starter

- **Any input** is overridden with `with:`; the gallery lists them all with their defaults.
- **A command** is replaced with `test_command`, `lint_command`, `install_command` or `build_command`.
- **A starter of your own** replaces a built-in one by living in a library you list: write `uses: platform/go-service`. The name
  `launchway` is reserved for the built-in library, so a server's library cannot take it.
- **The scripts** are files in the library (`scripts/`), each unit-tested (`tests/run.sh`), so reading what a step does is
  reading a short script, never a string inside a template.

## How it is kept

The files are embedded in Launchway and written into the workspace at `.launchway/lib/launchway/` for each Sortie, like any library.
A Sortie records the library at a content hash where a Git library has a commit. A Station of another release refuses the Sortie and
says to update it, rather than run scripts the server did not plan with. Each Flight plan is run for real against a tiny
repository of its language in the project's tests (a toolchain a machine lacks is skipped, with the reason).

## What is not built yet

- Launch, verify and Return to base (rollback) inside the Flight plans, with approval for protected Airfields.
- Language starters that also write assistant instruction files, formatter and linter configuration and a test layout into the repository.
- Values for a matrix read from an earlier step's output or from a file in the repository (the design is in
  [the reference](../reference/pipeline.md#for-each)).
- Findings from the secret scan in the Security tab; today the step fails with the file and line.
