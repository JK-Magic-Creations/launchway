# Git hosting acceptance suite

Executable form of AT-HG-01 to AT-HG-31 and AT-GAP-9 (design repository, `docs/32-replacing-homelab-gitea.md`, story S40-01).
The same Go tests run against Launchway's own Git hosting (switch it on with the installation setting `forge.hosting`) and
against any other compatible Git host, such as the one being replaced (it must pass: that proves the tests). Nothing is skipped by silence: every test records the values it compared, and every part it does not
cover is listed in the report with the reason.

```sh
export LW_ACCEPT_URL=http://192.168.1.242:3000 LW_ACCEPT_TOKEN=...      # token from Vault, never on a command line
export LW_ACCEPT_URL_ALT=http://git.home.jk-magic-creations.com          # optional second address
LW_ACCEPT_REPORT=/tmp/report timeout -k 30s 12m go test -count=1 -timeout 10m -v ./test/acceptance/githost/
```

Without `LW_ACCEPT_URL` every test skips, so `go test ./...` is unaffected. The variables are listed in `doc.go`.
Against Launchway add `LW_ACCEPT_FLAVOR=launchway`; the default flavor is `gitea`, the host kind the suite was first proven against (turns on the checks only Launchway must meet, such as `/metrics`).

The tests create only repositories named `lw-acceptance-<random>` under the token's own user and delete exactly those. They
never touch another repository, setting or user. The token travels in the environment of the Git processes, never in an
argument or URL, and every line of output is scrubbed of it.

## What Launchway must provide

The suite drives the host only through these Gitea-compatible surfaces. Each is a requirement on Launchway's forge
(stories S40-02 to S40-08, S34-12 and S34-38 or the GAP-10 shim):

| Test | Launchway must provide |
|---|---|
| 01, 02, 24 | Smart HTTP at `/<owner>/<repo>.git` and `/<owner>/<repo>`: clone, fetch, push, `push --mirror`, protocol v2, shallow, `--filter=blob:none` (needs `uploadpack.allowFilter`), non-fast-forward refusal, 50 MB and 200 MB pushes; `POST /api/v1/user/repos` (or `/admin/users/{u}/repos`) and `DELETE /api/v1/repos/{o}/{r}` |
| 04 | `POST /api/v1/repos/migrate` with `mirror: true`, `mirror_interval`, `POST .../mirror-sync`, mirror repositories refuse pushes |
| 05 | `POST /api/v1/repos/{o}/{r}/push_mirrors` with `sync_on_commit`, `POST .../push_mirrors-sync` |
| 06 | `PATCH` repository `archived`; pushes refused while archived |
| 07, 18 | Private by default; anonymous and wrong-token requests refused with the same answer for existing private and unknown names; per-repository read-only tokens (LW_ACCEPT_RO_*); `Authorization: Basic user:token` and `Authorization: token T` |
| 08 | `POST .../tag_protections` (or equal) that refuses force-move and delete of a `v*` tag |
| 09, 10, 16 | Pull requests (create, rebase/replay merge, list with state and timestamps), issues with labels, milestones, comments, and commit statuses with a combined state: `/pulls`, `/issues`, `/labels`, `/milestones`, `/statuses/{sha}`, `/commits/{sha}/status` |
| 11 | `GET /settings/repository`, `/releases`, `/wiki/pages`, `/packages/{owner}` answering with empty lists |
| 14 | Webhooks `POST .../hooks` of type `gitea`, a signed (`X-Gitea-Signature` or `X-Hub-Signature-256`, HMAC-SHA256) `push` payload with `ref`, `after` and `repository.full_name`, delivered within 15 s to a private address (the homelab's receivers are private) |
| 17 | A sign-in start path that redirects to the OIDC provider |
| 19, 25 | `/api/healthz` and `/api/v1/version` under 1 s; `/metrics` (Launchway) |
| 20, 30 | The read API Beacon, mcp-accounts and the Blackbox probe use (`/version`, `/user`, `/repos/search`, `/commits`, `/contents`, `/branches`, `/git/trees`, `/compare`), `/raw/branch/<b>/<path>` and `/archive/<ref>.tar.gz` and `.zip` |
| 26 | p95 clone under 2 s and push under 1 s on a seeded repository |

`census/census.py` is the AT-GAP-9 tool: it summarises a window of Gitea access logs from Loki or a log file.
