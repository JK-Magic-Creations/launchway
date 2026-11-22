# Security

Launchway is early software. Read this before letting anyone but yourself reach a server.

## Running it safely

- **Turn on sign-in** (`auth.mode: oidc`) for any server other people can reach, and grant roles
  with `auth.access`; see [Sign-in and access](docs/guide/access.md). Without sign-in,
  anyone who can reach the server can start pipelines, which run code on its runners, and approve
  deployments. Such a server refuses to listen on anything but a loopback address unless
  `auth.allow_anonymous: true` says something else already restricts who can reach it.
- **Serve it over HTTPS**, normally behind a reverse proxy, and set `server.trust_proxy: true` only
  if clients cannot reach Launchway except through that proxy.
- **Keep the runner token secret.** It lets a machine take work, and with it pipelines and the
  secret references they hold. It is separate from sign-in and from API tokens.
- **Protect the database.** It holds runs, logs and the Logbook. It never holds secret values,
  session ids or API tokens, only hashes of the last two, but anyone who can write to it can
  change who has done what.
- **Revoke the API tokens of people who leave.** Launchway cannot see that someone left your
  sign-in provider; their tokens keep their last known groups until they expire.

## What sign-in protects

Every UI and API call, except `/api/v1/info`, health checks and the runner API (which has its own
token), needs a session or an API token, and is checked against the caller's role on the project
concerned. Things you may not see answer `404`. Sessions and tokens are random 256-bit values
stored only as SHA-256 hashes. Cookie-authenticated requests that change something need a CSRF
header and a matching `Origin`. Every change and every sign-in is recorded in the Logbook.

## Reporting a problem

Please report security problems **privately**, through GitHub's
[private vulnerability reporting](https://github.com/JK-Magic-Creations/launchway/security/advisories/new),
rather than in a public issue, a discussion or a pull request. Say what you found, how to reproduce
it, which version or commit, and what you think an attacker gains. Do not include real secrets, and
do not test against servers you do not own. The contact details are also in
[security.txt](docs/public/.well-known/security.txt).

What to expect:

- **Acknowledgement** within 3 working days; a **first assessment** (accepted, needs more, or not
  a vulnerability, with the reason) within 10.
- **A fix** for confirmed high and critical problems aimed at within 30 days, lower severities in
  the normal release cycle. These are aims for a project run by one maintainer, not promises; if
  one will be missed, you will be told why.
- **Updates** while we work, and notice before anything is published.

### Disclosure

Coordinated disclosure: we ask for a reasonable time to fix a problem before you disclose it, and
no more than 90 days from your report unless we agree a different date together. When a fix is
released, we publish a GitHub security advisory (with a CVE where one applies), describing the
affected and fixed versions, and credit you if you want it. If a problem is being actively
exploited, we may publish sooner. Reports made in good faith, within this policy, that avoid
harming other people's data and services, will not be met with legal action.

There is no bug bounty.

## Supported versions

| Version | Receives security fixes |
|---|---|
| The latest minor release | yes |
| The minor release before it | yes |
| Older releases | no: upgrade |
| `main` (unreleased) | yes, as the next release |

Launchway is before 1.0: a minor release can change behavior, and its notes say how. Versioning and
what the project supports are in [SUPPORT.md](SUPPORT.md). If no release has been published yet
(a tagged release is a draft until the maintainer has checked it), only `main` is supported.

## Verifying what you run

Every release is built by the project's release workflow from a tagged commit, not by hand:

- `SHA256SUMS` has a keyless [Sigstore](https://www.sigstore.dev) signature by that workflow
  (`SHA256SUMS.sigstore.json`), checked with `cosign verify-blob`.
- Every binary, package and chart has a CycloneDX SBOM next to it.
- The release files and the container images have signed build provenance, checked with
  `gh attestation verify`; the images are signed, and carry SBOM and provenance attestations.
- The build is reproducible: the release can be rebuilt and compared byte for byte.

The exact commands, and how to rebuild a release, are in
[Verifying downloads](docs/security/verify-downloads.md). In production, refer to container images
by digest.

## How Launchway checks itself

Dependency, secret, static-analysis, image, chart and fuzzing scans run on every change; the list,
the policy and the latest results are in [How Launchway is scanned](docs/security/how-launchway-is-scanned.md).
