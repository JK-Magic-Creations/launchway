# Command-line reference

This page describes every `launchway` command, subcommand and flag, with defaults, environment variables, exit codes and an example of each. It is for anyone who runs Launchway from a terminal or a script.

> **Verified** on 2026-11-19 against commit f717337: every command was run from a binary built with `make web build`, against a local server on `127.0.0.1` started from a configuration like `examples/local.yaml` and the `make demo` repositories, first without sign-in and then with OpenID Connect sign-in (a test provider) and real API tokens; the help texts and exit codes below are copied from those runs. **Not verified:** the Kubernetes flags (`--k8s-*`), `--images-config`, `--secrets-config`, `--health-listen`, a deploy runner (`runner --config` with a `deploy:` section) and `login` reading a token typed at a terminal.

One binary does everything: the server, runners, local runs and the API client.

```text
launchway server   [--config launchway.yaml]   run the server, its web UI and a built-in runner
launchway runner   --server URL --join J      enrol a runner with a join token, then take work
                   [--config runner.yaml] [--health-listen ADDR] [--labels a,b]
launchway runner   --server URL --token T     run a runner with the shared token (no identity)
launchway run      [--file F] [--executor E]  run this directory's pipeline on this machine
launchway validate [file] [--project P]       check a pipeline file
launchway graph    [file]                     print a pipeline's graph as JSON
launchway login    --server URL               store an API token for a server
launchway runs     start|list|show|logs|cancel|lanes|resume|rerun  start, follow and re-run runs on a server
launchway policy   list|run                   the policies in force; a run's decisions
launchway waivers  list|grant|revoke          waive a policy's rule for a while, with a reason
launchway blobs    status|copy                see and move where artifacts are kept
launchway mcp      [--server URL]             the server's MCP tools for agents, on stdin and stdout
launchway deployments options|rollback|review  roll a service back after a deployment succeeded; review emergency rollbacks
launchway verifications list|show|mark        verifications on a server
launchway health-sources | models             what verify and ai steps may name
launchway runners  list|join-token|revoke-token|revoke|rotate  runners, and join tokens to enrol them
launchway step-resources get|set-limits|set-default  the default CPU and memory of steps, and their limits
launchway caches   list|delete|policy         build caches: what is kept, deleting, how much is kept
launchway variables list|effective|set|delete|lock|unlock  variables set at a level; the nearest setting wins
launchway doctor   [database|git|ssh|encryption]  would this installation survive losing its database? is git new enough? is Git over SSH reachable? are its secrets encrypted at rest?
launchway encryption init|status|rotate|rewrap   the root key for encryption at rest
launchway repos    list|get|create|update|delete|deploy-keys|hooks|protected-branches   hosted Git repositories (installation setting forge.hosting)
launchway repos    connection|import|verify|mirror  bring repositories in from other servers, keep them in step
launchway keys     list|add|delete|host             your SSH keys for Git over SSH, and the server's host key fingerprints
launchway version
```

`launchway`, `launchway help`, `--help` and `-h` print this summary. Without a command it exits with `2`.

## Flags

Flags are written `--name value`, `--name=value`, `-name value` or `-name=value`; they mean the same. A boolean flag takes no value (`--wait`), or `--wait=false`. For `runs`, `policy` and `waivers`, flags may come before or after the positional arguments. `-h` or `--help` on any command prints its help and exits `0`, without a server and without signing in.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. For `runs start --wait`, the run succeeded. For `run`, the local run succeeded. |
| `1` | The run failed (`runs start --wait`, `run`); or an error such as a configuration file that is not valid, a pipeline file with problems (`validate`, `graph`, `run`), or a runner that cannot start. |
| `2` | The command was written wrongly: an unknown command or flag, a missing argument, or not signed in. |
| `3` | `runs start --wait`: the run was canceled. |
| `4` | `login`, `config` and the commands that take its verbs, `runs`, `policy`, `waivers`, `mcp`, `verifications`, `health-sources`, `models`, `runners`, `step-resources`, `caches`, `repos`: the server refused the request (including input values that do not fit, each named) or could not be reached. |
| `5` | `runs start --wait --timeout`: the time passed before the run finished. The run goes on. |
| `130` | `run`: interrupted twice with Ctrl-C. |

## Credentials

`launchway login` stores a server address and an API token. `runs`, `policy` and `waivers` use them.

| Where | Value |
|---|---|
| `LAUNCHWAY_SERVER` | the server URL; wins over the stored one |
| `LAUNCHWAY_TOKEN` | the API token; wins over the stored one |
| `LAUNCHWAY_CONFIG_DIR` | the directory of `credentials.json`; default `launchway` in your configuration directory (`~/.config/launchway` on Linux) |

The file is written readable by you alone (mode `600`, in a directory of mode `700`). If its permissions are wider, each command warns. Without a server or a token, the commands exit with `2`:

```text
launchway: not signed in: run launchway login --server URL, or set LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN
```

