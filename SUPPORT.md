# Versions, support and verifying releases

## Versions

Releases are numbered with [semantic versioning](https://semver.org): `vMAJOR.MINOR.PATCH`, with
pre-releases such as `v1.4.0-rc.1`. Git tags, binaries, packages, charts and images all carry the
same version (packages write a pre-release as `1.4.0~rc.1`, which sorts before `1.4.0`).

- **Patch** releases fix bugs and security problems. They add no settings, pipeline keys or
  database migrations.
- **Minor** releases add features. Their database migrations are *additive* unless the release
  notes say otherwise (see [docs/development/migrations.md](docs/development/migrations.md)).
- **Major** releases may remove things, after a minor release has warned of it.

Until 1.0 the project is young: a minor release may change behaviour, and its notes say how.

## Which releases get fixes

Fixes go into the latest minor release. Security fixes also go into the minor release before it.
Report security problems privately, as [SECURITY.md](SECURITY.md) describes; fixed problems are
published as GitHub security advisories.

## Servers, runners and mixed versions

What the code does today:

- **Servers of different versions share a database safely** during a rolling upgrade, a canary or a
  rollback, between releases from schema 8 on. A server applies the migrations it knows when it
  starts; an older server keeps running against a database with *additive* migrations it does not
  know, and leaves alone runs that use something only a newer server understands (they are shown
  as needing a newer server). A *breaking* migration raises the database's minimum, and older
  servers then refuse to start, saying which version they need. `/readyz` and `/api/v1/info`
  report the schema each server knows and the database's. The rules are in
  [docs/development/migrations.md](docs/development/migrations.md).
- **Runners report their version and what they can do** (features such as `artifacts` and
  `images`). A server gives a step that needs a feature only to a runner that reports it, so an
  older runner keeps running the steps it can and is never given one it would run wrongly. The
  Runners list (`/api/v1/runners`) shows each runner's version.
- **There is no enforced version window yet**: a server does not refuse runners for being old. A
  stated window (runners up to two minor releases behind the servers, and a minimum below which a
  runner gets no work) is designed but not built.
- **Settings files are strict**: a server or runner refuses a setting it does not know. Set a new
  setting only once every server and runner that reads it is upgraded, and take it out again
  before rolling back. The Helm charts write new settings only when you set them.

So: upgrade servers first, then runners; keep runners within a minor release or two of the
servers; and read the release notes of every minor release you cross.

## Platforms

| | linux/amd64 | linux/arm64 | darwin/amd64 | darwin/arm64 | windows |
|---|---|---|---|---|---|
| Binary (server, runner, CLI) | yes | yes | yes | yes | no |
| Container images | yes | yes | | | |
| Runner as a service: `install.sh`, deb, rpm | yes | yes | | | |

- There is no Windows build: the runner's host executor uses Unix process groups and signals.
- The runner service needs systemd. It is tested (`make test-install`) on Debian 12, Ubuntu 24.04
  and Rocky Linux 9, and should work on their relatives (RHEL 9, AlmaLinux 9, Fedora). Alpine and
  other OpenRC systems are not supported; run the runner in a container there.
- Kubernetes 1.30 or later and Helm 3.8 or later for the charts.

## Verifying what you downloaded

Every release has `SHA256SUMS` with a keyless Sigstore signature by the release workflow, a
CycloneDX SBOM per file, and build provenance; the images are signed and carry SBOM and provenance
attestations. The commands to check them, and to rebuild a release and compare it byte for byte,
are in [Verifying downloads](docs/security/verify-downloads.md). `install.sh` checks the
checksum, and the signature when cosign is installed, before it installs anything.
