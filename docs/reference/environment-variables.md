# Environment variables

This page lists every environment variable Launchway reads, and every variable a pipeline step receives. It is for operators who configure servers and runners, and for pipeline authors who write step scripts.

> **Verified** on 2026-11-19 against commit f717337: the step variables were printed by real steps, once with `launchway run` on a scratch repository (container executor, Podman) and once in a draft run on a local server using a library template; the server, runner and command-line variables were checked by running `launchway server`, `launchway runner` and `launchway login`/`launchway runs` with them set. **Not verified:** `LAUNCHWAY_PULL_REQUEST` (needs a pull-request run from a Git host), the S3 credential variables, the Kubernetes executor's copy of `LAUNCHWAY_OUTPUTS`.

## Variables Launchway reads

These are read by the `launchway` binary itself: the server, a runner, or the command line.

| Variable | Read by | What it does |
|---|---|---|
| `LAUNCHWAY_DATABASE` | `launchway server` | The database to use, instead of `database:` in the configuration. A SQLite file path, or a `postgres://` URL. Surrounding white space is ignored. Without it, and without `database:`, the server uses `launchway.db` in its data directory. |
| `LAUNCHWAY_RUNNER_TOKEN` | `launchway server` | The token runners must send. When set, it wins over the token kept in the database, and the start-up banner says `Runner token for other machines: LAUNCHWAY_RUNNER_TOKEN` instead of a file path. |
| `LAUNCHWAY_RUNNER_TOKEN` | `launchway runner` | The runner token, when `--token` is not given. Not together with a join token. |
| `LAUNCHWAY_JOIN` | `launchway runner` | A join token, when `--join` is not given: the runner enrols with it once and gets an identity of its own, then starts without it. In a container, use it instead of `--join`. |
| `LAUNCHWAY_SERVER` | `launchway runner` | The server URL, when `--server` is not given. It wins over `server:` in the `--config` file. |
| `LAUNCHWAY_SERVER` | `launchway login`, `runs`, `policy`, `waivers`, `mcp`, `runners`, `verifications`, `health-sources`, `models` | The server URL. For `login` it is the default of `--server`; for the other commands it replaces the server stored by `login`. |
| `LAUNCHWAY_TOKEN` | `launchway login` | The API token to store. Without it, `login` reads the token from the terminal (hidden) or from standard input. |
| `LAUNCHWAY_TOKEN` | `launchway runs`, `policy`, `waivers`, `mcp`, `runners`, `verifications`, `health-sources`, `models` | The API token, instead of the one stored by `login`. |
| `LAUNCHWAY_CONFIG_DIR` | `launchway login`, `runs`, `policy`, `waivers` | The directory that holds `credentials.json`. The default is `launchway` in your user configuration directory (`~/.config/launchway` on Linux). |
| `NO_COLOR` | `launchway run` | Any value turns off coloured output. Colour is also off when standard output is not a terminal. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | `launchway server` | Credentials of the S3 artifact store. Each may instead be given as a file named by `AWS_ACCESS_KEY_ID_FILE` or `AWS_SECRET_ACCESS_KEY_FILE`; setting both forms of one is refused. |
| `AWS_SESSION_TOKEN` | `launchway server` | An optional session token for the S3 artifact store. |
| `KUBECONFIG` | the Kubernetes executor, deploy runners | Where the Kubernetes client looks for its kubeconfig, as with `kubectl`. |

