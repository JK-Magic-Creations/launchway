# Packaging and releases

Everything a release publishes is built by the scripts in [`release/`](release), which a
maintainer runs locally and the [release workflow](../.github/workflows/release.yml) runs on a
version tag. Nothing here publishes anything by itself except that workflow.

| Make target | What it does |
|---|---|
| `make tools` | cosign, syft, nfpm, govulncheck, go-licenses, actionlint at pinned versions, into `bin/tools` |
| `make release-local` | the whole release into `dist/VERSION`: binaries, deb and rpm, `install.sh`, charts, SBOMs, `SHA256SUMS`, its signature, provenance, apt and yum repositories |
| `make release-reproduce` | the release built twice and compared byte for byte |
| `make release-images` | server and runner images (amd64, arm64) from those binaries, pushed to `IMAGE_REPO` (default: a throwaway registry on 127.0.0.1:5555), signed, with SBOM and provenance attestations |
| `make bundle` | the air-gapped bundle `dist/launchway-bundle-VERSION.tar` (after the two above) |
| `make test-install` | `install.sh` and the packages on Debian, Ubuntu and Rocky Linux with systemd, against a real server |
| `make test-runner-chart` | the server chart and the runner chart in separate namespaces of a kind cluster, a run through the remote runner |
| `make test-bundle` | the bundle's images loaded into kind with no registry, both charts installed from it |
| `make vulncheck license-check lint-scripts lint-workflows chart-lint` | the checks CI runs |

`RELEASE_VERSION` defaults to the tag at `HEAD`, else `v0.0.0-dev.<commit>`.

## How a release is built

- **Binaries** (`build.sh`): `CGO_ENABLED=0`, the Go toolchain `go.mod` names (`GOTOOLCHAIN`),
  `-trimpath`, `-buildid=`, fixed `GOAMD64=v1`/`GOARM64=v8.0`, `-mod=readonly`, the version from the
  argument, the commit and its time from Git. The web UI is built from the files Git tracks in a
  Node image pinned by digest. Same commit, same bytes.
- **Packages** (`packages.sh`, [`nfpm.yaml`](nfpm.yaml)): `launchway-runner` deb and rpm with the
  binary in `/usr/bin`, the unit in `/usr/lib/systemd/system`, `/etc/launchway/runner.yaml`
  (640 root:launchway-runner) and `runner.env` (600 root) as configuration files kept on upgrade,
  and [scripts](scripts) that create the system user, restart a running service on upgrade and
  stop it on removal. The service is not enabled on install: it needs a server and a token first.
- **Charts** (`charts.sh`): both charts with the release's version, repacked deterministically.
- **SBOMs, checksums, signatures** (`finish.sh`): a CycloneDX SBOM per artifact; `SHA256SUMS`;
  `SHA256SUMS.sigstore.json`. `SIGN=keyless` (the workflow) signs with the workflow's identity
  through Sigstore's public instance; `SIGN=key` (the default) with `COSIGN_KEY`, offline unless
  `TLOG=1`, and writes `cosign.pub` and signed SLSA provenance (`provenance.json`,
  `provenance.sigstore.json`). Without `COSIGN_KEY` a key for the machine is made in
  `.release-key/`: fine for tests, not for anything you hand to others.
- **Repositories** (`repo.sh`): apt (`dists/stable`, `InRelease`) and yum (`repodata`,
  `repomd.xml.asc`, the rpms signed), signed with `REPO_GPG_KEY`.
- **Images** (`images.sh`, [`release/Containerfile`](release/Containerfile)): Alpine pinned by
  digest, assembled for each architecture without emulation; the binary, Git, its SSH transport,
  CA certificates; no shell, user 65532. Alpine's packages are whatever its 3.23 repository holds
  at build time, so images are not bit-for-bit reproducible; their SBOMs list the versions.

## The runner's systemd unit

[`systemd/launchway-runner.service.in`](systemd/launchway-runner.service.in) runs the runner as
`launchway-runner` with `NoNewPrivileges`, no capabilities, `ProtectSystem=strict` (it may write
only its state directory `/var/lib/launchway-runner`), `ProtectHome`, `PrivateTmp`,
`PrivateDevices`, the kernel and control-group protections, `RestrictNamespaces`,
`RestrictSUIDSGID` and a restricted set of address families; `systemd-analyze security` rates it
2.9 (OK). It restarts always, with a start limit, `TimeoutStopSec=90` (the runner hands its steps
back on SIGTERM), `LimitNOFILE=65536`, `TasksMax=8192` and `MemoryHigh=90%`.

What each executor needs relaxed, and why:

- **host** (the default on a VM): nothing. Steps run as `launchway-runner` inside the same sandbox,
  so they can write only the runner's state directory and a private /tmp, and cannot gain
  privileges (no `sudo`). Raise `TasksMax` or memory with `systemctl edit launchway-runner` for
  heavy builds. Not set on purpose:
  `MemoryDenyWriteExecute` (breaks the JIT compilers of Node.js, Java and others) and a
  `SystemCallFilter` (steps run arbitrary build tools).
- **podman** (rootless): `install.sh --executor podman` installs the drop-in
  [`systemd/container-executor.conf`](systemd/container-executor.conf). Rootless Podman needs
  the setuid helpers `newuidmap`/`newgidmap` (so `NoNewPrivileges=no`, `RestrictSUIDSGID=no`, and
  `CAP_SETUID CAP_SETGID` in the bounding set), user and other namespaces
  (`RestrictNamespaces=no`), `/dev/fuse` and `/dev/net/tun` (`PrivateDevices=no`), its own cgroup
  (`Delegate=yes`, `ProtectControlGroups=no`) and a runtime directory; the runner user gets
  subordinate ids. Steps are then isolated by the containers instead.
- **docker**: the runner user joins the `docker` group, which is equivalent to root on the
  machine. The sandbox stays as it is (the Docker daemon does the work), but prefer Podman.
- **kubernetes** from a VM: nothing; give the runner a kubeconfig it can read, outside `/home`.

## Publishing the first release (once)

1. Repository settings → Actions → General: allow GitHub Actions to create releases (workflow
   permissions "Read and write", or keep "Read" and rely on the job's own `permissions:`, which
   the workflow sets). Packages: the first push creates `ghcr.io/jk-magic-creations/launchway` and
   `launchway-runner`; make both public in their package settings and link them to the
   repository.
2. For the apt and yum repositories: make an OpenPGP signing key without a passphrase
   (`gpg --quick-gen-key 'Launchway releases <...>' ed25519 sign 2y`), store its armored private
   key as the secret `REPO_GPG_PRIVATE_KEY`, and publish its public key in the README. Without it
   the release has no repositories (the workflow warns), everything else still works. The
   repositories are attached as `launchway-repositories-VERSION.tar.gz`; serve its contents from
   a static host (GitHub Pages, a bucket) at the `BASE_URL` the `.repo` and `.sources` files name.
3. Protect tags `v*` (rulesets) so only maintainers can create them.
4. Tag and push: `git tag -s v0.1.0 -m 'Launchway v0.1.0' && git push origin v0.1.0`. The workflow
   runs the checks, builds, signs, pushes the images and creates a **draft** release; check it
   (`SUPPORT.md` has the verification commands) and publish it.
