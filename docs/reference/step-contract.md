# The step contract

What a custom step receives and returns. It is small, versioned and the same in every language: a
step is any program that reads a JSON file, writes a JSON file and appends lines to another. The
kits for [Go, Python, TypeScript and shell](../guide/write-your-own-step.md) wrap it; a program
in any other language implements it directly. The current version is **1**.

## One screen

| What | Where | Form |
|---|---|---|
| Inputs | the file named by `LAUNCHWAY_STEP_INPUTS` | `{"contract":1,"inputs":{"name":value,...}}` |
| Scalar inputs | `LAUNCHWAY_WITH_<NAME>` | string, number and boolean inputs as text |
| Outputs | write the file named by `LAUNCHWAY_STEP_OUTPUTS` | one JSON object, `{"key":value,...}` |
| Results | append to the file named by `LAUNCHWAY_STEP_RESULTS` | one JSON object per line |
| Cargo (artifacts) | files in the directory `LAUNCHWAY_STEP_ARTIFACTS` | kept as the Cargo named `step` |
| Reports | files in `LAUNCHWAY_STEP_REPORTS` | SARIF (`*.sarif`) and JUnit XML (`*.xml`) |
| Secrets | `{"secret":"ENV_NAME"}` in the inputs | the value is in that environment variable |

Also set: `LAUNCHWAY_STEP_CONTRACT` (the version the Station speaks), `LAUNCHWAY_STEP_NAME`,
`LAUNCHWAY_STEP_DIR` (the step's code) and `LAUNCHWAY_WORKSPACE` (the checkout). The step starts in its own
directory.

**Types.** `string`, `number`, `boolean`, `list` (of strings), `record` (a JSON object) and `secret`.
An input arrives as its JSON value. A `secret` input arrives by reference and is masked in the log;
a `secret` output is masked and kept only as `***`. Outputs reach later steps as text (a list or record as
JSON), and the [manifest](step-manifest.md) says each one's type.

**Results.** Each line has a `t`:

| `t` | Fields | What it becomes |
|---|---|---|
| `summary` | `markdown` | A summary on the step's page, shown through the product's sanitizing Markdown renderer |
| `annotation` | `file`, `line`, `level` (`note`, `warning`, `error`), `message` | A note on a line of a file of the workspace |
| `finding` | `rule`, `severity` (`critical`, `high`, `medium`, `low`), `file`, `line`, `message` | A finding in the Sortie's findings, as if a scanner had reported it |
| `test` | `name`, `status` (`passed`, `failed`, `skipped`), `suite`, `duration_ms`, `message` | A test result, in the Sortie's tests |
| `mask` | `value` | The value is hidden in everything Launchway records from then on |
| `needs_person` | `message` | The question to put to a person; goes with exit code 65 |

A line that is too long, not JSON, of an unknown kind or with a field out of range is skipped, and the step's
log says which line and why. A step writes at most 5,000 results and 4 MiB.

**Exit codes.**

| Code | Meaning |
|---|---|
| 0 | Passed |
| 1 to 63 | Failed |
| 64 | Failed but allowed: recorded as a pass with a warning |
| 65 | Needs a person: recorded as failed, with the `needs_person` message as the reason (there is no waiting state yet) |
| above 65, or a signal | A crash: failed |

A step that exits 0 or 64 must have written every output its manifest declares, with the declared type; if it
did not, it fails with "the step broke the contract".

## The trust boundary

Launchway reads **nothing** from what a step prints. Results, outputs and Cargo are read only from the
files above, which the Station creates for this attempt and gives to the step alone. An ordinary log
line, even one that looks like a result, is only a log line. A program the step starts, such as a build tool, can reach the
files only if the step lets it: the variables it inherits hold their paths. The kits give a way to start a
tool without them (`lwstep.Environ()`, `launchway_step.environ()`, `environ()` and `lw_run_tool`), and the step
should use it for tools it does not trust. A step that passes the variables on chooses to let that tool write
results. Whatever a step writes is bounded in size and number, checked, and masked of secrets; the web page shows a
summary only through a sanitizing renderer.

## Versions

A manifest names the contract version it was written for (`contract: 1`). A Station accepts every version
from the oldest it supports to the one it speaks, and refuses a newer one, naming it. The inputs file
repeats the version, so a kit can refuse a Station that is newer than it knows.
`launchway step test` checks a step against the contract and tells what it found: an output that is not
declared or of the wrong type, a result on the wrong channel, an exit code the contract reserves, and, when the
step runs without a sandbox, files it changed that its manifest does not list under `permissions.write`.
