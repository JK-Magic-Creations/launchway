# Contributing to Launchway

Thank you for looking. Launchway is early: the foundation works and is tested, and most of the
design is still to be built. Issues, ideas and pull requests are all welcome.

## Getting set up

You need Go 1.27 or newer, Node 24 or newer, Git, and Podman or Docker.

```sh
make run        # builds everything and starts a server at http://127.0.0.1:8080 with a demo project
make test       # Go tests with the race detector, and a UI type check
```

Two further test suites need real infrastructure and are skipped by default:

```sh
make test-containers    # real Podman or Docker containers
make test-kubernetes    # a real cluster; see the Makefile for a kind one-liner
make test-s3            # a real S3-compatible server, for the artifact store
```

## Pull requests

- Keep changes focused. A small pull request that does one thing is reviewed quickly.
- Add or update tests. Durability and correctness are the point of this project. Anything that
  touches the engine needs a test showing what happens when things go wrong, not only when they go right.
- Changing the database schema, or adding an event type? Read
  [Writing a migration](docs/development/migrations.md) first: servers of two versions share one
  database during every upgrade, and a rollback must keep working.
- Run `make test` before pushing.
- Write messages and UI text in plain words.

## Sign your work

Launchway uses the [Developer Certificate of Origin](https://developercertificate.org/) instead
of a contributor licence agreement. You keep the copyright to what you write, and no single party
can relicense the project. Sign each commit with:

```sh
git commit -s
```

This adds a `Signed-off-by:` line certifying that you wrote the change or otherwise have the right
to contribute it under the Apache 2.0 licence.

## Where things are

| Path | What |
|---|---|
| `internal/pipeline` | Pipeline file parsing and validation |
| `internal/engine` | The durable run engine (event log, leases, retries, resume) |
| `internal/executor` | Running steps in containers, on Kubernetes, or on the host |
| `internal/runner` | The runner that takes work from the server |
| `internal/source` | Git polling and push decisions |
| `internal/api` | HTTP API and live event stream |
| `web/` | The web UI (React and TypeScript) |
