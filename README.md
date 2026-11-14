# Launchway

A build and delivery platform for teams and enterprises, run as a shared service on Kubernetes or
on your own servers.

What Launchway is built around:

- **High availability from the ground up.** Several servers share one PostgreSQL database with no
  leader. Nothing is held in a server's memory, so losing one loses no work.
- **Durable runs.** Every change to a run is recorded as an event. A run survives crashes and
  restarts, and resumes from the step that failed.
- **Runners that only connect outbound**, in any network, cluster or cloud. Steps run in Kubernetes
  pods or containers, with named toolbox containers and services sharing a workspace.
- **Secrets that never reach the server.** Pipelines hold references; the runner fetches values
  from Vault or OpenBao just before a step and masks them in output.
- **A delivery model, not only builds:** approvals today; environments, releases, promotion,
  verification and rollback are next.
- **Pipelines you can check and run anywhere**, with line-numbered errors before anything starts.

It is early. What is described below works and is tested, including with several servers and with
real Kubernetes, PostgreSQL and OpenBao. Much of the rest is designed but not built yet (see
[Status](#status)). There is no sign-in yet, so do not expose it to an untrusted network.

## Running several servers

Point every server at the same PostgreSQL database and put them behind a load balancer:

```yaml
# launchway.yaml
listen: 0.0.0.0:8080
database: postgres://launchway:...@db.internal:5432/launchway
```

`LAUNCHWAY_DATABASE` overrides the file. Each server exposes `/healthz` (the process is up) and
`/readyz` (the database answers). Runners may connect to any server. With no `database` setting a
single server keeps its data in an embedded SQLite file, which suits development and small
installs.

## Try it on one machine

You need Go 1.27+, Node 24+, Git, and Podman or Docker.

```sh
make run
```

`make run` does four things:

- builds the UI and the binary;
- creates a demo repository at `../launchway-demo`;
- starts the server at **http://127.0.0.1:8080**;
- watches two projects: the demo repository, and Launchway itself.

Things to try:

- **Projects → demo-app → Start a run.** The branch list is read from the repository as you open it.
- **Watch the run live.** The steps appear as a graph and the logs stream in.
- **Resume a failed run.** The demo's `smoke-test` fails on its first attempt on purpose while a
  file called `FLAKY` exists. Press *Resume from smoke-test*. Only that step and the ones after it
  run again; the earlier results are kept.
- **Approve a deployment.** On `main`, the demo waits for approval before `deploy-dev`. Open
  **Approvals**, or the run, and approve or reject it.
- **Push to trigger a run.** Commit to `../launchway-demo` on `main` or `feature/*` and push. A run
  starts within a few seconds, and *Recent pushes* on the project explains every decision.
- **Survive a restart.** Stop the server while a step is running and start it again. The step is
  run again, and nothing that had finished runs twice.
- **Open the Playground.** Write a pipeline and watch it validate and draw as you type.
- **Run locally.** In any repository with a `.launchway/pipeline.yaml`, run `bin/launchway run`. It
  uses the same engine, on your machine, against your working copy.

## A pipeline

```yaml
# .launchway/pipeline.yaml
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
      echo "building $LAUNCHWAY_COMMIT"
      echo digest=sha256:... >> "$LAUNCHWAY_OUTPUTS"

  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main }
    run: echo "deploying $LAUNCHWAY_NEEDS_BUILD_DIGEST"
```

| Key | Meaning |
|---|---|
| `image` | Container image the step runs in |
| `run` | Shell script, run with `sh -e` in the repository checkout |
| `needs` | Steps that must succeed first; steps with no unmet needs run in parallel |
| `when` | `branch`, `tag` and `event` (`push`, `manual`) patterns; the step is skipped if they do not match |
| `env` | Extra environment variables |
| `secrets` | Environment variables whose values a runner looks up; see [Secrets](#secrets) |
| `container` | A named container from the `containers` section, instead of `image` |
| `retry` | Extra attempts after a failure |
| `timeout` | Per attempt; default 1h, at most 24h. For an approval, how long to wait for a decision; default 24h, at most 30 days |
| `kind` | `step` (the default) or `approval`; see [Approvals](#approvals) |
| `message` | For an approval: the question to decide, one line of at most 500 characters |

### Containers and services

Name the toolboxes a pipeline needs once, then use them by name. Every container in a run shares
the workspace. Services run beside the steps for the whole run and are reachable at `localhost`.

```yaml
containers:
  maven:    { image: maven:3.9-eclipse-temurin-21, memory: 2Gi, cpu: "1" }
  dotnet:   { image: mcr.microsoft.com/dotnet/sdk:9.0 }
  gcloud:   { image: gcr.io/google.com/cloudsdktool/google-cloud-cli:stable }
  postgres: { image: postgres:17, service: true, env: { POSTGRES_PASSWORD: test } }

steps:
  build:  { container: maven,  run: mvn -B package }
  test:   { container: dotnet, run: dotnet test, needs: build }
  deploy: { container: gcloud, run: gcloud run deploy ..., needs: test, when: { branch: main } }
```

On Kubernetes, a run is one pod: a container per toolbox plus the services, so steps start
instantly once the pod is up. On Podman, services run in a pod per run. Plain `image:` steps still
work, and each image used that way gets a container of its own.

Steps write `key=value` lines to `$LAUNCHWAY_OUTPUTS`. Steps that need them receive
`LAUNCHWAY_NEEDS_<STEP>_<KEY>`. Every step also gets `LAUNCHWAY_COMMIT`, `LAUNCHWAY_BRANCH`,
`LAUNCHWAY_RUN_NUMBER` and related variables.

Mistakes are reported with line numbers, before anything runs:

```
$ launchway validate
.launchway/pipeline.yaml:14: steps.build.needs: needs "tests", but there is no step with that name
```

## Approvals

An approval step pauses the run until a person decides:

```yaml
steps:
  approve-prod:
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy 2.14 to production?
    timeout: 24h
  deploy-prod:
    image: alpine:3.22
    needs: approve-prod
    run: ./deploy.sh
```

An approval takes `needs`, `when`, `message` and `timeout`, and runs nothing, so it has no image or
script. When its needs have succeeded it waits, and every pending approval is listed on the
**Approvals** page. Approving lets the steps after it run. Rejecting, or nobody deciding before the
timeout, fails it: the steps after it are skipped, and resuming the run asks again. Each decision
is kept with the run: who made it, when, and their comment.

Decide in the web UI, or over the API with `POST /api/v1/runs/{id}/steps/{step}/approve` (or
`/reject`) and an optional `{"comment": "..."}`. `launchway run` asks in the terminal, and rejects
the approval if there is no terminal to ask in.

Until there is sign-in, everyone acts as the server's configured `user`, who may approve runs they
started. To forbid that once separate users exist, set:

```yaml
approvals:
  allow_self: false   # the person who started a run may reject it, but not approve it
```

## Secrets

A step names the secrets it needs. Each one becomes an environment variable:

```yaml
steps:
  push:
    image: alpine:3.22
    secrets:
      REGISTRY_PASSWORD: { store: vault, key: apps/harbor/robot, field: password }
      NPM_TOKEN:         { store: env,   key: NPM_TOKEN }
    run: echo "$REGISTRY_PASSWORD" | some-login
```

**The server never sees a secret value.** The pipeline, the server and the UI hold only the
reference (`vault:apps/harbor/robot#password`); the runner looks the value up just before the step
runs, inside the network that owns the secret, and replaces it with `***` in the step's logs and
outputs. Values shorter than 4 characters are not masked, and the log says so.

Each runner has its own stores:

| Type | `key` is | Settings |
|---|---|---|
| `env` | an environment variable of the runner process (not `LAUNCHWAY_*`) | none |
| `file` | a file name in a directory, such as a mounted Kubernetes secret; nothing outside it can be read | `dir` |
| `vault` | a path in a KV version 2 engine of HashiCorp Vault or OpenBao; `field` is required | `address`, `mount` (default `secret`), `namespace`, `ca_file`, and one of `token_env`, `token_file`, or `role` for Kubernetes auth (with `auth_path`, default `kubernetes`, and `jwt_file`, default the pod's service account token) |

A runner has only the stores you configure. The one exception is `launchway run` on your own
machine, which also has an `env` store so a pipeline can read your own environment. A server or
shared runner gets an `env` store only if you define one, because it lets pipelines read the
runner's environment.

Any pipeline can ask for any key, so every store takes allow-lists, checked on the runner:
`projects` and `keys`, lists of patterns where `*` does not cross a `/`. An empty list allows
everything.

On Kubernetes, a step's environment (secrets included) is held in memory inside the run's pod
while the step runs, where the run's other containers could read it. Do not put containers you do
not trust in a pipeline that uses secrets.

The built-in runner reads its stores from `runner.secrets` in the server's configuration; `launchway
runner` and `launchway run` read the same map from `--secrets-config FILE`:

```yaml
runner:
  secrets:
    vault:
      type: vault
      address: https://vault.example:8200
      role: launchway          # Kubernetes auth
      projects: [payments, "web-*"]
      keys: ["apps/*/*"]
    mounted:
      type: file
      dir: /var/run/launchway-secrets
```

A secret that does not exist or may not be read fails the step, naming the store and key. A store
that cannot be reached (no answer, sealed, 5xx) is asked again for 30 seconds, and then the step is
handed back to be run again later. `launchway run` uses the project name `local`.

## Commands

```
launchway server   [--config launchway.yaml]   the server, web UI and a built-in runner
launchway runner   --server URL --token T     a runner on another machine (outbound connections only)
                   [--secrets-config FILE]
launchway run      [--secrets-config FILE]    this directory's pipeline, on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     the graph a pipeline produces, as JSON
```

## How it is built

| Package | What it does |
|---|---|
| `internal/pipeline` | Parses and validates the pipeline file into a typed graph |
| `internal/engine` | The durable run engine. Every change is an event in the database (PostgreSQL, or SQLite for one server) and state is rebuilt from events. Runners hold steps on leases; a lost runner's step is run again. Handles retries, cancel, resume and approvals. |
| `internal/executor` | Runs a step in a Podman or Docker container, in a Kubernetes pod, or on the host |
| `internal/runner` | Takes steps from the server over HTTP, streams logs, sends heartbeats |
| `internal/secrets` | Secret stores (env, file, Vault/OpenBao) and masking, used only by runners |
| `internal/source` | Mirrors Git repositories, starts runs on matching pushes, records why |
| `internal/api` | Runner API, UI API and the live event stream |
| `web/` | The React UI, compiled into the binary |

`make test` runs the Go tests with the race detector and type-checks the UI. The tests cover:

- crash recovery;
- lost and killed runners;
- many runners competing for the same steps;
- retries, cancel and resume;
- approvals, including two servers deciding or expiring the same one at once;
- real Git repositories;
- secrets kept out of the database, events, logs and API (`make test-vault` adds a real OpenBao);
- real containers (`make test-containers`);
- a real Kubernetes cluster (`make test-kubernetes`).

## Status

Built so far:

- the pipeline model;
- the durable engine;
- the Podman, Docker, host and Kubernetes executors;
- named containers and services;
- secrets (environment, files, Vault and OpenBao);
- approvals;
- Git polling;
- the web UI.

Not built yet, in the order planned:

- Image builds
- Releases and promotion
- The Argo CD driver
- Inputs
- Policy
- Infrastructure runs
- VM fleets

Not suitable for untrusted networks yet: the web UI has no sign-in and listens on localhost by
default.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
