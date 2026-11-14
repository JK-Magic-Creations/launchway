# Slipway

A build and delivery platform: one binary that runs your pipelines on a laptop, a home server or a
fleet of runners, with a live web UI. *Slipway* is a working name.

The design lives in a separate repository, `cicd-platform-design`. This repository is the
implementation, built foundation first.

## Try it

You need Go 1.27+, Node 24+, Git, and Podman or Docker.

```sh
make run
```

`make run` does four things:

- builds the UI and the binary;
- creates a demo repository at `../slipway-demo`;
- starts the server at **http://127.0.0.1:8080**;
- watches two projects: the demo repository, and Slipway itself.

Things to try:

- **Projects → demo-app → Start a run.** The branch list is read from the repository as you open it.
- **Watch the run live.** The steps appear as a graph and the logs stream in.
- **Resume a failed run.** The demo's `smoke-test` fails on its first attempt on purpose while a
  file called `FLAKY` exists. Press *Resume from smoke-test*. Only that step and the ones after it
  run again; the earlier results are kept.
- **Push to trigger a run.** Commit to `../slipway-demo` on `main` or `feature/*` and push. A run
  starts within a few seconds, and *Recent pushes* on the project explains every decision.
- **Survive a restart.** Stop the server while a step is running and start it again. The step is
  run again, and nothing that had finished runs twice.
- **Open the Playground.** Write a pipeline and watch it validate and draw as you type.
- **Run locally.** In any repository with a `.slipway/pipeline.yaml`, run `bin/slipway run`. It
  uses the same engine, on your machine, against your working copy.

## A pipeline

```yaml
# .slipway/pipeline.yaml
pipeline: my-service
on:
  push: { branches: [main, "release/*"] }

steps:
  test:
    image: golang:1.27
    run: go test ./...
    retry: 1

  build:
    image: alpine:3.22
    needs: test
    run: |
      echo "building $SLIPWAY_COMMIT"
      echo digest=sha256:... >> "$SLIPWAY_OUTPUTS"

  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "deploying $SLIPWAY_NEEDS_BUILD_DIGEST"
```

| Key | Meaning |
|---|---|
| `image` | Container image the step runs in |
| `run` | Shell script, run with `sh -e` in the repository checkout |
| `needs` | Steps that must succeed first; steps with no unmet needs run in parallel |
| `when` | `branch`, `tag` and `event` (`push`, `manual`) patterns; the step is skipped if they do not match |
| `env` | Extra environment variables |
| `retry` | Extra attempts after a failure |
| `timeout` | Per attempt; default 1h |

Steps write `key=value` lines to `$SLIPWAY_OUTPUTS`. Steps that need them receive
`SLIPWAY_NEEDS_<STEP>_<KEY>`. Every step also gets `SLIPWAY_COMMIT`, `SLIPWAY_BRANCH`,
`SLIPWAY_RUN_NUMBER` and related variables.

Mistakes are reported with line numbers, before anything runs:

```
$ slipway validate
.slipway/pipeline.yaml:14: steps.build.needs: needs "tests", but there is no step with that name
```

## Commands

```
slipway server   [--config slipway.yaml]   the server, web UI and a built-in runner
slipway runner   --server URL --token T     a runner on another machine (outbound connections only)
slipway run                                 this directory's pipeline, on this machine
slipway validate [file]                     check a pipeline file
slipway graph    [file]                     the graph a pipeline produces, as JSON
```

## How it is built

| Package | What it does |
|---|---|
| `internal/pipeline` | Parses and validates the pipeline file into a typed graph |
| `internal/engine` | The durable run engine. Every change is an event in SQLite and state is rebuilt from events. Runners hold steps on leases; a lost runner's step is run again. Handles retries, cancel and resume. |
| `internal/executor` | Runs a step in a Podman or Docker container, or on the host |
| `internal/runner` | Takes steps from the server over HTTP, streams logs, sends heartbeats |
| `internal/source` | Mirrors Git repositories, starts runs on matching pushes, records why |
| `internal/api` | Runner API, UI API and the live event stream |
| `web/` | The React UI, compiled into the binary |

`make test` runs the Go tests with the race detector and type-checks the UI. The tests cover:

- crash recovery;
- lost and killed runners;
- many runners competing for the same steps;
- retries, cancel and resume;
- real Git repositories;
- real containers (`make test-containers`).

## Status

This is the foundation. Not built yet, in the order planned:

- Kubernetes executor and toolbox images
- Releases and promotion
- The Argo CD driver
- Approvals and inputs
- Secrets with Vault and OpenBao
- Policy
- Infrastructure runs
- VM fleets

Not suitable for untrusted networks yet: the web UI has no sign-in and listens on localhost by
default.
