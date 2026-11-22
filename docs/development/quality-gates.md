# Quality gates

Launchway's engineering standard says that code follows best practice, is well tested, and is
easy to add to, change and modify. These gates make that a rule the build enforces. Each one
fails the build; none of them is advice.

Most gates use a **baseline**: what was already wrong when the gate was introduced is recorded and
tolerated, so the gate can be strict from day one. Only something *new* fails. A baseline may only
shrink. It never grows to make a change pass.

Run all the fast gates with one command:

```sh
make check        # formatting, vet, lint, architecture, originality, size ratchet, web lint, type check, web tests
make check-all    # the above, plus the bundle budget and the Go tests with the coverage floors
```

## The gates

| Gate | Command | Fails when | Baseline file |
|---|---|---|---|
| Format | `make fmt-check` | `gofmt` would change a file | none |
| Vet | `make vet` | `go vet` reports anything | none |
| Go lint | `make lint` | a finding appears that is not in the baseline | `quality/lint-baseline.json` |
| Architecture | `make arch` | a package imports one it may not | table in `internal/archtest/arch_test.go` |
| Originality | `make originality` | a comparison or an unlisted product name appears in code or docs | deny-list and allow-list in `internal/archtest/originality_test.go` |
| Vocabulary | `make vocabulary` | a forbidden word (British spelling, `org`, `tenant`, `audit log`, a hard-coded product word) returns in what people read | rules in `docs/vocabulary.json`, allow-list `quality/vocabulary-allow.json`, baseline `quality/vocabulary-baseline.json` |
| Size | `make ratchet` | a file over 1,000 lines or a function over 150 lines is new or grew | `quality/size-baseline.json` |
| Coverage | `make coverage-check` | a package's coverage is below its floor | `quality/coverage-floor.json` |
| Web lint | `make web-lint` | `eslint` or `tsc` reports something new | `web/eslint-suppressions.json` |
| Web tests | `make web-test` | a `vitest` test fails | none |
| Bundle size | `make web-size` | the JavaScript or CSS of the first load, or of the whole build, is larger than the budget | `web/bundle-budget.json` |

### Go lint

`.golangci.yml` turns on `errcheck`, `govet`, `staticcheck`, `ineffassign`, `unused`, `gosec`,
`bodyclose`, `contextcheck`, `noctx`, `sqlclosecheck`, `rowserrcheck`, `revive`, `gocyclo`
(over 30), `funlen` (over 150 lines or 100 statements), `dupl` and `misspell`. Tests have a relaxed
set: length, duplication and cleanup errors are allowed there.

`golangci-lint` is pinned (see `GOLANGCI_LINT_VERSION` in the Makefile) and installed into the
repository's own `.tools/bin` directory by `make tools`, never into your `GOPATH`.

The baseline holds 507 findings today, counted by file, linter and message, not by line number, so
editing a file does not disturb it. When you fix findings, run `make lint-baseline-update`: it
rewrites the file and refuses if anything would be added. The findings the baseline allows are the
work list for the refactoring steps: `contextcheck` and `noctx` (database calls without a context),
`sqlclosecheck` and `rowserrcheck`, and `gocyclo` and `funlen` (long functions).

Never silence a finding with `//nolint` unless the reason is written after it, for example
`//nolint:gosec // the path comes from the server's own configuration`.

### Architecture test

`internal/archtest` reads the imports of every non-test file under `internal/` and `cmd/` and
compares them with a table of allowed imports. Today's graph is the table. It fails when:

- a package or an import edge is not in the table (a new dependency has to be a visible, reviewed
  change to the table, with the reason in the pull request);
- an edge in the table no longer exists (so the table only shrinks);
- an edge breaks one of the rules below, even if it is in the table, unless it is a known exception.

The rules are the ones the code health audit calls harmful:

- leaf packages (`redact`, `ids`, `resources`, `hub`, `policy`, `auth`, `verify`, `testreport`,
  `blobstore`, `coverage`, `runnerauth`, `accept`) import nothing internal;
- runner-side packages (`runner`, `executor`, `imagebuild`, `verify`, `deploy`, `secrets`,
  `redact`, `resources`) must not import `internal/engine`;
- `engine` must not import `api`, `runner`, `source`, `config` or `cmd`;
- `api` must not import `config`, `runner` or `cmd`;
- only `cmd` imports `config`, and nothing imports `cmd`;
- production code must not import the test support packages.

