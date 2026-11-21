# Launchway

**Build, test and deliver software on infrastructure you control.**

[![CI](https://github.com/JK-Magic-Creations/launchway/actions/workflows/ci.yml/badge.svg)](https://github.com/JK-Magic-Creations/launchway/actions/workflows/ci.yml)
[![Docs](https://github.com/JK-Magic-Creations/launchway/actions/workflows/docs-pages.yml/badge.svg)](https://jk-magic-creations.github.io/launchway/)
[![Licence: AGPL-3.0](https://img.shields.io/badge/licence-AGPL--3.0-blue.svg)](LICENSE)

Launchway is one place to host your code, build it, test it, scan it and deliver it, on servers you
control or as a hosted service. It builds each change, tests it, scans it and takes it to production
through approvals, policy checks and GitOps deployments. Runs are durable records, so they survive
crashes, resume from the step that failed and show what was delivered, how it was checked and who
approved it. Setup is guided, every capability is also in the API, command line and agent endpoint,
and all that it needs is a database and Git.

![A run in the Launchway web UI: four steps drawn as a graph, and the log of the selected step.](docs/public/images/landing/run.png)

## Quick start

You need Go 1.27.4+, Node 24+, Git, and Podman or Docker.

```sh
git clone https://github.com/JK-Magic-Creations/launchway.git
cd launchway
make run          # then open http://127.0.0.1:8080
```

The first-run wizard asks plain questions and sets up your first project.

## Learn more

- **[Documentation](https://jk-magic-creations.github.io/launchway/)**: [what Launchway is](docs/start/overview.md),
  [tutorials](docs/tutorials/index.md), the [user guide](docs/guide/index.md),
  [how-to guides](docs/howto/index.md), [reference](docs/reference/index.md) and
  [security](docs/security/index.md).
- **[Roadmap](https://jk-magic-creations.github.io/launchway/roadmap.html)**
  ([as a list](docs/roadmap-list.md)): what is available, in progress and planned, in milestones
  marked Now, Next and Later. The first milestone is hosting your code and running your pipelines in
  one place.

Launchway is early software. Turn on sign-in before anyone else can reach a server.

## Contributing, security and licence

See [CONTRIBUTING.md](CONTRIBUTING.md). Report security problems privately as described in
[SECURITY.md](SECURITY.md). Releases are signed and rebuild byte for byte; see
[verifying downloads](docs/security/verify-downloads.md) and [versions and support](SUPPORT.md).

Launchway is free software under the [GNU Affero General Public License, version 3](LICENSE).
[LICENSING.md](LICENSING.md) explains what that means for you and how to get a commercial licence.
