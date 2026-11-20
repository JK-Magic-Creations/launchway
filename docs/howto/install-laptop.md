# Install on a laptop

Build Launchway from source on your own computer, to write and run pipelines locally and to try
the server. For developers. There are no published binaries or packages yet; signed releases are
[in progress](../roadmap-list.md).

> **Verified** on 2026-11-19 against commit f717337: every command below on Linux (Fedora, Go
> 1.27.4, Node 26, Podman 5) from a fresh clone. **Not verified:** macOS, Windows (WSL), Docker.

## What you need

| Tool | Version | Why |
|---|---|---|
| Git | any recent | to clone, and for the projects Launchway watches |
| Go | 1.27 or newer | to build the binary |
| Node.js | 24 or newer | to build the web UI, which is compiled into the binary |
| Podman or Docker | any recent | steps run in containers; without either, steps run directly on your machine |
| make | any | the build is driven by the Makefile |

## 1 Build

```sh
git clone https://github.com/JK-Magic-Creations/launchway.git ~/launchway
cd ~/launchway
make all
```

`make all` builds the web UI (`web/dist`) and then the binary `bin/launchway`, which contains
everything: server, runner, command line and UI. Nothing else needs installing.

```sh
~/launchway/bin/launchway version
```

## 2 Put it on your path

```sh
mkdir -p ~/.local/bin
cp ~/launchway/bin/launchway ~/.local/bin/
launchway --help
```

If `launchway --help` is not found, add `~/.local/bin` to `PATH` in your shell's start-up file.

## 3 Use it

- **Run a pipeline locally:** in a repository with `.launchway/pipeline.yaml`, run
  `launchway validate` and `launchway run`.
- **Run the server with demo projects:** `make run` in the checkout; see the
  [quick start](../start/quick-start.md).
- **Run the server for your own repositories:** see step 5 of
  [First pipeline in 10 minutes](../tutorials/first-pipeline.md#5-let-a-server-run-it-on-every-commit).

## Which executor runs the steps

`launchway run` and the built-in runner choose with `executor: auto`: Podman if it is installed,
otherwise Docker, otherwise the host itself. On the host, steps run as your user without any
container, and `image:` is ignored. Choose explicitly with `launchway run --executor podman` (or
`docker`, `host`). See [Runner configuration](../reference/runner-config.md).

> [!WARNING]
> With the host executor, a step can do anything your user can. Run only pipelines you trust that way.

## Updating

```sh
cd ~/launchway
git pull
make all
cp bin/launchway ~/.local/bin/
```

The server upgrades its database when it starts. See
[Backup, restore, upgrade](backup-restore-upgrade.md) before upgrading a server other people use.

## What next

- [First pipeline in 10 minutes](../tutorials/first-pipeline.md).
- [Install on one server](install-single-server.md) for a team.