Known exceptions, each with a TODO, are `runner` to `engine` and `deploy/argocd` to `engine`. Both
go away when the wire types move to a new `internal/wire` package. When the edge is gone, the test
fails until the exception is deleted, so it cannot come back unnoticed.

To add an edge on purpose, add it to `allowedImports` in the same pull request and explain why. A
pull request that adds an edge a rule forbids needs a design decision first.

### Size ratchet

Files over 1,000 lines and functions over 150 lines make code hard to review and to test. The
test (`TestSizeRatchet`) lists today's offenders in `quality/size-baseline.json`: 15 files (13 Go,
2 in the web UI) and 17 functions. It fails when a listed item grows, or a new one appears. It
looks at Go outside tests, and at `.ts` and `.tsx` files in `web/src`.

When you split or shorten one, run `make ratchet-update`. It lowers the baseline and refuses to
record growth. Prefer a new file or package to growing a large one.

### Coverage floors

`make coverage` runs every Go test (without the race detector) and writes the per-package result to
`bin/coverage.txt`. `quality/coverage-floor.json` holds each package's coverage rounded down to
0.1. `make coverage-check` fails when a package is below its floor, or when a new package, which
has no floor yet, starts below 70%.

Floors only move up. After adding tests, run `make coverage-update`: it raises floors, adds new
packages, and never lowers one. Commit the file with your change. The security-relevant packages
`auth`, `runner`, `executor` and `imagebuild` have low floors today; raising them is planned work.

### Web gates

In `web/`:

- `npm run lint` runs `eslint` with `typescript-eslint`, `react-hooks` and `jsx-a11y`. Findings
  that existed are recorded by ESLint's own bulk suppressions in `eslint-suppressions.json`. After
  fixing some, run `npm run lint:prune` to drop them from the file. Do not run `npm run
  lint:baseline` to make a new finding pass.
- `npm test` runs `vitest` with Testing Library in `jsdom`. `src/format.test.ts`,
  `src/router.test.tsx` and `src/components/ui.test.tsx` are the pattern: test what a person sees
  and does (roles, names, text), not how the component is built. A test file sits next to its
  source file and is called `<name>.test.ts` or `<name>.test.tsx`.
- `npm run size` builds the UI and fails when the JavaScript or CSS of the first load
  (`initial_*`: what index.html names and the chunks it imports statically) or of the whole build
  (`total_*`, a generous cap) is larger than `bundle-budget.json`. Shrink the first load with
  `React.lazy` per route; when it gets smaller, run `npm run size:update` (it never raises the
  budget).

## The oldest supported git

