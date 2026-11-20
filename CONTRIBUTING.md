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

## Licence and contributor licence agreement

Launchway is free software under the [GNU Affero General Public License, version 3](LICENSE)
(AGPL-3.0). [LICENSING.md](LICENSING.md) explains what that means in plain words. JK Magic
Creations, which owns Launchway, also offers a commercial licence to organisations that cannot
accept the AGPL's terms.

To make that possible, every contributor agrees to the [contributor licence agreement](CLA.md)
(CLA):

- **You keep the copyright** to what you write.
- **You grant JK Magic Creations a licence** to your contribution, including a patent licence and
  the right to sublicense it. This lets JK Magic Creations relicense Launchway, including under
  commercial terms, and distribute your contribution under those terms as well as under the AGPL.
  Read [CLA.md](CLA.md) for the exact terms; it, not this summary, is the agreement.
- **A pull request cannot be merged until its author has agreed to the CLA.**

To agree, comment on your pull request with the statement given in
[How to agree](CLA.md#how-to-agree), including your name and e-mail address. The maintainer
records your agreement. You only need to do this once. Until an automated CLA bot is set up, this
is done by hand.

The CLA text is awaiting review by a lawyer. Until that is done, no agreement is accepted, so
pull requests from anyone but the owner cannot be merged yet.

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
