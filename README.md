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
- **Sign-in with your organisation's OpenID Connect provider**, roles per project, API tokens for
  scripts, and an audit trail of every change.
- **Secrets that never reach the server.** Pipelines hold references; the runner fetches values
  from Vault or OpenBao just before a step and masks them in output.
- **A delivery model, not only builds:** approvals today; environments, releases, promotion,
  verification and rollback are next.
- **Pipelines you can check and run anywhere**, with line-numbered errors before anything starts.

It is early. What is described below works and is tested, including with several servers and with
real Kubernetes, PostgreSQL and OpenBao. Much of the rest is designed but not built yet (see
[Status](#status)). Turn on [sign-in](#sign-in-and-access) before anyone else can reach it.

## Running several servers

Point every server at the same PostgreSQL database and put them behind a load balancer:

```yaml
# launchway.yaml
listen: 0.0.0.0:8080
database: postgres://launchway:...@db.internal:5432/launchway
auth:
  mode: oidc   # see Sign-in and access
  # ...
```

`LAUNCHWAY_DATABASE` overrides the file. Each server exposes `/healthz` (the process is up) and
`/readyz` (the database answers, and the server is not shutting down). A starting server waits up
to `database_wait` (default `60s`) for the database. On SIGTERM a server reports not ready at
once, keeps serving for `server.drain_delay` (default `0s`) so that load balancers checking
`/readyz` move away, and then gives requests in flight up to 15 seconds. Runners may connect to
any server. With no `database` setting a
single server keeps its data in an embedded SQLite file, which suits development and small
installs. Sessions, API tokens and sign-ins in progress are kept in the database too, so a browser
or script may be sent to any server.

## Run it on Kubernetes

`make image` builds a container image, and the Helm chart in `deploy/helm/launchway` runs
several servers against your PostgreSQL database, with runners that execute steps in pods in a
namespace of your choice:

```sh
helm install launchway deploy/helm/launchway -n launchway --create-namespace \
  --set database.existingSecret=launchway-database
```

See [deploy/README.md](deploy/README.md) for the required values, a high-availability example,
upgrades, and security notes.

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
- **Edit a pipeline.** *Projects → demo-app → Edit pipeline* opens the file from a branch. Change
  it, press *Run this draft* to try the change without saving it, then *Save…* to commit it (the
  example configuration lets the demo be saved; see [Editing pipelines](#editing-pipelines)).
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
| `description` | One line saying what the step is for, shown under its name and on the graph |
| `image` | Container image the step runs in |
| `run` | Shell script, run with `sh -e` in the repository checkout |
| `needs` | Steps that must succeed first; steps with no unmet needs run in parallel |
| `when` | `branch`, `tag` and `event` (`push`, `manual`, `draft`) patterns; the step is skipped if they do not match. In a [draft run](#editing-pipelines), a step with a `when` runs only if its `event` names `draft` |
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

The workspace lives on one runner, so all of a run's steps go to the runner that took its first
step, for as long as that runner keeps in touch with the server. The cost is that a run's parallel
steps share that one runner's concurrency; different runs still spread across runners. If the
runner stops responding for 90 seconds (three lease periods), or shuts down, another runner takes
the run over and carries on in a fresh checkout: files that earlier steps wrote are then gone, and
the step's log says so. Values passed as outputs (below) always survive.

Steps write `key=value` lines to `$LAUNCHWAY_OUTPUTS`. Steps that need them receive
`LAUNCHWAY_NEEDS_<STEP>_<KEY>`. Every step also gets `LAUNCHWAY_COMMIT`, `LAUNCHWAY_BRANCH`,
`LAUNCHWAY_RUN_NUMBER` and related variables.

Mistakes are reported with line numbers, before anything runs:

```
$ launchway validate
.launchway/pipeline.yaml:14: steps.build.needs: needs "tests", but there is no step with that name
```

When a step fails, its last few lines of output (on stderr if it wrote any there, else on stdout,
with secrets masked as in the log) are kept with the step and shown where the run says it failed,
so "exit code 1" comes with its reason.

## Editing pipelines

Git is the only place a pipeline is kept. Launchway never holds a copy of its own: the editor
(*Projects → Edit pipeline*, or *Edit this pipeline* on a run's *Pipeline* tab) reads
`.launchway/pipeline.yaml` from the tip of a branch, checks it as you type with the same parser
the server uses, marks problems on their lines and draws the graph. It shows which branch and
commit the text came from and whether you have changed it. Unsaved edits stay in that browser tab
only. A branch without a pipeline opens with a starting point.

**Run this draft** runs the edited text against the branch's current commit without saving it.
The run is marked *Draft* everywhere; it records who started it and the SHA-256 of the text, and
keeps the text itself (which is in no commit) so that its *Pipeline* tab shows exactly what ran.
A draft is its own trigger event, `draft`:

- a step with no `when` runs as usual;
- a step with a `when` runs only if its `event` list names `draft`. `when: { branch: main }` or
  `when: { event: [push, manual] }` does not run in a draft of `main`, so a deployment guarded by
  `when` does not run from unsaved, unreviewed text;
- a step that declares `secrets` fails before any runner sees it, unless the project allows
  drafts to read secrets (below). Its runner never looks a secret up.

A draft lets someone run steps nobody has reviewed, with whatever the runners can reach, so who
may start one is a setting of its own, checked on the server with or without sign-in:

```yaml
drafts:
  allow: operator      # admin, operator (the default) or none: the role needed on a project
  secrets: false       # the default: steps of drafts may not read secrets
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    drafts: { allow: admin, secrets: true }   # overrides, per project
```

Resuming a failed draft run runs its text again, so it needs the same right, and whether its
steps may read secrets is decided again by the setting at that moment.

**Save…** commits the file as you: the author is your name and e-mail address as your sign-in
provider gave them (an address the provider says is unverified is not used; without one it is
`<you>@users.noreply.launchway`), the committer is Launchway, and only the pipeline file changes.
By default it goes to a new branch, `<branch_prefix><you>/<date-time>`, with a link to open a pull
request when the repository is on GitHub, GitLab, Gitea or Forgejo (judged by its host name;
Launchway calls no Git host API). A save is refused, and nothing is pushed, if the branch has
moved on since the text was opened, so two people cannot overwrite each other; or if the text has
problems, unless an admin of the project chooses to save it anyway. Saving needs the operator role
and a `push` section for the project:

```yaml
projects:
  - name: payments
    repo: https://git.example.com/team/payments.git
    push:
      branch_prefix: launchway/          # the default
      allow_direct: ["release/*"]        # branches that may be committed to directly; default none
```

Pushing uses the server's Git credentials, exactly as fetching does: in the repository URL, or in
Git settings in the server's environment, such as `GIT_CONFIG_COUNT=1`,
`GIT_CONFIG_KEY_0=http.https://git.example.com/.extraHeader` and
`GIT_CONFIG_VALUE_0=Authorization: Bearer ...`. Give that credential only the rights it needs,
and protect the branches that matter on the Git host: Launchway refuses direct saves outside
`allow_direct`, but the credential itself could push anywhere it is allowed to. A project without
`push` cannot be saved to; the editor offers *Copy* and *Download* instead. Each save works in a
temporary clone of the server's mirror, which is removed whatever happens, and pushes with a
lease, so saves on several servers at once are safe. If the push fails, Git's own message is
shown, with any credentials in it removed.

Over the API: `GET /api/v1/projects/{name}/pipeline?ref=main` reads the file;
`POST /api/v1/projects/{name}/draft-runs` with `{"ref": "main", "pipeline": "..."}` starts a draft
run (problems answer `422` with their lines); `POST /api/v1/projects/{name}/pipeline` with
`{"base_ref", "base_sha", "pipeline", "message", "branch", "force"}` saves (`branch` empty for a
new branch, or `base_ref` itself to commit to it directly; a stale `base_sha` answers `409`). The
text may be at most 256 KiB.

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

With sign-in on, the person who started a run may reject it but not approve it: someone else must.
Without sign-in everyone is the same configured `user`, so they may. Either default can be changed:

```yaml
approvals:
  allow_self: false   # true lets people approve runs they started
```

A run started by a push records the commit's author name, which anyone can set in Git; it is not
checked against anyone's sign-in.

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
run` reads the same map from `--secrets-config FILE`, and `launchway runner` from either
`--secrets-config FILE` or the `secrets:` section of its `--config` file (see [Commands](#commands)):

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

## Sign-in and access

Out of the box Launchway has no sign-in (`auth.mode: none`): everything is done as the configured
`user`, with full rights. That suits a server on your own machine and nothing else, so **a server
without sign-in refuses to start unless it listens only on a loopback address** (`127.0.0.1`, `::1`,
`localhost`). If something in front of it already decides who may reach it, say so with
`auth.allow_anonymous: true`. The UI shows "No sign-in" on every page while this is the case.

### Signing in with OpenID Connect

Any OpenID Connect provider works (Authentik, Keycloak, Okta, Entra ID, Dex and others), through
discovery, without anything specific to one of them:

```yaml
listen: 0.0.0.0:8080
server:
  trust_proxy: true          # only behind a reverse proxy; see below
auth:
  mode: oidc
  oidc:
    issuer: https://sso.example.com/realms/acme   # exactly as in the provider's discovery document
    client_id: launchway
    client_secret_env: LAUNCHWAY_OIDC_SECRET      # or client_secret_file: /run/secrets/oidc-secret
    redirect_url: https://launchway.example.com/auth/callback
    # scopes: [openid, profile, email, groups]    # the default
    # username_claim: preferred_username          # the default; then email, then sub
    # groups_claim: groups                        # the default
  idle_timeout: 8h        # the default: a session unused this long ends
  session_lifetime: 24h   # the default: every session ends this long after sign-in
  access:
    - { group: platform-admins, role: admin }
    - { group: payments,        role: operator, projects: ["payments-*"] }
    - { group: everyone,        role: viewer }
    - { user: dana,             role: operator, projects: [web] }
```

Register Launchway with the provider as a confidential web application using the authorization
code flow, with `redirect_url` as its redirect URI. Launchway sends PKCE (S256), `state` and
`nonce`, and checks the ID token's signature, issuer, audience, expiry and nonce. The client secret
is read from an environment variable or a file, never from the configuration file.

The provider must put the person's groups in the ID token, or in its userinfo response, under
`groups_claim`; most providers need a claim mapping for that. Entra ID sends group object IDs
rather than names (use the IDs in `access`), and leaves groups out for people in more than 200.
Choose a `username_claim` that people cannot change themselves at the provider: `user:` bindings,
and every record of who did what, use it.

The endpoints are `GET /auth/login?next=/path` (where to return afterwards; only paths on this
server are accepted), `GET /auth/callback`, `POST /auth/logout`, and `GET /api/v1/me`, which says
who you are and what you may do. `GET /api/v1/info` then reports only the version and that sign-in
is on.

### Roles

| Role | May |
|---|---|
| `viewer` | see a project and its runs, logs, events, pipeline files and approvals; see the runners; use the Playground; open the pipeline editor |
| `operator` | everything a viewer may, and start, cancel and resume runs, approve and reject approvals, save pipelines, and start draft runs if `drafts.allow` is `operator` |
| `admin` | everything an operator may, read the audit trail, start draft runs if `drafts.allow` is `admin`, and save a pipeline that has problems |

Each entry in `access` grants a role to one `group` (from `groups_claim`) or one `user` (from
`username_claim`), on the projects matching one of `projects`, or on every project if there are
none. Patterns use `*` (any characters but `/`), `?` (one character) and `[...]`. Where several entries
match, the highest role wins. Someone matching none can sign in but sees nothing. The audit trail
needs `admin` from an entry without `projects`.

Rights are worked out on every request from the configuration and the groups the provider gave at
the person's latest sign-in, and checked by the server whatever the UI shows. A run, project or
approval you may not see answers `404`, exactly as if it did not exist; one you may see but not
change answers `403`. Lists and the live stream contain only what you may see.

### Sessions

Signing in sets a session cookie (`HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` when the
browser uses HTTPS). The session is a random 256-bit value; the database keeps only its SHA-256, so
every server accepts it and a copy of the database does not contain it. Signing out ends it on
every server, and live streams it opened close within 15 seconds. What the UI fetches by itself,
such as refreshing a list, does not count as activity for `idle_timeout`.

### API tokens

Scripts use API tokens: `Authorization: Bearer lw_...`. Make one on the **API tokens** page with a
name and an expiry (default 90 days, at most a year). It is shown once; only its SHA-256 is stored.
A token acts as you, with your rights as of your latest sign-in, and can be revoked at any time.
Tokens can only be made from a signed-in browser, not with another token. Over the API:
`GET /api/v1/tokens`, `POST /api/v1/tokens` (`{"name": "...", "expires_in_days": 30}`) and
`DELETE /api/v1/tokens/{id}`.

Launchway cannot tell when someone leaves your organisation: their tokens keep the groups of their
last sign-in until they expire. Remove their tokens when they leave, with
`DELETE FROM api_tokens WHERE username = '...'` in the database.

### Requests from other sites

A request that changes something and is authenticated by the session cookie must carry the header
`X-Launchway-CSRF: 1`, and if it has an `Origin` header, it must be this server; otherwise it is
refused with `403`. Browsers do not let another site add that header, so no web page can act with
a visitor's session. The UI sends it; scripts using a token need not. Without sign-in the header is
required all the same, so a web page cannot drive a Launchway on `localhost`. Launchway's pages
cannot be shown in a frame on another site.

### Behind a reverse proxy

Set `server.trust_proxy: true` only if every client reaches Launchway through one reverse proxy.
Launchway then believes its `X-Forwarded-Proto` (to mark cookies `Secure`) and takes the client's
address from the last entry of `X-Forwarded-For`, the one the proxy added. The proxy must pass the
`Host` header through unchanged, and must not buffer `/api/v1/stream`. Cookies are also `Secure`
whenever `redirect_url` starts with `https://`.

### Audit trail

Every call that changes something (starting, canceling and resuming runs, starting draft runs
(`run.draft`), saving pipelines (`pipeline.save`), approving and rejecting, making and revoking
tokens) by someone signed in, allowed or refused, and every sign-in, failed sign-in and sign-out is
recorded with its time, who, their address, the action, project, target (run, step or token name),
outcome (`ok`, `denied` or `failed`) and a short detail. A draft run is recorded with the SHA-256
of its text and a save with its branch and commit; the pipeline text itself is never in the
trail, nor are secrets, tokens and session ids. Launchway only ever adds to it. Admins read it on the **Audit trail** page or with
`GET /api/v1/audit?user=&project=&action=&from=&to=&before=&limit=` (times in Unix milliseconds;
`before` is the `next_before` of the previous page). It is kept until you remove old rows from the
`audit` table yourself.

## Commands

```
launchway server   [--config launchway.yaml]   the server, web UI and a built-in runner
launchway runner   --server URL --token T     a runner on another machine (outbound connections only)
                   [--config FILE] [--secrets-config FILE] [--health-listen ADDR]
launchway run      [--secrets-config FILE]    this directory's pipeline, on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     the graph a pipeline produces, as JSON
```

`launchway runner --config FILE` reads the same settings as `runner:` in the server's
configuration, plus where the server is. Unknown settings are refused. Flags given on the command
line win, then `LAUNCHWAY_SERVER` and `LAUNCHWAY_RUNNER_TOKEN`, then the file. The token is never
in the file.

```yaml
# runner.yaml
server: https://launchway.example.com
name: build-2                 # default: the host name
data: /var/lib/launchway-runner
concurrency: 4
executor: kubernetes          # auto, container, podman, docker, host or kubernetes
kubernetes:
  namespace: builds
  helper_image: registry.example.com/busybox:1.37
  service_account: builder    # for step pods; default none
  node_selector: { pool: builds }
  cpu: 250m                   # request per container, unless the pipeline asks
  memory: 256Mi
  memory_limit: 4Gi
  image_pull_secrets: [registry-credentials]
  keep_failed: 2h             # keep a failed run's pod this long, for a resume
  start_timeout: 10m
secrets: {}                   # see Secrets
```

`--health-listen :8081` serves `/healthz` (the process is up) and `/readyz` (the server answered
within the last two minutes) for probes.

## How it is built

| Package | What it does |
|---|---|
| `internal/pipeline` | Parses and validates the pipeline file into a typed graph |
| `internal/engine` | The durable run engine. Every change is an event in the database (PostgreSQL, or SQLite for one server) and state is rebuilt from events. Runners hold steps on leases; a lost runner's step is run again. Handles retries, cancel, resume and approvals. |
| `internal/executor` | Runs a step in a Podman or Docker container, in a Kubernetes pod, or on the host |
| `internal/runner` | Takes steps from the server over HTTP, streams logs, sends heartbeats |
| `internal/secrets` | Secret stores (env, file, Vault/OpenBao) and masking, used only by runners |
| `internal/source` | Mirrors Git repositories, starts runs on matching pushes, records why |
| `internal/auth` | Roles and access bindings, OpenID Connect sign-in, and the cryptography sessions and tokens need |
| `internal/api` | Runner API, UI API, sign-in, API tokens, access checks, the audit trail and the live event stream |
| `internal/fakeidp` | An OpenID Connect provider for tests, which can be made to misbehave |
| `web/` | The React UI, compiled into the binary |

`make test` runs the Go tests with the race detector and type-checks the UI. The tests cover:

- crash recovery;
- lost and killed runners;
- many runners competing for the same steps;
- retries, cancel and resume;
- approvals, including two servers deciding or expiring the same one at once;
- real Git repositories, including saves racing from two servers, stale saves and pushes the
  repository refuses;
- secrets kept out of the database, events, logs and API (`make test-vault` adds a real OpenBao);
- sign-in against a fake OpenID Connect provider: forged state, stolen codes (PKCE), bad ID tokens,
  open redirects, CSRF, session expiry, API tokens, a permission matrix over every route, the live
  stream, the audit trail, and sign-in across two servers;
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
- the pipeline editor: draft runs of unsaved text, and saving to Git as the person who saved;
- sign-in with OpenID Connect, roles per project, API tokens and an audit trail;
- the web UI.

Not built yet, in the order planned:

- Image builds
- Releases and promotion
- The Argo CD driver
- Inputs
- Policy
- Infrastructure runs
- VM fleets

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
