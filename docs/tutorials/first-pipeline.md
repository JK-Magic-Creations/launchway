# First pipeline in 10 minutes

Get a pipeline for a repository of your own in a few lines, check it, run it on your laptop, then let a
Launchway server run it on every commit. For developers new to Launchway.

> **Verified** on 2026-11-21 against the `feat/matrix-starters` branch: `launchway init`, `launchway validate --resolved`
> and `launchway run --executor host` for the Go example in step 1, on Linux. The steps that write a pipeline by hand
> (step 2 onward) were verified on 2026-11-19 against commit f717337 with Podman 5. **Not verified:** macOS, Windows (WSL),
> Docker instead of Podman, and the Go example in containers.

## What you need

- Launchway built from source: follow [Install on a laptop](../howto/install-laptop.md), or run
  the [quick start](../start/quick-start.md) once. This page assumes the binary is at
  `~/launchway/bin/launchway`; change the path if yours is elsewhere.
- Git, and Podman or Docker (steps run in containers).
- About 10 minutes.

```sh
export PATH="$HOME/launchway/bin:$PATH"
launchway --help
```

## 1 Let a starter write it

Most repositories need no pipeline of their own writing. Launchway ships Flight plans for Go, Python, Node, Java,
images, static sites and Helm charts (see [Starters](../guide/starters.md)). Make a tiny Go module:

```sh
mkdir -p ~/hello-go && cd ~/hello-go
git init -b main
printf 'module example.com/hello\n\ngo 1.24\n' > go.mod
printf 'package hello\n\nfunc Add(a, b int) int { return a + b }\n' > hello.go
printf 'package hello\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal("2+3")\n\t}\n}\n' > hello_test.go
```

Ask what fits:

```sh
launchway init
```

```text
launchway/go-library fits: go.mod was found.

# hello-go: made by launchway init because go.mod was found.
# Every input of the flight plan has a default; see what it runs with: launchway validate --resolved
pipeline: hello-go
on:
  push: { branches: [main] }
  pull_request: {}
supersede: true
steps:
  ci:
    uses: launchway/go-library
    with: { go_version: "1.24", test_versions: ["1.24"] }

This is only printed. Save it with: launchway init --write
```