The order for `launchway runner` is: a flag on the command line, then the environment, then the `--config` file, then the default. The token is never read from the file. See [the command-line reference](cli.md#launchway-runner).

For `launchway runs`, `policy` and `waivers`, the environment wins over what `launchway login` stored. See [the command-line reference](cli.md#credentials).

### Variables your configuration names

Secrets are never written in the configuration file. Instead, a setting names a variable (or a file) that holds the value. The server or runner reads the variable you name:

| Setting | Holds |
|---|---|
| `auth.oidc.client_secret_env` | the OpenID Connect client secret (or `client_secret_file`) |
| `runner_pools[].token_env` | a deploy runner pool's token, at least 32 characters (or `token_file`) |
| `ai.models[].key` and `health_sources.<name>.auth.secret` | not variables: secret references (`store`, `key`, `field`), read from the runner's own secret stores, where an `env` store reads a variable of the runner |
| `projects[].git_webhook.secret_env` | the secret a Git host signs its webhook calls with (or `secret_file`) |
| `projects[].webhooks.<name>.token_env` | the bearer token a webhook trigger's caller must send (or `token_file`) |
| `projects[].webhooks.<name>.hmac_secret_env` | the HMAC secret a webhook trigger's caller signs with (or `hmac_secret_file`) |
| `token_env` of a `vault` secret store | the Vault or OpenBao token the runner logs in with (or `token_file`, or Kubernetes auth) |

A secret store of kind `env` reads the runner's own environment. It refuses any name that starts with `LAUNCHWAY_`, so a pipeline cannot read the runner's token. On `launchway run`, an `env` store is always available, because the environment is your own. See [Secrets](../howto/secrets.md) and [the server configuration](server-config.md).

### Variables only the test suite reads

`make test-postgres`, `test-vault`, `test-s3`, `test-containers`, `test-kubernetes` and `test-argo` set variables that start with `LAUNCHWAY_TEST_` (for example `LAUNCHWAY_TEST_POSTGRES`). Launchway itself never reads them. The `Makefile` describes each one.

## Variables a step receives

Every step runs with the variables below, besides the `env:` of its container and of the step, and the secrets it asks for. The server works out the values when it hands the step to a runner, and records them (apart from secrets) with the `step.leased` event, so you can see later what each attempt was given. See [Run events](events.md#run-events).

You may not set names that start with `LAUNCHWAY_` in `env:` or `secrets:`: the pipeline is refused with `names starting with LAUNCHWAY_ are reserved`.

### Standard values

| Variable | Value |
|---|---|
| `CI` | `true` |
| `LAUNCHWAY` | `true` |
| `LAUNCHWAY_RUN_ID` | the run's ID, such as `run_06gzdcazwx897r7gs6m0` |
| `LAUNCHWAY_RUN_NUMBER` | the run's number within its project: `1`, `2`, … |
| `LAUNCHWAY_PROJECT` | the project's name (`local` for `launchway run`) |
| `LAUNCHWAY_PIPELINE` | the `pipeline:` name in the pipeline file |
| `LAUNCHWAY_STEP` | the step's ID; for a looped step it includes the member, such as `show[os=alpine]`; for a step from a template it includes the using step, such as `vars/show` |
| `LAUNCHWAY_ATTEMPT` | the attempt number, starting at `1`; a retry or a resume adds one |
| `LAUNCHWAY_EVENT` | what started the run: `push`, `pull_request`, `manual`, `schedule`, `webhook` or `draft` |
| `LAUNCHWAY_REF` | the full Git ref, such as `refs/heads/main` or `refs/tags/v1.2.0`; empty if there is none |
| `LAUNCHWAY_BRANCH` | the branch name, or empty for a tag |
| `LAUNCHWAY_TAG` | the tag name, or empty for a branch |
| `LAUNCHWAY_COMMIT` | the full commit ID the run checks out |
| `LAUNCHWAY_COMMIT_TIME` | the commit's committer time in UTC, RFC 3339 with a Z, such as `2026-11-20T06:12:56Z`; not set when the run has no commit |
| `SOURCE_DATE_EPOCH` | the same time as Unix seconds, for reproducible builds; not set when the run has no commit |
| `LAUNCHWAY_PULL_REQUEST` | the pull request's number; only in runs whose event is `pull_request` |
| `LAUNCHWAY_PULL_REQUEST_INTO` | the branch the pull request targets, as the project's forge said; only in pull-request runs of a pipeline whose `on.pull_request` has `into` |
| `LAUNCHWAY_OUTPUTS` | the file the step appends `key=value` lines to, to give outputs to the steps that need it (see below) |

`LAUNCHWAY_OUTPUTS` is `/launchway/outputs` in a container step. With the host executor it is a path in the runner's data directory; with the Kubernetes executor it is a path in the pod. Always use the variable, not a fixed path.

### Values from other steps and from inputs and loops

| Variable | Value |
|---|---|
| `LAUNCHWAY_NEEDS_<STEP>_<KEY>` | the output `<KEY>` of a step this step `needs`. A template-using step that this step needs exposes the template's `outputs` the same way, under the using step's name |
| `LAUNCHWAY_INPUT_<NAME>` | the value of the run input `<NAME>`; a list input is a JSON list, such as `["3.21","3.22"]` |
| `LAUNCHWAY_EACH_<KEY>` | in a step with `for_each`, this member's value of the loop key `<KEY>` |

### Values in steps from a library template

| Variable | Value |
|---|---|
| `LAUNCHWAY_LIB` | the directory of the library the step's template came from, inside the workspace: `/workspace/.launchway/lib/<library>` in a container |
| `LAUNCHWAY_LIB_<LIBRARY>` | the directory of each library the run uses, by the name the pipeline gives it under `libraries:` |
| `PARAM_<NAME>` | the value of the template parameter `<NAME>`, from the using step's `with:` or the parameter's default |

Inside a template, `env:` and `secrets:` may not use names that start with `PARAM_`.

### How names are formed

`<STEP>`, `<KEY>`, `<NAME>` and `<LIBRARY>` are upper-cased, and every character other than a letter or digit becomes `_`. So the output `image` of the step `unit-tests` is `LAUNCHWAY_NEEDS_UNIT_TESTS_IMAGE`, and the library `ci-templates` is `LAUNCHWAY_LIB_CI_TEMPLATES`. For a looped step, the `]` of a member's ID is dropped first. Two template parameters that would become the same variable are refused when the template is checked; for other names, choose names that stay distinct (`a-b` and `a_b` are the same variable). Library names have no underscores for this reason.

### Which value wins

When two sources set the same name, later ones replace earlier ones in this order: the container's `env`, the step's `env` (and, for a template step, its `PARAM_` values), outputs of needed steps, the standard values, run inputs, loop values, then secrets. Library directories are added last, by the runner.

### Example

This pipeline gives an output in one step and prints the variables in the next:

```yaml
pipeline: env-demo
inputs:
  target: { type: string, default: staging }
steps:
  first:
    image: docker.io/library/alpine:3.22
    env: { GREETING: hello }
    run: echo "version=1.2.3" >> "$LAUNCHWAY_OUTPUTS"
  show:
    needs: first
    for_each: { os: [alpine] }
    image: docker.io/library/alpine:3.22
    run: env | grep -E '^(LAUNCHWAY|CI=)' | sort
```

Run it in a Git repository with:

```sh
launchway run -i target=prod
```

The `show` step prints (IDs and commit shortened to placeholders):

```text
CI=true
LAUNCHWAY=true
LAUNCHWAY_ATTEMPT=1
LAUNCHWAY_BRANCH=main
LAUNCHWAY_COMMIT=16d2b80c4c831cce497fe9f1ba65d47f6ebc3c73
LAUNCHWAY_EACH_OS=alpine
LAUNCHWAY_EVENT=manual
LAUNCHWAY_INPUT_TARGET=prod
LAUNCHWAY_NEEDS_FIRST_VERSION=1.2.3
LAUNCHWAY_OUTPUTS=/launchway/outputs
LAUNCHWAY_PIPELINE=env-demo
LAUNCHWAY_PROJECT=local
LAUNCHWAY_REF=refs/heads/main
LAUNCHWAY_RUN_ID=run_06gzdcazwx897r7gs6m0
LAUNCHWAY_RUN_NUMBER=1
LAUNCHWAY_STEP=show[os=alpine]
LAUNCHWAY_TAG=
```

A step from a library template also receives the library and parameter variables. A template `envdump` with a parameter `colour`, used as `uses: platform/envdump` with `with: { colour: green }`, prints:

```text
LAUNCHWAY_LIB=/workspace/.launchway/lib/platform
LAUNCHWAY_LIB_PLATFORM=/workspace/.launchway/lib/platform
PARAM_COLOUR=green
```

### Other variables in a step

With the host executor, a step gets only `PATH` and `HOME` from the runner's environment, plus `LANG=C.UTF-8`; nothing else of the runner's environment reaches it. In a container, the image's own environment applies. With the Kubernetes executor, `HOME` is `/home/launchway`. See [the runner configuration](runner-config.md).
