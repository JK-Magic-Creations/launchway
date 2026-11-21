# Container images

Image steps build with rootless BuildKit, scan, push by digest, sign, and record provenance.

An image step builds a container image from a Dockerfile in the workspace, scans it, pushes it and
signs it, and records what it made:

```yaml
steps:
  test:
    image: golang:1.27
    run: go test ./... && echo version=1.4.2 >> "$LAUNCHWAY_OUTPUTS"

  image:
    kind: image
    needs: test
    context: .                        # default .
    file: Dockerfile                  # default <context>/Dockerfile; relative to the workspace
    target: runtime                   # a stage of the Dockerfile; optional
    platforms: [linux/amd64, linux/arm64]   # optional; see below
    build_args: { VERSION: { output: test.version }, COMMIT: { run: sha }, CREATED: { run: commit_time } }
    secrets:                          # RUN --mount=type=secret,id=npm_token in the Dockerfile
      - { id: npm_token, secret: { store: vault, key: ci/npm, field: token } }
    tags:
      - { registry: harbor, repository: team/shop, tag: { run: short_sha } }
      - { registry: harbor, repository: team/shop, tag: latest }
    cache: { registry: harbor, repository: team/cache/shop }   # optional
    sbom: true                        # default false
    provenance: true                  # the default
    sign: true                        # with the runner's key; or { key: {store,key,field}, password: {...} }
    scan: { fail_on: high, mode: enforce }   # or scan: true (report only)

  deploy-prod:
    kind: deploy
    needs: image
    environment: prod
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop/rollout.yaml, image: team/shop, digest: { output: image.digest } }
```

| Key | Meaning |
|---|---|
| `context`, `file`, `target` | What to build: a directory and a Dockerfile in the workspace, as earlier steps left it, and a stage. Neither may lead outside the workspace, through a symbolic link or otherwise |
| `platforms` | `os/arch` values; default the builder's own. See *Several platforms* |
| `build_args` | Values as for deploy steps: written in place, `{ output: step.key }`, `{ run: sha \| short_sha \| branch \| tag \| number \| commit_time }` or `{ input: name }`. They are recorded in the provenance: never put a secret in one |
| `secrets` | Build secrets by ID, read from the runner's secret stores like any step's secrets. They reach the build only as `RUN --mount=type=secret` mounts: never in a layer, the image's history, the provenance, the log (masked like any secret), an event or the database |
| `tags` | Names to push under: a registry as the runner's configuration names it, a repository in it, and a tag (a value, as above) |
| `push` | Default true. Pull-request and draft runs never push, whatever it says |
| `cache` | A repository the build cache is read from and, on runs that push and may push there, written to |
| `sbom` | Make a CycloneDX SBOM of each platform's image: kept as the step's `sbom` artifact and summarized, as for `reports.sbom`, and searchable under *Components* |
| `provenance` | Record where the image came from (see *Provenance*); kept as the step's `provenance` artifact |
| `sign` | Sign the image, its platforms' images, the provenance and the SBOMs, in the form cosign verifies |
| `scan` | Look for known vulnerabilities in each platform's image. `mode: report` (the default) records them; `mode: enforce` fails the step, before anything is pushed, when one is at `fail_on` or worse. They are findings like any scanner's: the run's Security tab, the project's Security page |

Its outputs are `digest` (the image index's, or the one image's), `image` (`host/repository@sha256:…`,
empty when nothing was pushed), `tags` (comma-separated `host/repository:tag`) and `pushed` (`true`
or `false`). Later steps receive them as `LAUNCHWAY_NEEDS_IMAGE_DIGEST` and so on, and deploy steps
as `{ output: image.digest }`. A deploy change with `digest:` instead of `tag:` pins every reference
to the image as `repository@sha256:…`, which nobody can move; references already pinned are moved
to the new digest. A tag cannot move a reference pinned by digest, and says so.

An image step runs no script and has no container in the run's pod: the runner builds it. It may
`fetch` artifacts into the workspace first, is retried with `retry`, may be in a template, and
cannot be repeated by `for_each` yet. A run with image steps runs, from its first step, on a runner
that can build images, since its steps share that runner's workspace.