The file is a few lines of your own: `uses:` names the Flight plan, and `with:` carries what was read from
`go.mod`. A file can be shorter still: `uses: launchway/go-service` alone is a whole pipeline, and where the
project has environments the same Flight plan delivers to them in order (see [Starters](../guide/starters.md#delivery)).
Any stage can be left out with `skip:`, extended with `extend:` or taken over with `replace:`. Save it, and see exactly what it does:

```sh
launchway init --write
launchway validate --resolved
```

```text
hello-go runs 5 step(s):

  ci/check            Format and vet the Go code
    in docker.io/library/golang:1.24; runs launchway/scripts/go/check.sh
  ci/test[go=1.24]    Run every Go test, once for each Go version in test_versions
    in docker.io/library/golang:1.24; runs launchway/scripts/go/test.sh
  ci/build            Build every package
    in docker.io/library/golang:1.24; after ci/check, ci/test[go=1.24]; runs launchway/scripts/go/build.sh
  ci/vuln             Look for known vulnerabilities in the dependencies
    in docker.io/library/golang:1.24; runs launchway/scripts/go/vuln.sh
  ci/secrets          Look for secrets committed by mistake
    in docker.io/library/alpine:3.22; runs launchway/scripts/secrets.sh
```

Run it on your laptop (the steps run in the Go image; add `--executor host` to use the Go you have installed instead):

```sh
git add -A && git commit -m "Hello"
launchway run
```

The test report and the coverage profile are written where the **Tests** and **Coverage** tabs of a server read them. Every
input has a default and every default can be changed: a different test command is `with: { test_command: "make test" }`, two Go
versions are `test_versions: ["1.23", "1.24"]` (a **matrix**: each version is a step of its own, `test[go=1.23]`), and an image
that is built and pushed is `push: true` with an `image_repository`.

A repository with no file at all can get the same pipeline when its level of the server allows it; see
[A repository with no pipeline file](../guide/starters.md#a-repository-with-no-pipeline-file).

The rest of this page writes a pipeline by hand, which you do when no Flight plan fits.

## 2 Make a repository to write by hand

```sh
mkdir -p ~/hello-launchway && cd ~/hello-launchway
git init -b main
echo "# Hello, Launchway" > README.md
mkdir -p .launchway
```

## 3 Write the pipeline

Create `.launchway/pipeline.yaml`:

```yaml
pipeline: hello
on:
  push: { branches: [main] }

steps:
  lint:
    image: alpine:3.22
    run: |
      echo "checking the files"
      test -f README.md

  test:
    image: alpine:3.22
    needs: lint
    run: |
      echo "running the tests"
      grep -q Hello README.md

  build:
    image: alpine:3.22
    needs: test
    run: |
      mkdir -p dist
      echo "Hello from commit $LAUNCHWAY_COMMIT" > dist/hello.txt
      echo "version=1.0.$LAUNCHWAY_RUN_NUMBER" >> "$LAUNCHWAY_OUTPUTS"

  report:
    image: alpine:3.22
    needs: build
    run: echo "built version $LAUNCHWAY_NEEDS_BUILD_VERSION"
```

What it says:

- `on.push.branches: [main]`: run on every new commit to `main`.
- Four **steps**, each a shell script in the `alpine:3.22` container image.
- `needs` orders them: `test` waits for `lint`, and so on. Steps with no unmet needs run at the same time.
- `build` writes an **output**, `version=...`, to the file named by `$LAUNCHWAY_OUTPUTS`. The step
  that needs it reads it as `$LAUNCHWAY_NEEDS_BUILD_VERSION`.

```mermaid
flowchart LR
  lint --> test --> build --> report
```

## 4 Check it

```sh
git add -A
git commit -m "First pipeline"
launchway validate
```

```text
.launchway/pipeline.yaml: pipeline "hello" is valid, 4 step(s) in 4 level(s)
```

Mistakes are reported with their line before anything runs. Change `needs: lint` to `needs: lnt`
and run `launchway validate` again:

```text
.launchway/pipeline.yaml:14: steps.test.needs: needs "lnt", but there is no step with that name
```

Put it back to `needs: lint`.

## 5 Run it on your laptop

```sh
launchway run
```

`launchway run` uses the same engine as the server, on your machine, against your working copy:

```text
lint   │ checking the files
lint   │ ✓ succeeded in 100ms
test   │ running the tests
test   │ ✓ succeeded in 100ms
build  │ ✓ succeeded in 100ms
report │ built version 1.0.1
report │ ✓ succeeded in 100ms

  ✓ lint    succeeded in 100ms
  ✓ test    succeeded in 100ms
  ✓ build   succeeded in 100ms
  ✓ report  succeeded in 100ms

Succeeded in 500ms
```

Make a test fail to see what that looks like: change `grep -q Hello` to `grep -q Goodbye` and run
again. `test` fails and the steps after it are skipped:

```text
  ✓ lint    succeeded in 100ms
  ✗ test    failed    in 100ms
  ↷ build   skipped
  ↷ report  skipped

Failed in 300ms
```

Change it back to `grep -q Hello`.

## 6 Let a server run it on every commit

Make a directory for a small server, beside the repository:

```sh
mkdir -p ~/launchway-server && cd ~/launchway-server
```

Create `~/launchway-server/launchway.yaml`. Replace `/home/you` with your home directory
(`echo $HOME` prints it):

```yaml
listen: 127.0.0.1:8080
data: ./data
projects:
  - name: hello
    repo: /home/you/hello-launchway
    branch: main
    poll: 5s
```

Start the server:

```sh
launchway server --config launchway.yaml
```

```text
  Launchway f717337 is listening on http://127.0.0.1:8080 (no sign-in: anyone who can reach it has full access)
  1 project(s), data in /home/you/launchway-server/data, SQLite database /home/you/launchway-server/data/launchway.db
```

Open **http://127.0.0.1:8080** and choose **Projects → hello**. There are no runs yet: when a
server first sees a repository, it records the commits already there and runs none of them.

In another terminal, make a commit:

```sh
cd ~/hello-launchway
echo "More text" >> README.md
git commit -am "Second commit"
```

Within a few seconds a run starts. Open it: the steps are drawn as a graph, and choosing a step
shows its log, live. *Recent pushes* on the project page says why the run started (or why not).

> [!TIP]
> A repository on a Git host works the same way: put its address in `repo:`, for example
> `https://git.example.com/team/hello.git`, and push to it. See
> [Triggers](../guide/triggers.md) for pull requests, schedules and webhooks.

## 7 Start a run by hand

In the web UI, press **Start a run** on the project page and choose `main`.

Over the API, a server without sign-in needs the header that proves the request is not from
another web site:

```sh
curl -X POST -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' \
  -d '{"ref":"main"}' http://127.0.0.1:8080/api/v1/projects/hello/runs
```

On a server with sign-in, use the command line instead: `launchway login --server URL`, then
`launchway runs start hello --wait`. See [Using the command line](../guide/commands.md).

## What you learned

- A flight plan gives a repository a whole pipeline in a few lines (`launchway init`), and
  `launchway validate --resolved` shows what it runs.
- A pipeline is a file in the repository, checked with `launchway validate` and run locally with
  `launchway run`.
- Steps run in containers, ordered by `needs`, and pass values with outputs.
- A server polls the repository and runs the pipeline for new commits.

## What next

- [Starters](../guide/starters.md): every Flight plan and step template, and the gallery.
- [Tests and reports](tests-and-reports.md): show test results on the run.
- [Pipelines](../guide/pipelines.md): containers, services, inputs and loops.
- [Pipeline reference](../reference/pipeline.md): every key.
