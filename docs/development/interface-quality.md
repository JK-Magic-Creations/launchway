# Quality bar for the API, the MCP server and the command line

What every interface other than the web page promises, how each promise is tested, and what is still open. It is for people who add a route, a tool or a command. The web interface's rules are in [Design principles for the interface](../contributing/design-principles.md).

## The HTTP API

| Promise | How it is kept | Tested by |
|---|---|---|
| Every error answer has the same three parts: `error` (what went wrong, in a plain sentence), `code` (a stable lower-case word a program can branch on) and `next` (what to do) | `writeJSON` adds `code` and `next` to every body of status 400 or higher that has an `error`, from the status; a handler that knows better puts its own first | `TestErrorAnswersHaveOneShape`, `TestErrorAnswersKeepWhatTheHandlerAdded` |
| Status codes mean what they say: 400 the request is wrong, 401 not signed in, 403 not allowed, 404 not found or not visible, 409 conflicts with the current state, 410 gone, 422 well formed but not valid (pipeline and input problems), 429 slow down, 503 try again | `Server.fail` maps the engine's errors; a route answers with these only | The end-to-end tests of each area |
| One description of the API: OpenAPI 3.1 at `docs/public/api/openapi.yaml`, made from the route table and the HTTP API reference, so it cannot lack a route or list one that is not there | `LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/api -run TestOpenAPI` rewrites it | `TestOpenAPIIsCurrent`, `TestOpenAPIIsValidAndMatchesTheRoutes` (valid structure, unique operation ids, declared path parameters, resolvable references, a success and the error shape for each operation, the access class and audit action of each route) |
| Times are Unix milliseconds, as whole numbers, in every field named `*_at` | Convention | Reviewed |
| Lists that can be long say where the next page starts (`limit` with `cursor` or `before`) | Convention | Reviewed |
| No breaking change inside `v1`: fields are added, never removed or retyped; a break gets a new prefix | Convention, stated in the description | Reviewed |
| Rate limits: failed authentication, sign-in checks and MCP calls answer 429 with `Retry-After`; a 503 carries `Retry-After` too | `limits.go`, `unavailable.go`, `mcp.go` | The tests of each |
| Idempotency: a webhook call with an `Idempotency-Key` answers with the run the first call started | `hooks.go` | The webhook tests |

## The MCP server

Every tool says in a sentence of at least six words what it does and when to use it; has annotations with a title, `readOnlyHint`, and for a tool that changes things `destructiveHint` and `openWorldHint`; has an input schema in which every field has a description an assistant can read; has an output schema, so results are structured; and fails with a message in plain words as a tool error, not a protocol error. `TestMCPToolsMeetTheQualityBar` and `TestMCPErrorsAreSentencesAnAssistantCanActOn` check all tools, so a new one cannot arrive without them.

## The command line

| Promise | Tested by |
|---|---|
| `-h` or `--help` on every command prints its help and exits 0, with no server and no sign-in | `TestEveryCommandHasHelpThatNeedsNoServer` |
| Every read command takes `--json` | Reviewed with the parity table (`internal/parity`) |
| Exit codes are documented in `docs/reference/cli.md` and are the ones used: 0 success, 1 failed, 2 written wrongly or not signed in, 3 canceled, 4 the server refused or could not be reached, 5 timeout, 130 interrupted | `TestExitCodesAreDocumented`, `TestExitCodesOfCommonCases` |
| No color off a terminal, with `NO_COLOR` set (and not empty) or with `TERM=dumb` | `TestLocalRunHasNoColorOffATerminalAndStatusInWords` |
| Output reads without color: every outcome is a word (`succeeded`, `failed`), and the symbols and the column rule are decoration | Same test |
| An error says what to do: the server's `next` is printed after the message | Reviewed; `client.go` |

## Gaps and checklist

Gaps found when the bar was written. Fixed ones are checked.

- [x] API errors had only `error`; now `code` and `next` everywhere.
- [x] No machine-readable description of the API; now OpenAPI, tested against the routes.
- [x] MCP input fields without descriptions (21 fields in 13 tools); now all described and tested.
- [x] `--help` on a client command asked for a sign-in; now it needs none and exits 0.
- [x] `NO_COLOR` set to an empty string counted as set; now only a non-empty value counts, and `TERM=dumb` turns color off.
- [ ] The OpenAPI file has no request or response body schemas, only the shared error. Add them route by route, from the Go types, and make the test fail when a response has none.
- [ ] Rate-limit headers are only `Retry-After`. Decide whether to send `RateLimit` headers (RFC 9331 draft) on the limited routes.
- [ ] Idempotency keys exist only on webhooks. Decide whether `POST .../runs` takes one too.
- [ ] `--json` is not on every command that reads: `launchway run` (local run), `login` and `version` print text only. Add `--json` to `version` at least.
- [ ] The list endpoints do not all take `limit` and a cursor: `GET /api/v1/runs` takes `limit` only. Make them consistent and test it.
- [ ] A test that reads every error string in `internal/api` for plain language (no Go error text such as `context deadline exceeded` reaching a person).
- [ ] The command line's local run prints box-drawing characters as a column rule. Offer `--plain` that prints `step: text` for a screen reader that reads every character.
