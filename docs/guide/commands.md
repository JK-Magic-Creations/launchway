# Using the command line

An overview of the `launchway` command. Every flag is in the [command-line reference](../reference/cli.md).

```
launchway server   [--config launchway.yaml]   the server, web UI and a built-in runner
launchway runner   --server URL --token T     a runner on another machine (outbound connections only)
                   [--config FILE] [--secrets-config FILE] [--health-listen ADDR]
launchway run      [--secrets-config FILE] [--images-config FILE]   this directory's pipeline, on this machine
launchway validate [file]                     check a pipeline file
launchway graph    [file]                     the graph a pipeline produces, as JSON
launchway login    --server URL               store an API token for a server
launchway runs     start <project> [--ref R] [-i name=value ...] [--wait] [--timeout D] [--json]
launchway runs     list [--project P] [--status S] | show <run-id> | logs <run-id> [--step S] [--follow]
launchway runs     cancel <run-id>
launchway policy   list | run <run-id>        the policies in force; a run's policy decisions
launchway waivers  list | grant | revoke      waivers of policy rules (see Policies)
```

`launchway login` reads a token (from `LAUNCHWAY_TOKEN`, or typed, or piped in), checks it with the
server, and keeps the server and token in `credentials.json` in your configuration directory
(`~/.config/launchway`, or `LAUNCHWAY_CONFIG_DIR`), readable by you alone. It refuses a plain
`http://` address that is not on this machine unless given `--allow-http`. `LAUNCHWAY_SERVER` and
`LAUNCHWAY_TOKEN` override what it stored, for scripts. `runs start --wait` follows the run and
exits with its outcome: `0` succeeded, `1` failed, `3` canceled; `2` means the command was written
wrongly, `4` that the server refused (such as an input value that does not fit, named) or could
not be reached, and `5` that `--timeout` passed (the run goes on). `launchway run -i name=value`
gives inputs to a pipeline run on this machine.

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
  keep_failed: 0s             # a grace period for a failed run's pod; 0 removes it when the run ends
  start_timeout: 10m
  restricted: true            # the default: steps run as run_as_user, not root (see below)
  run_as_user: 1000
  run_as_group: 1000
secrets: {}                   # see Secrets
reports:                      # test report files collected after each step; see Test reports
  max_files: 100
  max_file_mib: 10
  max_total_mib: 20
images: {}                    # builders and registries for image steps; see Container images
```

With the Kubernetes executor, every container of a step pod runs as `run_as_user` (default
1000, never root) with no privileges, so the step namespace can enforce Pod Security
`restricted`. `HOME` is a writable `/home/launchway`, and `/tmp` and the checkout are writable.
Steps that need root, such as `apt-get install`, fail; use images that already have their tools.
`restricted: false` (or `--k8s-unrestricted`) runs images as their own users, often root, and
needs a namespace that allows that. Pipelines cannot change this setting.
[deploy/README.md](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/README.md#step-pods-and-pod-security) has the details, including
service images that expect root.

`--health-listen :8081` serves `/healthz` (the process is up) and `/readyz` (the server answered
within the last two minutes) for probes.
