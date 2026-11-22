# Verifying downloads

How to check that a Launchway binary, package, chart or image is the one the Flight's release
workflow built from a tagged commit, and nothing else. For operators installing Launchway and for
security reviewers.

> **Status.** The release workflow, the signing and the files below are built and were exercised
> locally with a key-signed release (`make release-local`, `make release-images`, `make test-install`,
> `make test-bundle`). **No release has been published yet**, so the keyless commands below have not
> been run against a real GitHub release; until the first one is out, build from source as the
> install guides describe.

## What a release contains

Every release (`https://github.com/JK-Magic-Creations/launchway/releases`) holds:

| File | What it is |
|---|---|
| `launchway-VERSION-OS-ARCH` | the static binary (server, Station (runner) and command line) for linux and darwin, amd64 and arm64 |
| `launchway-runner_VERSION-1_ARCH.deb`, `launchway-runner-VERSION-1.ARCH.rpm` | the Station as a systemd service |
| `install.sh` | the Station installer for Linux with systemd |
| `launchway-VERSION.tgz`, `launchway-runner-VERSION.tgz` | the server chart and the runner-only chart |
| `*.cdx.json` | a CycloneDX SBOM next to each binary, package and chart |
| `SHA256SUMS` | the SHA-256 of every file above |
| `SHA256SUMS.sigstore.json` | a Sigstore signature on `SHA256SUMS`, made by the release workflow |
| `images.txt` | the server and Station images, by digest |

There is no Windows build: the Station's host executor uses Unix process groups and signals.

## Check the files

With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/), check that
`SHA256SUMS` was signed by the release workflow of this repository for a version tag, then check the
files you downloaded against it:

```sh
id='^https://github\.com/(?i:jk-magic-creations/launchway)/\.github/workflows/release\.yml@refs/tags/v'
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c SHA256SUMS
```

The signature is keyless: a short-lived certificate names the workflow and the tag, and the
signature is recorded in Sigstore's public transparency log. `(?i:...)` matches the owner's name
whatever its capitals.

GitHub also records build provenance for the same files, which the
[GitHub CLI](https://cli.github.com) checks:

```sh
gh attestation verify launchway-1.4.0-linux-amd64 --repo JK-Magic-Creations/launchway
```

## Check an image

The images are `ghcr.io/jk-magic-creations/launchway` (server) and
`ghcr.io/jk-magic-creations/launchway-runner`, for linux/amd64 and linux/arm64. Each is signed the
same way and carries a CycloneDX SBOM per platform and SLSA provenance as signed attestations:

```sh
img=ghcr.io/jk-magic-creations/launchway:1.4.0
id='^https://github\.com/(?i:jk-magic-creations/launchway)/\.github/workflows/release\.yml@refs/tags/v'
cosign verify "$img" --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$img" --type slsaprovenance1 --certificate-identity-regexp "$id" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify "oci://$img" --repo JK-Magic-Creations/launchway
```

`cosign verify-attestation --type cyclonedx` on one platform's digest gives that platform's SBOM.
In production, refer to images by digest; `images.txt` lists them. The images' labels name the
commit they were built from:

```sh
skopeo inspect "docker://$img" | jq '.Labels'
# org.opencontainers.image.revision  the full commit
# org.opencontainers.image.version   the release
# org.opencontainers.image.created   the commit's time
# org.opencontainers.image.source    the repository
# org.opencontainers.image.licenses  AGPL-3.0-only
```

## What the installer checks

`install.sh` does these checks itself before it installs anything: it refuses a binary whose
checksum does not match `SHA256SUMS`, and it checks the signature on `SHA256SUMS` with cosign when
cosign is installed. Without cosign it says that only the checksum was checked;
`--require-signature` turns that into an error. See [Connect a Station](../howto/connect-runner.md).

The installer itself is a plain file in the release. To check it before running it, download it
with `SHA256SUMS` and its signature, and run the commands under [Check the files](#check-the-files).

## Releases built inside your organization

Releases rebuilt from source with `make release-local` (for example inside a network with no
internet access) are signed with a key instead of a keyless certificate, and hold the public key `cosign.pub`
and signed SLSA provenance (`provenance.json`, `provenance.sigstore.json`):

```sh
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json --key cosign.pub --insecure-ignore-tlog
```

`install.sh --public-key cosign.pub` checks the same signature, with cosign or, without it, with
openssl. Get `cosign.pub` from whoever built the release, through a channel other than the files
themselves.

## Rebuild and compare

Building the same commit twice gives byte-identical binaries, packages, charts and SBOMs: the Go
toolchain is pinned, paths are trimmed, and the build time is the commit's time. Anyone can check
a published binary against its source:

```sh
git clone https://github.com/JK-Magic-Creations/launchway.git && cd launchway
git checkout v1.4.0
make release-local
sha256sum dist/v1.4.0/launchway-1.4.0-linux-amd64   # the same as in the release's SHA256SUMS
```

`make release-reproduce` builds a release twice in separate directories and compares every file.
Images are not bit-for-bit reproducible: they take Alpine's packages as they are on the day they
are built, and their SBOMs list the versions.

## Air-gapped bundles

`make bundle` packs a release, its images (as OCI layouts with their signatures and attestations)
and both charts into one archive with `BUNDLE-SHA256SUMS` and its signature, and an offline install
guide. Verify the bundle's checksums and signature first, as above, then load the images into your
own Depot (registry).
