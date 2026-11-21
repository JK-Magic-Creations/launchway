# Git hosting acceptance suite

Executable form of AT-HG-01 to AT-HG-31 and AT-GAP-9 (design repository, `docs/32-replacing-cluster-a-gitea.md`, story S40-01).
The same Go tests run against Launchway's own Git hosting (switch it on with the installation setting `forge.hosting`) and
against any other compatible Git host, such as the one being replaced (it must pass: that proves the tests). Nothing is skipped by silence: every test records the values it compared, and every part it does not
cover is listed in the report with the reason.

```sh
export LW_ACCEPT_URL=http://192.168.1.242:3000 LW_ACCEPT_TOKEN=...      # token from Vault, never on a command line
export LW_ACCEPT_URL_ALT=http://git.example.test          # optional second address
LW_ACCEPT_REPORT=/tmp/report timeout -k 30s 12m go test -count=1 -timeout 10m -v ./test/acceptance/githost/
```

Without `LW_ACCEPT_URL` every test skips, so `go test ./...` is unaffected. The variables are listed in `doc.go`.
Against Launchway add `LW_ACCEPT_FLAVOR=launchway`; the default flavor is `gitea`, the host kind the suite was first proven against (turns on the checks only Launchway must meet, such as `/metrics`).

The tests create only repositories named `lw-acceptance-<random>` under the token's own user and delete exactly those. They
never touch another repository, setting or user. The token travels in the environment of the Git processes, never in an
argument or URL, and every line of output is scrubbed of it.

## Running it against Launchway, in process

Launchway's auth mode `none` cannot issue tokens, so the suite cannot be pointed at a plain `launchway serve`. The repository
keeps the harness that does it, as a test helper: `internal/api/acceptance_harness_test.go` starts the signed-in test server with
Git hosting on, makes a token for an administrator and runs this suite against it with `LW_ACCEPT_FLAVOR=launchway`.

```sh
LAUNCHWAY_ACCEPTANCE=1 LAUNCHWAY_ACCEPTANCE_REPORT=/tmp/report \
  env -u SSH_ASKPASS go test -count=1 -timeout 15m -run TestHostingAcceptanceSuite -v ./internal/api/
# LAUNCHWAY_ACCEPTANCE_RUN=AT_HG_09 runs one test
```

Without `LAUNCHWAY_ACCEPTANCE` it skips and says why. It fails while any id fails, so it is a real check, and the ids below fail
until the Gitea-compatible routes exist.

Pull requests (AT-HG-09) are driven through Launchway's own routes (`/api/v1/repositories/OWNER/NAME/pulls`, merge by `rebase`)
when the flavor is `launchway`; `LW_ACCEPT_PR_API=gitea` makes the same test use the Gitea shape (`/repos/OWNER/NAME/pulls` with
`head` and `base`, merge with `Do`), which tools written for the host being replaced use and which Launchway does not serve.

### Where Launchway stands (in process, 21 November 2026)

| Result | Ids |
|---|---|
| pass | AT-HG-01, 02, 06, 07, 09, 15, 18, 24, 26 (and the catalog check) |
| skip, with the reason in the report | AT-HG-05 (needs the Gitea push-mirror route), 08 (needs the Gitea tag-protection route or `LW_ACCEPT_OTHER_USER`), 17 (no OIDC sign-in source in the test server), and the eleven cluster-a-only ids |
| fail, because a Gitea-compatible route is missing | AT-HG-04 `POST /repos/migrate`; 10 `POST .../labels`, `/milestones`, `/issues`, `/issues/{n}/comments`, `PATCH /issues/{n}`; 11 `GET .../releases`, `/wiki/pages`, `/packages/{owner}`, `/settings/repository`; 14 `POST .../hooks` (Gitea body, `gitea` type); 16 `POST .../statuses/{sha}`, `GET .../commits/{ref}/status` and `/statuses`; 19 `GET /api/healthz`, `/api/v1/version`; 20 `GET /version`, `/user` (exists), `/repos/search`, `/commits`, `/contents`, `/branches`, `/git/trees`, `/compare`; 25 `GET /api/v1/version`, `/metrics`; 30 `GET /raw/branch/B/PATH`, `/archive/REF.tar.gz`, `.zip`, `/repos/search`, `/git/trees` |

None of the failures is a pull request: AT-HG-09 is the only id about them, and it passes through Launchway's own routes. It would
fail with `LW_ACCEPT_PR_API=gitea` until `POST /repos/{o}/{r}/pulls`, `POST .../pulls/{n}/merge` (`Do`) and `GET .../pulls` exist in the
Gitea shape.

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
| 14 | Webhooks `POST .../hooks` of type `gitea`, a signed (`X-Gitea-Signature` or `X-Hub-Signature-256`, HMAC-SHA256) `push` payload with `ref`, `after` and `repository.full_name`, delivered within 15 s to a private address (the cluster-a's receivers are private) |
| 17 | A sign-in start path that redirects to the OIDC provider |
| 19, 25 | `/api/healthz` and `/api/v1/version` under 1 s; `/metrics` (Launchway) |
| 20, 30 | The read API Webapp, mcp-accounts and the Blackbox probe use (`/version`, `/user`, `/repos/search`, `/commits`, `/contents`, `/branches`, `/git/trees`, `/compare`), `/raw/branch/<b>/<path>` and `/archive/<ref>.tar.gz` and `.zip` |
| 26 | p95 clone under 2 s and push under 1 s on a seeded repository |

`census/census.py` is the AT-GAP-9 tool: it summarises a window of Gitea access logs from Loki or a log file.