API tokens are made in the web UI, on the **API tokens** page, and start with `lw_`. A server without sign-in has no tokens; see the warning under [launchway runs start](#launchway-runs-start).

## launchway server

Runs the server, its web UI and, unless the configuration turns it off, a built-in runner.

```text
launchway server [--config FILE] [--listen ADDR] [--data DIR] [--ignore-database-config]
```

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `launchway.yaml` | The configuration file. A file that does not exist is not an error: the server starts with the defaults. |
| `--listen` | the configuration's `listen`, else `127.0.0.1:8080` | The address to listen on. |
| `--data` | the configuration's `data`, else `launchway-data` in the current directory | The data directory. |
| `--ignore-database-config` | off (or `LAUNCHWAY_IGNORE_DATABASE_CONFIG=1`) | Recovery. Starts with the bootstrap file only and neither reads nor changes the configuration stored in the database; the configuration API is off. See [Locked out](../howto/backup-restore-upgrade.md#locked-out-or-a-stored-configuration-that-will-not-load). |

| Variable | Meaning |
|---|---|
| `LAUNCHWAY_DATABASE` | The database (SQLite path or `postgres://` URL), instead of `database:`. |
| `LAUNCHWAY_RUNNER_TOKEN` | The runner token, instead of the one kept in the database. |

On start it applies database migrations, then prints where it listens:

```sh
launchway server --config launchway.yaml
```

```text
  Launchway f717337 is listening on http://127.0.0.1:8080 (no sign-in: anyone who can reach it has full access)
  2 project(s), data in /home/dana/launchway-data, SQLite database /home/dana/launchway-data/launchway.db
  Runner token for other machines: /home/dana/launchway-data/runner-token
```

A server without sign-in refuses to listen on an address other than a loopback one, and exits with `1`:

```text
launchway: refusing to listen on 0.0.0.0:8080 without sign-in.
```

On `SIGINT` or `SIGTERM` it stops gracefully: `/readyz` answers `503` at once, it waits `server.drain_delay`, stops the built-in runner, and gives requests in flight up to 15 seconds. Steps in progress run again when a server starts. See [the server configuration](server-config.md).

> [!WARNING]
> Because a missing configuration file is not an error, a typo in `--config` starts a server with the defaults (no projects, data in `./launchway-data`). Check the project count in the start-up message.

## launchway runner

Runs a runner on another machine. It connects out to the server; nothing connects to it.

```text
launchway runner --server URL --join JOIN_TOKEN [flags]
launchway runner --server URL --token TOKEN [flags]
```

With `--join`, the runner enrols once: it makes a key that never leaves it (`<data>/identity/key`), registers the public half, and gets a runner ID from the server. From then on it starts with neither `--join` nor `--token`, using its identity; the join token is used up. With `--token` it uses the shared runner token (or a deploy pool's token) and has no identity of its own. See [Runners on other machines and clusters](../guide/runner-identity.md).

| Flag | Default | Meaning |
|---|---|---|
| `--server` | `LAUNCHWAY_SERVER`, then `server:` in `--config` | The server URL. Required. |
| `--token` | `LAUNCHWAY_RUNNER_TOKEN` | The shared runner token, or a runner pool's token. Never read from the file. Give this or `--join`, not both; neither is needed once the runner is enrolled. |
| `--join` | `LAUNCHWAY_JOIN` | A join token, to enrol this runner once and give it an identity of its own. Never read from the file. |
| `--labels` | `labels:` in the file | Labels this runner has, separated by commas, such as `arm64,gpu`. |
| `--config` | none | A YAML file of runner settings: the same keys as `runner:` in the server configuration, plus `server`, `name` and `data`. Unknown keys are refused. With a `deploy:` section the runner is a deploy runner. |
| `--name` | `name:` in the file, else the host name | The runner's name. Its ID is `runner-<name>`. |
| `--data` | `data:` in the file, else `launchway-runner` | The directory for workspaces. |
| `--concurrency` | `concurrency:` in the file, else `2` | Steps to run at once. |
| `--executor` | `executor:` in the file, else `auto` | `auto`, `container`, `podman`, `docker`, `host` or `kubernetes`. `auto` uses Podman or Docker if installed, otherwise the host. |
| `--k8s-context` | the kubeconfig's current context | Context for the `kubernetes` executor. |
| `--k8s-namespace` | the context's namespace | Namespace for step pods. |
| `--k8s-unrestricted` | off | Run step pods' containers as their images' own users (often root), without the restricted security context. The same as `kubernetes.restricted: false`. |
| `--secrets-config` | none | A YAML file of secret stores, the same as `runner.secrets` in the server configuration. Replaces `secrets:` from `--config`. |
| `--health-listen` | none | An address, such as `:8081`, to serve `/healthz` (the process is up) and `/readyz` (the server answered within the last two minutes) on. |

A flag wins over the environment, which wins over the file, which wins over the default.

```sh
export LAUNCHWAY_RUNNER_TOKEN="$(cat runner-token)"
launchway runner --server https://launchway.example.com --name build-2 --concurrency 4
```

```text
INFO runner started name=build-2 executor="container (podman)" concurrency=4 server=https://launchway.example.com
```

Without a server it exits with `1`: `launchway: a server URL (--server, LAUNCHWAY_SERVER or server: in --config) is required`. Without a join token or a token, and not yet enrolled, it exits with `1` too: `launchway: a join token (--join or LAUNCHWAY_JOIN) to enrol this runner, or a shared token (--token or LAUNCHWAY_RUNNER_TOKEN), is required`. Giving both is refused.

A wrong token does not stop the runner: it logs `could not reach the server; retrying err="lease: a valid runner token is required (HTTP 401)"` and tries again, waiting up to 30 seconds between tries. On `SIGINT` or `SIGTERM` it tells the server it is leaving and exits with `0`. See [Connect a runner](../howto/connect-runner.md) and [the runner configuration](runner-config.md).

## launchway run

Runs the pipeline of the current directory on this machine, without a server, and prints its steps as they run. Approval steps ask on the terminal (`Approve? [y/N]`); without a terminal they are rejected.

```text
launchway run [--file F] [--executor E] [-i name=value ...] [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `--file` | `.launchway/pipeline.yaml` | The pipeline file. |
| `--executor` | `auto` | `auto`, `container`, `podman`, `docker`, `host` or `kubernetes`. |
| `-i` | none | An input value, `name=value`. Give it once for each input; the others take their defaults. |
| `--secrets-config` | none | A YAML file of secret stores. An `env` store, which reads your own environment, is always added. |
| `--images-config` | none | A file with an `images:` section (builders and registries), as in a runner's configuration. Required if a step builds an image. |
| `--k8s-context`, `--k8s-namespace`, `--k8s-unrestricted` | | As for `launchway runner`. |

The run is recorded as project `local`, event `manual`, with the current Git branch and commit if the directory is a Git repository. Everything it keeps lives in a temporary directory that is removed at the end. `NO_COLOR` turns color off.

```sh
launchway run -i target=prod
```

```text
▶ env-demo  · 2 steps · container (podman)
/home/dana/env-demo (main)

first           │ Runner this machine, executor container (podman)
first           │ ✓ succeeded in 200ms
show[os=alpine] │ LAUNCHWAY_INPUT_TARGET=prod
…
  ✓ first            succeeded in 200ms
  ✓ show[os=alpine]  succeeded in 200ms

Succeeded in 400ms
```

It exits with `0` if the run succeeded and `1` otherwise. The first Ctrl-C cancels the run; a second exits at once with `130`. See [Environment variables](environment-variables.md#variables-a-step-receives) for what each step receives.

## launchway validate

Checks a pipeline file and prints a summary.

```text
launchway validate [FILE] [--project NAME] [--library NAME=DIRECTORY ...] [--resolved]
```

`FILE` defaults to `.launchway/pipeline.yaml`. By default steps that use library templates are not expanded: the command reads only the file. To check them without a server, give each library's directory with `--library` (below); to check them against a server, use `--project`, the editor of a server, or [`POST /api/v1/validate?expand=1`](http-api.md#pipeline-editor).

With `--library NAME=DIRECTORY`, once for each library the pipeline uses, the templates are expanded from those directories, with no server and no network. `NAME` is what the pipeline's `libraries:` section calls the library; `DIRECTORY` is the library's root, the directory that holds `templates/`. It is the same parse, binding and expansion a server runs when it checks a pipeline (a test compares the two on the example library), so the same problems come out, each with the file and line of the template it is in. Which libraries and environments a server allows, and its limits, are the server's to say: add `--project` against a server for that. `--resolved` prints the resolved pipeline (every step of every template, with its needs, containers and environment) as JSON, the graph `POST /api/v1/validate` returns; it needs `--library` for each library the pipeline uses. The library's own version is not looked at: the directory is what is read, so check out the tag you mean.

```sh
launchway validate .launchway/pipeline.yaml --library platform=../pipeline-library
launchway validate .launchway/pipeline.yaml --library platform=../pipeline-library --resolved
```

With `--project NAME` the server you are signed in to checks the file for that project as well, as its editor does: the libraries templates use, the limits the installation sets (such as `max_run_timeout`), and what the project must have for the pipeline to work. It warns, for example, that a pipeline that filters pull requests by their target branch (`on.pull_request.into`) will run none in a project with no forge connection. Warnings are printed to standard error and do not change the exit code; problems do.

```sh
launchway validate
```

```text
.launchway/pipeline.yaml: pipeline "env-demo" is valid, 2 step(s) in 2 level(s)
```

Problems are printed one per line with the file, line and path, and the command exits with `1`:

```text
bad.yaml:3: steps.a: say what to run in, for example: image: alpine:3.22, or container: one of the containers above
bad.yaml:4: steps.b.needs: needs "c", but there is no step with that name
```

`launchway validate` also accepts a file name after the flags.

## launchway graph

Prints the pipeline as parsed, its steps, dependencies and settings, as JSON.

```text
launchway graph [FILE]
```

```sh
launchway graph | head -8
```

```text
{
  "name": "env-demo",
  "on": {},
  "steps": [
    {
      "id": "first",
      "image": "docker.io/library/alpine:3.22",
      "run": "echo \"version=1.2.3\" >> \"$LAUNCHWAY_OUTPUTS\"",
```

A file with problems is reported as for `validate`, with exit code `1`.

## launchway login

Checks an API token with the server and stores it with the server's address.

```text
launchway login --server URL [--allow-http]
```

| Flag | Default | Meaning |
|---|---|---|
| `--server` | `LAUNCHWAY_SERVER` | The server's URL, `https://…` or `http://…`. |
| `--allow-http` | off | Allow a plain `http://` server that is not on this machine (`localhost` and loopback addresses are always allowed). |

The token comes from `LAUNCHWAY_TOKEN`; otherwise it is read from the terminal without echo, or from the first line of standard input.

```sh
launchway login --server https://launchway.example.com
```

```text
API token (make one in the web UI under API tokens):
Signed in to https://launchway.example.com as dana. The token is kept in /home/dana/.config/launchway/credentials.json.
```

| Problem | Message | Exit |
|---|---|---|
| no or bad `--server` | `give the server's address: launchway login --server https://launchway.example.com` | `2` |
| `http://` to another machine | `… is plain HTTP: the token would cross the network unencrypted. Use https, or --allow-http if you are sure` | `2` |
| not a token | `that is not a Launchway API token (they start with lw_)` | `2` |
| the server refuses it | `sign in first, or send a valid API token (the token may have expired or been revoked: make a new one and run launchway login again)` | `4` |

## launchway runs

Starts and follows runs on a server. `launchway runs` alone prints a summary of the subcommands and exits with `2`. An unknown subcommand exits with `2`.

### launchway runs start

Starts a run of a project.

```text
launchway runs start PROJECT [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--ref` | the project's default branch | The branch or tag to run. |
| `-i` | none | An input value, `name=value`. Once for each input; a list input as `name=a,b,c`. The same name twice is refused. |
| `--wait` | off | Wait for the run to finish, printing each step as its status changes, and exit with its outcome. |
| `--timeout` | none | With `--wait`, stop waiting after this long, such as `10m`. The run goes on. |
| `--json` | off | Print the run as JSON. |

```sh
launchway runs start demo-app --ref main -i scan=quick
```

```text
Started run #1 of demo-app: https://launchway.example.com/runs/run_06gzde02yts1v325jvp0
```

With `--wait`, progress goes to standard error and the outcome to standard output:

```text
Started run #3 of demo-app: https://launchway.example.com/runs/run_06gzde1rb0s24hb652q0
  leased     lint
  succeeded  lint
  …
  failed     smoke-test
  skipped    approve-dev
Run #3 failed
```

The exit code is `0` (succeeded), `1` (failed), `3` (canceled) or `5` (`--timeout` passed: `launchway: run #2 is still running after 5s; it goes on`). Input values that do not fit are named, with exit code `4`:

```text
launchway: the run's inputs have problems:
  scan: choose one of "quick", "full"
```

> [!WARNING]
> On a server without sign-in, `runs start`, `runs cancel`, `waivers grant` and `waivers revoke` fail with `launchway: this request did not come from Launchway's own pages (the X-Launchway-CSRF header is missing)` and exit code `4`: the command line does not send the header such a server requires. The reading commands work. Use the web UI, or the HTTP API with the header, on such a server.

### launchway runs list

Lists runs, newest first.

```text
launchway runs list [--project P] [--status S] [--lane NAME] [--limit N] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--project` | every project you may see | Only this project's runs. |
| `--status` | any | Only runs with this status: `queued`, `running`, `succeeded`, `failed` or `canceled`. |
| `--lane` | any | Only runs in the [lane](pipeline.md#lane) of this name. |
| `--limit` | `20` | At most this many (the server allows up to 500). |
| `--json` | off | Print JSON. |

```sh
launchway runs list --project demo-app --limit 3
```

```text
run_06gzde34x0b6151zjapg demo-app         #4     canceled   manual       main           2026-11-19 20:10:58
run_06gzde1rb0s24hb652q0 demo-app         #3     failed     manual       main           2026-11-19 20:10:30
run_06gzde0f5gjz0scwmhz0 demo-app         #2     failed     manual       main           2026-11-19 20:10:20
```

The columns are the run ID, project, number, status, event, branch or tag, and when it was created.

### launchway runs show

Shows a run and its steps.

```text
launchway runs show RUN_ID [--json]
```

`--json` prints the whole run as the server returns it. Without it the steps are listed after the run's **stages**: the steps
of a stage do not wait for each other, so a stage with several runs them at the same time (`stage 2 (runs together): test
(running), security-go (running)`).

```sh
launchway runs show run_06gzde02yts1v325jvp0
```

```text
Run #1 of demo-app: running
  main at fa0b04cb0a, manual by dana, 2026-11-19 20:10:16
  input alpine = ["3.21","3.22"]
  input scan = quick
  https://launchway.example.com/runs/run_06gzde02yts1v325jvp0

Steps:
  succeeded  lint
  succeeded  unit-tests
  running    build
  pending    smoke-test
```

### launchway runs logs

Prints a run's logs, step by step in the pipeline's order, each step under a `── <step> (attempt N)` header.

```text
launchway runs logs RUN_ID [--step S] [--service NAME] [--follow]
```

| Flag | Default | Meaning |
|---|---|---|
| `--step` | every step | Only this step, without the header. A step that does not exist exits with `4`. |
| `--service` | off | Print this service container's output (the lines of the stream `service` whose text starts with the service's name) instead of the steps' own. |
| `--follow` | off | Keep printing new lines until the run finishes. |

```sh
launchway runs logs run_06gzde1rb0s24hb652q0 --step smoke-test
```

```text
Runner build-1, executor container (podman)
…
GET /healthz → 503 Service Unavailable
Failed with exit code 1 after 1.3s
```

### launchway runs cancel

Asks a run to cancel.

```text
launchway runs cancel RUN_ID
```

```sh
launchway runs cancel run_06gzde34x0b6151zjapg
```

```text
Asked run run_06gzde34x0b6151zjapg to cancel.
```

A run that has finished exits with `4`: `launchway: the run has already finished`. A run waiting for its [lane](pipeline.md#lane) is canceled like any other, and leaves the line.

### launchway runs resume

Continues a failed or canceled run: the same run goes on, running again its failed and canceled steps and keeping what succeeded.

```text
launchway runs resume RUN_ID [--wait] [--timeout D] [--json]
```

With `--wait` it follows the run and exits as [`runs start`](#launchway-runs-start) does: `0` if it succeeded, `1` if it failed, `3` if it was canceled, `5` if `--timeout` passed first. A run that is not failed or canceled, or that opened a deployment, is refused with `4`. The same operation is `POST /api/v1/runs/{id}/resume`.

### launchway runs rerun

Makes a new run of a finished one, from steps you name or from the ones that failed; every other step is reused.

```text
launchway runs rerun RUN_ID [--from STEP]... [--failed] [--plan] [--wait] [--timeout D] [--json]
```

| Flag | Meaning |
|---|---|
| `--from` | A step to start from; give it once for each step. The steps after any of them run again |
| `--failed` | The smallest set of steps that covers every failed and canceled step |
| `--plan` | Print what would run and what would be reused, or why it cannot be done (exit `4`), and make no run |
| `--wait`, `--timeout` | As for `runs start`, with the same exit codes |
| `--json` | Print JSON |

```text
launchway runs rerun run_06gzde1rb0s24hb652q0 --failed --plan
Re-running from lint, unit would run again:
  run    lint
  run    unit
  run    deploy
  reuse  build
```

The same operation is `POST /api/v1/runs/{id}/rerun`, and the MCP tool `retry_run` with `from_steps` or `failed_only`. See [Re-running](../guide/approvals-and-reruns.md#re-running-from-a-step-and-what-each-step-was-given) for what a re-run reuses and how it differs from a resume.

### launchway runs lanes

Lists a project's [lanes](pipeline.md#lane) that have a run going or runs waiting.

```text
launchway runs lanes PROJECT [--json]
```

```text
release/ref main: held by run #41 (running)
  1. run #42 waits
  2. run #43 waits
```

`launchway runs show` prints a run's own place in its lane (`lane release/ref main: waiting, number 1 in line, behind run #41`). The same data is `GET /api/v1/projects/{name}/lanes` in the [HTTP API](http-api.md) and the MCP tool `list_lanes`.

### launchway runs queue

Lists the runs held back by [limits on concurrent runs](../guide/queues.md), in the order they will go, with why each waits; or moves one.

```text
launchway runs queue [PROJECT] [--json]
launchway runs queue next|hold|release RUN_ID
```

`next` puts the run at the front (and lets it go now if the limits have room), `hold` keeps it waiting until released, `release` lets the limits decide again. The same is `GET /api/v1/queue` and `POST /api/v1/runs/{id}/queue` in the [HTTP API](http-api.md) and the MCP tools `list_queue` and `queue_run`.

### launchway runs trust

Approves a restricted run, a pull request from an automation account: *Run with secrets*, recorded with your name. See [Pull-request trust](../guide/pull-request-trust.md).

```text
launchway runs trust RUN_ID [--comment TEXT]
```

The same is `POST /api/v1/runs/{id}/trust` and the MCP tool `trust_run`.

## launchway policy

Shows the policies in force and a run's policy decisions. See [Policy rules](policy-rules.md). `launchway policy` alone prints a summary and exits with `2`.

### launchway policy list

```text
launchway policy list [--json]
```

Lists each policy source, its policies with their mode, rules and scope, and the most recent verdicts of runs you may see.

```text
Source config (the server's configuration), version sha256:f374978e187f
  demo-shape               report   rules: forbid_secrets, max_timeout, step_images
                           scope: projects=demo-app

Recent verdicts:
  demo-shape               fail    demo-app #4 (run) 2026-11-19 20:10
```

A source whose policies cannot be loaded shows `FAILING CLOSED:` and the reason.

### launchway policy run

```text
launchway policy run RUN_ID [--json]
```

Prints each decision of the run, policy by policy and rule by rule: what was found, what a failing rule needs, and any waiver that passed it.

```text
2026-11-19 20:10:30  run start: WARN
  demo-shape (report, config ): fail
    step_images        fail   container tools: docker.io/library/alpine:3.22 (not pinned by digest); …
                              needs every step and container image pinned by digest (name@sha256:…)
    max_timeout        pass   every timeout is within 2h
    forbid_secrets     pass   this is a manual run
```

## launchway waivers

Lists, grants and revokes waivers of policy rules. `launchway waivers` alone prints a summary and exits with `2`.

### launchway waivers list

```text
launchway waivers list [--project P] [--active] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--project` | every project you may see | Only this project's waivers. |
| `--active` | off | Only waivers in force. |
| `--json` | off | Print JSON. |

```text
wvr_06gzde4tsd2z3aje3wh0  active   demo-shape/step_images for demo-app, by dana until 2026-11-19 22:10: docs check
```

### launchway waivers grant

```text
launchway waivers grant PROJECT --policy P --rule R --reason "..." --expires D [--run RUN] [--environment E] [--finding F] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--policy` | required | The policy. |
| `--rule` | required | The rule of the policy. |
| `--reason` | required | Why. It is recorded and shown wherever the verdict is. |
| `--expires` | required | How long it lasts, such as `72h`. Rounded up to whole hours; at most `policy.waivers.max_days`. |
| `--run` | none | Only for this run. |
| `--environment` | none | Only for this environment (deploy rules). |
| `--finding` | none | Only for this finding's fingerprint (the `findings` rule). |
| `--json` | off | Print the waiver as JSON. |

```sh
launchway waivers grant demo-app --policy demo-shape --rule step_images --reason "docs check" --expires 2h
```

```text
Granted wvr_06gzde4tsd2z3aje3wh0  active   demo-shape/step_images for demo-app, by dana until 2026-11-19 22:10: docs check
```

A missing required flag prints the usage line and exits with `2`. Granting needs the role `policy.waivers.allow` names (admin by default); otherwise the server refuses with exit code `4`: `you need the admin role on demo-app to waive its policies (policy.waivers.allow)`.

### launchway waivers revoke

```text
launchway waivers revoke PROJECT WAIVER_ID [--reason "..."]
```

```sh
launchway waivers revoke demo-app wvr_06gzde4tsd2z3aje3wh0 --reason "done"
```

```text
Revoked wvr_06gzde4tsd2z3aje3wh0  revoked  demo-shape/step_images for demo-app, by dana until 2026-11-19 22:10: docs check
```

## launchway blobs

Shows and moves where artifacts are kept. It works on the database and the stores directly, not through the server, so it reads the same `--config` as the server (and `LAUNCHWAY_DATABASE`). See [Where artifacts are kept](../artifacts.md).

### launchway blobs status

```text
launchway blobs status [--config launchway.yaml]
```

Says which stores the database records artifacts in, and how much each holds.

### launchway blobs copy

```text
launchway blobs copy --from STORE --to STORE [--config launchway.yaml] [--from-dir DIR] [--to-dir DIR] [--s3 s3.yaml] [--delete-source] [--dry-run] [--json]
```

Copies every stored artifact file not yet recorded in the target store from the source store to the target, checking each file's SHA-256 as it is read and again after it is written, and records it there. Servers may keep running. It is safe to stop and run again: it carries on where it was, and picks up what servers still on the old store kept meanwhile.

| Flag | Meaning |
|---|---|
| `--from`, `--to` | `database`, `filesystem` or `s3`. The database is the one in the configuration file; the filesystem is `--from-dir` or `--to-dir`, else `artifacts.dir`, else `<data>/artifacts`; S3 is the settings in `--s3 FILE` (the same fields as `artifacts.s3`), else `artifacts.s3` |
| `--delete-source` | Delete each file from the source once it is recorded in the target. Only once no server uses the source |
| `--dry-run` | List what would be copied |
| `--json` | Print the report as JSON |

S3 credentials come from `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or the `_FILE` forms). To move with servers running: copy, set `artifacts.store` to the new store and roll the servers, copy again for what servers still on the old store kept, and optionally copy with `--delete-source` once no server uses the old one.

## launchway mcp

```text
launchway mcp [--server URL]
```

Serves the server's MCP tools on standard input and output, for agents that start MCP servers as commands. It uses the server and token `launchway login` stored, or `LAUNCHWAY_SERVER` and `LAUNCHWAY_TOKEN` (a server without sign-in needs no token). Nothing runs on this machine: every message goes to the server's `/mcp` endpoint, which decides what the token may do. See [Agents (MCP)](../guide/agents.md).

## launchway verifications

Shows and marks the verifications of [verify steps](../guide/verification.md). Like `runs`, these use the stored credentials.

```text
launchway verifications list [--environment E] [--project P] [--service S] [--limit N] [--json]
launchway verifications show RUN_ID [--json]
launchway verifications mark RUN_ID STEP passed|failed --reason TEXT
```

`list` shows recent verifications (default `--limit 20`). `show` prints a run's verifications interval by interval: the verdict, each metric and log query's outcome and reason, the top log clusters, and a model's explanation (marked as untrusted text) if one was asked. `mark` marks a running or paused verification passed or failed; `--reason` is required and is kept in the Logbook (`verification.override`). On a protected environment the person who started the run may not mark it passed.

## launchway deployments

Rolls a service back after its deployment succeeded, and reviews emergency rollbacks. It uses the stored credentials, like `runs`. The server decides what is possible and who may, and its sentences are printed as they are. See [rollback after success](../guide/rollback-after-success.md).

```text
launchway deployments options  --env E [--project P] [--service S] [--json]
launchway deployments rollback --env E [--project P] [--service S] [--to DEPLOYMENT|VERSION] --reason TEXT [--queue] [--dry-run]
launchway deployments review list [--json]
launchway deployments review RUN_ID --comment TEXT
```

`options` says what the service can be rolled back to: the earlier successful deployments, newest first, each with what it would revert and the consequence, or why nothing can be done and what would help. `rollback` prints the same, then asks for the rollback; the previous successful deployment is the default, and `--to` takes a deployment ID or a version. `--reason` is required and recorded. `--queue` waits behind a deployment that holds the environment's lock; without it the command is refused, naming that run. `--dry-run` only prints what it would do. `--project` may be left out when one service is deployed to the environment. `review list` shows emergency rollbacks of protected environments that no approver has acknowledged, and whether you may; `review RUN_ID --comment TEXT` acknowledges one (`--comment` is required).

The same operations are the MCP tools `get_rollback_options`, `rollback_deployment`, `list_rollback_reviews` and `review_rollback`, and the routes in the [HTTP API](http-api.md).

## launchway health-sources and launchway models

```text
launchway health-sources [--json]
launchway models [--json]
```

`health-sources` lists the health sources your projects may name in verify steps, by name, type and projects. `models` lists the AI models your projects may name, with their limits and today's use. Neither shows where a source or model is or how it is reached, and nothing else if none is configured.

## launchway runners

Lists runners and manages their enrolment. Making join tokens, revoking and rotating need the role `runner_admin` names (`admin` by default) on every project. See [Runners on other machines and clusters](../guide/runner-identity.md).

```text
launchway runners list [--json]
launchway runners join-token --pool NAME [--uses N] [--ttl 1h] [--labels a,b] [--ephemeral] [--description TEXT] [--json]
launchway runners revoke-token ID
launchway runners revoke RUNNER [--reason TEXT]
launchway runners rotate RUNNER
```

| Command | What it does |
|---|---|
| `list` | Each runner, whether it is online, offline or revoked, its pool, whether it is enrolled or uses a shared or pool token (no identity), its version and labels, and what it is running |
| `join-token` | Makes a join token for a pool and prints it once, with the command to run on the runner's machine. `--uses` (default `1`) is how many runners may enrol with it, `--ttl` (default `1h`, at most `168h`) how long it may be used, `--ephemeral` revokes each runner when it leaves. The pool defaults to `default` |
| `revoke-token` | Stops a join token from enrolling runners |
| `revoke` | Revokes an enrolled runner at once on every server; the steps it held are queued again. `--reason` is for the Logbook |
| `rotate` | Asks an enrolled runner to move to a new key the next time it gets an access token |

## launchway config

Reads and changes the [configuration objects](config-objects.md) kept in the database: projects, libraries, environments, runner pools, role bindings, policies, policy sources, health sources, AI models, secret stores, step resources, units, and the installation's settings. Every command is a call of the [HTTP API](http-api.md#configuration) and needs the admin role on every project; changes are recorded in the object's history and the Logbook.

```text
launchway config overview [--json]
launchway config list KIND [--deleted] [--json]
launchway config get KIND NAME [--json]
launchway config set KIND NAME -f SPEC.yaml [--unit U] [--version N] [--comment TEXT] [--manager terraform]
launchway config create KIND NAME -f SPEC.yaml
launchway config delete KIND NAME [--version N] [--comment TEXT] [--force-in-use]
launchway config restore KIND NAME
launchway config restore --offline --revision N [--config FILE]
launchway config history [KIND [NAME]] [--import ID] [--limit N] [--json]
launchway config validate -f CHANGES.yaml
launchway config apply -f CHANGES.yaml
launchway config export [-o FILE]
launchway config import FILE [--prune] [--apply]
launchway config imports [--json]
launchway config effective PROJECT [--json]
launchway config check-repository URL [--branch B] [--json]
launchway config check-signin --issuer URL --client-id ID --redirect-url URL --secret-env VAR [--admin-group G] [--admin-user U] [--json]
```

KIND is `projects`, `libraries`, `environments`, `runner-pools`, `role-bindings`, `policies`, `policy-sources`, `health-sources`, `models`, `secret-stores`, `step-resources`, `units`, `installation` or `organisation` (fixed names, whatever the installation calls its levels; `organization` is accepted too). Each kind except the last three also has its own command: `launchway projects`, `launchway libraries`, `launchway environments`, `launchway policies`, `launchway runner-pools`, `launchway role-bindings`, `launchway policy-sources`, `launchway secret-stores`, `launchway units`, and `launchway health-sources` and `launchway models` (which still list when given no verb) take the same verbs without KIND: `launchway projects set shop -f shop.yaml`. A spec is the object's settings with the keys the configuration file uses (see the kinds' pages in [Configuration objects](config-objects.md)); `-` reads standard input.

| Command | What it does |
|---|---|
| `overview` | The configuration revision this server runs with, what the levels are called here, and each kind with a sentence on what it is |
| `list`, `get` | Reads objects. `get` prints the spec as YAML, with the version, who manages it and who changed it last |
| `set`, `create` | `set` creates or replaces an object (the whole spec replaces the old one); `create` fails if it exists. `--version N` refuses the change if the object has moved on, so two people do not overwrite each other. The same checks as the configuration file are applied and every problem is listed |
| `delete`, `restore` | Deletes an object, which is kept and can be brought back. Refused if something depends on it |
| `history` | Who changed what and when, with the fields that changed |
| `validate`, `apply` | `CHANGES.yaml` is a list of `{kind, name, unit, spec, action (put, delete or restore), version}`; `validate` checks them, `apply` makes them all or none |
| `export`, `import` | `export` writes every object as configuration text for backup, review or Git. `import FILE` shows what importing would create, change and (with `--prune`) delete; `--apply` makes exactly those changes, recorded as one import |
| `check-repository`, `check-signin` | The checks the setup wizards use: read a repository the way the server would, and check sign-in answers against the identity provider and print the `auth:` block for the configuration file |

The same operations are the MCP tools `list_config`, `get_config`, `put_config`, `delete_config`, `restore_config`, `get_config_history`, `validate_config`, `apply_config`, `get_effective_config`, `export_config`, `import_config`, `list_config_imports`, `check_repository` and `check_signin`; see [Interface parity](interface-parity.md) for the table that ties every API operation to its tool and command.

## launchway step-resources

Shows and changes the installation's [step resources](pipeline.md#resources): the default CPU and memory a step gets when it writes none, for each kind of step, and the most any step may ask for. Reading needs only sign-in. Changing needs the admin role on every project and is recorded in the Logbook (`step_resources.set_limits`, `step_resources.set_default`). The values live in the database, so a change applies to every server at once, to the next pod a run starts.

```text
launchway step-resources get [--json]
launchway step-resources set-limits [--max-cpu Q] [--max-memory Q] [--memory-per-thread Q] [--json]
launchway step-resources set-default KIND [--cpu Q] [--cpu-limit Q] [--memory Q] [--memory-limit Q] [--json]
```

| Command | What it does |
|---|---|
| `get` | Prints each kind's default (`step` is the only kind) and the limits. A value that is not set is shown as `not set` |
| `set-limits` | Changes `max_cpu` and `max_memory` (the most a step may ask for) and `memory_per_thread` (default `1Gi`, at least `64Mi`). A flag you give sets that value, and `--max-memory ""` clears it; flags you leave out keep their value. Refused if a default is above the new maximum |
| `set-default` | Changes the default of a kind in the same way. Refused for a quantity that does not read, a limit below what is promised, or a value above the limits |

Quantities are written as in a pipeline: `500m` or `2` for CPU, `512Mi` or `6Gi` for memory. The same operations are `GET /api/v1/step-resources` and the routes under it in the [HTTP API](http-api.md#step-resources), and the MCP tools `get_step_resources`, `set_step_resource_limits` and `set_step_resource_default`.

## launchway run-limits

Shows and changes the installation's limits on pipelines: `max_run_timeout`, the longest top-level [`timeout`](pipeline.md#top-level-keys) a pipeline may ask for. Reading needs only sign-in. Changing needs the admin role on every project and is recorded in the Logbook (`run_limits.set`). The values live in the database, so a change applies to every server at once.

```text
launchway run-limits get [--json]
launchway run-limits set --max-run-timeout D [--json]
```

| Command | What it does |
|---|---|
| `get` | Prints the longest run timeout, or `not set` |
| `set` | Changes it: a duration from `1m` to `720h`, such as `48h`; `--max-run-timeout ""` removes the limit. A pipeline whose timeout is longer is refused when a run starts and by validation in the editor and the MCP tool |

The same operations are `GET` and `PUT /api/v1/run-limits` in the [HTTP API](http-api.md#run-limits), and the MCP tools `get_run_limits` and `set_run_limits`.

## launchway caches

Shows a project's build caches (the `cache:` key of a step), deletes entries, and shows and changes the policy that says how much is kept. Listing needs the rights to read the project, deleting those to operate it (it is recorded in the Logbook as `cache.delete`), and the policy is an administrator's (`cache_policy.set`, `cache_policy.set_project`, `cache_policy.clear_project`). The policy is in the database: an installation's default and, optionally, an override for one project.

```text
launchway caches list PROJECT [--json]
launchway caches delete PROJECT --name N | --id ID [--json]
launchway caches policy get [PROJECT] [--json]
launchway caches policy set [PROJECT] [--enabled=false] [--max-project-gib N] [--max-entry-gib N] [--idle-days N] [--max-age-days N] [--json]
launchway caches policy clear PROJECT [--json]
```

| Command | What it does |
|---|---|
| `list` | Prints each entry (its ID, name, the start of its key, scope, platform, size, how often and when it was last restored, and the run that wrote it) and the total against the project's quota |
| `delete` | Ends one entry by `--id`, or every entry of a cache by `--name` (all scopes and keys). The next run that wanted it starts without it and saves it again |
| `policy get` | Prints the installation's policy, or the one that applies to a project |
| `policy set` | Changes `enabled`, `max_project_gib` (default 10), `max_entry_gib` (default 2), `idle_days` (default 7) and `max_age_days` (default 7: how old an entry may be before a restore treats it as missing); a flag you leave out keeps its value. With a project, it sets that project's own policy |
| `policy clear` | Removes a project's own policy, so the installation's applies again |

The same operations are the routes under [Build caches](http-api.md#build-caches) in the HTTP API, and the MCP tools `list_caches`, `delete_cache`, `get_cache_policy` and `set_cache_policy`.

## launchway variables

Sets, shows, locks and unlocks variables at a level, and shows what is in effect for a project. The nearest setting wins: what is given when a run starts, then the pipeline file's own `with:`, then the project, the Squadron, the Wing, the Base, then the template's own default; at the same level a variable set for the environment being deployed to beats the general one; and a lock set at a level beats everything below it. Setting needs an operator of the project, or an administrator of the Squadron, Wing or installation; locking needs the level's administrator. Each change is audited (`variable.set`, `variable.delete`, `variable.lock`, `variable.unlock`).

```text
launchway variables list --installation|--organization|--unit U|--project P [--environment E] [--json]
launchway variables effective PROJECT [--environment E] [--json]
launchway variables run RUN [--json]
launchway variables set NAME LEVEL --type T --value V [--of KIND] [--environment E] [--description D] [--json-value J]
launchway variables delete NAME LEVEL [--environment E]
launchway variables lock NAME LEVEL --reason R [--environment E]
launchway variables unlock NAME LEVEL [--environment E]
```

The same operations are the routes under [Variables](http-api.md#variables) in the HTTP API, and the MCP tools `list_variables`, `get_effective_variables`, `get_run_variables`, `set_variable`, `delete_variable`, `lock_variable` and `unlock_variable`.

## launchway doctor

Checks an installation and says what it finds in plain words. With no section it runs both, git first. It reads the same `--config` as the server (and `LAUNCHWAY_DATABASE`) and opens the database directly, not through a server.

### launchway doctor git

```text
launchway doctor git [--json]
```

Names the git installed on this machine and says whether Git hosting can be turned on with it: `Git: 2.47.3; Git hosting can be turned on (it needs 2.38 or newer)`. With an older git, or none, it says what is needed instead; turning on `forge.hosting` is refused with the same words, and everything else keeps working. `--json` prints `version`, `hosting_minimum` and `hosting` (`ok`, `too old` or `no git`).

### launchway doctor ssh

```text
launchway doctor ssh [--address HOST:PORT] [--config launchway.yaml]
```

Connects to the Git over SSH address (without signing in) and prints the host keys it presents, as SHA256 fingerprints, for ed25519, ECDSA and RSA. With no `--address` it reads `forge.ssh.public_host` and the port from the configuration file's forge section; it reports "off" when `forge.ssh.listen` is not set there. It exits with status 1 when nothing answers as an SSH server (check the Service or load balancer, a firewall, and that servers were restarted after `forge.ssh.listen` was set) or when some host key cannot be read.

### launchway doctor database

```text
launchway doctor [database] [--config launchway.yaml] [--json]
```

Says whether this installation would survive the loss of its database, and what would be lost. The first line is the verdict: `highly available` (a writable PostgreSQL primary with a synchronous standby streaming), `at risk` (standbys but none synchronous, or the database is a standby or read-only), `not highly available` (one database), or `unknown` (PostgreSQL whose replication the database user may not see; it needs the `pg_monitor` or `pg_read_all_stats` role). For one database it says so:

```text
Database availability: not highly available
  Database: SQLite file /var/lib/launchway/launchway.db.
  Not highly available: a failure of this machine or its disk stops Launchway until the file is restored.
  If the file is lost, everything since the last backup is lost; Launchway cannot see backups of the file, so the last backup is not known to it.
  Riding through an outage: a request waits up to 2m0s for the database (resilience.stall_budget), then is answered 503 with Retry-After; runners keep running steps and keep their logs and results on disk.
```

On PostgreSQL it also says whether WAL archiving is on and when the last segment was archived. `--json` prints the state and the assessment.

### launchway doctor encryption

```text
launchway doctor encryption [--restore-drill] [--config launchway.yaml] [--json]
```

Says whether the secrets this installation stores are encrypted at rest, reading only: which holder keeps the root key (never the key), the key versions and the age of the oldest, how many stored secrets are sealed and how many are still in the clear, the progress of sealing, any addresses that carry a password, and **whether a database dump alone could reveal a secret** (yes only when a root key is in use and nothing is left in the clear). It names what is not yet encrypted (artifacts, logs, caches, Git packs) and that disk or volume encryption cannot be seen from here. Exit status 1 when a dump alone could reveal a secret, so a script can gate on it. A database that is encrypted, with no key configured, or with a key that does not open it, is refused with the plain message the server gives. `--restore-drill` also opens every sealed secret with the configured key, reading only, and exits 1 if one does not open: run it against a restored copy to prove the dump and the key belong together. The plain `launchway doctor` prints the one-sentence state. See [Encryption at rest](../guide/encryption-at-rest.md).

### launchway doctor network

```text
launchway doctor network [--namespace launchway] [--kubeconfig FILE] [--context NAME]
```

Says whether the cluster already has a system deciding network policy in the installation's namespace: Kubernetes NetworkPolicies that are not Launchway's own, or objects of any kind whose name contains `NetworkPolicy` from a non-core API group (a network plugin's own policy objects, found by listing the cluster's API). If so it says to let one system be the authority and leave the chart's policies off (`networkPolicy.enabled: false`); `launchway install` makes the same check and defaults to that. It reads the cluster with your kubeconfig, so it runs where the installer runs, not inside the server pod. See [Upgrading](../guide/upgrading.md#network-policies).

## launchway encryption

```text
launchway encryption init   [--key-file PATH] [--config launchway.yaml]
launchway encryption init   --passphrase [--keystore PATH] [--passphrase-file F] [--config launchway.yaml]
launchway encryption status [--json]
launchway encryption rotate [--data] [--config launchway.yaml] [--json]
launchway encryption rewrap [--config launchway.yaml]
```

The root key for [encryption at rest](../guide/encryption-at-rest.md). `init`, `rotate` and `rewrap` run on a machine that has the root key (the same configuration file and environment as the server) and open the database directly, so they work while servers run; `status` asks a server and needs `launchway login` as an administrator.

`init` makes a key file (32 random bytes in base64, mode `0600`; the default path is `encryption.key_file`, else `<data>/root.key`), or with `--passphrase` a keystore whose passphrase comes from `--passphrase-file` or is asked twice on the terminal. It never overwrites a file, never prints the key, and says what to do next: keep a copy somewhere other than the database's backups, and tell the servers where it is.

`rotate` makes a new key-encryption key and re-wraps every data key under it; `--data` also makes new data keys and re-seals every stored value under them. Online: no restart, nothing blocked. `rewrap` replaces the root key: configure the new key as the root key and the old one as `encryption.previous_key_file`, run it, restart the servers with the new key alone. Both are recorded in the Logbook (`encryption.kek_rotated`, `encryption.dek_rotated`, `encryption.rewrap`).

The same report as `status` is `GET /api/v1/encryption` in the [HTTP API](http-api.md) and the MCP tool `get_encryption_status`.

## launchway repos

Lists, makes, changes and deletes the Git repositories this server hosts. Git hosting must be on (the Base setting `forge.hosting`, in Configuration under Base settings; off by default); otherwise every command answers `feature_off` and nothing is lost. A repository is `OWNER/NAME`, and who may do what comes from the same role bindings as a project's, matched on that path. Making one is an administrator's; changing or deleting one is its administrator's. Changes are recorded in the Logbook (`repository.create`, `repository.update`, `repository.delete`).

```text
launchway repos list [--json]
launchway repos get OWNER/NAME [--json]
launchway repos create NAME [--owner O] [--branch B] [--visibility private|internal|public] [--json]
launchway repos update OWNER/NAME [--visibility V] [--branch B] [--archived=false] [--rename-to OWNER/NAME] [--json]
launchway repos delete OWNER/NAME
```

The same operations are the routes under [Repositories](http-api.md#repositories-git-hosting) in the HTTP API, and the MCP tools `list_repositories`, `get_repository`, `create_repository`, `update_repository` and `delete_repository`. A Terraform resource `launchway_repository` is planned.

## launchway repos mirror

Mirrors of hosted repositories (the server needs `forge.hosting`). A **pull mirror** keeps a hosted repository an exact copy of another server's repository: every branch, tag and note, deletions followed, on a schedule and on demand; meanwhile it takes no pushes. A **push mirror** copies every accepted update to another server. Setting one up needs the admin role on the repository, running one now the operator role; both are recorded in the Logbook (`mirror.create`, `mirror.sync`, `mirror.promote`).

```text
launchway repos mirror status [OWNER/NAME] [--behind] [--json]
launchway repos mirror add OWNER/NAME --url URL [--direction pull|push] [--username U]
                  [--credential-env NAME | --credential-file PATH] [--interval 1h] [--no-prune]
launchway repos mirror sync OWNER/NAME [--id mir_...]
launchway repos mirror pause|resume OWNER/NAME --id mir_...
launchway repos mirror remove OWNER/NAME --id mir_...
launchway repos mirror stop OWNER/NAME [--final-sync]
```

`status` lists each mirror with its state (`ok`, `error`, `pending`, `paused`, `running`), its lag, the number of refs and the last error, with credentials removed; `--behind` lists only those that have fallen behind. `add --direction pull` needs a repository with no branches or tags of its own, because mirroring replaces them. `sync` runs the pull mirror (or the one named with `--id`) now and exits with `4` if the run failed, printing why.

`stop` is the cut-over step: the repository stops being a mirror, the mirror is deleted and pushes are accepted. With `--final-sync` the mirror runs once more first, and nothing changes if that run fails. A pull mirror whose source suddenly lists no branches or tags refuses to delete everything; stop mirroring if the other server really was emptied.

A credential is a **reference**: `--credential-env` names an environment variable of the server and `--credential-file` a file on the server; the token itself is never given to the command, stored in the repository's record, shown or logged. An address is `https` or `http`, without a user name or password in it; an internal address (loopback, private, link-local, cloud metadata) is refused unless the server's `forge.mirror_allow` lists it. The same operations are the routes under `/api/v1/repositories/{owner}/{name}/mirrors` in the [HTTP API](http-api.md#mirrors-git-hosting), and the MCP tools `list_mirrors`, `set_mirror`, `sync_mirror` and `stop_mirroring`.

## launchway install

The guided installer. It asks where Launchway should run, a few plain questions with defaults, shows what it
is about to do, and does it. See [Install Launchway in ten minutes](../howto/install-in-ten-minutes.md).

```text
launchway install [--answers FILE] [--set KEY=VALUE ...] [--target this-machine|kubernetes]
                  [--dry-run] [--yes] [--non-interactive] [--transcript FILE] [--no-browser]
                  [--kubeconfig FILE] [--context NAME] [--namespace NAME] [--image REF]
launchway install --uninstall [...]
launchway install bundle --output FILE        (not built yet)
launchway doctor security [--url ADDRESS] [--config launchway.yaml]
```

`--answers` is a YAML file of `key: value`; every question prints its key in the transcript. `--dry-run` shows the
summary and changes nothing. `--yes` skips the last "go ahead?". `--uninstall` states what it removes and what it
keeps: the root key and your data are kept unless you answer, in so many words, to delete the data. The transcript
(`launchway-install.log` by default) records the questions and answers and never a secret. `launchway doctor
security` prints the plain security checklist the installer ends with.

## launchway repos import, verify and connection

Getting existing repositories into Launchway before anything is switched over. Everything here needs the admin role and is recorded in the Logbook.

```text
launchway repos connection add NAME --kind gitea|forgejo|plain [--base-url URL] [--credential-env NAME | --credential-file PATH]
launchway repos connection list
launchway repos connection remove NAME
launchway repos import --connection NAME [--owner O] [--repo O/N] [--target-owner T] [--url ADDRESS ...]
                  [--dry-run] [--once] [--interval 1h] [--username U] [--json]
launchway repos import-all --connection NAME [--owner O] [--target-owner T] [--mirror] [--retries N] [--dry-run] [--json]
launchway repos verify --against NAME|URL [--owner O] [--repo O/N] [--target-owner T] [--url ADDRESS ...] [--refs] [--json]
launchway repos import-archive OWNER/NAME FILE [--json]
launchway repos archive list OWNER/NAME [--state open|closed|all] [--query TEXT] [--page N] [--json]
launchway repos archive show OWNER/NAME NUMBER [--json]
launchway repos commits OWNER/NAME [--ref R] [--path P] [--limit N] [--json]
launchway repos files OWNER/NAME [PATH] [--ref R] [--json]
launchway repos put-file OWNER/NAME PATH [--file F | --text TEXT] --message M [--branch B] [--expect-sha SHA] [--json]
```

A **connection** is a server the repositories are on: its kind (`gitea` or `forgejo`, whose REST API lists them; `plain` for addresses you give with `--url`), its address and a credential **reference** (the name of an environment variable of the server, or a file on the server; never the token). Launchway sends the credential only to the connection's own host, refuses internal addresses unless `forge.mirror_allow` lists them, and does not follow redirects.

A mirror's credential (`--credential-env` or `--credential-file`, with `--username`) is sent as HTTP Basic authentication with the very first request, not after a challenge, so hosts that answer an unauthenticated request with "not found" work; the token is the password, and the user name is whatever the host expects (`x-access-token` for some, any non-empty name for others; default `launchway`). A token in the address itself is refused: the address is stored and shown, the credential never is.

`import-all` is `import` for a bulk move, with the choices a bulk move always makes plain: every repository the connection can see (or those of `--owner`) is copied and checked ref by ref, each becomes an ordinary repository (add `--mirror` to keep each a pull mirror of its source instead), and the repositories that did not finish are tried again `--retries` times (default 2) before the command exits `4` and names them. The setup guide's import step does the same from the browser, with progress for each repository and a retry for the ones that failed.


`import` makes every repository the connection lists (or those of `--owner`, or the one `--repo`) with the same owner, name and visibility, copies every branch, tag and note, and the source's pull request refs (`refs/pull/*`, kept read-only under `refs/imported/pull/` because `refs/pull/` is this server's own), checks the copy ref by ref and prints the result for each. By default each is left a **pull mirror** of its source, so the source stays the primary until you decide. `--once` makes ordinary repositories instead, once each copy is checked. `--dry-run` lists what would be created, what would be left alone and why (including an address Launchway may not contact), and changes nothing. It copies one repository at a time, so a failure of one stops nothing; it exits `4` when any did not finish, and running the same command again resumes: a repository already made as a mirror of the same address is synced and checked again, one that exists in any other state is never touched.

`verify` proves that every repository of the source exists here with exactly the branches, tags, notes and pull request refs at exactly the hashes the source has, and names each ref that differs. It prints a table of counts per namespace (branches, tags, notes, imported pull request refs, and the source's refs in namespaces that are not copied), then the refs that differ, or with `--refs` every ref with its hash at the source and here. `--against` takes a connection's name or the address of one repository. It exits `0` when everything is equal and `1` when not. Run it before a cut-over. The same operations are `POST /api/v1/imports` and `POST /api/v1/imports/verify` in the [HTTP API](http-api.md#importing-repositories-git-hosting), and the MCP tools `list_connections`, `add_connection`, `import_repositories` and `verify_repositories`.

`commits` lists the newest commits of a branch, tag or commit; `files` prints a directory's entries or a file's text; `put-file` makes one commit on an existing branch that adds a file, or with `--expect-sha` (the file's current id) replaces it. Protected branches take it only from the repository's administrators, a mirror or archived repository takes none, and webhooks, push mirrors and pull requests are told as for a push.

`import-archive` stores the issues and comments exported from another server (`scripts/export-issues.sh`, see the [archive format](archive-format.md)) in the repository's read-only archive, 200 issues a request; importing a number again replaces it. `archive list` and `archive show` read it back. It is a record, not an issue tracker.

### Moving a server's repositories, in stages

```text
launchway repos connection add oldgit --kind gitea --base-url https://git.corp.example --credential-env OLDGIT_TOKEN
launchway repos import --connection oldgit --dry-run          # what would be made, what is refused and why
launchway repos import --connection oldgit                    # copy and check each; every one a pull mirror
launchway repos verify --against oldgit                       # exit 0: every ref equal
launchway repos mirror stop acme/shop --final-sync            # per repository, at the cut-over: it becomes the primary
launchway repos mirror add acme/shop --direction push --url https://git.corp.example/acme/shop.git --credential-env OLDGIT_TOKEN
                                                              # optional: keep the old server an exact copy while people move
```

### Deploy keys, protected branches and webhooks

```text
launchway repos deploy-keys list OWNER/NAME [--json]
launchway repos deploy-keys add OWNER/NAME --file KEY.pub [--name N] [--write] [--expires T] [--json]
launchway repos deploy-keys delete OWNER/NAME ID
launchway repos protected-branches OWNER/NAME [--set main,release/*] [--json]
launchway repos hooks list TARGET [--json]
launchway repos hooks add TARGET --url URL --events push,create,delete,repository [--name N] [--secret-file F] [--json]
launchway repos hooks update TARGET ID [--url U] [--events E] [--name N] [--active=false] [--secret-file F] [--json]
launchway repos hooks delete TARGET ID
launchway repos hooks test TARGET ID [--json]
launchway repos hooks deliveries TARGET ID [--json]
launchway repos hooks redeliver TARGET ID DELIVERY [--json]
```

A deploy key opens one repository over SSH, read-only unless `--write` ([Git over SSH](../guide/git-over-ssh.md)). `protected-branches` shows the patterns, or replaces them with `--set` (an empty value removes the protection). For `hooks`, TARGET is `OWNER/NAME` for one repository or `OWNER` for every repository of an owner ([Webhooks and events](../guide/webhooks.md)); the secret is read from a file, never from the command line, and a secret Launchway makes is printed once. The same operations are the MCP tools `list_deploy_keys`, `add_deploy_key`, `delete_deploy_key`, `get_protected_branches`, `set_protected_branches`, `list_webhooks`, `add_webhook`, `update_webhook`, `delete_webhook`, `test_webhook`, `list_webhook_deliveries` and `redeliver_webhook`.

## launchway pr

Pull requests and code review on hosted repositories ([Pull requests and code review](../guide/pull-requests.md)). `N` is a pull request number; `-R OWNER/NAME` names the repository, and without it the repository is the one `origin` points to.

```text
launchway pr list [-R OWNER/NAME] [--state open|closed|merged|all] [--mine] [--review-requested] [--author U] [--label L] [--json]
launchway pr view N [--files] [--commits] [--comments] [--json]
launchway pr create --title T [--body B | --body-file F] [--head BRANCH] [--base BRANCH] [--draft] [--label L] [--reviewer U]
launchway pr checkout N
launchway pr diff N [--name-only] [--file PATH] [--since REVISION] [--full]
launchway pr comment N --body TEXT [--path P --line L [--start-line S]] [--reply-to ID] [--edit ID] [--resolve ID] [--reopen ID] [--delete ID]
launchway pr review N (--approve | --request-changes | --comment) [--body TEXT] [--dismiss ID --reason TEXT] [--request USER,USER | --unrequest USER,USER]
launchway pr merge N [--merge | --squash | --rebase] [--delete-branch] [--message M] [--auto] [--bypass-reason TEXT]
launchway pr close N
launchway pr reopen N
launchway pr edit N [--title T] [--body B] [--ready] [--draft] [--label L,L] [--assignee U,U] [--base BRANCH]
launchway pr revert N
launchway pr apply N COMMENT-ID
launchway pr settings [--allow-merge=B] [--allow-squash=B] [--allow-rebase=B] [--default-method M] [--delete-branch=B] [--dismiss-stale=B]
launchway pr rules [--branch PATTERN --approvals N --approval-role operator|admin --check NAME,NAME --up-to-date --resolved --no-self-approval=B --admin-bypass]
```

`view` prints what blocks the merge in one sentence and the actions you may take; a merge that the rules refuse prints the same sentence and exits non-zero. `checkout` fetches `refs/pull/N/head` and checks it out as the branch `pr/N`. `settings` and `rules` show the repository's merge settings and the merge rules of its protected branches, or set them (repository administrators). The same operations are the MCP tools `list_pull_requests`, `get_pull_request`, `compare_branches`, `create_pull_request`, `update_pull_request`, `merge_pull_request`, `revert_pull_request`, `comment_on_pull_request`, `update_pull_comment`, `review_pull_request`, `get_pull_settings` and `set_pull_settings`.

## launchway keys

Your SSH keys for Git over SSH, and the server's host key fingerprints. Git hosting must be on; the SSH connection itself needs `forge.ssh.listen`.

```text
launchway keys list [--json]
launchway keys add --file KEY.pub [--name N] [--expires 2027-06-30T00:00:00Z] [--json]
launchway keys delete ID
launchway keys host [--json]
```

`add` takes ed25519, ECDSA and RSA keys of at least 3072 bits, and says why when it refuses one. `host` prints the address and the fingerprints to compare with what `ssh` shows the first time you connect. The same operations are the MCP tools `list_ssh_keys`, `add_ssh_key`, `delete_ssh_key` and `get_ssh_info`, and the routes `/api/v1/user/ssh-keys` and `/api/v1/ssh`.

## launchway version

Prints the version. `--version` and `-v` do the same.

```sh
launchway version
```

```text
launchway f717337
```

## What next

- [HTTP API](http-api.md): what the client commands call.
- [Environment variables](environment-variables.md)
- [Server configuration](server-config.md) and [runner configuration](runner-config.md)

### launchway approvals

Lists the approvals waiting for a decision, and approves or rejects one.

```text
launchway approvals list [--project P] [--json]
launchway approvals approve RUN_ID STEP [--comment TEXT]
launchway approvals reject RUN_ID STEP [--comment TEXT]
```

`list` prints the run, step, question, who started it, when it has waited since and expires, and
"1 of 2 approved (ann)" for an approval that asks for several people. The server's refusals (you
started the run, it expired, it is not waiting, you may not decide it) are printed as they are, and
the exit code is `4`. The same operations are `GET /api/v1/approvals` and
`POST /api/v1/runs/{id}/steps/{step}/approve` and `/reject`, and the MCP tools `list_approvals` and
`decide_approval`.

### launchway templates

```text
launchway templates list [LIBRARY] [--version V] [--json]
```

Without a library, the libraries the server lists; with one, its templates and each parameter's type,
default, and `[names a secret]` when a step takes a secret reference's store, key or field from it. The
same data is `GET /api/v1/libraries/{library}/templates` and the MCP tool `list_templates`.
