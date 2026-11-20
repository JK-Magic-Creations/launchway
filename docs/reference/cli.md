# Command-line reference

This page describes every `launchway` command, subcommand and flag, with defaults, environment variables, exit codes and an example of each. It is for anyone who runs Launchway from a terminal or a script.

> **Verified** on 2026-11-19 against commit f717337: every command was run from a binary built with `make web build`, against a local server on `127.0.0.1` started from a configuration like `examples/local.yaml` and the `make demo` repositories, first without sign-in and then with OpenID Connect sign-in (a test provider) and real API tokens; the help texts and exit codes below are copied from those runs. **Not verified:** the Kubernetes flags (`--k8s-*`), `--images-config`, `--secrets-config`, `--health-listen`, a deploy runner (`runner --config` with a `deploy:` section) and `login` reading a token typed at a terminal.

One binary does everything: the server, runners, local runs and the API client.

```text
launchway server   [--config launchway.yaml]   run the server, its web UI and a built-in runner
launchway runner   --server URL --token T     run a runner that takes work from a server
launchway run      [--file F] [--executor E]  run this directory's pipeline on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     print a pipeline's graph as JSON
launchway login    --server URL               store an API token for a server
launchway runs     start|list|show|logs|cancel  start and follow runs on a server
launchway policy   list|run                   the policies in force; a run's decisions
launchway waivers  list|grant|revoke          waive a policy's rule for a while, with a reason
launchway version
```

`launchway`, `launchway help`, `--help` and `-h` print this summary. Without a command it exits with `2`.

## Flags

Flags are written `--name value`, `--name=value`, `-name value` or `-name=value`; they mean the same. A boolean flag takes no value (`--wait`), or `--wait=false`. For `runs`, `policy` and `waivers`, flags may come before or after the positional arguments. `-h` on `server`, `runner` and `run` prints their flags.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. For `runs start --wait`, the run succeeded. For `run`, the local run succeeded. |
| `1` | The run failed (`runs start --wait`, `run`); or an error such as a configuration file that is not valid, a pipeline file with problems (`validate`, `graph`, `run`), or a runner that cannot start. |
| `2` | The command was written wrongly: an unknown command or flag, a missing argument, or not signed in. |
| `3` | `runs start --wait`: the run was canceled. |
| `4` | `login`, `runs`, `policy`, `waivers`: the server refused the request (including input values that do not fit, each named) or could not be reached. |
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
launchway server [--config FILE] [--listen ADDR] [--data DIR]
```

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `launchway.yaml` | The configuration file. A file that does not exist is not an error: the server starts with the defaults. |
| `--listen` | the configuration's `listen`, else `127.0.0.1:8080` | The address to listen on. |
| `--data` | the configuration's `data`, else `launchway-data` in the current directory | The data directory. |

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
launchway runner --server URL --token TOKEN [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `--server` | `LAUNCHWAY_SERVER`, then `server:` in `--config` | The server URL. Required. |
| `--token` | `LAUNCHWAY_RUNNER_TOKEN` | The runner token, or a runner pool's token. Required. Never read from the file. |
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

Without a server or token it exits with `1`:

```text
launchway: a server URL (--server, LAUNCHWAY_SERVER or server: in --config) and a token (--token or LAUNCHWAY_RUNNER_TOKEN) are required
```

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

The run is recorded as project `local`, event `manual`, with the current Git branch and commit if the directory is a Git repository. Everything it keeps lives in a temporary directory that is removed at the end. `NO_COLOR` turns colour off.

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
launchway validate [FILE]
```

`FILE` defaults to `.launchway/pipeline.yaml`. Steps that use library templates are not expanded: the command reads only the file. To check them, use the editor of a server, or [`POST /api/v1/validate?expand=1`](http-api.md#pipeline-editor).

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

`validate` takes no flags: `launchway validate -h` looks for a file called `-h`.

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
launchway runs list [--project P] [--status S] [--limit N] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--project` | every project you may see | Only this project's runs. |
| `--status` | any | Only runs with this status: `queued`, `running`, `succeeded`, `failed` or `canceled`. |
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

`--json` prints the whole run as the server returns it.

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
launchway runs logs RUN_ID [--step S] [--follow]
```

| Flag | Default | Meaning |
|---|---|---|
| `--step` | every step | Only this step, without the header. A step that does not exist exits with `4`. |
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

A run that has finished exits with `4`: `launchway: the run has already finished`.

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
