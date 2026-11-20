# Test reports

How Launchway reads test result files, and what the Tests tab and the project Tests page show.

A step can say which test result files it writes, and Launchway reads them when the step ends,
**whether it passed or failed** (a failing test step is the case that matters), was canceled or
timed out:

```yaml
steps:
  test:
    image: python:3.12
    run: pytest --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml          # one glob or a list; relative to the workspace

  go-test:
    image: golang:1.27
    run: |
      mkdir -p reports
      status=0
      go test -json ./... > reports/go.json || status=$?
      exit $status                  # keep the step failing when a test fails
    reports:
      tests: [reports/go.json]
```

- **Formats**, worked out from each file's content, not its name: JUnit XML, as pytest, Maven
  Surefire, Gradle, Jest and Vitest reporters, gotestsum and most other tools write it (nested
  suites, failures, errors, skipped tests and captured output included; a test Surefire ran again
  until it passed counts as flaky), and the output of `go test -json`. A file that is neither is
  shown as such, with the reason ("reports/x.xml: not a test report: ..."); it never fails the step.
- **Globs** use `*`, `?` and `[a-z]` within a path part, and `**` for any number of directories
  (`**/TEST-*.xml`); `**` does not enter `.git` or `node_modules`. Absolute paths, `..`, and empty
  globs are refused with the line they are on. In a template, a glob may come from a parameter
  (`{ param: dir, format: "%s/*.xml" }`).
- **What is collected**: regular files only, from the run's pod on Kubernetes and from the workspace
  directory otherwise. Symbolic links and pipes are not followed or read, so a step cannot make a
  runner read anything outside its workspace. The step's log says what was collected, and when a
  glob matched nothing or a file was cut short. Collecting never changes the step's outcome.
- **The Tests tab** of a run shows the totals, a pass rate and a bar, then every result, failures
  first and then the slowest, each opening to its message, stack trace and captured output with a
  button to copy them. Filters by status, a search box, and grouping by step or suite. A test that
  failed and then passed on a retry of the step is marked **flaky**; one that passed in the previous
  run of the same branch and fails now is a **new failure**. A failed step links to its failed tests,
  and run lists show each run's counts. A re-run shows the results of the steps it reused, linked
  to the run where they ran.
- **A project's Tests page** shows, for a branch (its default branch first), the totals and the time
  the tests took over its last runs, the tests that fail most, flaky tests (they passed and failed on
  the same commit, or passed only on a retry), the slowest, and each test's history: its last results
  as a strip of links to their runs.
- **API** (anyone who may see the run or project): `GET /api/v1/runs/{id}/tests` (summary, step by
  step), `GET /api/v1/runs/{id}/tests/cases?status=failed|passed|skipped|flaky&q=&step=&limit=&offset=`,
  `GET /api/v1/runs/{id}/tests/cases/{case}` (one result in full),
  `GET /api/v1/projects/{name}/tests?ref=&runs=` and
  `GET /api/v1/projects/{name}/tests/history?key=&ref=`. Run summaries carry `tests` totals.

**What is kept, and limits.** Each step attempt's event records a short summary: the totals, the
files and what was wrong with them. The individual results are stored beside the event log, in the
same transaction: messages cut to 2 KiB, traces to 8 KiB, and captured output (only for tests that
failed) to its last 4 KiB. A test is known by its suite or class, its name and, when the report
gives one, its file, so its history follows it from run to run. Launchway keeps runs forever (there
is no run retention yet), so individual results are bounded per project: when a project has more
than `max_cases_per_project`, the oldest runs' results are removed first; their totals stay, and the
run says so.

| Setting | Where | Default | Meaning |
|---|---|---|---|
| `tests.max_upload_mib` | server | 24 | MiB of report files accepted per step attempt; runners are told and send no more |
| `tests.max_cases_per_run` | server | 50000 | individual results one run keeps; beyond it, only the totals |
| `tests.max_cases_per_project` | server | 1000000 | individual results a project keeps; the oldest runs' go first |
| `runner.reports.max_files` | runner | 100 | files collected per step attempt (the first, in name order) |
| `runner.reports.max_file_mib` | runner | 10 | MiB read of one file; a larger one is cut, and what was read is still used |
| `runner.reports.max_total_mib` | runner | 20 | MiB read of all of an attempt's files |

With the Helm chart these are `config.tests.*` and `runner.reports.*` in the values. A server
older than the one that brought test reports cannot read a pipeline with `reports:`; use the key
once every server is upgraded (see [docs/development/migrations.md](../development/migrations.md)).
