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

Every release has `SHA256SUMS`, listing every file, and `SHA256SUMS.sigstore.json`, a
[Sigstore](https://www.sigstore.dev) signature on it made by the release workflow (keyless: the
certificate names the workflow and the tag). With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```sh
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/jk-magic-creations/launchway/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c SHA256SUMS
```

GitHub's build provenance for the same files: `gh attestation verify launchway-1.4.0-linux-amd64
--repo jk-magic-creations/launchway`. Each artifact has a CycloneDX SBOM next to it
(`<file>.cdx.json`).

Images (`ghcr.io/jk-magic-creations/launchway` and `.../launchway-runner`) are signed the same way,
and carry an SBOM per platform and SLSA provenance as signed attestations:

```sh
img=ghcr.io/jk-magic-creations/launchway:1.4.0
id='^https://github\.com/jk-magic-creations/launchway/\.github/workflows/release\.yml@refs/tags/v'
cosign verify $img --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation $img --type slsaprovenance1 --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://$img --repo jk-magic-creations/launchway
```

(`cosign verify-attestation --type cyclonedx` on a platform's image digest gives its SBOM.) Pin
images by digest in production: `images.txt` in the release lists them.

`install.sh` does the checks itself: it refuses a binary whose checksum does not match, and checks
the signature with cosign when it is installed (`--require-signature` makes that mandatory).

Releases rebuilt from source with `make release-local` (for example inside a network with no
internet access) are signed with a key instead: verify with `cosign verify-blob --key cosign.pub
--insecure-ignore-tlog ...`, or `install.sh --public-key cosign.pub`, which also works with only
openssl. Building the same commit twice gives byte-identical binaries, packages, charts and SBOMs
(`make release-reproduce`), so anyone can check a published binary against its source.
