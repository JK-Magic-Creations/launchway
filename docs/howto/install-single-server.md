# Install Launchway on one server

This guide sets up Launchway on one Linux server or virtual machine, for a team: the binary or the
container image, a configuration file, a database, a systemd service, and HTTPS through a reverse
proxy. It is for operators. For a laptop, see [Install on a laptop](install-laptop.md); for several
servers on Kubernetes, see [Install on Kubernetes](install-kubernetes.md).

> **Verified** on 2026-11-19 against commit f717337: `make all` and `make image` (Podman 6.1) from a
> clean clone; the server on SQLite and on PostgreSQL 17; the refusal to listen without sign-in;
> the server as a transient systemd user unit (`systemd-run --user`) including `/readyz` during a
> stop; `systemd-analyze verify` on the unit below; the image run with Podman with a Station (runner) on the
> host; nginx 1.29 in a container in front of it with TLS, `trust_proxy`, an unbuffered
> `/api/v1/stream`, `Secure` cookies and a Station connecting through it. **Not verified:** the unit
> as a system service with `User=launchway` (no root on the test machine); rootless Podman for the
> built-in Station under that system service; Docker instead of Podman; a Caddy configuration.

## What you need

- A Linux server with a fixed name, for example `launchway.example.com`, and a TLS certificate for it.
- To build from source: Go 1.27 or later, Node 24 or later, Git and `make`.
- Git 2.38 or newer on the server, if you turn on Launchway's own Git hosting (the container image has it;
  `launchway doctor git` checks a server). Everything else works with any git.
- To build the container image instead: Podman (or Docker) only.
- For steps in containers on this server: Podman or Docker. Steps can also run on other machines;
  see [Connect a Station](connect-runner.md).
- An OpenID Connect provider for sign-in, before anyone else can reach the server. See
  [Sign-in with OpenID Connect and roles](oidc-and-roles.md).
- Optional: a PostgreSQL database (version 17 was tested). Without one, Launchway uses an embedded
  SQLite file.

## Get the binary

### From a release

Releases publish static binaries for Linux and macOS (amd64 and arm64), signed, with an SBOM each.
Download the binary with `SHA256SUMS` and its signature, and check both before installing:

```sh
v=1.4.0
base=https://github.com/JK-Magic-Creations/launchway/releases/download/v$v
curl -fsSLO "$base/launchway-$v-linux-amd64" -O "$base/SHA256SUMS" -O "$base/SHA256SUMS.sigstore.json"
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/(?i:jk-magic-creations/launchway)/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c SHA256SUMS
mv "launchway-$v-linux-amd64" launchway && chmod +x launchway
```

[Verifying downloads](../security/verify-downloads.md) explains the checks. No release has been
published yet: until the first one is, build from source.

### From source

Clone the repository and build the web UI and the binary:

```sh
git clone https://github.com/jk-magic-creations/launchway.git
cd launchway
make all
bin/launchway version
```

```text
launchway f717337
```

`make all` runs `npm ci` and `npm run build` in `web/`, then builds a static binary
(`CGO_ENABLED=0`) at `bin/launchway`. The UI is compiled into the binary, so `bin/launchway` is
the only file you need to copy to the server. The same binary is the server, the Station and the
command line.

Install it, and create a system user and the configuration directory:

```sh
sudo install -m 0755 bin/launchway /usr/local/bin/launchway   # or ./launchway from a release
sudo useradd --system --home-dir /var/lib/launchway --shell /usr/sbin/nologin launchway
sudo install -d -m 0750 -o root -g launchway /etc/launchway
```

The systemd unit below creates `/var/lib/launchway` itself.

## Or use the container image

