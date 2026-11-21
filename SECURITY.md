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

Please report security problems privately through GitHub's
[private vulnerability reporting](https://github.com/jk-magic-creations/launchway/security/advisories/new)
rather than in a public issue. We will acknowledge the report within 3 working days, give a first
assessment within 10, and aim to fix confirmed high and critical problems within 30 days, telling you
before anything is published. Fixed problems are published as GitHub security advisories, with credit
if you want it. Please give us a reasonable time to fix a problem before you disclose it. The
contact details are also in [security.txt](docs/public/.well-known/security.txt).

## Supported versions

Fixes go into the latest minor release; security fixes also go into the minor release before it
([SUPPORT.md](SUPPORT.md)). Until a first release is published, only the `main` branch is supported.

## How Launchway checks itself

Dependency, secret, static-analysis, image, chart and fuzzing scans run on every change; the list,
the policy and the latest results are in [How Launchway is scanned](docs/security/how-launchway-is-scanned.md).
