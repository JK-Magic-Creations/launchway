# Releasing Launchway

For the maintainer who pushes tags. [README.md](README.md) explains how each part is built;
this page records the decisions, what a release weighs, and the steps for the first release,
`v0.1.0-rc.1`.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| First version | `v0.1.0-rc.1` | A pre-release: the workflow marks it so, and packages write it as `0.1.0~rc.1`, which sorts before `0.1.0` |
| Who tags | only the owner | Tags `v*` are protected; a tag is what starts a signed build |
| Image registry | `ghcr.io/jk-magic-creations/launchway` and `launchway-runner` | Next to the source, pushed with the workflow's own token, no extra secret |
| Signing | keyless, through GitHub's OIDC identity (`id-token: write` in the release job) | No key to guard or rotate; the certificate names the workflow and the tag, and the signature is in Sigstore's transparency log |
| Releases built inside an organisation | key-signed (`make release-local`, `cosign.pub`) | Works with no internet access; see [Verifying downloads](../docs/security/verify-downloads.md) |
| `install.sh` | served from the documentation site, and attached to each release | The site gives a short, stable URL; the release copy is covered by `SHA256SUMS` and its signature |
| apt and yum repositories | yes, signed with an OpenPGP key kept as a repository secret | Package managers upgrade the runner without the installer |
| Release object | a draft, created by the workflow | A person checks the files and publishes |
| Binaries | linux and darwin, amd64 and arm64; no Windows | The host executor uses Unix process groups and signals |
| Reproducibility | binaries, packages, charts and SBOMs are byte for byte; images are not | Images take Alpine's packages as they are on the day |
| Runner service sandbox | systemd hardening on (rated 2.9 OK by `systemd-analyze security`), relaxed only by the Podman drop-in | See "The runner's systemd unit" in [README.md](README.md) |

## What a release weighs

From a local `make release-local` of this branch (the sizes of the files that matter):

| Artifact | Size |
|---|---|
| `launchway-VERSION-linux-amd64` / `-linux-arm64` | 71 MB / 66 MB |
| `launchway-VERSION-darwin-amd64` / `-darwin-arm64` | 72 MB / 68 MB |
| runner `.deb` amd64 / arm64 | 22 MB / 20 MB |
| runner `.rpm` x86_64 / aarch64 | 21 MB / 19 MB |
| server chart / runner chart | 26 KB / 19 KB |
| `install.sh` | 21 KB |
| SBOMs (one per artifact) | about 130 KB each |
| apt and yum repositories (`repo/`) | about 79 MB |
| Air-gapped bundle `launchway-bundle-VERSION.tar` | about 700 MB (binaries, both images for two architectures as OCI layouts, charts, signatures) |

## Before the first release

Nothing here has been run against GitHub. What was exercised locally: reproducible builds,
the installer and packages on Debian 12, Ubuntu 24.04 and Rocky Linux 9, both charts on kind, the
bundle on kind with no registry, shellcheck, actionlint, `helm lint`, vulnerability and licence
checks. What only GitHub can show: the workflow itself, keyless signing, `gh attestation`, the
push to ghcr.io and the draft release. The workflow calls the same make targets that were tested,
so those are the only unverified parts. Expect to fix a small thing on the first run, and use a
throwaway `-rc` tag for it.

## Publishing `v0.1.0-rc.1` (once)

1. **Repository settings.** Actions, General: allow Actions to create releases (or leave the
   token read-only; the workflow's jobs set their own `permissions:`).
2. **Tag protection.** Add a ruleset for tags `v*` that only the owner can create.
3. **Packages.** The first push creates `ghcr.io/jk-magic-creations/launchway` and
   `launchway-runner`. Make both public and link them to the repository.
4. **Package repository key.** Make an OpenPGP key without a passphrase:
   `gpg --quick-gen-key 'Launchway releases <releases@example.org>' ed25519 sign 2y`. Store the
   armored private key (`gpg --armor --export-secret-keys FINGERPRINT`) as the Actions secret
   `REPO_GPG_PRIVATE_KEY`. Keep an offline copy. Publish the fingerprint in the docs.
5. **Where the repositories live.** Set the repository variable `PACKAGE_REPO_URL` to the static
   address that will serve them (release assets cannot hold their directories). Without the
   secret and the variable the workflow warns and the release has no repositories; everything
   else still works.
6. **`install.sh` on the documentation site.** The site is built by
   `.github/workflows/docs-pages.yml` from `docs/`, and nothing there copies `install.sh` yet. Add
   a step before the upload that copies `packaging/install.sh` into `docs/.vitepress/dist/`, and
   point the install pages at that address. Do this after the tag, so that the script on the
   site is the one the release carries (it fetches the release it is told to with `--version`).
7. **Tag and push** (only the owner):

   ```sh
   git switch main && git pull
   git tag -s v0.1.0-rc.1 -m 'Launchway v0.1.0-rc.1'
   git push origin v0.1.0-rc.1
   ```

   The workflow runs the checks, builds, signs, pushes the images and creates a **draft
   pre-release**.
8. **Check the draft.** Download `SHA256SUMS` and its `.sigstore.json` and run the commands in
   [Verifying downloads](../docs/security/verify-downloads.md), including `cosign verify` on both
   images and `gh attestation verify`. Try `install.sh` on a clean Debian or Rocky machine. Then
   publish the draft.
9. **Publish the repositories.** Copy the contents of `launchway-repositories-VERSION.tar.gz`
   to `PACKAGE_REPO_URL`. Check with a clean machine: `apt update` and `dnf makecache` see the
   package and accept the key.
10. **Update the docs.** Remove the "no release has been published yet" note at the top of
    Verifying downloads, and replace `v1.4.0` in the examples with a real version once one exists.

If a run fails after the images were pushed, delete the tag and the draft, fix, and tag the next
`-rc.N`; do not move a tag.

## Later releases

Tag a version with the same command. Patch and minor numbering and what each may change are in
[SUPPORT.md](../SUPPORT.md). The air-gapped bundle is not built by the workflow: run
`make release-local release-images bundle` with `IMAGE_REPO` pointing at a registry you can reach,
and attach the result if a customer needs it.