**How it is built.** Images are built by [BuildKit](https://github.com/moby/buildkit) daemons the
runner's operator runs apart from the steps, rootless (see
[deploy/buildkit/README.md](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/buildkit/README.md): nothing that builds a Dockerfile runs under
Pod Security "restricted", so the daemon has a namespace of its own with exactly two relaxations,
and step pods stay restricted). The runner is the daemon's client, over mutual TLS: it sends the
build context (copied out of the run's pod on Kubernetes), answers the daemon's requests for build
secrets and registry credentials over the build's session, and receives the image. For registries
that use tokens, an honest daemon asks the runner for a token, which the runner fetches itself, and
never sees the password; for registries that take a user name and password directly (basic
authentication), the daemon is given the password. The session answers both kinds of request, so a
daemon that someone has taken over can obtain the user name and password of every registry the
current build may use. That is why a daemon is one trust boundary (see
[deploy/buildkit/README.md](https://github.com/JK-Magic-Creations/launchway/blob/main/deploy/buildkit/README.md)) and why the credentials a build may use are
scoped (see *Who gets which credentials* below). It then has it scanned, by a pinned
[Trivy](https://trivy.dev) image run inside the daemon on exactly what was built, enforces the
scan's policy, and pushes and signs it itself. Base images, the build cache and the scanner's
database are the daemon's business; it keeps them between builds.

**Registries and who may push** are the runner's configuration, never the pipeline's:

```yaml
# runner.yaml (or runner: in the server's configuration)
images:
  builders:
    - name: shared
      address: tcp://buildkitd.launchway-build.svc:1234
      ca_file: /var/run/launchway/buildkit/ca.crt      # the daemon's CA
      cert_file: /var/run/launchway/buildkit/tls.crt   # the runner's client certificate
      key_file: /var/run/launchway/buildkit/tls.key
      projects: ["*"]                 # which projects may build here
    - { name: arm, address: tcp://buildkitd-arm.launchway-build.svc:1234, platforms: [linux/arm64], ... }
  registries:
    harbor:
      host: harbor.example.com
      credentials: { username: "robot$ci", password: { store: vault, key: ci/harbor, field: password } }
      pull_credentials: { username: "robot$pull", password: { store: vault, key: ci/harbor-pull, field: password } }
      pull_projects: [shop, "web-*"]          # who gets pull_credentials; empty: every project
      ca_file: /etc/launchway/harbor-ca.crt   # optional
      push:
        - { projects: [shop], repositories: [team/shop, "team/cache/*"] }
        - { projects: ["platform-*"], repositories: ["platform/**"] }
  signing: { key_file: /var/run/launchway/signing/cosign.key, password_file: /var/run/launchway/signing/cosign.password }
  scanner: { db_repository: harbor.example.com/mirror/trivy-db }   # optional
  max_context_mib: 2048
  max_image_mib: 20480
```

- **Push rules.** Nothing is pushed without a rule naming the project and the repository (`*`
  within one level, a final `/**` any depth below). The runner checks that the registry accepts its
  credentials for each repository before building, so a wrong password or a registry that cannot be
  reached fails the step in seconds, saying which.
- **Pull requests and drafts** never push, never write the cache and never read `credentials`:
  the server tells the runner they may not push, and the runner refuses again on its own. They do
  build, scan and make the SBOM, so a pull request shows what its image would be. Their build
  secrets and `pull_credentials` are refused unless the project allows secrets for pull requests;
  without them, they pull anonymously, and the log says once that private base images cannot be
  pulled. The server decides this and tells the runner; a runner that is not told treats a
  pull-request or draft run as not allowed.
- **Who gets which credentials.** A Dockerfile can send anything it can pull to anywhere (a `RUN`
  line with network access), so any credentials a build gets are as good as the project's. The
  builder gets `pull_credentials` only for the projects in `pull_projects`; with `pull_projects`
  empty, **every project's builds, and pull requests that may read secrets, can pull whatever that
  account can read**, from any repository. Give each registry's pull account read access to what
  those projects need and nothing more, and name the projects. Push `credentials` are given to the
  builder only for the registries a build pushes to (for the build cache), and they pull too: a
  project allowed to push to one repository can pull whatever that account can read. Use a registry
  account per project or team (robot accounts scoped to their repositories), not one account for
  everything.
- **Token services.** A registry names where the runner should send its credentials for a token;
  the runner sends them only over HTTPS, unless the registry is marked `insecure: true`.
- **No plain HTTP** to a registry unless it is marked `insecure: true` (for a registry on the
  runner's own machine): the registry client would otherwise fall back to HTTP for private
  addresses when TLS fails, sending the credentials in the clear.

**Signing.** `sign: true` signs with the runner's own key (`images.signing`), a file only the runner
reads: steps never see it, so a signature says the image came from this runner's build. `sign: {
key: …, password: … }` signs with a key from a secret store, which steps of projects that store
allows can read too: such a signature is only as trustworthy as access to that key. Keys are ECDSA
keys as `cosign generate-key-pair` makes them (encrypted, with their password) or PKCS#8 PEM. The
signature, the provenance and the SBOMs are written to the image's repository the way cosign keeps
them (`sha256-<digest>.sig` and `.att`); nothing is sent to a public transparency log. Check them
with:

```sh
cosign verify --key cosign.pub --insecure-ignore-tlog harbor.example.com/team/shop@sha256:…
cosign verify-attestation --key cosign.pub --insecure-ignore-tlog --type slsaprovenance1 harbor.example.com/team/shop@sha256:…
```

Keyless signing (a short-lived certificate for an OIDC identity, and a transparency log entry) is
designed for but not built; `sign: { keyless: … }` is refused for now. Checking signatures at deploy
time (a deploy step that refuses an image not signed by a given key) is a later feature: the image
step records the key that signed (its fingerprint) with the image, which is where that check will
start.

**Provenance.** Two statements, both in-toto with SLSA provenance v1 predicates:

- *Launchway's*, written by the runner from what it knows first hand: the repository, ref and commit
  it checked out and the event that started the run; the libraries and the commits they were used
  at; the Dockerfile, target, platforms, build arguments and the IDs (never the values) of the build
  secrets; the runner, the builders, and the run and step attempt; and the base images by digest.
  Kept as the `provenance` artifact; attached to the image (signed) when the step signs.
- *The builder's*, which BuildKit attaches to the image's index: what it observed while building,
  including whether the build had network access.

What it does not vouch for: a Dockerfile runs code, which may download anything, so a build is not
hermetic, and the builder's statement says so. The run's earlier steps run code too and may change
the workspace: the source is "this commit, as the run's earlier steps left it".

**Several platforms.** A builder builds for its own architecture, and for others only through
emulation, which needs `binfmt_misc` handlers installed on its machine (a privileged change to the
node, which Launchway does not make). Give each architecture a builder of its own, with
`platforms:`: a step that asks for several platforms is built on each builder at once, and the
images are pushed as one index, with each builder's provenance. A platform no builder of the
project builds is refused before anything starts, naming what the builders build.

**On the run page**, the Images tab shows each image: its digest, the pushed name and tags (or why
nothing was pushed), size, platforms, whether and with which key it was signed, the provenance and
the SBOM (links to the artifacts), the scan's counts and policy, and how much of the build came from
the cache. **The Images page** finds the runs that built a digest (the index's or a platform's), and
the latest images of a repository or tag.

- **API**: `GET /api/v1/runs/{id}/images`, and
  `GET /api/v1/images?digest=sha256:…` or `?repository=host/path&tag=…` or `?project=…` (only projects
  the caller may see).
- **Runner settings** are `images:` in a runner's configuration; with the Helm chart,
  `runner.images` (unset by default: runners then take no image steps). `launchway run
  --images-config FILE` builds image steps on your own machine, with an `images:` section of your own.

Image steps and digest changes need schema 15; use them once every server and runner is upgraded.
