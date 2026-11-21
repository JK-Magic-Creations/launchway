# Monitor Launchway

This guide says how to watch Launchway itself: the health and readiness endpoints of servers and
runners, what the logs contain, how to use the audit trail, and what to alert on. It is for
operators.

> **Verified** on 2026-11-19 against commit f717337: every endpoint below with `curl`, on servers
> using SQLite and PostgreSQL 17; `/readyz` while the server was stopping and while its PostgreSQL
> container was stopped; a runner's `--health-listen` endpoints before it reached the server, while
> connected, and after its server stopped; the log lines quoted; audit queries. **Not verified:**
> the `incompatible` answer of `/readyz` (quoted from the code); log shipping into any particular
> system.

## What you need

- A running server, and the address your monitoring system can reach it on. See
  [Install on one server](install-single-server.md).
- For audit queries: an API token of someone who is an admin of every project. See
  [Sign-in and roles](oidc-and-roles.md#api-tokens).

## Health and readiness of a server

Both endpoints need no sign-in and answer JSON.

| Endpoint | Means | `200` | Otherwise |
|---|---|---|---|
| `GET /healthz` | the process is up | always, while it serves | no answer |
| `GET /readyz` | it can do useful work | the database answered within 2 seconds, its schema suits this build, and the server is not stopping | `503` |

```sh
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8080/readyz
```

```text
{"status":"ok"}
{"schema":{"code":21,"database":21,"minimum":7},"status":"ok"}
```

`schema` says which database schema this build knows (`code`), where the database is
(`database`), and the oldest build schema that may run against it (`minimum`).

`/readyz` answers `503` with a `status` and an `error`:

| `status` | When | Example `error` |
|---|---|---|
| `stopping` | the server received SIGTERM | `the server is shutting down` |
| `read_only` | the database answers but takes no writes: a standby (in recovery) or one set read-only | `the database is a standby (in recovery): it follows a primary and cannot take writes` |
| `unavailable` | the database did not answer | `the database did not answer: failed to connect to ...: connection refused` |
| `incompatible` | a newer build applied a migration this build cannot work with | `the database is at schema N, which needs a Launchway build that knows schema M or newer; this build knows schema K. Run a newer version` |

While the database is down, `/healthz` still answers `200`: the process keeps running and recovers
by itself when the database comes back. Use `/healthz` for "restart it if it hangs" (a liveness
probe) and `/readyz` for "send it traffic" (a load balancer or readiness probe). On SIGTERM a
server answers `stopping` at once and keeps serving for `server.drain_delay` (default `0s`), so
set that to at least your load balancer's check interval when you run several servers.

`GET /api/v1/info` also needs no sign-in. It gives the version, whether sign-in is on, and the
schema, which helps to spot servers left on an old version:

```json
{"auth_mode":"oidc","schema":{"code":21,"database":21,"minimum":7},"version":"f717337"}
```

## Health and readiness of a runner

A runner on another machine serves the same two paths, in plain text, when started with
`--health-listen`:

```sh
launchway runner --server https://launchway.example.com --health-listen 127.0.0.1:8081
curl -s http://127.0.0.1:8081/readyz
```

| Endpoint | `200` `ok` when | `503` with |
|---|---|---|
| `GET /healthz` | the process is up | no answer |
| `GET /readyz` | the server answered within the last two minutes | `the server has not answered yet`, or `the server last answered 2m3s ago` |

A runner whose token is wrong never gets an answer it counts, so its `/readyz` stays at
`the server has not answered yet`. Without `--health-listen` a runner serves nothing. The built-in
runner of a server has no endpoints of its own.

In the UI, **Runners** lists every runner with its executor, version and when it was last seen;
`GET /api/v1/runners` gives the same with `online` true or false (any signed-in person with a role
may read it).

## Prometheus metrics

There is no `/metrics` endpoint: Launchway does not export Prometheus metrics yet. See the
[roadmap](../roadmap-list.md). Until then, probe the endpoints above, read the logs, and query the API.

## Logs

Servers and runners write logs to standard error, one line per event. There is no access log of
HTTP requests, and no option for JSON.

A server writes `key=value` text:

```text
time=2026-11-19T20:07:00.829-06:00 level=INFO msg="runner started" name="build-1 (built in)" executor="container (podman)" concurrency=2 server=http://127.0.0.1:8080
time=2026-11-19T20:01:24.256-06:00 level=INFO msg="step finished" run=run_06gzdby06aahy4j3ekhg step=hello attempt=1 outcome=succeeded
time=2026-11-19T20:09:08.519-06:00 level=WARN msg="a sign-in failed" err="exchanging the code: oauth2: \"invalid_client\" \"Invalid client credentials.\""
```

At start-up it also prints a block saying where it listens, the number of projects, the data
directory, the database (password hidden) and where the runner token is.

A runner started with `launchway runner` writes the date first, without `key=`:

```text
2026/11/19 20:02:09 INFO runner started name=build-2 executor="container (podman)" concurrency=2 server=https://launchway.example.com
2026/11/19 20:02:21 WARN could not reach the server; retrying err="lease: a valid runner token is required (HTTP 401)" in=1s
```

Logs never contain secret values, tokens or session ids. Requests that fail on the server answer
`something went wrong on the server; its log has the details`, and the details are in a
`level=ERROR msg="request failed"` line.

Step output is not in these logs: it is stored with the run, and read in the UI or with
`launchway runs logs <run-id>`.

## The audit trail

The audit trail records every change, every refused request by someone signed in, every refused API
token, every webhook call, every sign-in, failed sign-in and sign-out. Admins of every project read
it on the **Audit trail** page or through the API:

```sh
curl -s -H "Authorization: Bearer $LAUNCHWAY_TOKEN" \
  'https://launchway.example.com/api/v1/audit?action=auth.sign_in&limit=100'
```

Each entry has `at` (Unix milliseconds), `user`, `source` (the client's address), `action`,
`project`, `target`, `outcome` (`ok`, `denied` or `failed`) and `detail`. Filter with `user`,
`project`, `action`, `from`, `to`, `limit`, and page with `before` set to the previous answer's
`next_before`.

Useful for monitoring:

| Look for | Means |
|---|---|
| `action=token.use`, `outcome=denied` | unknown, revoked or expired tokens, or tokens used beyond their limits |
| `action=auth.sign_in`, `outcome=failed` | sign-ins the provider's answer did not pass; `detail` says why |
| `detail` ending in `20 failures within 1m0s, so this address is refused for the rest of it` | an address hit the limit of 20 failed attempts a minute |
| `action=webhook.git` or `webhook.run`, `outcome=denied` | webhook calls with a wrong secret, or for a project or trigger that does not exist |
| `action=policy.waive` | someone waived a policy rule |

The trail is kept until you delete old rows from the `audit` table yourself. See
[Sign-in and roles](oidc-and-roles.md#the-audit-trail).

## What to alert on

| Signal | Check | Suggested threshold |
|---|---|---|
| A server is not ready | `GET /readyz` is not `200` | for 1 minute |
| A server is down | `GET /healthz` does not answer | 2 checks in a row |
| A runner lost its server | runner `GET /readyz` is `503` | as it happens (it already waits 2 minutes) |
| Too few runners | `GET /api/v1/runners`: count of `online: true` | below what you expect, for 5 minutes |
| Servers on different versions | `GET /api/v1/info` `version` differs between servers | outside an upgrade |
| Database trouble | log lines `level=ERROR` with `failed to connect`, `the database does not answer yet` | any, for 1 minute |
| Repositories cannot be read | log lines `msg="polling a project"` | the same project for 10 minutes |
| The sign-in provider is unreachable | log line `the sign-in provider cannot be reached yet` | any |
| Policy sources | log lines `a policy source was refused` (runs are refused) or `could not reach a policy source` | any |
| Servers with different policies | log line `another server started recently with different policies in its configuration` | any |
| Brute force | audit `token.use` denied, or 429 refusals | more than you normally see |
| Disk | the data directory and the database | 80% full |

Runs that wait for a runner that never comes show a hint on the run (`runner_hint` in the API),
such as for image steps when no runner can build images. A deploy step fails with
`no deploy runner serving target <target> took the step within <timeout>`.

## What next

- [Troubleshooting](troubleshooting.md)
- [Back up, restore and upgrade](backup-restore-upgrade.md)
- [Install on Kubernetes](install-kubernetes.md), which wires these endpoints into probes
- [HTTP API reference](../reference/http-api.md)
