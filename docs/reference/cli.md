# Command-line reference

This page describes every `launchway` command, subcommand and flag, with defaults, environment variables, exit codes and an example of each. It is for anyone who runs Launchway from a terminal or a script.

> **Verified** on 2026-11-19 against commit f717337: every command was run from a binary built with `make web build`, against a local server on `127.0.0.1` started from a configuration like `examples/local.yaml` and the `make demo` repositories, first without sign-in and then with OpenID Connect sign-in (a test provider) and real API tokens; the help texts and exit codes below are copied from those Sorties (runs). **Not verified:** the Kubernetes flags (`--k8s-*`), `--images-config`, `--secrets-config`, `--health-listen`, a launch Station (`runner --config` with a `deploy:` section) and `login` reading a token typed at a terminal.

One binary does everything: the server, Stations, local runs and the API client.

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
launchway release list|show|next|create|publish|promote|...  releases: version, notes, assets, promotion across Airfields, freezes, the report
launchway rollout  status|tree|manifest|logs|pause|resume|restart|scale|promote|abort ...  look into a delivery and steer it
launchway verifications list|show|mark        verifications on a server
launchway health-sources | models             what verify and ai steps may name
launchway runners  list|join-token|revoke-token|revoke|rotate  runners, and join tokens to enrol them
launchway step-resources get|set-limits|set-default  the default CPU and memory of steps, and their limits
launchway caches   list|delete|policy         build caches: what is kept, deleting, how much is kept
launchway registry status|list|tags|inspect|delete|retention|policy|gc   the built-in image and chart registry
launchway variables list|effective|set|delete|lock|unlock  variables set at a level; the nearest setting wins
launchway doctor   [database|git|ssh|encryption|registry]  would this installation survive losing its database? is git new enough? is Git over SSH reachable? are its secrets encrypted at rest? would the built-in registry work?
launchway encryption init|status|rotate|rewrap   the root key for encryption at rest
launchway repos    list|get|create|update|delete|deploy-keys|hooks|protected-branches   hosted Git repositories (installation setting forge.hosting)
launchway repos    connection|import|verify|mirror  bring repositories in from other servers, keep them in step
launchway keys     list|add|delete|host             your SSH keys for Git over SSH, and the server's host key fingerprints
launchway version
```

`launchway`, `launchway help`, `--help` and `-h` print this summary. Without a command it exits with `2`.

## squadrons

Friendly aliases over the same routes the web pages use. A Squadron's permanent name is `unit` and a Flight's is `project`: `launchway config ... units`, `--unit` and `--project` keep working as before. `launchway units` is an alias of `launchway squadrons`. See [Squadrons and Flights](../guide/squadrons-and-flights.md).

```text
launchway squadrons list [--json]
launchway squadrons create NAME [--display-name TEXT] [--description TEXT]
launchway squadrons show NAME [--json]
launchway squadrons candidates [--query TEXT] [--connection NAME]
launchway squadrons add-flight NAME (--repository PATH | --repo ADDRESS) [--name FLIGHT] [--branch B]
launchway squadrons move FLIGHT... (--to SQUADRON | --unfile) [--preview] [--comment TEXT]
launchway squadrons organize [--apply] [--group NAME=FLIGHT,FLIGHT ...] [--undo FILE]
```

## Flags

Flags are written `--name value`, `--name=value`, `-name value` or `-name=value`; they mean the same. A boolean flag takes no value (`--wait`), or `--wait=false`. For `runs`, `policy` and `waivers`, flags may come before or after the positional arguments. `-h` or `--help` on any command prints its help and exits `0`, without a server and without signing in.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. For `runs start --wait`, the Sortie succeeded. For `run`, the local run succeeded. |
| `1` | The Sortie failed (`runs start --wait`, `run`); or an error such as a configuration file that is not valid, a pipeline file with problems (`validate`, `graph`, `run`), or a Station that cannot start. |
| `2` | The command was written wrongly: an unknown command or flag, a missing argument, or not signed in. |
| `3` | `runs start --wait`: the run was canceled. |
| `4` | `login`, `config` and the commands that take its verbs, `runs`, `policy`, `waivers`, `mcp`, `verifications`, `health-sources`, `models`, `runners`, `step-resources`, `caches`, `registry`, `repos`: the server refused the request (including input values that do not fit, each named) or could not be reached. |
| `5` | `runs start --wait --timeout`: the time passed before the Sortie finished. The Sortie goes on. |
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

Runs the server, its web UI and, unless the configuration turns it off, a built-in Station.

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
| `LAUNCHWAY_RUNNER_TOKEN` | The Station token, instead of the one kept in the database. |

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

On `SIGINT` or `SIGTERM` it stops gracefully: `/readyz` answers `503` at once, it waits `server.drain_delay`, stops the built-in Station, and gives requests in flight up to 15 seconds. Steps in progress run again when a server starts. See [the server configuration](server-config.md).

> [!WARNING]
> Because a missing configuration file is not an error, a typo in `--config` starts a server with the defaults (no Flights (codebase), data in `./launchway-data`). Check the Flight count in the start-up message.

## launchway Station {#launchway-runner}

Runs a Station on another machine. It connects out to the server; nothing connects to it.

```text
launchway runner --server URL --join JOIN_TOKEN [flags]
launchway runner --server URL --token TOKEN [flags]
```

With `--join`, the Station enrols once: it makes a key that never leaves it (`<data>/identity/key`), registers the public half, and gets a Station ID from the server. From then on it starts with neither `--join` nor `--token`, using its identity; the join token is used up. With `--token` it uses the shared Station token (or a deploy pool's token) and has no identity of its own. See [Stations on other machines and clusters](../guide/runner-identity.md).

| Flag | Default | Meaning |
|---|---|---|
| `--server` | `LAUNCHWAY_SERVER`, then `server:` in `--config` | The server URL. Required. |
| `--token` | `LAUNCHWAY_RUNNER_TOKEN` | The shared Station token, or a Ramp's token. Never read from the file. Give this or `--join`, not both; neither is needed once the Station is enrolled. |
| `--join` | `LAUNCHWAY_JOIN` | A join token, to enrol this Station once and give it an identity of its own. Never read from the file. |
| `--labels` | `labels:` in the file | Labels this Station has, separated by commas, such as `arm64,gpu`. |
| `--config` | none | A YAML file of Station settings: the same keys as `runner:` in the server configuration, plus `server`, `name` and `data`. Unknown keys are refused. With a `deploy:` section the Station is a launch Station. |
| `--name` | `name:` in the file, else the host name | The Station's name. Its ID is `runner-<name>`. |
| `--data` | `data:` in the file, else `launchway-runner` | The directory for workspaces. |
| `--concurrency` | `concurrency:` in the file, else `2` | Steps to run at once. |
| `--executor` | `executor:` in the file, else `auto` | `auto`, `container`, `podman`, `docker`, `host` or `kubernetes`. `auto` uses Podman or Docker if installed, otherwise the host. |
| `--k8s-context` | the kubeconfig's current context | Context for the `kubernetes` executor. |
| `--k8s-namespace` | the context's namespace | Namespace for step pods. |
| `--k8s-unrestricted` | off | Run step pods' containers as their images' own users (often root), without the restricted security context. The same as `kubernetes.restricted: false`. |
| `--secrets-config` | none | A YAML file of secret stores, the same as `runner.secrets` in the server configuration. Replaces `secrets:` from `--config`. |
| `--health-listen` | none | An address, such as `:8081`, to serve `/healthz` (the process is up) and `/readyz` (the server answered within the last two minutes) on. |

A flag wins over the Airfield (environment), which wins over the file, which wins over the default.

```sh
export LAUNCHWAY_RUNNER_TOKEN="$(cat runner-token)"
launchway runner --server https://launchway.example.com --name build-2 --concurrency 4
```

```text
INFO runner started name=build-2 executor="container (podman)" concurrency=4 server=https://launchway.example.com
```

Without a server it exits with `1`: `launchway: a server URL (--server, LAUNCHWAY_SERVER or server: in --config) is required`. Without a join token or a token, and not yet enrolled, it exits with `1` too: `launchway: a join token (--join or LAUNCHWAY_JOIN) to enrol this runner, or a shared token (--token or LAUNCHWAY_RUNNER_TOKEN), is required`. Giving both is refused.

A wrong token does not stop the Station: it logs `could not reach the server; retrying err="lease: a valid runner token is required (HTTP 401)"` and tries again, waiting up to 30 seconds between tries. On `SIGINT` or `SIGTERM` it tells the server it is leaving and exits with `0`. See [Connect a Station](../howto/connect-runner.md) and [the Station configuration](runner-config.md).

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
| `--images-config` | none | A file with an `images:` section (builders and Depots (registries)), as in a Station's configuration. Required if a step builds an image. |
| `--k8s-context`, `--k8s-namespace`, `--k8s-unrestricted` | | As for `launchway runner`. |

The run is recorded as Flight `local`, event `manual`, with the current Git branch and commit if the directory is a Git repository. Everything it keeps lives in a temporary directory that is removed at the end. `NO_COLOR` turns color off.

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

It exits with `0` if the Sortie succeeded and `1` otherwise. The first Ctrl-C cancels the Sortie; a second exits at once with `130`. See [Environment variables](environment-variables.md#variables-a-step-receives) for what each step receives.

## launchway validate

Checks a pipeline file and prints a summary.

```text
launchway validate [FILE] [--project NAME] [--library NAME=DIRECTORY ...] [--resolved]
```

`FILE` defaults to `.launchway/pipeline.yaml`. By default steps that use library templates are not expanded: the command reads only the file, except that the [starters that ship with Launchway](../guide/starters.md) (`uses: launchway/go-service`) are always expanded and checked here, with no `--library`. To check them without a server, give each library's directory with `--library` (below); to check them against a server, use `--project`, the editor of a server, or [`POST /api/v1/validate?expand=1`](http-api.md#pipeline-editor).

With `--library NAME=DIRECTORY`, once for each library the pipeline uses, the templates are expanded from those directories, with no server and no network. `NAME` is what the pipeline's `libraries:` section calls the library; `DIRECTORY` is the library's root, the directory that holds `templates/`. It is the same parse, binding and expansion a server runs when it checks a pipeline (a test compares the two on the example library), so the same problems come out, each with the file and line of the template it is in. Which libraries and Airfields a server allows, and its limits, are the server's to say: add `--project` against a server for that. `--resolved` prints the resolved pipeline (every step of every template, with its needs, containers and Airfield) as JSON, the graph `POST /api/v1/validate` returns; it needs `--library` for each library the pipeline uses. The library's own version is not looked at: the directory is what is read, so check out the tag you mean.

```sh
launchway validate .launchway/pipeline.yaml --library platform=../pipeline-library
launchway validate .launchway/pipeline.yaml --library platform=../pipeline-library --resolved
```

With `--project NAME` the server you are signed in to checks the file for that Flight as well, as its editor does: the libraries templates use, the limits the installation sets (such as `max_run_timeout`), and what the Flight must have for the pipeline to work. It warns, for example, that a pipeline that filters pull requests by their target branch (`on.pull_request.into`) will run none in a Flight with no forge connection. Warnings are printed to standard error and do not change the exit code; problems do.

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

## launchway init

Proposes the pipeline file for the repository in the current directory, from what it holds.

```text
launchway init [--write] [--force]
```

It looks at the top level for `go.mod`, `pyproject.toml` (or `requirements.txt`, `uv.lock`, `poetry.lock`), `package.json`, `pom.xml`, `build.gradle`,
`Chart.yaml`, a `Dockerfile` and an `index.html`, chooses a [flight plan](../guide/starters.md), and reads the version the repository's own
files name (`go.mod`, `.python-version`, `.nvmrc`, `pom.xml`). Without `--write` it only prints the file; with it, the file is saved as
`.launchway/pipeline.yaml`. A repository that already has one is left alone unless `--force`. When nothing fits it says what it looked for
and exits with `1`.

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

### launchway graph, the software graph

With one of the words below, `launchway graph` asks the software graph instead (see [the guide](../guide/software-graph.md)).
Every answer is for you alone: nothing you may not read is listed, followed or counted. A node is named `kind:key`
(`repository:team/app`, `package:go:example.com/lib`, `image:registry.example/team/app`, `deployment:prod/shop/api`).
Questions take `--depth`, `--nodes`, `--as-of TIME` (RFC 3339) or `--commit SHA -R OWNER/NAME`, and `--json`; an answer that
stopped short says which limit it reached.

| Command | What it does |
|---|---|
| `launchway graph summary` | what you may read of the graph and when each source last read |
| `launchway graph search TEXT [--kind K]` | find nodes by a part of a name |
| `launchway graph show REF` | one node and the edges directly around it |
| `launchway graph impact REF` | what is affected if it changes: the way to each thing, by owner, with the deployments |
| `launchway graph where REF` | the Airfields and digests where a repository, package, commit or image runs |
| `launchway graph contents REF` | what is in a deployment, image or component: images, packages and versions, commits |
| `launchway graph path FROM TO` | how two nodes are connected |
| `launchway graph order [-R OWNER/NAME]... [--upstream]` | the order repositories are built in; cycles with their path; overrides marked |
| `launchway graph rebuild-set -R OWNER/NAME` | what must be built again after a push to it, in order |
| `launchway graph overrides` | the corrections to the order |
| `launchway graph override add --kind before\|ignore\|pin --from A --to B --reason TEXT [--value V]` | add a correction; a reason is required |
| `launchway graph override remove ID` | take one away |
| `launchway graph needs REF` | what a component or repository needs, declared and inferred |
| `launchway graph placement REF` | which machines and platforms satisfy it, near misses and gaps |
| `launchway graph spec REF... [--headroom PERCENT] [--format json\|yaml]` | a machine specification for components that run together |
| `launchway graph reach REF` | which deployments run a version of a package that has an advisory |
| `launchway graph propose-requirements -R OWNER/NAME --file FILE` | open a pull request that changes `.launchway/requirements.yaml` |
| `launchway graph refresh -R OWNER/NAME` | read one repository again |
| `launchway graph rescan`, `launchway graph rebuild` | administrators: read everything again; clear the graph and read everything again |

```sh
launchway graph order -R team/app --upstream
launchway graph impact package:go:example.com/lib
launchway graph spec repository:team/app --format yaml
```

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

Starts a run of a Flight.

```text
launchway runs start PROJECT [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--ref` | the Flight's default branch | The branch or tag to run. |
| `-i` | none | An input value, `name=value`. Once for each input; a list input as `name=a,b,c`. The same name twice is refused. |
| `--wait` | off | Wait for the run to finish, printing each step as its status changes, and exit with its outcome. |
| `--timeout` | none | With `--wait`, stop waiting after this long, such as `10m`. The Sortie goes on. |
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
| `--project` | every Flight you may see | Only this Flight's runs. |
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

The columns are the Sortie ID, Flight, number, status, event, branch or tag, and when it was created.

### launchway runs show

Shows a Sortie and its steps.

```text
launchway runs show RUN_ID [--json]
```

`--json` prints the whole run as the server returns it. Without it the steps are listed after the Sortie's **stages**: the steps
of a stage do not wait for each other, so a stage with several runs them at the same time (`stage 2 (runs together): test
(running), security-go (running)`).

While a Sortie (run) goes on, a line under the stages gives the time left (`Estimate: about 6 min left; waiting for a decision: approve.`), and each step that has not finished says what it usually takes (`[usually about 2 min]`). A finished Sortie says `Took 8m 03s, 12% faster than usual.` See [Watching a Sortie live](../guide/watching-runs.md).

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

A step that ran also gets a line under it with what it reserved and what it really used, measured
from its container: `reserved 4.0Gi memory, used 1.1Gi memory at most and 1.9 CPU at most`. Where a
Station could not measure (the host executor has no container, or the container offers no cgroup)
the line says `use not measured` and why; a number is never guessed. A container is shared by the
steps of a Sortie that use the same image, so two such steps running at once show the container's use.

### launchway runs logs

Prints a Sortie's logs, step by step in the pipeline's order, each step under a `── <step> (attempt N)` header.

```text
launchway runs logs RUN_ID [STEP] [--step S] [--service NAME] [--follow]
```

| Flag | Default | Meaning |
|---|---|---|
| `--step` | every step | Only this step, without the header. A step that does not exist exits with `4`. |
| `--service` | off | Print this service container's output (the lines of the stream `service` whose text starts with the service's name) instead of the steps' own. |
| `--follow` | off | Keep printing new lines until the Sortie finishes. |
| `--follow` | off | Keep printing new lines until the Sortie finishes, asking every 400 ms. |

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

A Sortie that has finished exits with `4`: `launchway: the run has already finished`. A Sortie waiting for its [lane](pipeline.md#lane) is canceled like any other, and leaves the line.

### launchway runs resume

Continues a failed or canceled run: the same Sortie goes on, running again its failed and canceled steps and keeping what succeeded.

```text
launchway runs resume RUN_ID [--wait] [--timeout D] [--json]
```

With `--wait` it follows the Sortie and exits as [`runs start`](#launchway-runs-start) does: `0` if it succeeded, `1` if it failed, `3` if it was canceled, `5` if `--timeout` passed first. A Sortie that is not failed or canceled, or that opened a deployment, is refused with `4`. The same operation is `POST /api/v1/runs/{id}/resume`.

### launchway runs rerun

Makes a new run of a finished one, from steps you name or from the ones that failed; every other step is reused.

```text
launchway runs rerun RUN_ID [--from STEP]... [--failed] [--plan] [--wait] [--timeout D] [--json]
```

| Flag | Meaning |
|---|---|
| `--from` | A step to start from; give it once for each step. The steps after any of them run again |
| `--failed` | The smallest set of steps that covers every failed and canceled step |
| `--plan` | Print what would run and what would be reused, or why it cannot be done (exit `4`), and make no Sortie |
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

Lists a Flight's [lanes](pipeline.md#lane) that have a Sortie going or runs waiting.

```text
launchway runs lanes PROJECT [--json]
```

```text
release/ref main: held by run #41 (running)
  1. run #42 waits
  2. run #43 waits
```

`launchway runs show` prints a Sortie's own place in its lane (`lane release/ref main: waiting, number 1 in line, behind run #41`). The same data is `GET /api/v1/projects/{name}/lanes` in the [HTTP API](http-api.md) and the MCP tool `list_lanes`.

### launchway runs queue

Lists the Sorties held back by [limits on concurrent Sorties](../guide/queues.md), in the order they will go, with why each waits; or moves one.

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

## launchway debrief and launchway tests

The report of a Sortie's tests, and what the history of a Flight's tests says. See [The Debrief](../guide/debrief.md).

| Command | What it does |
|---|---|
| `launchway debrief show <run-id> [--json]` | The Debrief: summary, change against the default branch, failures with expected against actual, flaky and quarantined tests, coverage |
| `launchway debrief summary <run-id>` | The short Markdown form for a pull request comment |
| `launchway debrief export <run-id> --format html\|pdf\|json\|junit [--out FILE]` | A self-contained report; `--out -` writes to standard output |
| `launchway tests flaky <project> [--limit N] [--json]` | Flaky tests with score, branches and first seen, and a quarantine entry to copy |
| `launchway tests timings <project> [--limit N] [--json]` | Each test's typical duration, slowest first |
| `launchway tests split <project> --shards N [--shard I] [--json]` | A balanced split of the tests; `--shard I` prints the names of one shard, one per line |

## launchway policy

Shows the policies in force and a Sortie's policy decisions. See [Policy rules](policy-rules.md). `launchway policy` alone prints a summary and exits with `2`.

### launchway policy list

```text
launchway policy list [--json]
```

Lists each policy source, its policies with their mode, rules and scope, and the most recent verdicts of Sorties you may see.

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

Prints each decision of the Sortie, policy by policy and rule by rule: what was found, what a failing rule needs, and any waiver that passed it.

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
| `--project` | every Flight you may see | Only this Flight's waivers. |
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
| `--run` | none | Only for this Sortie. |
| `--environment` | none | Only for this Airfield (deploy rules). |
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

Shows and moves where Cargo (artifacts) are kept. It works on the database and the stores directly, not through the server, so it reads the same `--config` as the server (and `LAUNCHWAY_DATABASE`). See [Where Cargo are kept](../artifacts.md).

### launchway blobs status

```text
launchway blobs status [--config launchway.yaml]
```

Says which stores the database records Cargo in, and how much each holds.

### launchway blobs copy

```text
launchway blobs copy --from STORE --to STORE [--config launchway.yaml] [--from-dir DIR] [--to-dir DIR] [--s3 s3.yaml] [--delete-source] [--dry-run] [--json]
```

Copies every stored Cargo file not yet recorded in the target store from the source store to the target, checking each file's SHA-256 as it is read and again after it is written, and records it there. Servers may keep running. It is safe to stop and run again: it carries on where it was, and picks up what servers still on the old store kept meanwhile.

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

`list` shows recent verifications (default `--limit 20`). `show` prints a Sortie's verifications interval by interval: the verdict, each metric and log query's outcome and reason, the top log clusters, and a model's explanation (marked as untrusted text) if one was asked. `mark` marks a running or paused verification passed or failed; `--reason` is required and is kept in the Logbook (`verification.override`). On a protected Airfield the person who started the run may not mark it passed.

## launchway deployments

Rolls a service back after its deployment succeeded, and reviews Aborts (emergency Returns to base (rollbacks)). It uses the stored credentials, like `runs`. The server decides what is possible and who may, and its sentences are printed as they are. See [Return to base (rollback) after success](../guide/rollback-after-success.md).

```text
launchway deployments options  --env E [--project P] [--service S] [--json]
launchway deployments rollback --env E [--project P] [--service S] [--to DEPLOYMENT|VERSION] --reason TEXT [--queue] [--dry-run]
launchway deployments review list [--json]
launchway deployments review RUN_ID --comment TEXT
```

`options` says what the service can be returned to base to: the earlier successful deployments, newest first, each with what it would revert and the consequence, or why nothing can be done and what would help. `rollback` prints the same, then asks for the Return to base; the previous successful deployment is the default, and `--to` takes a deployment ID or a version. `--reason` is required and recorded. `--queue` waits behind a deployment that holds the Airfield's lock; without it the command is refused, naming that run. `--dry-run` only prints what it would do. `--project` may be left out when one service is deployed to the Airfield. `review list` shows Aborts of protected Airfields that no approver has acknowledged, and whether you may; `review RUN_ID --comment TEXT` acknowledges one (`--comment` is required).

The same operations are the MCP tools `get_rollback_options`, `rollback_deployment`, `list_rollback_reviews` and `review_rollback`, and the routes in the [HTTP API](http-api.md).

## launchway release

Releases and release management: making a release, its notes and files, publishing, promoting it through the Airfields, returning to base, checklists, freezes, formations and the report. It uses the stored credentials, like `runs`. The server decides what is possible and who may, and its sentences are printed as they are. See [Making a release](../guide/releases.md), [Promoting a release](../guide/promoting-releases.md), [Release checklists, windows and freezes](../guide/release-checklists.md) and [Measuring delivery](../guide/measuring-delivery.md).

```text
launchway release list [--project P] [--state draft|candidate|published|yanked] [--json]
launchway release show PROJECT VERSION [--json]
launchway release next --project P [--from REF] [--pre rc] [--json]
launchway release notes --project P [--to REF] [--version V]
launchway release create --project P --version V [--from REF | --adopt TAG] [--notes-file F | --generate-notes] [--title T] [--run RUN] [--allow-lower] [--attach-cargo] [--publish [--public]]
launchway release edit PROJECT VERSION [--notes-file F] [--title T] [--regenerate-notes]
launchway release assets add PROJECT VERSION --file PATH [--name N] | --run RUN [--artifact ID --path P] [--images]
launchway release assets remove PROJECT VERSION NAME
launchway release assets get PROJECT VERSION NAME [-o FILE]
launchway release candidate PROJECT VERSION
launchway release publish PROJECT VERSION [--public]
launchway release yank PROJECT VERSION --reason TEXT
launchway release delete PROJECT VERSION
launchway release changelog PROJECT VERSION [--base BRANCH]
launchway release compare PROJECT VERSION-A VERSION-B
launchway release checklist PROJECT VERSION --airfield A [--json]
launchway release clearance PROJECT VERSION --airfield A [--comment TEXT]
launchway release promote PROJECT VERSION --airfield A [--override REASON]
launchway release plan PROJECT VERSION --airfield A --at TIME [--note TEXT]
launchway release return --project P --airfield A --reason TEXT [--to DEPLOYMENT|VERSION] [--queue]
launchway release matrix [--json]
launchway release calendar [--from T] [--to T] [--json]
launchway release report [--project P] [--from T] [--to T] [--html FILE] [--json]
launchway release settings PROJECT [--scheme S] [--tag-prefix P] [--changelog F] [--public=bool]
launchway release installation [--public-downloads=bool]
launchway release gate list PROJECT
launchway release gate set PROJECT --airfield A [--checks] [--notes] [--no-findings] [--soak MINUTES] [--clearances N] [--clearance-role R] [--previous A] [--window-days D] [--window-from HH:MM] [--window-to HH:MM] [--window-zone Z]
launchway release freeze list | create --to TIME --reason TEXT [--from TIME] [--project P] [--airfield A] | cancel ID
launchway release formation list | show NAME | create NAME PROJECT@VERSION ... [--note T] | promote NAME --airfield A [--override R] | advance NAME
```

A published release never changes; `yank` withdraws one with a recorded reason. `--override` is for administrators and only for a window, a soak time or a freeze. The same operations are MCP tools (`list_releases`, `get_release`, `propose_next_release_version`, `create_release`, `publish_release`, `promote_release`, `return_to_base`, `get_release_matrix` and the rest: see the [interface parity](interface-parity.md) table) and routes of the [HTTP API](http-api.md#releases).
## launchway rollout
Looks into a delivery and steers it. It uses the stored credentials. The server decides who may; the tree and manifests need the viewer role, logs and controls the operator role on the Airfield (environment). See [watching and steering a Launch](../guide/watching-and-steering-a-launch.md).
launchway rollout status   RUN DEPLOYMENT [--json]
launchway rollout tree     RUN DEPLOYMENT [--json]
launchway rollout manifest RUN DEPLOYMENT OBJECT
launchway rollout logs     RUN DEPLOYMENT (--pod P | --replica-set R | --role stable|Pathfinder (canary)|preview|active | --job J) [--container C] [--previous] [--tail N] [--follow]
launchway rollout pause|resume|restart RUN DEPLOYMENT --reason TEXT [--object KIND/NS/NAME]
launchway rollout scale    RUN DEPLOYMENT --replicas N --reason TEXT [--object KIND/NS/NAME]
launchway rollout promote|promote-full|abort|retry RUN DEPLOYMENT --reason TEXT
`status` prints the strategy, its steps with the current one marked, the wanted and actual share of real traffic, and which controls are possible now (and why not for the rest); `tree` adds every object with its health, role, replicas and what changed. `logs --follow` reads the live stream; secrets are masked before a line leaves the launch Station (runner). `--reason` is required for every control and is kept in the Logbook; `promote-full` needs one too. The same operations are the MCP tools `get_delivery`, `get_delivery_manifest`, `get_delivery_logs`, `control_delivery` and `control_rollout`.

## launchway health-sources and launchway models

```text
launchway health-sources [--json]
launchway models [--json]
```

`health-sources` lists the health sources your Flights may name in verify steps, by name, type and Flights. `models` lists the AI models your Flights may name, with their limits and today's use. Neither shows where a source or model is or how it is reached, and nothing else if none is configured.

## launchway Stations {#launchway-runners}

Lists Stations and manages their enrolment. Making join tokens, revoking and rotating need the role `runner_admin` names (`admin` by default) on every Flight. See [Stations on other machines and clusters](../guide/runner-identity.md).

```text
launchway runners list [--json]
launchway runners join-token --pool NAME [--uses N] [--ttl 1h] [--labels a,b] [--ephemeral] [--description TEXT] [--json]
launchway runners revoke-token ID
launchway runners revoke RUNNER [--reason TEXT]
launchway runners rotate RUNNER
```

| Command | What it does |
|---|---|
| `list` | Each Station, whether it is online, offline or revoked, its pool, whether it is enrolled or uses a shared or pool token (no identity), its version and labels, and what it is running |
| `join-token` | Makes a join token for a pool and prints it once, with the command to run on the Station's machine. `--uses` (default `1`) is how many Stations may enrol with it, `--ttl` (default `1h`, at most `168h`) how long it may be used, `--ephemeral` revokes each Station when it leaves. The pool defaults to `default` |
| `revoke-token` | Stops a join token from enrolling Stations |
| `revoke` | Revokes an enrolled Station at once on every server; the steps it held are queued again. `--reason` is for the Logbook |
| `rotate` | Asks an enrolled Station to move to a new key the next time it gets an access token |

## launchway config

Reads and changes the [configuration objects](config-objects.md) kept in the database: Flights, libraries, Airfields, Ramps (Station (runner) pools), role bindings, policies, policy sources, health sources, AI models, secret stores, step resources, units, and the installation's settings. Every command is a call of the [HTTP API](http-api.md#configuration) and needs the admin role on every Flight; changes are recorded in the object's history and the Logbook.

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

Shows and changes the installation's [step resources](pipeline.md#resources): the default CPU and memory a step gets when it writes none, for each kind of step, and the most any step may ask for. Reading needs only sign-in. Changing needs the admin role on every Flight and is recorded in the Logbook (`step_resources.set_limits`, `step_resources.set_default`). The values live in the database, so a change applies to every server at once, to the next pod a Sortie starts.

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

Shows and changes the installation's limits on pipelines: `max_run_timeout`, the longest top-level [`timeout`](pipeline.md#top-level-keys) a pipeline may ask for. Reading needs only sign-in. Changing needs the admin role on every Flight and is recorded in the Logbook (`run_limits.set`). The values live in the database, so a change applies to every server at once.

```text
launchway run-limits get [--json]
launchway run-limits set --max-run-timeout D [--json]
```

| Command | What it does |
|---|---|
| `get` | Prints the longest run timeout, or `not set` |
| `set` | Changes it: a duration from `1m` to `720h`, such as `48h`; `--max-run-timeout ""` removes the limit. A pipeline whose timeout is longer is refused when a Sortie starts and by validation in the editor and the MCP tool |

The same operations are `GET` and `PUT /api/v1/run-limits` in the [HTTP API](http-api.md#run-limits), and the MCP tools `get_run_limits` and `set_run_limits`.

## launchway events

The event log: what reached the server and what was decided about it. See [Events](../guide/events.md).

```text
launchway events list [--project P] [--source S] [--decision D] [--verified yes|no] [--ref R] [--limit N] [--json]
launchway events show EVENT [--json]
launchway events replay EVENT
```

`list` prints each event with a line for every decision and its reason. `show` adds the payload as kept. `replay` starts the run the event asked for again, at the same commit and with the same inputs, for operators; only verified events can be replayed, and the new event points back to the first. The same are `GET /api/v1/events`, `GET /api/v1/events/{id}` and `POST /api/v1/events/{id}/replay` in the [HTTP API](http-api.md) and the MCP tools `list_events`, `get_event` and `replay_event`.

## launchway run-triggers

Start a run of one Flight when another Flight's run or launch ends. See [Triggers on results](../guide/result-triggers.md).

```text
launchway run-triggers list PROJECT [--json]
launchway run-triggers create PROJECT --name NAME --on UPSTREAM [--launch] [--when RESULT] [--pipeline NAME] [--filter EXPR] [--ref REF] [--map INPUT=EXPRESSION ...]
launchway run-triggers delete PROJECT NAME
launchway run-triggers graph PROJECT [--create] [--json]
launchway run-triggers allow UPSTREAM DOWNSTREAM [--share-outputs]
launchway run-triggers allowed UPSTREAM [--json]
launchway run-triggers revoke UPSTREAM DOWNSTREAM
```

`graph` shows the Flights the [software graph](../guide/software-graph.md#start-dependents-after-a-push) says Flight depends on, in build order, whether each has allowed it and whether a trigger exists; with `--create` it makes a trigger named `graph-UPSTREAM` for each that has allowed it. `allow`, `allowed` and `revoke` are the upstream Flight's (its operators decide who may trigger on it); `create`, `list` and `delete` are the downstream Flight's. The same are the `run-triggers` and `run-trigger-allows` routes of the [HTTP API](http-api.md) and the MCP tools `list_run_triggers`, `create_run_trigger`, `delete_run_trigger`, `list_run_trigger_allows`, `allow_run_triggers` and `revoke_run_triggers`.

## launchway event-sources

Incoming webhook sources of a Flight. See [Event sources](../guide/event-sources.md).

```text
launchway event-sources list PROJECT [--json]
launchway event-sources create PROJECT --name NAME [--map INPUT=EXPRESSION ...] [--filter EXPR] [--dedupe-key EXPR]
                               [--schema-file FILE] [--ref REF] [--debounce SECONDS | --batch SECONDS] [--max-body BYTES] [--rate PER_MINUTE]
launchway event-sources change PROJECT NAME [the same flags] [--enable=false] [--rotate-secret]
launchway event-sources delete PROJECT NAME
launchway event-sources test PROJECT NAME --body FILE|-
```

`create` prints the signing secret once. `test` judges a body the way a call would be and starts nothing. The same are the `event-sources` routes of the [HTTP API](http-api.md) and the MCP tools `list_event_sources`, `test_event_source` and `delete_event_source` (a source is made on the console or in the web interface, because its secret is shown once).

## launchway caches

Shows a Flight's build caches (the `cache:` key of a step), deletes entries, and shows and changes the policy that says how much is kept. Listing needs the rights to read the Flight, deleting those to operate it (it is recorded in the Logbook as `cache.delete`), and the policy is an administrator's (`cache_policy.set`, `cache_policy.set_project`, `cache_policy.clear_project`). The policy is in the database: an installation's default and, optionally, an override for one Flight.

```text
launchway caches list PROJECT [--json]
launchway caches delete PROJECT --name N | --id ID [--json]
launchway caches policy get [PROJECT] [--json]
launchway caches policy set [PROJECT] [--enabled=false] [--max-project-gib N] [--max-entry-gib N] [--idle-days N] [--max-age-days N] [--json]
launchway caches policy clear PROJECT [--json]
```

| Command | What it does |
|---|---|
| `list` | Prints each entry (its ID, name, the start of its key, scope, platform, size, how often and when it was last restored, and the Sortie that wrote it) and the total against the Flight's quota |
| `delete` | Ends one entry by `--id`, or every entry of a cache by `--name` (all scopes and keys). The next Sortie that wanted it starts without it and saves it again |
| `policy get` | Prints the installation's policy, or the one that applies to a Flight |
| `policy set` | Changes `enabled`, `max_project_gib` (default 10), `max_entry_gib` (default 2), `idle_days` (default 7) and `max_age_days` (default 7: how old an entry may be before a restore treats it as missing); a flag you leave out keeps its value. With a Flight, it sets that Flight's own policy |
| `policy clear` | Removes a Flight's own policy, so the installation's applies again |

The same operations are the routes under [Build caches](http-api.md#build-caches) in the HTTP API, and the MCP tools `list_caches`, `delete_cache`, `get_cache_policy` and `set_cache_policy`.

## launchway Depot {#launchway-registry}

Shows what the built-in OCI registry holds, deletes images, and shows and changes its quota, retention and immutable-tag policy. A repository is `PROJECT/NAME`, the Flight first. Reading needs the viewer role on the Flight; deleting, policy changes and retention runs the admin role of the Flight; garbage collection and the installation's policy an administrator's. Everything that changes something is recorded in the Logbook (`registry.image_delete`, `registry.tag_delete`, `registry.policy`, `registry.retention_run`, `registry.gc_run`). See [the Depot guide](../guide/registry.md).

```text
launchway registry status [--json]
launchway registry list PROJECT|REPOSITORY [--json]
launchway registry tags REPOSITORY [--json]
launchway registry inspect REPOSITORY REFERENCE [--json]
launchway registry delete REPOSITORY REFERENCE [--tag] [--json]
launchway registry retention PROJECT|REPOSITORY [--run] [--json]
launchway registry policy get [PROJECT|REPOSITORY] [--json]
launchway registry policy set [PROJECT|REPOSITORY] [--quota-gib N] [--keep-last N] [--keep-tags P,P] [--untagged-days N] [--immutable-tags P,P] [--pull-from P,P] [--json]
launchway registry gc [--dry-run] [--last] [--json]
```

| Command | What it does |
|---|---|
| `status` | The Depot's address, how to log in, its limits, how many repositories you may see, and what protects blobs at rest (Launchway does not encrypt them in this release) |
| `list` | With a Flight, its repositories with images, tags and bytes, and usage against the quota; with a repository, its images: tags, digest, size, platforms, who pushed it (and which run), signature, SBOM and vulnerability summary, and the pull reference to copy |
| `tags` | The repository's tags and the digest each points at |
| `inspect` | One image or Cargo, by tag or digest, with its manifest |
| `delete` | Deletes an image by tag or digest, with its tags, signatures and SBOMs; `--tag` deletes only the tag. Refused for an immutable tag and for an image a deployment uses. The bytes go at the next collection |
| `retention` | Previews what the retention rules would delete (nothing is deleted); `--run` deletes it |
| `policy get` | The policy set at a level and the effective values after the levels are combined |
| `policy set` | Replaces the policy at a level: the quota, the retention rule (keep the newest N tagged images, keep tags matching patterns, delete untagged images older than D days), the immutable tag patterns and which Flights' runs may pull. Without a Flight it is the installation's |
| `gc` | Collects garbage: blobs and uploads nothing refers to. `--dry-run` lists what would go; `--last` shows the last report |

The same operations are the routes under [Built-in Depot](http-api.md#built-in-registry) in the HTTP API, and the MCP tools `registry_status`, `list_registry_images`, `inspect_registry_image`, `delete_registry_image`, `get_registry_policy`, `set_registry_policy`, `preview_registry_retention`, `run_registry_retention` and `registry_gc`. Pushing and pulling are done with the clients you already have (`docker`, `podman`, `skopeo`, `helm`), with an API token as the password.

## launchway standards

Shows and changes Standing orders: the checks every repository gets, with where each setting comes from. Reading needs the rights to read the Flight (or any Flight, for a level); changing a level needs to administer that level (an admin of the Flight at a Flight). The API refuses a value weaker than a lock above, with the sentence that names the lock.

```text
launchway standards show PROJECT [--json]
launchway standards show --level LEVEL [--scope NAME] [--json]
launchway standards set --level LEVEL [--scope NAME] [KEY=VALUE ...] [--unset KEY] [--reason TEXT] [--lock KEY=REASON] [--unlock KEY] [--json]
launchway standards explain PROJECT KEY
launchway standards coverage [--level LEVEL] [--scope NAME] [--json]
launchway standards profile [--json]
```

| Command | What it does |
|---|---|
| `show` | For a Flight: each check, its state, where the state comes from, what a lock above holds, the gates, license lists, exemptions and notices. With `--level`, the settings that level has |
| `set` | Changes a level's settings: `check.secrets=block`, `gate.fail_on=critical`, `gate.require_fix=false`, `gate.default_branch=block`, `gate.incomplete=fail`, `licenses.deny=GPL-3.0-only,AGPL-3.0`, `licenses.allow=...`, `off=true --reason "..."` (switches Standing orders off for the level; recorded in the Logbook). `--lock` and `--unlock` set and remove locks |
| `explain` | Why a setting has the value it has: every level that sets it, which one won, and any lock |
| `coverage` | Which repositories of a level have which checks in effect, and their open findings |
| `profile` | Every check with its tool, license and default |

## launchway findings

What the checks found. `list` and `show` read the findings the default branch has had; `accept` and `reopen` need the operator role on the Flight; `inspect` shows a Sortie's Inspection.

```text
launchway findings list PROJECT [--status open|fixed|accepted|all] [--severity S,S] [--check ID] [--q TEXT] [--oldest] [--json]
launchway findings show PROJECT FINGERPRINT [--json]
launchway findings accept PROJECT FINGERPRINT --reason TEXT (--expires YYYY-MM-DD | --days N) [--false-positive] [--json]
launchway findings reopen PROJECT FINGERPRINT
launchway findings export PROJECT [--format json|csv|sarif] [--status S] [-o FILE]
launchway findings inspect RUN [--standing S] [--json]
```

| Command | What it does |
|---|---|
| `list`, `show` | The findings, most severe first (`--oldest` for oldest first), with package, fixed version and how to fix; `show` adds the decision about it |
| `accept` | Records an accepted risk (or a false positive) with a reason of at least 10 characters and an end date within a year; it stops counting on that date |
| `reopen` | Ends the decision in force |
| `export` | The findings as JSON, CSV or SARIF 2.1.0 |
| `inspect` | A Sortie's Inspection: the verdict, each check and whether it ran, and the findings as new, existing or accepted |

The same operations are the routes under [Standing orders](http-api.md#standing-orders) in the HTTP API, and the MCP tools `get_standards_profile`, `get_standards`, `get_standards_settings`, `get_standards_coverage`, `list_findings`, `export_findings`, `get_run_inspection`, `accept_finding` and `reopen_finding`. The MCP tools read and accept; they do not change settings.

## launchway variables

Sets, shows, locks and unlocks variables at a level, and shows what is in effect for a Flight. The nearest setting wins: what is given when a Sortie starts, then the pipeline file's own `with:`, then the Flight, the Squadron, the Wing, the Base, then the template's own default; at the same level a variable set for the Airfield being deployed to beats the general one; and a lock set at a level beats everything below it. Setting needs an operator of the Flight, or an administrator of the Squadron, Wing or installation; locking needs the level's administrator. Each change is audited (`variable.set`, `variable.delete`, `variable.lock`, `variable.unlock`).

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

## launchway toggles

Switches (feature toggles): make them, change what they do in an Airfield, read their history, revert, and manage the keys
applications use. See the [guide](../guide/feature-toggles.md). Every change is audited (`toggle.*`) and goes through the
Flight's safeguards: a reason, and in a protected Airfield another person's approval (the command says when a change
waits, and who approves it).

```
launchway toggles list    PROJECT [--json]
launchway toggles get     PROJECT KEY [--json]
launchway toggles create  PROJECT KEY --type boolean|string|number|json [--default V] [--kind K] [--owner O] [--description D] [--expires DATE] [--client-visible] [--reason R]
launchway toggles set     PROJECT KEY ENV (--on | --off | --percent N) [--serve JSON] [--reason R] [--at TIME]
launchway toggles rules   PROJECT KEY ENV -f FILE [--reason R] [--at TIME]
launchway toggles evaluate PROJECT KEY --context JSON
launchway toggles history PROJECT KEY [--environment E] [--json]
launchway toggles revert  PROJECT CHANGE --reason R
launchway toggles waiting PROJECT
launchway toggles approve|reject|withdraw PROJECT CHANGE [--comment C]
launchway toggles timeline PROJECT ENV
launchway toggles revert-since PROJECT ENV --since MS --reason R [--dry-run]
launchway toggles usage   PROJECT KEY
launchway toggles schedules list PROJECT | cancel PROJECT ID
launchway toggles lock|unlock PROJECT KEY [--reason R]
launchway toggles keys list PROJECT | create PROJECT ENV --kind server|client [--name N] [--origin URL]... | rotate PROJECT ID [--overlap-hours N] | revoke PROJECT ID
launchway toggles safeguards get PROJECT [--environment E]
launchway toggles safeguards set PROJECT [--environment E] [--require-reason=BOOL] [--require-approval=BOOL] [--approvals N] [--watch-minutes N] [--lock NAME]... --reason R
launchway toggles export  PROJECT [-o FILE]
launchway toggles plan    PROJECT -f FILE
launchway toggles apply   PROJECT -f FILE --reason R [--origin config:<commit>]
launchway toggles segments list PROJECT | set PROJECT NAME -f FILE [--reason R] | delete PROJECT NAME --reason R
```

`set --on` and `--off` keep the rules the Airfield already has; `--percent N` serves the value to N percent of
keys (for a boolean, true) and the off value to the rest. `rules` replaces the Airfield's whole state from a YAML
file. `--at` makes the change at a time (RFC 3339) instead of now. A key made by `keys create` or `keys rotate` is
printed once. `plan` changes nothing; `apply` never removes what the file leaves out. `deployments rollback`
lists the toggle changes made since the version it restores, and `--revert-toggles` reverts them with it.

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

Says whether the secrets this installation stores are encrypted at rest, reading only: which holder keeps the root key (never the key), the key versions and the age of the oldest, how many stored secrets are sealed and how many are still in the clear, the progress of sealing, any addresses that carry a password, and **whether a database dump alone could reveal a secret** (yes only when a root key is in use and nothing is left in the clear). It names what is not yet encrypted (Cargo, logs, caches, Git packs) and that disk or volume encryption cannot be seen from here. Exit status 1 when a dump alone could reveal a secret, so a script can gate on it. A database that is encrypted, with no key configured, or with a key that does not open it, is refused with the plain message the server gives. `--restore-drill` also opens every sealed secret with the configured key, reading only, and exits 1 if one does not open: run it against a restored copy to prove the dump and the key belong together. The plain `launchway doctor` prints the one-sentence state. See [Encryption at rest](../guide/encryption-at-rest.md).

### launchway doctor Depot {#launchway-doctor-registry}

```text
launchway doctor registry [--config launchway.yaml] [--json]
```

Says whether the built-in Depot would work as configured: whether it is on, which Cargo store holds its blobs, whether sign-in is on (it answers every request with `503` while it is off, because there is no anonymous Depot), whether an address is set, and, plainly, **that Launchway does not encrypt the Depot's blobs itself in this release**, so the volume, database or bucket that holds the Cargo store must be encrypted. Exit status 1 when something stops it working.

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

The root key for [encryption at rest](../guide/encryption-at-rest.md). `init`, `rotate` and `rewrap` run on a machine that has the root key (the same configuration file and Airfield as the server) and open the database directly, so they work while servers run; `status` asks a server and needs `launchway login` as an administrator.

`init` makes a key file (32 random bytes in base64, mode `0600`; the default path is `encryption.key_file`, else `<data>/root.key`), or with `--passphrase` a keystore whose passphrase comes from `--passphrase-file` or is asked twice on the terminal. It never overwrites a file, never prints the key, and says what to do next: keep a copy somewhere other than the database's backups, and tell the servers where it is.

`rotate` makes a new key-encryption key and re-wraps every data key under it; `--data` also makes new data keys and re-seals every stored value under them. Online: no restart, nothing blocked. `rewrap` replaces the root key: configure the new key as the root key and the old one as `encryption.previous_key_file`, run it, restart the servers with the new key alone. Both are recorded in the Logbook (`encryption.kek_rotated`, `encryption.dek_rotated`, `encryption.rewrap`).

The same report as `status` is `GET /api/v1/encryption` in the [HTTP API](http-api.md) and the MCP tool `get_encryption_status`.

## launchway repos

Lists, makes, changes and deletes the Git repositories this server hosts. Git hosting must be on (the Base setting `forge.hosting`, in Configuration under Base settings; off by default); otherwise every command answers `feature_off` and nothing is lost. A repository is `OWNER/NAME`, and who may do what comes from the same role bindings as a Flight's, matched on that path. Making one is an administrator's; changing or deleting one is its administrator's. Changes are recorded in the Logbook (`repository.create`, `repository.update`, `repository.delete`).

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

`status` lists each mirror with its state (`ok`, `error`, `pending`, `paused`, `running`), its lag, the number of refs and the last error, with credentials removed; `--behind` lists only those that have fallen behind. `add --direction pull` needs a repository with no branches or tags of its own, because mirroring replaces them. `sync` runs the pull mirror (or the one named with `--id`) now and exits with `4` if the Sortie failed, printing why.

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
launchway repos delete-file OWNER/NAME PATH --message M --expect-sha SHA [--branch B] [--json]
launchway repos tree OWNER/NAME [PATH] [--ref R] [--json]
launchway repos cat OWNER/NAME PATH [--ref R] [--binary]
launchway repos log OWNER/NAME [--ref R] [--path P] [--limit N] [--json]
launchway repos show OWNER/NAME COMMIT [--patch PATH] [--json]
launchway repos blame OWNER/NAME PATH [--ref R] [--from N] [--to M] [--json]
launchway repos compare OWNER/NAME --head H [--base B] [--json]
launchway repos search OWNER/NAME QUERY [--ref R] [--regex] [--case] [--in content|path|both] [--dir D] [--ext go,ts] [--limit N] [--json]
launchway repos branches OWNER/NAME [--q TEXT] [--json]
launchway repos branches create OWNER/NAME NAME [--from REF]
launchway repos branches delete OWNER/NAME NAME
launchway repos tags OWNER/NAME [--q TEXT] [--json]
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
launchway pr apply N COMMENT-ID...
launchway pr owners N [--json]
launchway pr owners --check FILE
launchway pr queue N [--method M] [--priority P] [--cancel]
launchway pr update-branch N [--rebase]
launchway pr viewed N [--mark PATH,PATH] [--unmark PATH,PATH]
launchway pr inbox [--json]
launchway pr review N --request-group G,G | --unrequest-group G,G
launchway pr settings [--allow-merge=B] [--allow-squash=B] [--allow-rebase=B] [--default-method M] [--delete-branch=B] [--dismiss-stale=B] [--assign-reviewers owners|round_robin|least_loaded|off]
launchway pr rules [--branch PATTERN --approvals N --approval-role operator|admin --check NAME,NAME --up-to-date --resolved --no-self-approval=B --admin-bypass --owner-approval --dismiss-stale --fresh-approval --queue --path-check PATHS=CHECK]
```

`owners` shows who owns each file a pull request changes, from `.launchway/owners` on the target branch, or with `--check` says what is wrong with an owners file on your computer, by line. `apply` with several comment IDs applies the suggestions as one commit. `queue` shows a pull request's place in its branch's merge queue; with `--method` it adds it, with `--cancel` it takes it out. `update-branch` merges the target into the source branch (`--rebase` replays the branch instead and rewrites it). `viewed` lists or changes the files you marked viewed. `inbox` lists the pull requests that wait on you.

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
- [Server configuration](server-config.md) and [Station configuration](runner-config.md)

### launchway approvals

Lists the approvals waiting for a decision, and approves or rejects one.

```text
launchway approvals list [--project P] [--json]
launchway approvals approve RUN_ID STEP [--comment TEXT]
launchway approvals reject RUN_ID STEP [--comment TEXT]
```

`list` prints the Sortie, step, question, who started it, when it has waited since and expires, and
"1 of 2 approved (ann)" for an approval that asks for several people. The server's refusals (you
started the Sortie, it expired, it is not waiting, you may not decide it) are printed as they are, and
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

## launchway work

Work tracking from the command line: items, boards, sprints, charts, settings, views, the inbox, and agents.
Every command calls one route of the [HTTP API](http-api.md#work-tracking) as the signed-in person, or, with
`launchway work agent`, as an agent holding its own `lwa_` token. The guides are
[tracking work](../guide/tracking-work.md), [boards and sprints](../guide/boards-and-sprints.md),
[work and code](../guide/work-and-code.md) and [working with agents](../guide/working-with-agents.md).

Where work lives is a **scope**: `--scope project:OWNER/NAME`, `unit:NAME` (a Squadron), `organization` (the Wing)
or `installation` (the Base). `--repo OWNER/NAME` means `--scope project:OWNER/NAME`. Without either, the
environment variable `LAUNCHWAY_WORK_SCOPE` is used. Every command takes `--json`. An item is named by its
reference, such as `PAY-12`.

| Command | What it does |
|---|---|
| `launchway work items list [--query Q] [--sort S] [--desc] [--limit N] [--offset N] [--parent REF]` | Finds items; the [query language](work-queries.md) |
| `launchway work items show REF` | One item with its links, code, delivery, files and comments |
| `launchway work items create TITLE [--type T] [--body TEXT] [--priority P] [--estimate N] [--parent REF] [--milestone T] [--sprint N] [--label L] [--assignee A] [--criterion TEXT] [--confidential] …` | Creates an item |
| `launchway work items update REF [--title T] [--state S] [--add-label L] [--add-assignee A] [--expected-version N] …` | Changes an item; a field left out keeps its value |
| `launchway work items move REF STATE [--before REF]` | Moves an item to another state |
| `launchway work items rank REF top\|bottom\|before OTHER\|after OTHER` | Reorders the backlog |
| `launchway work items comment REF TEXT`, `comment --edit ID TEXT` | Adds a comment, or edits one you wrote |
| `launchway work items link REF blocks\|relates\|duplicates OTHER`, `unlink LINK_ID` | Links items |
| `launchway work items code-link REF --kind K --code-repo OWNER/NAME --ref X` | Ties a commit, branch, pull request or run to an item |
| `launchway work items by-code --kind K --code-repo OWNER/NAME --ref X` | The items tied to a piece of code |
| `launchway work items history REF`, `attach REF FILE`, `file ID`, `take-over REF` | History, files (10 MiB at most), and taking work from an agent |
| `launchway work board [--query Q] [--lanes L] [--sprint NAME]` | The board of a scope |
| `launchway work milestones list\|create\|update` | Milestones |
| `launchway work sprints list\|show\|create\|update\|start\|complete\|burndown` | Sprints; `complete --carry-to NAME` |
| `launchway work charts velocity\|flow\|times` | The charts |
| `launchway work roadmap`, `release-notes [--since D] [--until D]` | The roadmap rows, and the work finished between two dates |
| `launchway work settings show`, `settings set FILE` | How work is set up at the scope, and replacing it with a [JSON file](work-format.md#settings) |
| `launchway work views list\|save\|delete` | Saved queries |
| `launchway work inbox [--unread] [--read]`, `home` | What happened to work you follow, and what needs you |
| `launchway work agents list\|show\|create\|update\|disable\|enable\|delete\|rotate-token\|actions\|reviews` | Registering and managing agents (`create` prints the token once; `disable` is the Master switch (kill switch)) |
| `launchway work spend`, `autonomy clear-pause --reason TEXT`, `samples list\|complete` | What agents reported spending; letting autonomy resume; merges flagged for a look |
| `launchway work agent me\|queue\|claim\|heartbeat\|hand-back\|request-review\|usage\|pull-review` | The calls an agent makes with its own token |
| `launchway work import --format stories-json FILE [--preview] [--update]`, `--format archive --repository OWNER/NAME [--preview]` | The [imports](work-format.md) |
| `launchway work propose --from FILE [--scope S\|--repo OWNER/NAME]` | Sends an analyzer's [batch of findings](work-format.md#proposals) (`-` reads standard input) |
| `launchway work proposals list [--state S]`, `accept --source S GROUP... [--title-for GROUP=TEXT]`, `dismiss --source S GROUP... --reason TEXT`, `merge --source S GROUP... --into GROUP` | Reviewing [proposed work](../guide/proposals-from-analysis.md) |
| `launchway work health --repo OWNER/NAME` | Checks a hosted repository's basics and proposes what is missing |

The vertical bars in the table stand for "or": write one of the words.