Git hosting runs the real `git` binary, and git changes between releases in ways a test on one
version never sees: git 2.47 reports an extra `HEAD` line to the `reference-transaction` hook for a
push to the checked-out branch, and a hosting server built on git 2.56 passed every test while
Launchway's own pipeline, in an image with git 2.47, failed 72. So the hosting tests also run
against the oldest git Launchway supports (the minimum in `internal/forge/hosting/gitcheck.go`,
stated in [Hosted repository storage](forge-storage.md#git-version)):

```sh
make test-git-compat   # internal/forge/storage and internal/forge/storagespike on the minimum git
```

The target compiles the two packages in the Go image the pipeline uses, then runs them in an image
that has only the minimum git, built from the release tarball by `scripts/git-compat/Dockerfile`
(its checksum is pinned; the first Sortie (run) builds the image, a few minutes). It uses Podman or Docker
(`CONTAINER=docker` to choose), and when neither is installed it says so and skips. It is not part
of `make check` because of the time and the container; run it when you change anything in
`internal/forge/storage`, and add it to a pipeline step on a Station (runner) that has a container tool.
When you raise the minimum, change `MinGit`, `GIT_VERSION` and `GIT_SHA256` together.

## Before you push

`make check` is fast, but it is not what the pipeline runs. Three pushes in a row once failed only in
the pipeline, each for an Airfield (environment) reason: an older git in the build image, a generated file that
was stale because only part of the suite had been run, and a step user with no account entry.
`make test-like-ci` closes that gap:

```sh
make test-like-ci   # the pipeline's own test step, in its image, as the pipeline runs it
make prepush        # make check, the secret scan of this commit's history, make docs, make test-like-ci
```

The secret scan (`make secrets-head`, the hosted Security workflow's scan limited to the commits you
are about to publish) is part of `prepush` because it looks at history: a test canary or a long table
line that trips a scanner rule is in every later scan once it is committed. It needs Podman or Docker
and the scanner image; without them it prints `SKIPPED` and the hosted workflow is the only check. A
fake value in a test should start with `CANARY-` (the scanner config allows that shape); a finding
that is a false positive in an already-published commit is added to the baseline only by a reviewer
(`BASELINE_INIT=1 scripts/security/gitleaks.sh --update`), or covered by a path or pattern in
`quality/gitleaks.toml` with its reason in a comment.

`test-like-ci` reads the image and the `run:` block of the `test` step from `.launchway/pipeline.yaml`,
so the two cannot drift, and runs them in a container (Podman or Docker; `CONTAINER=docker` to choose)
that differs from a normal local run in exactly the ways that bit us:

- the user is `1000:1000` with no entry in `/etc/passwd` (`podman --passwd=false`), and `HOME` is a
  temporary directory; then the user is given an account entry exactly as the Station's step pods give
  one (`internal/executor/user_entry.sh`, the same script, in the same image), so the ssh tests run as
  they do in the pipeline once the Station has that fix (`TEST_LIKE_CI_USER_ENTRY=0` leaves it out, to see
  what a step without it sees);
- the tree is `git archive HEAD`, a copy of what is committed, so a file that is uncommitted, or
  generated and never committed, is missing exactly as it is in the pipeline (the copy is a throwaway
  and writable, because the step writes `reports/`; your working tree is never touched);
- the module cache is mounted read-only, the root filesystem is read-only, all capabilities are
  dropped, and memory is capped at 8 GB (`TEST_LIKE_CI_MEMORY` to change it);
- the failure summary the pipeline prints (`scripts/ci/go-test-summary.sh`) is part of the step, so a
  failure reads the same here as there.

The exit status is the step's. When neither Podman nor Docker is installed it says so and skips. It
takes several minutes and several gigabytes of memory, so run it once, after `make check` passes and
your work is committed, and not in a loop.

## Timing tests and the race detector

Some tests assert a wall-clock limit: a scale test that must answer within 300 ms with 200,000
Sorties in the database, a speed test of a list of 50,000 work items, a "stops within ten seconds"
test. The race detector makes code 5 to 20 times slower (more for work that touches a lot of
memory), and the hosted CI machines are slower again, so a limit written for a normal build cannot
hold there. The rules:

- A limit is asserted exactly as written in a normal build (no `-race`). Nothing is relaxed there.
- Under `-race`, a "must finish within" limit is multiplied by `testlib.RaceFactor` (10) through
  `testlib.Slow(limit)` (package `internal/testlib`). A limit on the *least* a thing must take, such
  as a wait that has to happen, is never scaled.
- A scale or speed test whose slowdown differs a lot from query to query (the hosted measurements were
  2 to 3 times for the work-item queries and 24 to 36 times for the Sortie queries) does not assert its
  limits under `-race` at all: it still seeds its data, runs every call, checks every result and logs
  the times, and tests `testlib.SkipTimingUnderRace()` before it fails on time. In a normal build
  `TestAttentionAndSearchAtScale` stretches its 300 ms limits, by at most 8 times, on a machine that is
  measurably slower than the reference one (`testlib.Scaled` and `testlib.MachineFactor`, which time a
  fixed piece of CPU work on two goroutines); on a machine as fast as the reference the limit is exactly
  300 ms, and the test logs the factor it used. These are
  `TestAttentionAndSearchAtScale` (`internal/engine`) and `TestFiftyThousandItems` (`internal/work`).
- A test that is a long end-to-end run skips itself with `testing.Short()`, and the hosted race jobs
  run with `-short`. Those tests run in the full non-race run.

Skipped under `-short` (so not in the race jobs, but in the full run): `TestServicePlanDeliversEndToEnd`,
`TestStarterGoLibraryRunsGreen`, `TestStarterNodeLibraryRunsGreen`, `TestStarterPythonLibraryRunsGreen`,
`TestStarterGoServiceRunsGreenWithoutImage`, `TestStarterNodeAppRunsGreenWithoutImage`,
`TestStarterPythonServiceRunsGreenWithoutImage`, `TestStepRidesThroughAnOutage`,
`TestOutageLongerThanTheOldRetryWindow` (all `internal/api`), `TestKillTheServerAtEveryStepOfAPush` and
`TestPushToAServerThatNeverAnswersFailsWithinABound` (`internal/forge/storage`), plus the ones that
already did: `TestAttentionAndSearchAtScale`, `TestFiftyThousandItems`, the three end-to-end tests in
`cmd/launchway`, the live-latency test and the work-script test in `internal/api`.

### How the hosted jobs are split

`.github/workflows/ci.yml` runs, side by side:

| Job | What it runs | Race detector |
|---|---|---|
| `test` | `go vet ./...` and `go test -count=1 -timeout 18m ./...`: the pipeline's own `test` step, every test, every limit | no |
| `race` (10 matrix entries) | `scripts/ci/race-group.sh GROUP`: `api` in 4 shards, `engine` and `storage` in 2 each (by test name), `forge` (the rest of `internal/forge`), `rest` in 2 shards (by package); `-short`, 20 minute package timeout, 25 minute job timeout | yes |
| `postgres` (10 matrix entries) | `internal/source`, `internal/engine` in 4 shards and `internal/api` in 5 (by test name, `MODE=plain`: no race detector, no `-short`) against a PostgreSQL service; 20 minute package timeout, 30 minute job timeout | no |
| `containers` | `internal/executor` with real containers | no |
| `supply-chain` | vulnerabilities, licenses, script and workflow lint, charts, reproducible release build | n/a |

`internal/forge/storagespike` is not raced (it starts the test binary as each Git hook); the `test` job
runs it. To run one group the way the hosted job does, `scripts/ci/race-group.sh api:1/3` (it needs 8 GB
of free memory and several minutes; `DRY_RUN=1` prints what it would run). A result of the `test` job
is comparable with the in-cluster pipeline and with `make test-like-ci`; a result of a `race` job is a
different question (data races) and is not.

`release.yml` is gated on `checks` (vulnerabilities, licenses, scripts and workflows, charts, vet) and
`tests` (the same full non-race run as the `test` job). It does not wait for the race jobs, which run
on every push to `main`.

## Security gates

`make check` stays fast. The Inspections (security scans) need the network (vulnerability databases, images) or
take longer, so they have targets of their own: `make security` (govulncheck, osv-scanner and npm
audit, the secret scan of the Git history, charts and Dockerfile, short fuzzing), `make image-scan`,
`make web-csp`, `make dast` and `make sbom`. The product's pipeline and
`.github/workflows/security.yml` run the same targets. Each gate, its policy, its exception file
and the licenses of the tools are on [How Launchway is scanned](../security/how-launchway-is-scanned.md).
Baselines follow the same rule as above: `quality/gitleaks-baseline.json` may only shrink, and
exceptions in `quality/trivy-ignore.yaml` and `osv-scanner.toml` carry a reason and an expiry date.

## Updating a baseline legitimately

A baseline moves in one direction.

- **Down**: you fixed something. Run the update command for that gate (`make lint-baseline-update`,
  `make ratchet-update`, `make coverage-update`, `npm run lint:prune`, `npm run size:update`) and
  commit the changed file with the fix. A reviewer sees the number fall.
- **Up**: not allowed for lint and size. If a finding or a long function is truly necessary,
  restructure it, or explain it in the pull request and ask for a decision. For coverage, a floor
  may be lowered by hand only when code is deleted, and the pull request says so.
- **New package**: add it to the architecture table, and give it at least 70% coverage.

## The engineering standard

A change is done when all of the following are true.

**Behavior is tested.** Every new behavior has a test that would fail without it: a unit test for
logic, an integration test where it crosses a boundary (database, Station, HTTP, container). A bug
fix starts with a test that reproduces the bug. Tests assert what a user or caller can observe, not
how the code is built. Anything that touches the engine also shows what happens on failure,
restart and retry.

**Gates pass.** `make check` passes locally, and the pipeline runs the same gates and the full
tests; nobody merges a red build.

**Boundaries hold.** Packages depend downward only (the table is in `internal/archtest`). Wire
types will live in `internal/wire`. Do not import `engine` from `runner`, nor `api` from `engine`.
If you need a new edge, change the table in the same pull request and say why.

**Small units.** Files stay under 1,000 lines and functions under 150; the baseline only shrinks.

**Context everywhere.** Every function that does input or output takes a `context.Context` first
and passes it on. No `context.Background()` below `main` except in documented background loops.

**Errors.** Return errors with context (`fmt.Errorf("...: %w", err)`). Do not discard errors except
`Close` on read paths and `Rollback` after commit. No `panic` in request or run paths.

**Input is validated at the edge.** Bodies are size-limited, JSON is decoded strictly, IDs and
names are validated before use, SQL uses placeholders only. Parsers that take outside input get a
fuzz target.

**Authorization is in the table.** A new route is added to the route table with a permission class
and a row in the permission test; hidden resources answer like missing ones.

**Parity.** A new capability is in the HTTP API, the MCP server, the CLI, Terraform and the web UI,
or has a recorded exemption with a reason.

**Docs.** Public behavior is documented and the docs tests pass, in plain words.

**Migrations are additive.** Follow [Writing a migration](./migrations.md); servers of two
versions share a database.

**No duplication.** Search for an existing package or helper before writing one; reuse `testdb`,
`testlib`, `redact` and `secrets`.

**Outages.** Code that talks to the database or the server must survive a brief outage of either.
`testdb.ThroughProxy` puts a TCP proxy in front of PostgreSQL that drops every connection or delays
what passes; the engine tests have a wrapper that refuses or breaks SQLite connections, fails one
statement or loses the answer to a commit; the API tests have a switch that makes the server answer
503 or drop connections, a transport that sends every Station write twice, and a server that is
restarted behind a fixed address. Writes a Station makes must be safe to repeat.

**Dependencies.** A new module needs an approved license, a reason in the pull request, and passes
`govulncheck`.

**Web.** Components use the shared tokens in `styles.css`, call the API only through the typed
client, are keyboard-operable, have labels, and have a test for any logic.

**Review.** Someone other than the author reads the diff. The reviewer checks tests first, then
boundaries, then names.

**Speed.** Tests that take over 20 seconds go in the slow tier. Do not add `time.Sleep` to wait for
a result: use the fake clock or a polling helper with a deadline.

## Running the gates in the pipeline

`.launchway/pipeline.yaml` should run the same gates as `make check`. Add these steps (the Go step
needs network access once to install the pinned linter; the web step needs Node):

```yaml
  quality-go:
    description: Format, vet, lint, architecture test and size ratchet
    image: docker.io/library/golang:1.27
    run: make fmt-check vet lint arch ratchet
    timeout: 15m

  quality-web:
    description: Web lint, type check, tests and bundle budget
    image: docker.io/library/node:24-alpine
    run: |
      cd web
      npm ci --no-audit --no-fund
      npm run lint
      npx tsc --noEmit
      npm test
      npm run size
    timeout: 10m

  coverage:
    description: Per-package coverage must not fall below its floor
    image: docker.io/library/golang:1.27
    needs: quality-go
    run: make coverage-check
    timeout: 30m
```

Make `build-ui` and `build-binary` depend on these (`needs:`), so a failed gate stops the build.
The `coverage` step runs the whole Go suite once more, so when the `test` step produces a
coverage report, point the floor check at that output instead.

## Tooling summary

| What | Where |
|---|---|
| Linter configuration | `.golangci.yml` |
| Pinned tools | `Makefile` (`GOLANGCI_LINT_VERSION`), installed in `.tools/bin` |
| Baselines and floors | `quality/` and `web/` (listed above) |
| Architecture and size tests | `internal/archtest/` |
| Lint and coverage comparers | `scripts/quality/` |
| Web gates | `web/eslint.config.js`, `web/vitest.config.ts`, `web/scripts/check-bundle.mjs` |

## The image can always be built

The image's web stage (`Dockerfile`) copies `web/`, then the files of the documentation that the interface's type check
reads from outside `web/`, then runs `npm run build`. A file under `web/src` that reads something outside `web/` fails
that build unless the stage copies it first; once nothing ran the stage and the image could not be built at all.
`make image-inputs` (part of `make check`, no container needed) reads every relative import of `web/src` that leaves
`web/`, and fails unless the Dockerfile's web stage copies that top-level directory before the build.
`make image-build-check` runs the stage itself (`CONTAINER=podman` or `docker`, and the network for the base image).
