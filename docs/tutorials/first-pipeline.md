# First pipeline in 10 minutes

Write a pipeline for a repository of your own, check it, run it on your laptop, then let a
Launchway server run it on every commit. For developers new to Launchway.

> **Verified** on 2026-11-19 against commit f717337: every command below, in order, on Linux with
> Podman 5, from a fresh clone. **Not verified:** macOS, Windows (WSL), Docker instead of Podman.

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

## 1 Make a repository

```sh
mkdir -p ~/hello-launchway && cd ~/hello-launchway
git init -b main
echo "# Hello, Launchway" > README.md
mkdir -p .launchway
```

## 2 Write the pipeline

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

## 3 Check it

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

## 4 Run it on your laptop

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

## 5 Let a server run it on every commit

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

## 6 Start a run by hand

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

- A pipeline is a file in the repository, checked with `launchway validate` and run locally with
  `launchway run`.
- Steps run in containers, ordered by `needs`, and pass values with outputs.
- A server polls the repository and runs the pipeline for new commits.

## What next

- [Tests and reports](tests-and-reports.md): show test results on the run.
- [Pipelines](../guide/pipelines.md): containers, services, inputs and loops.
- [Pipeline reference](../reference/pipeline.md): every key.
