# Troubleshooting

This page lists the messages Launchway gives when something is wrong, grouped by what you see,
with what each means and what to do. It is for operators and for anyone running pipelines.

> **Verified** on 2026-11-19 against commit f717337: every message in a `text` block below was
> reproduced on a server (SQLite and PostgreSQL 17), a Station (runner), `launchway run` or the command line,
> with Dex and OpenBao in containers, and is quoted as it appeared (paths and names shortened to
> examples). **Not verified:** messages marked "from the code", which are quoted from the source
> without being reproduced.

## What you need

- Access to the server's log (`journalctl -u launchway`, `podman logs launchway`, or
  `kubectl logs`). Many answers to callers are short on purpose; the log has the details.
- For sign-in problems: an admin who can read the [audit trail](oidc-and-roles.md#the-logbook).

## The server does not start

The server checks its whole configuration before it starts, and stops with a message that names
the file and the setting. It exits with status 1.

### It refuses to listen without sign-in

```text
launchway: refusing to listen on 0.0.0.0:8080 without sign-in.
With auth.mode "none" there is no sign-in: anyone who can reach this address could
start pipelines, which run code on the runners, and approve deployments. Either
  - listen on a loopback address, such as 127.0.0.1:8080; or
  - turn on sign-in with auth.mode: oidc (see "Sign-in and access" in the README); or
  - if something in front of Launchway already decides who may reach it, set
    auth.allow_anonymous: true
```

A server without sign-in may listen only on `127.0.0.1`, `::1` or `localhost`. Turn on sign-in
([guide](oidc-and-roles.md)), or listen on loopback behind a proxy that controls access. In a
container the server must listen on `0.0.0.0`, so it needs one of the other two.

### A setting is misspelt or wrong

```text
launchway: launchway.yaml: yaml: unmarshal errors:
  line 2: field lisen not found in type config.Config
```

Unknown settings are refused. Fix the setting on that line; the
[server configuration reference](../reference/server-config.md) lists them all.

```text
launchway: launchway.yaml: database_wait: "soon" is not a duration such as 30s
```

Durations are written like `30s`, `5m` or `8h`.

### The sign-in settings are incomplete

```text
launchway: launchway.yaml: auth: oidc: issuer, client_id and redirect_url are required
oidc: give exactly one of client_secret_env or client_secret_file
access is empty, so nobody could do anything; add at least one binding
```

```text
launchway: launchway.yaml: auth: access is set, but mode is "none", where everyone has full access; set mode: oidc
```

```text
launchway: launchway.yaml: auth: access[0]: role "boss" is not one of viewer, operator, admin
```

```text
launchway: launchway.yaml: auth.oidc.client_secret_env: the environment variable LAUNCHWAY_OIDC_SECRET is empty
```

Every problem in the `auth` section is listed at once. The last one means the service was started
without its Airfield (environment) file, or the variable has another name.

### The database does not answer

```text
time=... level=WARN msg="the database does not answer yet; trying again" attempt=1 in=500ms err="failed to connect to `user=launchway database=launchway`: 127.0.0.1:5432 (127.0.0.1): dial error: dial tcp 127.0.0.1:5432: connect: connection refused"
launchway: connecting to the database: failed to connect to `user=launchway database=launchway`: 127.0.0.1:5432 (127.0.0.1): dial error: dial tcp 127.0.0.1:5432: connect: connection refused
```

The server tries for `database_wait` (default `60s`), then stops. Check that PostgreSQL is up and
reachable, and the address in `LAUNCHWAY_DATABASE` or `database`. A wrong password is also retried
for the whole wait before the server stops:

```text
launchway: connecting to the database: failed to connect to `user=launchway database=launchway`: 127.0.0.1:5432 (127.0.0.1): failed SASL auth: FATAL: password authentication failed for user "launchway" (SQLSTATE 28P01)
```

With SQLite, a data directory the server cannot write to gives, after the same wait:

```text
launchway: connecting to the database: unable to open database file (14)
```

Make the data directory writable by the service's user. For the container image that is user
`65532`; see [Install on one server](install-single-server.md#or-run-the-container-with-podman).

### The port is taken

```text
launchway: listen tcp 127.0.0.1:8080: bind: address already in use
```

Another process (perhaps another Launchway) listens there. Stop it, or change `listen` or
`--listen`.

### A Ramp's token is missing or weak {#a-runner-pools-token-is-missing-or-weak}

```text
launchway: launchway.yaml: runner_pools: prod-deployers: the variable POOL_TOKEN is not set
launchway: launchway.yaml: runner_pools: prod-deployers: the token is shorter than 32 characters; make a long random one, for example with: openssl rand -hex 24
```

Each deploy Ramp (Station (runner) pool) needs its own long random token, given in the variable or file named.

### A secret store is configured wrongly

The server reports problems in `runner.secrets` as `launchway: launchway.yaml: runner.secrets:
secret store "NAME": ...`; a runner or `launchway run` reports them with the file name. Seen:

```text
secret store "a": token_env: the variable NOT_SET_VAR is not set
secret store "a": give exactly one way to log in: token_env, token_file, or role for Kubernetes auth
secret store "a": address "127.0.0.1:8200" is not an http or https URL
secret store "a": type "aws" is not known; use env, file or vault
secret store "a": dir does not apply to env stores
secret store "a": dir /nonexistent is not a directory
secret store "My-Store": not a valid name: use lower-case letters, digits, hyphens and underscores
```

See [Secrets](secrets.md).

## Sign-in fails

### Sign-in answers 502

```json
{"error":"the sign-in provider cannot be reached; try again shortly"}
```

The server cannot read the provider's discovery document. The log says why:

```text
level=WARN msg="the sign-in provider cannot be reached yet; sign-in will keep trying" err="discovering the sign-in provider at http://localhost:5556/dex: oidc: issuer URL provided to client (\"http://localhost:5556/dex\") did not match the issuer URL returned by provider (\"http://127.0.0.1:5556/dex\")"
```

Either the provider is unreachable from the server (check DNS, the firewall and the proxy), or
`issuer` is not written exactly as in the provider's discovery document. The server keeps trying;
nothing needs restarting once the provider answers.

### The sign-in page says the provider's answer could not be accepted

The browser comes back to the sign-in page with "The sign-in provider's answer could not be
accepted. If this keeps happening, ask your administrator to look at the Logbook." The audit
trail has an `auth.sign_in` entry with outcome `failed`, and the log the reason, for example:

```text
level=WARN msg="a sign-in failed" err="exchanging the code: oauth2: \"invalid_client\" \"Invalid client credentials.\""
```

`invalid_client` means the client secret Launchway sends is not the one the provider has. Other
reasons (from the code): `the provider returned no ID token`, `checking the ID token: ...`,
`the ID token's nonce does not match this sign-in`, and
`the ID token has no usable username (claims preferred_username, email, sub)`.

The other messages on that page:

| Message | Means |
|---|---|
| The sign-in took too long or was started in another browser. | more than the allowed time passed, or the browser lost the sign-in cookie; try again |
| The sign-in could not be matched to this browser. | the `state` did not match; try again from the start |
| The sign-in provider did not sign you in. | the provider sent an error back; its reason is in the Logbook |
| Launchway could not record the sign-in. | the database did not accept the session; check `/readyz` |

### Someone signs in but sees nothing

`GET /api/v1/me` shows their groups and roles:

```json
{"admin":false,"auth_mode":"oidc","groups":[],"roles":{},"runners":false,"user":"carol@example.com","via":"session"}
```

Empty `groups` means the provider sent none under `groups_claim`. Fix the claim mapping at the
provider, then sign in again; the Logbook's `auth.sign_in` entries say `groups: ...` or
`no groups`. With Dex, static users get groups only from v2.45 on (v2.41 ignored them). If groups
are there but `roles` is empty, no entry in `auth.access` matches them, or a `user:` binding uses
another form of the name than the `user` shown (Dex, for example, gives the email address).

### Sign-in is off

```json
{"error":"sign-in is off on this server (auth.mode: none)"}
```

`/auth/login`, `/auth/callback` and `/auth/logout` answer `404` with this when `auth.mode` is
`none`. The API token endpoints answer
`{"error":"API tokens need sign-in, which is off on this server (auth.mode: none)"}`.

## Requests are refused

| Status | Message | Means and what to do |
|---|---|---|
| 401 | `sign in first, or send a valid API token` | no session, or the token is unknown, revoked or expired; sign in, or make a new token |
| 403 | `you need the operator role on app to do this` | you may see the Flight (codebase), not change it |
| 403 | `only admins of every project may do this` | The Logbook and everyone's tokens need `admin` from a binding without `projects` |
| 403 | `you have no access to any project` | you have no role anywhere |
| 404 | `not found: project app` or `not found: run run_...` | it does not exist, or you may not see it: the answer is the same on purpose |
| 403 | `this request did not come from Launchway's own pages (the X-Launchway-CSRF header is missing)` | a request with the session cookie that changes something must send `X-Launchway-CSRF: 1` |
| 403 | `this request came from another site (Origin https://evil.example.com)` | the `Origin` is not this server; behind a proxy, check that it passes the `Host` header unchanged, port included |
| 429 | `too many failed attempts to authenticate from this address; try again in 60 seconds` | 20 failed authentications (tokens or webhook secrets) in a minute from one address; wait. Behind a proxy without `trust_proxy: true`, everyone shares the proxy's address |
| 403 | `tokens can only be made from a signed-in browser session, not with another token` | make tokens on the **API tokens** page |
| 400 | `a token's role can be no higher than yours: you are at most operator` | ask for a lower role |
| 400 | `a token must expire in 1 to 365 days` | the most is `auth.tokens.max_days` |
| 500 | `something went wrong on the server; its log has the details` | read the server's log for `msg="request failed"` |

### The command line is refused

```text
launchway: not signed in: run launchway login --server URL, or set LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN
```

Run `launchway login --server https://launchway.example.com` with an API token. Exit code `2`.

```text
launchway: sign in first, or send a valid API token (the token may have expired or been revoked: make a new one and run launchway login again)
```

The token was refused. Exit code `4`.

```text
launchway: http://launchway.example.com:8080 is plain HTTP: the token would cross the network unencrypted. Use https, or --allow-http if you are sure
```

Use the server's `https://` address.

```text
launchway: cannot reach http://127.0.0.1:8080: Get "http://127.0.0.1:8080/api/v1/runs?limit=20": dial tcp 127.0.0.1:8080: connect: connection refused
```

The server is down, or the address is wrong.

```text
launchway: this request did not come from Launchway's own pages (the X-Launchway-CSRF header is missing)
```

Seen when the command line is pointed at a server without sign-in with any `LAUNCHWAY_TOKEN`:
such a server has no API tokens, so the command line cannot be used there. Use the UI, or turn on
sign-in.

## Stations do not take work {#runners-do-not-take-work}

```text
launchway: a server URL (--server, LAUNCHWAY_SERVER or server: in --config) and a token (--token or LAUNCHWAY_RUNNER_TOKEN) are required
```

Give both. The token is in the server's data directory (`runner-token`) or in
`LAUNCHWAY_RUNNER_TOKEN` on the server; the server's start-up block says which.

```text
WARN could not reach the server; retrying err="lease: a valid runner token is required (HTTP 401)" in=1s
```

The server was reached, but the token is wrong. Despite the words "could not reach", retrying will
not help: give the Station the server's current token. The Station keeps retrying, up to every 30
seconds, and its `/readyz` stays `503` with `the server has not answered yet`.

```text
WARN could not reach the server; retrying err="Post \"http://127.0.0.1:8080/api/runner/v1/lease\": dial tcp 127.0.0.1:8080: connect: connection refused" in=1s
```

The server is down or the address is wrong. Through HTTPS with a private CA, give the Station the
CA (for example with `SSL_CERT_FILE`).

```text
launchway: unknown executor foo; use auto, container, podman, docker, host or kubernetes
```

See the [Station configuration reference](../reference/runner-config.md).

A Station that is not seen for a while is shown offline under **Stations**, and the steps it held
are run again elsewhere.

## A pipeline is refused

`launchway validate` lists every problem with its line:

```text
.launchway/pipeline.yaml:5: steps.build.needs: needs "tset", but there is no step with that name
.launchway/pipeline.yaml:7: steps.deploy: say what to run in, for example: image: alpine:3.22, or container: one of the containers above
.launchway/pipeline.yaml:8: steps.deploy.imag: unknown key "imag"; expected one of: kind, description, container, image, run, needs, when, env, secrets, retry, timeout, reports, message, uses, with, for_each, exclude, max_parallel, fail_fast, artifacts, fetch, deployment
.launchway/pipeline.yaml:10: steps.deploy.timeout: timeout must be between 1s and 24h
```

Starting a run of such a commit answers `422` with the same problems:

```json
{"error":"the pipeline has problems; fix them first","problems":[{"line":5,"path":"steps.build.needs","message":"needs \"tset\", but there is no step with that name"}]}
```

A ref that does not exist answers `404`:
`{"error":"not found: no branch or tag called \"nosuchbranch\""}`.

## Secrets

A step that cannot have its secrets fails before it runs, naming the variable, the reference and
the reason. The reason is one of:

```text
this runner has no secret store called "bao"
store "bao" does not allow project "payments-api" (projects: app)
store "bao" does not allow key "other/x" (keys: apps/*/*)
vault secrets need a field, for example field: password
there is no secret at secret/apps/demo/nothing
the secret at secret/apps/demo/registry has no field "token"
permission denied reading secret/other/x
vault answered 400 Bad Request: invalid role name "wrongrole"
vault refused the kubernetes login for role "launchway"
variables starting with LAUNCHWAY_ cannot be read as secrets
the runner has no environment variable "DEMO_PIN"
key "../bao-token" must be a relative file name inside the store's directory
```

For example:

```text
secret REGISTRY_PASSWORD (bao:apps/demo/registry#password): this runner has no secret store called "bao"
```

| Reason | What to do |
|---|---|
| no secret store called | the Station that took the step does not have that store; configure it on every Station that may take the step |
| does not allow Flight or key | widen the store's `projects` or `keys`, if this Flight should read it |
| no secret at, no field | write the secret, or fix `key` or `field` |
| permission denied | the token's policy or the Kubernetes role does not cover the path |
| refused the kubernetes login | the role's bound service account or namespace does not match the Station's; a wrong `auth_path` gave this message too |

A store that does not answer is asked again for 30 seconds, then the step is handed back to be
run again later:

```text
Secret store down is unavailable (vault did not answer: Get "http://127.0.0.1:8200/v1/secret/data/apps/a/b": dial tcp 127.0.0.1:8200: connect: connection refused); asking again in 4s
secret X (down:apps/a/b#f): vault did not answer: Get "http://127.0.0.1:8200/v1/secret/data/apps/a/b": dial tcp 127.0.0.1:8200: connect: connection refused. The step will be run again.
```

Draft and pull-request runs may not read secrets unless the Flight allows it:

```text
not run: this is a draft run, and draft runs of imgapp may not read secrets (the step asks for REGISTRY_PASSWORD). Save the pipeline and run it from its branch, or ask an administrator to set drafts.secrets: true for the project
```

See [Secrets](secrets.md#pull-requests-and-drafts).

## Image steps wait

A run with image steps shows:

```text
This run builds images, so all its steps wait for a runner that can build them, and none has been seen in the last 2 minutes. A runner builds images when its configuration names image builders (images.builders).
```

No connected Station has `images.builders` in its configuration. Configure one; see
[Build and sign an image](../tutorials/build-and-sign-image.md).

## A tool says "No user exists for uid 1000"

The OpenSSH client (and so `git` over ssh), and some other tools and language runtimes, look the current user up in
`/etc/passwd` and stop when there is no entry. The Kubernetes executor runs steps as a numeric user (1000 by default) that
most images do not list. Stations from this version on give that user an entry (`launchway`, home `/home/launchway`)
through an init container in each step image, and say so at the start of each step: `Running as launchway (uid 1000,
gid 1000)`. If your step shows `Running as uid 1000, gid 1000, with no account entry`, the operator set
`kubernetes.user_entry: false`; if it shows nothing like that, the Station is older than this fix: upgrade it. If the step
fails to start and names the init container, see [The step's user](../reference/runner-config.md#the-steps-user).

## Deploy steps fail

```text
deploy-prod: project other may not deploy to prod (see the environment's projects in the server's configuration)
```

The Sortie (run) is refused with `422` before it starts. Add the Flight to the Airfield's `projects`,
if it should deploy there.

```text
no deploy runner serving target prod-cluster took the step within 1m
```

No deploy Station of a pool serving that target is connected. Start one with the pool's token and a
`deploy:` section listing the target; see [Deploy with approval](../tutorials/deploy-with-approval.md).

## A policy blocks a Sortie {#a-policy-blocks-a-run}

```text
the run was refused when it started: blocked by policy: policy pinned-images (enforce), rule step_images: step hello: docker.io/library/alpine:3.22 (not pinned by digest); step publish: docker.io/library/alpine:3.22 (not pinned by digest); needs every step and container image pinned by digest (name@sha256:…). Grant a waiver or change what was found, then resume the run
```

The message names the policy, its mode, the rule, what was found and what would pass. Change the
pipeline, or have someone allowed by `policy.waivers.allow` grant a waiver, then resume. See
[Policy gates](../tutorials/policy-gates.md).

## Webhooks answer 404

```json
{"error":"not found"}
```

Every webhook caller that cannot prove itself gets this `404`, whatever the reason: no such
Flight, no `git_webhook` or `webhooks` entry for it, a trigger the configuration does not list, a
wrong secret or token, or a bad signature. The Logbook (`webhook.git`, `webhook.run`) says
which. Check the Flight name in the URL, the secret's variable on the server, and for a Git host
the `kind` (which header it checks).

## The container image misbehaves

```text
level=WARN msg="polling a project" project=app err="fetching /repos/app: fatal: cannot exec 'git-upload-pack '/repos/app/.git'': No such file or directory\nfatal: unable to fork"
```

The image has no shell, and Git needs one to read a repository from a local path. Use an
`https://` or SSH URL for `repo`. Starting a run of such a Flight answers
`something went wrong on the server; its log has the details`.

Every step fails at once when the server's built-in Station runs in the image:

```text
Could not run the step: fork/exec /bin/sh: no such file or directory
```

In the image the built-in Station picks the `host` executor (`executor=host` in the
`runner started` line), and the image has no shell. Set `runner: { enabled: false }` and connect
[Stations](connect-runner.md) on other machines.

## What next

- [Monitoring](monitoring.md)
- [Install on one server](install-single-server.md)
- [Sign-in and roles](oidc-and-roles.md)
- [Secrets](secrets.md)
- [FAQ](../faq.md)
