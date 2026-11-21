# Quick start

Run Launchway on your own machine with a set of demo projects, in a few minutes. For anyone who
wants to see it working before reading further.

> **Verified** on 2026-11-19 against commit f717337: a fresh `git clone` of the repository, then
> `make run` on Linux (Fedora, Go 1.27.4, Node 26, Podman 5), and the "Things to try" list in the
> web UI. **Not verified:** macOS and Windows (WSL).

## What you need

- Go 1.27.4 or newer, Node 24 or newer, Git, and Podman or Docker.
- Port 8080 free on `127.0.0.1`.

## Three commands

```sh
git clone https://github.com/JK-Magic-Creations/launchway.git
cd launchway
make run
```

When the server is up, it prints:

```text
  Launchway f717337 is listening on http://127.0.0.1:8080 (no sign-in: anyone who can reach it has full access)
  4 project(s), data in .../launchway/launchway-data, SQLite database .../launchway/launchway-data/launchway.db
  Runner token for other machines: .../launchway/launchway-data/runner-token
```

Open **http://127.0.0.1:8080**. Stop the server with Ctrl+C; the data stays in
`launchway-data/` for next time.

`make run` does four things:

- builds the web UI and the `bin/launchway` binary;
- creates a demo repository at `../launchway-demo`, and a demo [shared library](../guide/libraries.md)
  at `../launchway-demo-library` with two repositories that use it (beside your checkout);
- starts the server at **http://127.0.0.1:8080** with [examples/local.yaml](https://github.com/JK-Magic-Creations/launchway/blob/main/examples/local.yaml);
- watches four projects: the demo repository, the two that use the library, and Launchway itself.

The server listens only on `127.0.0.1` and has no sign-in. That is fine on your own machine and
nowhere else: a server without sign-in refuses to listen on any other address.

## Things to try

- **Projects → demo-app → Start a run.** The branch list is read from the repository as you open it.
- **Watch the run live.** The steps appear as a graph and the logs stream in.
- **Resume a failed run.** The demo's `smoke-test` fails on its first attempt on purpose while a
  file called `FLAKY` exists. Press *Resume from smoke-test*. Only that step and the ones after it
  run again; the earlier results are kept.
- **Re-run from a step.** On a finished run, open any step and press *Re-run from this step*. A new
  run starts at the same commit: that step and the ones after it run again, and the steps before
  it are reused with their outputs. Each step's *Inputs* say what it was given, and from where.
- **Approve a deployment.** On `main`, the demo waits for approval before `deploy-dev`. Open
  **Waiting for a decision** to find it, then press **Approve** or **Reject** (recorded as Go or No-go) on the run.
- **Push to trigger a run.** Commit to `../launchway-demo` on `main` or `feature/*` and push. A run
  starts within a few seconds, and *Recent pushes* on the project explains every decision.
- **Survive a restart.** Stop the server while a step is running and start it again. The step is
  run again, and nothing that had finished runs twice.
- **Edit a pipeline.** *Projects → demo-app → Edit pipeline* opens the file from a branch. Change
  it, press *Run this draft* to try the change without saving it, then *Save…* to commit it (the
  example configuration lets the demo be saved; see [Editing pipelines](../guide/editor.md)).
- **Open the Playground.** Write a pipeline and watch it validate and draw as you type.
- **Use a shared library.** *demo-lib-app* runs the library's `release` template at tag `v1`:
  its steps are framed as one group on the graph, and the run says which commit `v1` was. Start a
  run with *Advanced → library override* `platform` = `main` to try the library's unreleased work.
  *demo-plain* has no pipeline file at all; the server runs the library's `check` template for it.
- **Run locally.** In any repository with a `.launchway/pipeline.yaml`, run `bin/launchway run`. It
  uses the same engine, on your machine, against your working copy.

## If something goes wrong

- `address already in use`: something else uses port 8080. Copy `examples/local.yaml`, change
  `listen:`, and start the server with `./bin/launchway server --config <your copy>`.
- Steps fail with an error about Podman or Docker: install one of them, or see
  [Troubleshooting](../howto/troubleshooting.md).

## What next

- [First pipeline in 10 minutes](../tutorials/first-pipeline.md): your own repository.
- [Screenshot tour](../demo.md).
- [Install on one server](../howto/install-single-server.md) or
  [on Kubernetes](../howto/install-kubernetes.md) for a team.
