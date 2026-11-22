# Debriefs (test reports) {#test-reports}

The report of a Sortie's tests is the [Debrief](debrief.md).

How Launchway reads test result files and coverage, and what the Tests tab and the Flight (codebase) Tests page show.

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

  e2e:
    image: mcr.microsoft.com/playwright:v1.65.1-noble
    run: npx playwright test
    reports:
      tests:
        paths: [test-results/results.json]
        attachments: test-results   # screenshots, videos and traces the reports name
      html: { name: playwright, path: playwright-report }

  go-test:
    image: golang:1.27
    run: |
      mkdir -p reports
      status=0
      go test -json -coverprofile=reports/cover.out ./... > reports/go.json || status=$?
      exit $status                  # keep the step failing when a test fails
    reports:
      tests: [reports/go.json]
      coverage: { paths: reports/cover.out, fail_under: 75 }
```

## Formats

The format of each file is worked out from its content, never from its name. A file that is
none of these is shown as such, with the reason ("reports/x.json: not a Debrief: ..."); it
never fails the step. Every format was checked against output of the real tool, run in a
container; those files are the test data in `internal/testreport/testdata/real` and
`internal/coverage/testdata/real`.

| Format | Written by | Checked against | What Launchway shows |
|---|---|---|---|
| JUnit XML | pytest, Maven Surefire and Failsafe, Gradle, Jest (jest-junit), Vitest, PHPUnit, RSpec (rspec_junit_formatter), Mocha, Playwright, Cypress (mocha-junit-reporter), Karate, .NET (JunitXml.TestLogger), Robot, Bats, gotestsum | pytest 9.2.0 (xunit1 and xunit2), Surefire/Failsafe 3.6.1 with JUnit 6.1.3, Gradle 9.9.1, jest-junit 17.0.0, Vitest 5.1.1, PHPUnit 13.4.8, rspec_junit_formatter on Ruby 3.4, Mocha 11.8.0, Playwright 1.65.1, Cypress 16.3.0 | Results, failures with stack and output, properties; Surefire's reruns as retries and flaky; `[[ATTACHMENT\|path]]` lines as attachments |
| `go test -json` | Go | Go 1.27 | Results, panics, build failures, output |
| Cucumber JSON | Karate, cucumber-js, Cucumber-JVM, SpecFlow and others | Karate 2.1.4 and 1.5.2, cucumber-js 13.3.0 | Features and scenarios, every step with its status and time, tags, doc strings and tables, embedded screenshots and text; Karate's HTTP calls as formatted requests and responses |
| Karate JSON | Karate 1.x (`*.karate-json.txt`) and Karate 2's event stream (`karate-events.jsonl`) | Karate 1.5.2 and 2.1.4 | As Cucumber JSON |
| Playwright JSON | Playwright's `json` reporter | Playwright 1.65.1 | Each test in each Flight (browser), retries and flaky tests, steps, screenshots, videos and traces, tags and annotations |
| mochawesome | Cypress and Mocha with mochawesome (merged or one file per spec) | mochawesome 8.1.1 with Cypress 16.3.0 | Suites, tests, errors with stack, screenshots named in the context |
| CTRF | Reporters for Playwright, Jest, Vitest, Cypress, WebdriverIO, k6 and others (`ctrf-report.json`) | playwright-ctrf-json-reporter 0.2.0, jest-ctrf-json-reporter 0.2.0 | Tests, suites, retries and flaky, tags, browser, attachments, steps |
| TestNG XML | TestNG (`testng-results.xml`) | TestNG 7.12.0 | Classes and methods, groups as tags, parameters, failed configuration methods |
| NUnit 3 XML | NUnit (console or NunitXml.TestLogger) | NUnit 4.2.2 with NunitXml.TestLogger 8.1.0 | Fixtures and cases, categories as tags, properties, attachments, output |
| xUnit.net v2 XML | xunit.console, XunitXml.TestLogger | xunit 2.9.2 with XunitXml.TestLogger 8.1.0 | Tests, traits (`Category` as tags), skip reasons, output |
| TRX | `dotnet test --logger trx`, Visual Studio | .NET SDK 9.0.321 (xUnit and NUnit Flights) | Results and data-driven inner results, categories, output, result files as attachments |
| TAP 13/14 | `node --test --test-reporter=tap`, Bats, `prove`, many others | Node 24.22.3, Bats 1.14.0 | Tests and subtests, `# SKIP` and `# TODO`, YAML diagnostics as message and stack |
| Robot Framework `output.xml` | Robot Framework 6 and 7 | Robot Framework 7.5.1 | Suites and tests, tags, every keyword as a step, failure messages, embedded screenshots |

