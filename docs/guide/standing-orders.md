# Standing orders

The same scans and best-practice checks for every repository, on by default: what runs on yours and why, how to change it, and how to accept a risk.

Launchway holds its own code to a set of scans: secrets, known vulnerabilities, insecure code patterns, licenses, Dockerfiles, manifests and more (see [How Launchway is scanned](../security/how-launchway-is-scanned.md)). **Standing orders** offer the same to every repository, with no tool to install and no change to the repository. Every Sortie (run) gains an **Inspection**: one step per check that fits what the repository holds, run in parallel, followed by a verdict step.

## What runs on your repository

When a run is made, Launchway looks at the files of the commit and picks the checks that apply:

| The repository holds | Checks that run |
|---|---|
| Any repository | secrets in the history; pipeline files and Dockerfiles for unsafe patterns |
| A lock file or manifest (Go, JavaScript, Python, Java and more) | known vulnerabilities in dependencies; licenses; a software bill of materials |
| `go.mod` | reachable vulnerabilities (does your code call the vulnerable function?); security rules for Go code |
| Python files | security rules for Python code; lint (advice) |
| A Dockerfile | best practice for the Dockerfile |
| Helm charts, Kubernetes manifests, Terraform | misconfiguration; Kubernetes best practice |
| Shell scripts | shellcheck (advice) |

[The reference](../reference/standing-orders.md) lists every check with its tool, the tool's license, its time budget and what it needs from the network. The project's **Standing orders** page (Pipelines, then your project, then Standing orders) shows, for your repository, each check, whether it runs and why, and where each setting comes from.

The steps are **added by the server**. Your pipeline file does not mention them, and the graph of the Sortie marks them as added by Standing orders, with a link to the page that says why and how to change it. They run in the tool's own pinned container image, never see your project's secrets, use the step cache, and run unchanged for a pull request from a fork or an automation account (a restricted run), because they need no secret.

If your pipeline has a step of its own called `inspect-<check>` (for example `inspect-secrets`), Launchway keeps yours and does not add that check.

## What blocks, and what does not

- **A pull request is blocked only by what it introduces.** The baseline is what the default branch already has. A finding that was there before your change is shown as *existing* and never blocks you.
- By default a check set to **block** (secrets, vulnerabilities in dependencies) stops a change that introduces a finding of **high** or **critical** severity, and for a vulnerability only when a fixed version exists. Everything else advises: it is shown, never blocks.
- **The default branch shows its backlog without failing.** Findings on it are recorded, tracked from the day they were first seen, and counted as fixed when a later run no longer has them. Set `gate.default_branch` to `block` if you want a finding there to fail the Sortie.
- Until the default branch has been inspected once there is nothing to compare with, so nothing blocks and the verdict says so.
- A check that did not run is never taken for a clean result: the verdict says which one did not finish.

The verdict is a step called `inspection`. Make it a required check of a branch rule (`<pipeline>/inspection`) to stop a merge that fails it.

## Changing it

Settings live at four levels and the repository's own file; the nearest wins, and a level above can lock a value so nothing below weakens it.

| Level | Where |
|---|---|
| Base, Wing, Squadron | Settings, Standing orders (`launchway standards set --level organization ...`) |
| Project | the same page, or the project's Standing orders page |
| The repository | `.launchway/standards.yaml` on the default branch |

Everything is on by default; you opt out. Each level can set a check to `off`, `advise` or `block`, set the severity that blocks, require a fix, choose what findings on the default branch do, allow or deny licenses, and list exemptions. Each level also has one **off switch** for Standing orders as a whole: it needs a reason, it is written to the Logbook, and the Security pages show a reminder while it is off.

```yaml
# .launchway/standards.yaml
checks:
  sast-go: off              # we use another tool for this
  secrets: block
gate:
  fail_on: critical         # block at critical only
licenses:
  deny: [GPL-3.0-only]
ignore:
  - rule: G401
    path: "legacy/**"
    reason: weak hash used for cache keys only, tracked in TICKET-12
    expires: 2027-03-01
```

- **The file is the team's control.** It is read from the default branch only: a pull request that edits it takes effect after it merges, so a change cannot switch off its own checks.
- **It is validated with plain messages**, naming the line: an unknown check suggests the closest name, a missing reason or end date is refused, an end date more than a year away is refused. What is wrong is left out and shown on the project's Standing orders page; the rest still applies.
- **A lock above wins.** If your Wing locked `check.secrets` at `block` and your file says `advise`, the file's value is ignored, and the page says so: "locked by your Wing: the security team requires it". Nothing is silently dropped.
- The page shows where every value comes from, and what it overrides, struck through.
- To move your settings into Git, open the project's Standing orders page and choose "Show this project's settings as a file".

## Accepting a risk

On a finding (in a Sortie's Security tab, or the project's page) choose **Accept the risk**. It needs:

- a **reason** of at least 10 characters (why it is safe, or why it is not a problem), and
- an **end date** within a year.

You can also mark it a **false positive**. The decision is recorded with your name, appears in the Logbook, counts for that finding wherever it appears in the project, and **stops counting on its end date**, after which the finding blocks again if it would. Anyone with the operator role can end it early (**End this decision**). From the command line: `launchway findings accept PROJECT FINGERPRINT --reason "..." --days 30`.

Teams can also write an exemption in the file (`ignore:` above), with the same reason and end date.

## Fixing

Each finding says what it is, where, why the check exists, and how to fix it: the fixed version for a dependency, or a change the tool suggests. For a dependency the finding offers the command that asks the dependency updater for the pull request (`launchway deps update NAME`); Launchway never edits your code on its own.

Exports: the project's page offers CSV, JSON and SARIF; `launchway findings export PROJECT --format sarif` writes the same. A tool that Launchway does not run can report into it too: a step whose `reports.findings` names a SARIF file, or a file in the [native findings format](../reference/standing-orders.md#plugging-in-another-tool), is read like any other.

## Where the results are

- **The Security tab of a Sortie** starts with the Inspection: verdict, the checks and whether each ran, and the findings split into new, existing and accepted.
- **The project's Standing orders page**: the open findings over time (with a table of the same numbers), by check, oldest first, the mean time to fix, and exports.
- **Settings, Standing orders**: which repositories have which checks, at a level, and the settings of the level.

See also: [Security findings and SBOMs](security-findings.md), [Maintenance (dependency updates)](dependency-updates.md), [Variables at levels](variables.md) (the same Base, Wing, Squadron and Project levels and locks).
