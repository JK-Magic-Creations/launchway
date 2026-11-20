# Tests and reports

Make Launchway read your test results, so each run shows which tests failed and why, which tests
are flaky, and how tests behave over time. For developers. Takes about 15 minutes.

> **Verified** on 2026-11-19 against commit f717337: the steps below with a local server, Podman
> and `python:3.13-slim` (pytest installed from PyPI in the step). **Not verified:** other test
> frameworks than pytest here; the JUnit and `go test -json` readers are covered by the project's tests.

## What you need

- The repository and server from [First pipeline in 10 minutes](first-pipeline.md), with the
  server running.
- Network access from steps, to install pytest.

## 1 Add some tests

In `~/hello-launchway`, create `tests/test_greeting.py`. One test is wrong on purpose:

```python
def greet(name):
    return f"Hello, {name}!"

def test_greets_by_name():
    assert greet("Ada") == "Hello, Ada!"

def test_greets_the_world():
    assert greet("world") == "Hello, world!"

def test_shouts():
    assert greet("Bob").upper() == "HELLO, BOB"
```

## 2 Run them and declare the report

Replace the `test` step in `.launchway/pipeline.yaml`:

```yaml
  test:
    image: python:3.13-slim
    needs: lint
    run: |
      pip install --quiet pytest
      mkdir -p reports
      python -m pytest tests --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml
```

`reports.tests` names the files the step writes, relative to the workspace. Launchway reads them
when the step ends, **whether it passed or failed**. It understands JUnit XML (written by pytest,
Maven Surefire, Gradle, Jest, Vitest, gotestsum and most other tools) and the output of
`go test -json`, recognised by content, not by name.

Commit:

```sh
launchway validate
git add -A
git commit -m "Add tests with a report"
```

## 3 Read the failure

The run fails at `test`. Open it in the web UI:

- the failed step says which tests failed, and links to them;
- the **Tests** tab shows the totals, a pass rate, and every result, failures first. Open
  `test_shouts` to see its message:

```text
AssertionError: assert 'HELLO, BOB!' == 'HELLO, BOB'
  - HELLO, BOB
  + HELLO, BOB!
```

- the step's log ends with what was collected:

```text
Failed with exit code 1 after 4.8s
Test reports: collected reports/unit.xml (910 bytes)
```

The same data is in the API:

```sh
curl -s "http://127.0.0.1:8080/api/v1/runs/<run-id>/tests/cases?status=failed"
```

## 4 See a flaky test

A test that fails and then passes when the step is tried again is **flaky**. Fix `test_shouts`
(expect `"HELLO, BOB!"`) and add a test that fails only the first time:

```python
import os

def test_sometimes_slow_service():
    # Fails the first time it runs in a workspace, passes after: a flaky test.
    marker = ".first-attempt-done"
    if not os.path.exists(marker):
        open(marker, "w").close()
        assert False, "the service did not answer in time"
```

Let the step be tried once more after a failure, with `retry: 1`:

```yaml
  test:
    image: python:3.13-slim
    needs: lint
    retry: 1
    run: |
      pip install --quiet pytest
      mkdir -p reports
      python -m pytest tests --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml
```

Commit it. The run succeeds on the step's second attempt, and on the **Tests** tab
`test_sometimes_slow_service` is marked **flaky**. Filter by *flaky* to list such tests.

## 5 Watch tests over time

Open **Projects → hello → Tests**. For the default branch it shows the totals and time of recent
runs, the tests that fail most, the flaky tests, the slowest, and each test's recent results as a
strip of links to their runs. A test that passed in the previous run of the branch and fails now
is marked as a **new failure**.

## Keep the step failing

If a test command's exit status would be lost (for example when its output is piped), capture it
and exit with it, so the step still fails:

```yaml
  go-test:
    image: golang:1.27
    run: |
      mkdir -p reports
      status=0
      go test -json ./... > reports/go.json || status=$?
      exit $status
    reports:
      tests: [reports/go.json]
```

## What you learned

- `reports.tests` makes test results part of the run, for passing and failing steps.
- Failures, flaky tests and new failures are found for you, and kept as history per branch.

## What next

- [Security scans](security-scans.md).
- [Test reports guide](../guide/test-reports.md): formats, globs, limits and the API.