Coverage (see [Coverage](#coverage)) reads Cobertura XML, JaCoCo XML, LCOV, Go coverprofiles,
Clover XML and Istanbul's `coverage-summary.json`.

## Producing reports with common tools

```yaml
# Karate: Cucumber JSON (HTTP calls included) and Karate's own HTML report.
karate:
  image: registry.example.com/karate:2.1.4
  run: java -jar /opt/karate.jar run -o target/karate-reports -f html,cucumber:json tests/
  reports:
    tests: target/karate-reports/cucumber-json/*.json
    html: { name: karate, path: target/karate-reports, index: karate-summary.html }

# Playwright: reporter: [['json', { outputFile: 'test-results/results.json' }], ['html', { open: 'never' }]]
playwright:
  image: mcr.microsoft.com/playwright:v1.65.1-noble
  run: npx playwright test
  reports:
    tests: { paths: test-results/results.json, attachments: test-results }
    html: { name: playwright, path: playwright-report }

# Cypress: mochawesome JSON (or mocha-junit-reporter), screenshots and videos as attachments.
cypress:
  image: cypress/included:16.3.0
  run: npx cypress run --reporter mochawesome --reporter-options reportDir=reports/cypress,html=false,json=true
  reports:
    tests: { paths: "reports/cypress/*.json", attachments: cypress }

# pytest: JUnit XML, Cobertura coverage and coverage.py's HTML.
pytest:
  image: python:3.12
  run: pytest --junitxml=reports/junit.xml --cov=app --cov-branch --cov-report=xml:reports/coverage.xml --cov-report=html:reports/htmlcov
  reports:
    tests: reports/junit.xml
    coverage: { paths: reports/coverage.xml, fail_under: 80 }
    html: { name: coverage, path: reports/htmlcov }

# Jest (jest-junit) or Vitest (--reporter=junit): JUnit XML and LCOV.
jest:
  image: node:24
  run: npx jest --ci --coverage --coverageReporters=lcov --reporters=default --reporters=jest-junit
  env: { JEST_JUNIT_OUTPUT_DIR: reports }
  reports:
    tests: reports/junit.xml
    coverage: coverage/lcov.info
    html: { name: coverage, path: coverage/lcov-report }

# Maven (Surefire, Failsafe and JaCoCo); Gradle writes build/test-results/test/TEST-*.xml.
maven:
  image: maven:3-eclipse-temurin-21
  run: mvn -B verify -Dsurefire.rerunFailingTestsCount=2
  reports:
    tests: ["target/surefire-reports/TEST-*.xml", "target/failsafe-reports/TEST-*.xml"]
    coverage: target/site/jacoco/jacoco.xml

# .NET: TRX (or NUnit/xUnit XML with their loggers) and Cobertura from coverlet.
dotnet:
  image: mcr.microsoft.com/dotnet/sdk:9.0
  run: dotnet test --logger trx --results-directory reports --collect:"XPlat Code Coverage"
  reports:
    tests: "reports/*.trx"
    coverage: "reports/**/coverage.cobertura.xml"

# Go: go test -json and a coverprofile (see above).
```

## What you see

- **The Tests tab** of a Sortie (run) shows the totals, a pass rate and a bar, then every result, failures
  first. Results can be filtered by status, tag, suite and browser (or Playwright Flight),
  searched, sorted by time, and grouped by step, suite or browser. A strip compares the run with
  the previous run of the same branch: **new failures** (they passed then), **fixed** and **still
  failing**. Each result opens to its message and stack trace, captured output, properties, earlier
  tries, its steps (Cucumber and Karate steps, Robot keywords, Playwright steps) with time and
  errors, Karate's HTTP requests and responses laid out as such, and its attachments: screenshots
  inline with a viewer, videos with a player, Playwright traces to download (open them with
  `npx playwright show-trace`), text inline. Its history on the branch is a strip of its last
  results and a sparkline of its time. A test that failed and then passed on a retry, of the step
  or inside the tool, is **flaky**.
- **A Flight's Tests page** shows a branch's totals, time and coverage over its last runs, the
  tests that fail most, flaky tests (they passed and failed on the same commit, or passed only on a
  retry), the slowest, and each test's history.
- A test is known by its suite, name and file, and its browser or Flight when it has one, so its
  history follows it from run to run, separately in each browser.

## Attachments

`reports.tests.attachments` names one directory. When the step ends, it is kept as the step's
Cargo (artifact) `test-attachments` (whole or not at all, within `reports.max_attachment_files` and
`max_attachment_mib`), and a report's attachment is shown only if it is one of those files: a path
a report names, relative to the report or the workspace, or absolute inside the workspace, that
leads outside the directory (`..`, `/etc/passwd`) is listed with the reason and never read.
Symbolic links are never followed when the directory is kept, on any executor. Attachments that
reports carry inside them (Cucumber and Karate embeddings, Playwright bodies) are kept by the
server as the Cargo `test-embedded`, at most 5 MiB each and 20 MiB per report. Both count
toward the Sortie's and Flight's Cargo limits and expire like other Cargo.

Attachments are served by what their content is, not what the report says it is: PNG, JPEG, GIF
and WebP images, WebM and MP4 videos and plain text are shown inline; everything else (HTML, SVG,
XML, PDF, archives) is only ever a download. Every attachment is served with `nosniff` and a
Content-Security-Policy that allows nothing and sandboxes it.

## Coverage

```yaml
reports:
  coverage: coverage/lcov.info                  # one glob or a list
  # or
  coverage:
    paths: [coverage/cobertura.xml]
    fail_under: 80                              # percent of lines; or { lines: 80, branches: 60, functions: 70 }
```

- **Formats**: Cobertura XML (coverage.py, Jest, Vitest, coverlet, gcovr), JaCoCo XML (Maven,
  Gradle), LCOV (Jest, Vitest, c8, nyc, lcov), Go coverprofiles (lines count statements, as
  `go tool cover` does), Clover XML (Jest, OpenClover, PHPUnit) and Istanbul's
  `coverage-summary.json`. Totals are worked out from the line data, not from the rates the file
  states. Several files of one step are merged: a line covered in any of them is covered.
- **fail_under** fails a step that otherwise succeeded when its coverage is lower, saying so
  ("Coverage too low (fail_under): line coverage is 71.4% (25 of 35), under the 80% this step
  requires"); a step whose reports cannot be read fails it too. Such a failure is not retried.
- **The Coverage tab** of a Sortie shows lines (or statements), branches and functions, how they
  changed since the previous run of the branch and against the Flight's default branch, each
  step's coverage and threshold, and a table of packages and files with their change.
  A Flight's Tests page shows the trend.
- Stations (runners) collect coverage like Debriefs (`runner.reports.*` limits). A Station older than this
  feature is not given a step that declares coverage.

## HTML reports

```yaml
reports:
  html:
    - { name: playwright, path: playwright-report }                      # index defaults to index.html
    - { name: karate, path: target/karate-reports, index: karate-summary.html }
```

A step publishes a directory a tool generated: Playwright's or Karate's HTML report, an Allure
site, coverage HTML. It is kept as the Cargo `report-<name>` (within `reports.max_html_files`
and `max_html_mib`) and shown on the Sortie's **Reports** tab.

**The threat model.** A report's HTML and script come from whoever can push code, pull-request
authors included. Launchway must not let that script act as the person looking at it: read their
runs, start runs, approve deployments, create tokens. So:

- A report is served under `/hosted/<link>/`, where the link is signed by the servers, names one
  report and works for four hours. Those pages take no session: the link is the permission, and it
  is given only to people who may see the Sortie. Anyone they pass it on to can read that report until
  it expires, and nothing else.
- Every file is served with a Content-Security-Policy `sandbox` without `allow-same-origin`, so the
  page runs in an opaque origin: it cannot read Launchway's cookies or storage, and the browser
  sends Launchway's session cookie (`SameSite=Lax`) with none of its requests. The policy also lets
  the page load, connect to and frame nothing outside its own `/hosted/<link>/` directory, and
  submit no forms. The Reports tab frames it with the iframe `sandbox` attribute as well, without
  `allow-same-origin` or `allow-top-navigation`.
- The API refuses session-authenticated requests that come from an opaque origin (`Origin: null`)
  or from another site (`Sec-Fetch-Site: cross-site`, other than a link followed), and anything
  that changes something also needs the CSRF header, which a page of another origin cannot send.
- With `reports.origin` set to a separate origin (`https://reports.ci.example.com`, a host name of
  its own routed to the same servers), reports are served only there, and that origin serves
  nothing else: there is no Launchway session on it at all. This is the stronger setting, and the
  one to use where reports from untrusted pull requests are common.
- What is not covered: a report can still show misleading content inside its frame, run heavy
  script in the viewer's browser, or open pop-ups (sandboxed too). Treat a report from a pull
  request as you would the pull request.

Because a sandboxed page may not use the browser's storage, and many report generators stop
working without it (Playwright's and Karate's among them), every page of a report is served with a
few lines first that stand in for `localStorage` and `sessionStorage` in memory, for that page
only: nothing is kept between visits or shared with any other page.

`internal/api/reports_e2e_test.go` checks the headers, the links and the refusals. A report whose
script tries all of the above (read the cookies and Launchway's storage, reach the page around it,
read `/api/v1/runs` and `/api/v1/me`, create an API token) was opened in headless Chromium through
the Reports tab: every attempt was blocked.

## API and command line

Anyone who may see the Sortie or Flight:

- `GET /api/v1/runs/{id}/tests` (summary, step by step),
  `GET /api/v1/runs/{id}/tests/cases?status=failed|passed|skipped|flaky&q=&step=&tag=&suite=&variant=&sort=failures|slowest|name&limit=&offset=`
  (with the tags, browsers and suites to filter by),
  `GET /api/v1/runs/{id}/tests/cases/{case}` (one result in full: steps, attachments, retries),
  `GET /api/v1/runs/{id}/tests/cases/{case}/attachments/{n}` (`?download=1` to save it),
  `GET /api/v1/runs/{id}/tests/compare` (new failures, fixed, still failing),
  `GET /api/v1/projects/{name}/tests?ref=&runs=` and `GET /api/v1/projects/{name}/tests/history?key=&ref=`.
- `GET /api/v1/runs/{id}/coverage`, `GET /api/v1/runs/{id}/coverage/files?step=&q=&sort=path|lines|missed`,
  `GET /api/v1/projects/{name}/coverage?ref=&runs=`.
- `GET /api/v1/runs/{id}/reports` (each HTML report with a fresh link).
- `launchway runs tests <run> [--all | --status S] [--tag T]`, `launchway runs coverage <run> [--files]`
  and `launchway runs reports <run>`, each with `--json`.

Debriefs carry `tests` totals.

## What is kept, and limits

Each step attempt's event records a short summary: the totals, the files and what was wrong with
them, and for coverage the totals. The individual results are stored beside the event log, in the
same transaction: messages cut to 2 KiB, traces to 8 KiB, captured output (only for tests that
failed) to its last 4 KiB, and steps, attachments and properties to at most 64 KiB a result and
16 MiB an attempt (failing results first; a passing result's steps are the first thing left out).
A step keeps at most 200 steps a result, 20 attachments, 20 tags and 20 properties. Launchway keeps
runs forever (there is no Sortie retention yet), so individual results are bounded per Flight: when
a Flight has more than `max_cases_per_project`, the oldest runs' results are removed first; their
totals stay, and the Sortie says so. Coverage files are bounded the same way.

| Setting | Where | Default | Meaning |
|---|---|---|---|
| `tests.max_upload_mib` | server | 24 | MiB of report files accepted per step attempt; Stations are told and send no more |
| `tests.max_cases_per_run` | server | 50000 | individual results one Sortie keeps; beyond it, only the totals |
| `tests.max_cases_per_project` | server | 1000000 | individual results a Flight keeps; the oldest runs' go first |
| `tests.max_age_days` | server | 180 | how long individual results and per-test statistics are kept; older ones go whatever the count |
| `reports.max_attachment_files` | server | 2000 | files a step's attachments directory may hold |
| `reports.max_attachment_mib` | server | 512 | MiB a step's attachments directory may hold |
| `reports.max_html_files` | server | 5000 | files one HTML report may hold |
| `reports.max_html_mib` | server | 256 | MiB one HTML report may hold |
| `reports.max_coverage_upload_mib` | server | 24 | MiB of coverage reports accepted per step attempt |
| `reports.max_coverage_files_per_run` | server | 20000 | source files' coverage one Sortie keeps; beyond it, only the totals |
| `reports.max_coverage_files_per_project` | server | 2000000 | source files' coverage a Flight keeps; the oldest runs' go first |
| `reports.origin` | server | unset | a separate origin that alone serves HTML reports |
| `runner.reports.max_files` | Station | 100 | files collected per step attempt (the first, in name order), for tests and for coverage |
| `runner.reports.max_file_mib` | Station | 10 | MiB read of one file; a larger one is cut, and what was read is still used |
| `runner.reports.max_total_mib` | Station | 20 | MiB read of all of an attempt's files |

With the Helm chart these are `config.tests.*`, `config.reports.*` (camel case, such as
`maxHtmlMiB`) and `runner.reports.*` in the values, unset by default. A server older than the one
that brought Debriefs cannot read a pipeline with `reports:`, and one older than schema 18
cannot read `reports.tests.attachments`, `reports.coverage` or `reports.html`; use the keys once
every server is upgraded (see [docs/development/migrations.md](../development/migrations.md)).
