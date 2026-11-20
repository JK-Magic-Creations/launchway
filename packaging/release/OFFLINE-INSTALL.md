# Installing Launchway @VERSION@ without internet access

This bundle holds everything a Launchway installation needs: the binaries, the runner's deb and
rpm packages and their repositories, the install script, the Helm charts, the container images
with their signatures, SBOMs and provenance. Nothing in it reaches out to the internet.

## 1. Verify the bundle

On a machine with `cosign` (or with `openssl`, see below), from the bundle's directory:

```sh
sha256sum -c BUNDLE-SHA256SUMS
cosign verify-blob --key release/cosign.pub --insecure-ignore-tlog \
  --bundle BUNDLE-SHA256SUMS.sigstore.json BUNDLE-SHA256SUMS      # a key-signed bundle
```

Get `cosign.pub` (or, for an official release, the release workflow's identity) from a source
you trust, not only from the bundle. For an official release built by the release workflow, the
signature is keyless instead:

```sh
cosign verify-blob --bundle BUNDLE-SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/jk-magic-creations/launchway/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com BUNDLE-SHA256SUMS
```

`release/` is the release exactly as published; `cd release && sha256sum -c SHA256SUMS` checks it
again, and `SHA256SUMS.sigstore.json` is its signature.

## 2. Images

Into a registry of your own (signatures and attestations go along):

```sh
cosign load --dir images/launchway        registry.internal/launchway/launchway:@VER@
cosign load --dir images/launchway-runner registry.internal/launchway/launchway-runner:@VER@
cosign verify --key release/cosign.pub --insecure-ignore-tlog registry.internal/launchway/launchway:@VER@
```

Or straight into a node's container runtime, one architecture at a time:

```sh
podman load -i images/launchway-linux-amd64.oci.tar
ctr -n k8s.io images import images/launchway-linux-amd64.oci.tar
kind load image-archive images/launchway-linux-amd64.oci.tar
```

The archives name the images `localhost/launchway:@VER@` and `localhost/launchway-runner:@VER@`.
With the charts, use `--set image.repository=localhost/launchway --set image.pullPolicy=Never`
(and `localhost/launchway-runner` for the runner chart).

## 3. Servers and runners on Kubernetes

```sh
helm install launchway release/launchway-@VER@.tgz -n launchway --create-namespace \
  --set image.repository=registry.internal/launchway/launchway -f my-values.yaml
helm install runners release/launchway-runner-@VER@.tgz -n launchway-runners --create-namespace \
  --set image.repository=registry.internal/launchway/launchway-runner \
  --set server.url=https://launchway.internal --set token.existingSecret=launchway-runner-token
```

Step pods need their own images (and the helper image, `runner.kubernetes.helperImage`) from your
registry too.

## 4. A runner on a Linux machine

With the script, from the bundle (the signature is checked with the key you give):

```sh
sudo sh release/install.sh --from-dir release --public-key /path/to/trusted/cosign.pub \
  --require-signature --server https://launchway.internal --token-file /root/runner-token
```

Or with the packages, from the repositories in `release/repo` served by any internal web server
(see `release/repo/launchway.sources` and `release/repo/launchway.repo`), or directly:

```sh
sudo apt install ./release/launchway-runner_*_amd64.deb      # Debian, Ubuntu
sudo dnf install ./release/launchway-runner-*.x86_64.rpm     # RHEL, Rocky, Alma, Fedora
```

then set `server:` in `/etc/launchway/runner.yaml`, `LAUNCHWAY_RUNNER_TOKEN` in
`/etc/launchway/runner.env`, and `sudo systemctl enable --now launchway-runner`.