Releases publish `ghcr.io/jk-magic-creations/launchway` for linux/amd64 and linux/arm64, signed,
with SBOM and provenance attestations (see [Verifying downloads](../security/verify-downloads.md#check-an-image)).
To build it yourself instead:

```sh
make image
```

This builds the `Dockerfile` at the top of the repository with Podman and tags it `launchway:<version>`
(Podman shows it as `localhost/launchway:<version>`). Use Docker with `make image CONTAINER=docker`,
and another name with `make image IMAGE=registry.example.com/launchway:1.0.0`.

The image holds the binary, Git, OpenSSH's client and CA certificates, and nothing else: no shell
and no package manager. It runs as user and group `65532`, works in `/var/lib/launchway`, exposes
port `8080`, and starts `launchway server --config /etc/launchway/launchway.yaml`. Its OCI labels
name the version, the full commit it was built from and the commit's time; `make image` refuses to
build outside a Git clone unless `REVISION` and `CREATED` are given.

## Write the configuration

Create `/etc/launchway/launchway.yaml`. Every setting is described in the
[server configuration reference](../reference/server-config.md); unknown settings are refused, so
a typing mistake stops the server with the line it is on.

```yaml
# /etc/launchway/launchway.yaml
listen: 127.0.0.1:8080          # the reverse proxy on this machine connects here
data: /var/lib/launchway        # mirrors of repositories, workspaces, artifacts, the runner token
server:
  trust_proxy: true             # only because every client comes through the reverse proxy
  drain_delay: 0s
auth:
  mode: oidc                    # see the sign-in guide
  oidc:
    issuer: https://sso.example.com/realms/acme
    client_id: launchway
    client_secret_env: LAUNCHWAY_OIDC_SECRET
    redirect_url: https://launchway.example.com/auth/callback
  access:
    - { group: platform-admins, role: admin }
    - { group: developers,      role: operator }
projects:
  - name: shop
    repo: https://git.example.com/team/shop.git
    branch: main
    poll: 30s
runner:
  executor: podman              # the built-in runner; or enabled: false, and runners elsewhere
  concurrency: 2
```

Keep secrets out of this file. Put them in an Airfield (environment) file that only root and the service can
read:

```sh
sudo install -m 0640 -o root -g launchway /dev/null /etc/launchway/launchway.env
sudoedit /etc/launchway/launchway.env
```

```sh
# /etc/launchway/launchway.env
LAUNCHWAY_DATABASE=postgres://launchway:CHANGE-ME@db.example.com:5432/launchway?sslmode=verify-full
LAUNCHWAY_OIDC_SECRET=CHANGE-ME
```

Relative paths in the configuration file are relative to the file. With no `data`, the data
directory is `launchway-data` in the working directory, so always set it.

## Choose a database

| | SQLite (the default) | PostgreSQL |
|---|---|---|
| How | no `database` setting: `launchway.db` in the data directory | `database: postgres://...`, or `LAUNCHWAY_DATABASE`, which wins over the file |
| Servers | one | one or more sharing the database |
| Suits | trying it out, small teams | anything you would back up and keep |

The server applies database migrations itself when it starts, and logs each one. It waits up to
`database_wait` (default `60s`) for the database to answer; a server that gives up stops with
`launchway: connecting to the database: ...`. See [Troubleshooting](troubleshooting.md#the-server-does-not-start).

Back up the database and the data directory; see
[Back up, restore and upgrade](backup-restore-upgrade.md). The database holds runs, logs and the
audit trail, but never secret values, session ids or API tokens (only hashes of the last two).

> [!WARNING]
> The SQLite file is created readable by everyone (`0644`). Keep the data directory itself closed
> (the unit below sets `StateDirectoryMode=0750`).

## Decide who can reach it

Without sign-in (`auth.mode: none`, the default), anyone who can reach the server has full
rights. Such a server refuses to listen anywhere but on a loopback address:

```sh
launchway server --listen 0.0.0.0:8080
```

```text
launchway: refusing to listen on 0.0.0.0:8080 without sign-in.
With auth.mode "none" there is no sign-in: anyone who can reach this address could
start pipelines, which run code on the runners, and approve deployments. Either
  - listen on a loopback address, such as 127.0.0.1:8080; or
  - turn on sign-in with auth.mode: oidc (see "Sign-in and access" in the README); or
  - if something in front of Launchway already decides who may reach it, set
    auth.allow_anonymous: true
```

Turn on sign-in, as in the example above. Use `auth.allow_anonymous: true` only if something in
front of Launchway already decides who may reach it; the UI then shows "No sign-in" on every page.

> [!NOTE]
> The command line (`launchway runs`, `launchway login`) needs an API token, and API tokens need
> sign-in. On a server without sign-in, use the web UI.

## Run it with systemd

```ini
# /etc/systemd/system/launchway.service
[Unit]
Description=Launchway server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=launchway
Group=launchway
EnvironmentFile=/etc/launchway/launchway.env
ExecStart=/usr/local/bin/launchway server --config /etc/launchway/launchway.yaml
WorkingDirectory=/var/lib/launchway
StateDirectory=launchway
StateDirectoryMode=0750
Restart=on-failure
RestartSec=5s
TimeoutStopSec=60s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now launchway
journalctl -u launchway -f
```

When it starts, the server prints where it listens, where its data is and which database it uses
(with the password hidden):

```text
  Launchway f717337 is listening on http://127.0.0.1:8080 (sign-in with https://sso.example.com/realms/acme)
  1 project(s), data in /var/lib/launchway, PostgreSQL database postgres://launchway:xxxxx@db.example.com:5432/launchway?sslmode=verify-full
  Runner token for other machines: /var/lib/launchway/runner-token
```

On `systemctl stop` the server answers `503` on `/readyz` at once, keeps serving for
`server.drain_delay`, then gives requests in flight up to 15 seconds. Steps that were running are
run again when a server starts. `TimeoutStopSec=60s` leaves room for that and for the built-in
Station, which gets up to 30 seconds.

The built-in Station starts step containers as the service user. With `executor: podman`, that
user needs rootless Podman to work (entries in `/etc/subuid` and `/etc/subgid`, and its home
directory, here `/var/lib/launchway`, writable). If you would rather not run builds on the server,
set `runner: { enabled: false }` and [connect Stations](connect-runner.md) on other machines; they
read the token from `/var/lib/launchway/runner-token`, or you can set `LAUNCHWAY_RUNNER_TOKEN` in
the Airfield file.

## Or run the container with Podman

In a container, the server must listen on `0.0.0.0` to be reached through a published port, so it
needs sign-in (or `auth.allow_anonymous: true`). Turn the built-in Station off: the image has no
shell and no container runtime, so it could not run steps. Give the Station token yourself, so that
Stations on other machines can use it:

```yaml
# launchway.yaml for the container
listen: 0.0.0.0:8080
data: /var/lib/launchway
runner:
  enabled: false
auth:
  mode: oidc
  # ... as above
projects:
  - name: shop
    repo: https://git.example.com/team/shop.git
    branch: main
```

```sh
openssl rand -hex 24 > runner-token
podman volume create launchway-data
podman run -d --name launchway \
  -p 127.0.0.1:8080:8080 \
  -e LAUNCHWAY_RUNNER_TOKEN="$(cat runner-token)" \
  --env-file /etc/launchway/launchway.env \
  -v ./launchway.yaml:/etc/launchway/launchway.yaml:ro,Z \
  -v launchway-data:/var/lib/launchway \
  localhost/launchway:f717337
```

Then start a Station on a machine with Podman or Docker:

```sh
LAUNCHWAY_RUNNER_TOKEN="$(cat runner-token)" launchway runner \
  --server https://launchway.example.com --name build-1 --executor podman
```

Things to know about the image:

- Projects must be Git URLs (`https://` or SSH). A local path fails, because Git needs a shell to
  read a repository on disk, and the image has none.
- The data directory must belong to user `65532`. A named volume does this for you. For a
  directory on the host with rootless Podman, run `podman unshare chown 65532:65532 DIR` first;
  otherwise the server waits for `database_wait` and stops with
  `unable to open database file (14)`.

## Put it behind a reverse proxy with TLS

The proxy must:

- pass the `Host` header through unchanged, port included (the CSRF check compares `Origin` with it);
- set `X-Forwarded-Proto` and add the client's address as the last entry of `X-Forwarded-For`;
- not buffer `/api/v1/stream`, the live event stream (server-sent events, with a comment every
  15 seconds);
- accept large request bodies from Stations: step results carry test and SARIF reports (about
  80 MiB with the default limits) and Cargo (artifacts) arrive in 8 MiB pieces;
- keep requests open for at least 30 seconds: Stations wait up to 25 seconds for work.

Set `server.trust_proxy: true` only when clients cannot reach Launchway except through the proxy.
Then Launchway marks cookies `Secure` from `X-Forwarded-Proto` and takes the client's address
(for the Logbook and the limit on failed sign-ins) from the last `X-Forwarded-For` entry.
Without it, `X-Forwarded-For` is ignored. Cookies are also `Secure` whenever `redirect_url` starts
with `https://`.

An nginx example:

```nginx
upstream launchway {
  server 127.0.0.1:8080;
}

server {
  listen 443 ssl;
  server_name launchway.example.com;
  ssl_certificate     /etc/nginx/tls/launchway.crt;
  ssl_certificate_key /etc/nginx/tls/launchway.key;

  # Runners send step results (with test and SARIF reports) and 8 MiB
  # artifact chunks through here.
  client_max_body_size 100m;

  location / {
    proxy_pass http://launchway;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 120s;
  }

  # The live event stream: never buffer it.
  location = /api/v1/stream {
    proxy_pass http://launchway;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 1h;
  }
}
```

Raise `client_max_body_size` if you raise `tests.max_upload_mib` or `findings.max_upload_mib`.

Check through the proxy:

```sh
curl -s https://launchway.example.com/healthz
curl -s https://launchway.example.com/api/v1/info
```

```text
{"status":"ok"}
{"auth_mode":"oidc","schema":{"code":21,"database":21,"minimum":7},"version":"f717337"}
```

After signing in through the proxy, the browser's `launchway_session` cookie should be `HttpOnly`,
`SameSite=Lax` and `Secure`, and entries in the Logbook should show the client's address, not
the proxy's.

## Check that it is healthy

| Endpoint | Answers `200` when | Otherwise |
|---|---|---|
| `GET /healthz` | the process is up | no answer |
| `GET /readyz` | the database answers within 2 seconds and the server is not stopping | `503`, with `status` `unavailable`, `stopping` or `incompatible` and an `error` |

```sh
curl -s http://127.0.0.1:8080/readyz
```

```text
{"schema":{"code":21,"database":21,"minimum":7},"status":"ok"}
```

Both need no sign-in. See [Monitoring](monitoring.md) for what to alert on.

## Open the firewall

Inbound, to the server:

| From | To | Why |
|---|---|---|
| Browsers, the command line, Stations | the proxy, port 443 | everything; Stations use the same address as people |
| Git hosts | the proxy, port 443 | only if you use webhooks (`/api/v1/projects/<name>/git-hook` or webhook triggers) |

Launchway itself listens on one port (`listen`). Keep it closed to everything but the proxy.

Outbound, from the server: your Git hosts (to poll projects and libraries), the OpenID Connect
provider (discovery, keys, token exchange), PostgreSQL if you use it, and S3-compatible storage
if Cargo are kept there.

Outbound, from Stations (the built-in one included): the server, the Git hosts, the image
Depots (registries) steps pull from, secret stores, and the Kubernetes API for the Kubernetes executor.
Stations never need inbound connections.

## What next

- [Sign-in with OpenID Connect and roles](oidc-and-roles.md)
- [Secrets](secrets.md)
- [Connect a Station](connect-runner.md)
- [Monitoring](monitoring.md)
- [Back up, restore and upgrade](backup-restore-upgrade.md)
- [Server configuration reference](../reference/server-config.md)
