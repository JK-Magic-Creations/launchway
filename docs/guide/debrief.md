# The Debrief

How to read the report of a Sortie's tests, find flaky tests, quarantine them, export the report and split tests among shards.

The Debrief is what the **Tests** tab of a Sortie (run) opens on. It takes the results the Sortie's steps reported (see [Debriefs (test reports)](test-reports.md) for the formats) and puts them in the order a person needs: what happened, what changed, what broke and why. **All results** beside it is the full list with its filters.

## Reading a Debrief

- **Summary.** How many tests passed, failed, were skipped, were flaky or quarantined, how long they took and the pass rate, with a bar whose parts differ by pattern as well as color, and the same numbers in a table.
- **Against the default branch.** The run is compared with the last run of the default branch that succeeded: how many tests are newly failing and newly passing, how the test count and duration moved and which tests are at least twice as slow. On the default branch itself the comparison is with its own earlier run.
- **Failures.** New breakage first. Each failure has its message, **expected against actual** (a line-by-line diff when the message says what was expected and what came out), the stack, the captured output, screenshots the test attached, **First failed in** (the Sortie where this stretch of failures began on this branch) and **Also failing on the default branch** (yes: this change did not break it; no: it did). A failure that the test's history shows as flaky says so.
- **Flaky and quarantined tests**, the **slowest tests** (with what they usually take), the **suites** as a table with totals rolled up to each level, a **grid** of tests by browser or version when results carry one, **coverage**, and the **trend** of the branch's pass rate. Every chart has its numbers in a table.

A Sortie that is a restricted pull request (from a fork or an automation account) sees history only for its own branch and the default branch.

## Flaky tests

A test is **flaky** when its results go against its own history without a change that explains it:

- the same commit both passed and failed it (two Sorties, or a pass after a retry); or
- it failed on a single commit between commits that passed it, at least twice.

A break that lasts, and a failure that was fixed, are not flaky. The **score** is the share of the commits the test ran on that showed this. A test that passed on a retry (a retried step, or the test tool running it again) is shown as **passed on retry: flaky** and never as a plain pass.

The Flight's **Flaky tests** page (Tests page, `?view=flaky`) lists them with their history, branches and when each was first seen, and `launchway tests flaky <project>` prints the same. Retrying is the step's own `retry`; Launchway does not rerun single tests.

## Quarantine

A repository lists tests it knows are unreliable in `.launchway/quarantine.yml`:

```yaml
quarantine:
  - test: "pkg/db::TestMigrations*"   # suite::name, or the name; * matches any text
    owner: bo
    expires: 2027-02-15               # required, at most a year ahead
    reason: lock timeouts on the shared database
```

The Station (runner) sends the file with the step's Debriefs, from the commit under test, so adding an entry is a change to review in a pull request. While an entry is live, a failure of that test does not fail the step when every failing test of the step is quarantined: the step ends as succeeded with the reason written on it, and the Debrief shows the test loudly under **Quarantined tests**, counted. After the expiry day the entry excuses nothing. Launchway suggests entries on the Flaky tests page and never writes the file.

## Exports

From the Debrief, or `launchway debrief export <run> --format html|pdf|json|junit`:

- **HTML:** one file with its styles inside and no script or network use, light and dark, with the summary, failures (with small screenshots), flaky and quarantined tests, slowest, suites, coverage and trend.
- **PDF:** a cover, contents, an executive summary with a chart, the failures in detail, flaky and quarantined tests, tables that break across pages, coverage, the trend and page numbers, typeset by the server itself with embedded fonts. Characters outside the embedded fonts (such as Chinese) print as `?`; the HTML keeps them. The PDF is not tagged for accessibility: the HTML is the accessible form.
- **JSON:** the same facts, versioned (`version`).
- **JUnit XML:** every result, merged, for tools that read JUnit; quarantined failures are written as skipped.

`reports.brand_name` and `reports.brand_logo` in the server's configuration put an installation's name and logo on the HTML and PDF; the default is neutral.

## Coverage of a change

For a pull request on a repository this server hosts, the Debrief says how many of the lines the change added or modified are covered by tests, from the line data of the coverage reports (Cobertura, JaCoCo, LCOV and Go cover profiles carry it). When fewer than `reports.change_coverage_floor` percent (80 by default) are covered, it **advises** and lists the uncovered lines; nothing fails. Only the run of the pull request's current head is judged.

## Splitting tests among shards

Launchway keeps each test's typical duration. `launchway tests split <project> --shards 4` prints a balanced split (longest tests first, each to the lightest shard; the same history gives the same split), and `--shard 2` prints only the names of shard 2, one per line, as `suite::name`, for your test tool:

```yaml
steps:
  test:
    for_each:
      shard: ["0", "1", "2", "3"]
    run: |
      launchway tests split storefront --shards 4 --shard "$LAUNCHWAY_EACH_SHARD" > mine.txt
      ./run-tests.sh --only-file mine.txt
```

Each member of the loop receives its shard number as `LAUNCHWAY_EACH_SHARD`. The step's `launchway` command needs a token with read access to the Flight (codebase); this is documented, not yet a Maneuver of the starter library.

A test with no history is given the average. The same data is at `GET /api/v1/projects/{name}/tests/shards?count=N` and in the MCP tool `split_tests`. How long history is kept: see `tests.max_age_days`.
