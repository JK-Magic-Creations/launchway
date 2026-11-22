# Standing orders reference

The checks, their tools and licenses, the settings, the repository file, the findings model and the gate rules. For what they mean in practice see [Standing orders](../guide/standing-orders.md).

## Checks

Profile version `2026-11-21.1`. A check applies when a file of the repository matches; "default" is its state when nothing sets it. Only a check marked "can block" may be set to `block`. Every check may run in a restricted run (no secret is needed).

| Check | Looks for | Applies when | Tool | Tool license | Default | Can block | Needs the network |
|---|---|---|---|---|---|---|---|
| `secrets` | credentials in the history (all commits the first time, then the commits since the last Sortie (run)) | every repository | gitleaks | MIT | block | yes | no |
| `vuln-deps` | known vulnerabilities in locked dependencies | a lock file or manifest | osv-scanner | Apache-2.0 | block | yes | the OSV advisory database |
| `vuln-reach-go` | vulnerabilities whose code your Go program can call | `go.mod` | govulncheck | BSD-3-Clause | advise | yes | Go module proxy, Go vulnerability database |
| `sast-go` | insecure patterns in Go code | `go.mod` | gosec | Apache-2.0 | advise | yes | Go module proxy |
| `sast-python` | insecure patterns in Python code | `*.py` | ruff (the `S` rules) | MIT | advise | yes | no |
| `lint-python` | mistakes the language lets through | `*.py` | ruff | MIT | advise | no | no |
| `license` | denied, not allowed and unidentifiable licenses | a lock file or manifest | osv-scanner | Apache-2.0 | advise | yes | deps.dev |
| `sbom` | a bill of materials (CycloneDX) kept with the Sortie | a lock file or manifest | trivy | Apache-2.0 | advise | no | no |
| `dockerfile` | unsafe or fragile Dockerfile instructions | `Dockerfile`, `Containerfile`, `*.Dockerfile` | hadolint | GPL-3.0-only | advise | yes | no |
| `misconfig` | misconfiguration in Terraform, Helm, manifests, Dockerfiles | `*.tf`, `Chart.yaml`, `k8s/`, `manifests/`, `charts/`, `helm/`, `terraform/`, `ansible/` | trivy | Apache-2.0 | advise | yes | the rule bundle (built-in rules when unreachable) |
| `k8s-practice` | missing limits, probes, security context | `Chart.yaml`, `kustomization.yaml`, `k8s/`, `manifests/`, `charts/`, `helm/` | kube-linter | Apache-2.0 | advise | yes | no |
| `shell` | quoting and globbing mistakes | `*.sh`, `*.bash` | shellcheck | GPL-3.0-only | advise | no | no |
| `pipeline-lint` | images not pinned by digest, secrets in plain text, downloads piped into a shell | `.launchway/*.yaml`, Dockerfiles | launchway-pipeline-lint, Launchway's own rules (a script of this release, run in an Alpine image) | MIT | advise | yes | no |
| `image-scan` | vulnerabilities in the built image | Dockerfile | trivy | Apache-2.0 | not available yet | — | — |

`image-scan` is listed, shown as unavailable and never run: it needs the image a pipeline step built. The image step already scans what it builds. The Java and JavaScript static analysis (SpotBugs, ESLint rule sets) are not in this profile yet; their dependencies, secrets, licenses and bill of materials are.

The tools run as separate programs in their own container image, unmodified. They are not part of the Launchway server, binary or packages. Two of them (hadolint, shellcheck) are under the GPL; running them as separate containers does not make Launchway a derivative of them. The images, their versions and digests are in `internal/standards/tools.yaml`.

## Pinned tools

`internal/standards/tools.yaml` is the only place a tool image is named; every image is pinned by digest, and a test refuses one without. To update a tool, change its `version` and `image` together (`skopeo inspect docker://IMAGE:TAG` shows the digest of a multi-architecture image) and run `go test ./internal/standards`. The dependency updater reads the `image:` lines of this file like those of any pipeline file; add the path to its image globs and it proposes the change as a pull request.

## How the Inspection is added

When a run is made from a commit (push, pull request, run by hand, schedule or webhook; not a draft or a Return to base (rollback)), the server lists the files of the commit, reads `.launchway/standards.yaml` from the default branch, resolves the settings of the levels, and adds to a copy of the pipeline:

- one step `inspect-<check>` per check that applies and is not off, with no `needs`, so all run at once; `on_failure: continue`, a time budget (the step's timeout), a step cache where it helps, `reports.findings` or `reports.sbom`, and `added: standing-orders` with the reason it applies;
- one step `inspection` of kind `gate`, which needs all of them and is settled by the server from their findings. It carries the effective policy as it was when the run was made, so a verdict can be explained later whatever the settings are by then.

The Sortie asks for schema 45, so an older server leaves it alone. The pipeline file is not changed; `launchway validate` does not show these steps; the Sortie page and `GET /api/v1/runs/{id}` do.

**For the Flight plans.** The Flight plans of the starter library (`go-service`, `go-library`, `node-app`, `python-service`, the Java ones, `helm-chart`, `container-image`, `static-site`, `library-only` and `node-library`) name their secret scan `inspect-secrets`, and the two Go ones their reachable-vulnerability check `inspect-vuln-reach-go`. The server keeps those steps and does not add the check a second time. They are the server's own step (the same script, the same pinned image and the same report path, which a test holds equal), and they continue on failure: what blocks is the verdict step `inspection`, from what they found. A pipeline that skips the check with the parameter (`secret_scan: false`, `vuln_scan: false`) has no such step, so the server adds its own. The Flight plans' other stages (audit, analysis, licenses) are not the same work as a check and still run beside the Inspection.

## The findings model

Every tool's report is read by one reader (SARIF 2.1.0, plus the adapters below) with the same limits, masking and severity table (see [Security findings](../guide/security-findings.md)) and put in one model:

| Field | Meaning |
|---|---|
| `check`, `tool`, `rule` | which check ran which tool, and the rule that matched |
| `severity` | critical, high, medium, low, unknown |
| `kind` | secret, vulnerability, code, config, license, inventory, style |
| `title`, `file`, `line` | what is wrong and where; style findings are never above low |
| `package`, `version`, `fixed_in`, `advisories` | for dependencies: what, the version in use, the first fixed version, other names (CVE, GHSA) |
| `reachable` | `yes` or `no` where a tool analyses calls (govulncheck) |
| `fix` | what to do: the tool's own suggestion, else a sentence |
| `fingerprint` | the finding's identity across runs |
| first seen, status | recorded for the default branch: open, fixed, accepted risk, false positive (with reason, who, end date) |
| standing | in a Sortie: new (introduced by this change), existing (on the default branch), accepted, false positive |

**The fingerprint** is a hash of the tool, rule and file, plus, for a dependency, the package; otherwise the message with its numbers removed. It does not include the line, so a finding keeps its identity when the code around it moves. Two identical findings in one file are told apart by their order, so inserting a second identical finding before an existing one can swap their identities.

## The gate

For a Sortie that is not on the default branch, a finding **gates** when its check is set to `block`, its severity is at or above `gate.fail_on`, it is not accepted (a decision or an exemption), it is not on the default branch's open list, and, for a vulnerability, a fixed version exists (`gate.require_fix`) or the code is reachable. An unreachable vulnerability (govulncheck: the module is listed, the function is not called) never gates. With no baseline yet, nothing gates. On the default branch nothing gates unless `gate.default_branch` is `block`. A check that did not run makes the verdict fail only if `gate.incomplete` is `fail`.

## Settings

| Key | Values | Default |
|---|---|---|
| `off` (with a reason) | true, false | false |
| `check.<id>` | off, advise, block | per check |
| `gate.fail_on` | critical, high, medium, low | high |
| `gate.require_fix` | true, false | true |
| `gate.default_branch` | advise, block | advise |
| `gate.incomplete` | pass, fail | pass |
| `licenses.deny` | license names (SPDX) | AGPL-3.0, AGPL-3.0-only, AGPL-3.0-or-later, SSPL-1.0 |
| `licenses.allow` | license names; empty means any not denied | empty |
| `ignore` | entries: `check`, `rule`, `package`, `path` (a glob, `**` matches directories), `fingerprint`, `status`, `reason`, `owner`, `expires` | none |
| locks | any key above, with a reason; set from the page or the API, never from a file | none |

**Resolution.** From the widest to the nearest: the default, Base, Wing, Squadron, Flight (codebase), then `.launchway/standards.yaml`. The nearest wins. A lock at a level makes the value in force there a floor for every level below and the file. Weaker means: a lower state, a higher severity threshold, `require_fix: true` where it was false, `default_branch: advise` where it was block, `incomplete: pass` where it was fail, a denied list that lost an entry, an allowed list that gained one, exemptions where they are locked. A stricter value is accepted. Saving a level's settings through the API refuses a weaker value with `409` and the sentence that names the lock; the file cannot be refused, so its weaker value is ignored and the page says so. Licenses and lists replace, never merge.

## The repository file

`.launchway/standards.yaml`, at most 64 KiB, read from the default branch. Keys: `version`, `off`, `off_reason`, `checks` (a name and a state, or `{ state: ... }`), `gate`, `licenses`, `ignore`. Unknown keys, unknown checks, invalid states and severities, license names that are not SPDX identifiers, an exemption without a reason of at least 10 characters or without an end date within 365 days are reported with their line; the entry is left out. Locks cannot be written in the file.

## Decisions and expiry

A decision (accepted risk, false positive) has a reason (10 to 500 characters), an end date at most 365 days away, and the name of whoever made it; making a new one ends the previous one for the finding. It stops counting at the end of its day. It is recorded in the Logbook as `finding.accept`, and ending one as `finding.reopen`. Settings changes are `standards.settings_set`.

## Plugging in another tool

A tool that writes **SARIF 2.1.0**, or this JSON, can report into the same model through a step's `reports.findings`:

```json
{ "format": "launchway-findings/1", "tool": "acme-scan",
  "findings": [ { "rule": "ACME-1", "severity": "high", "message": "is vulnerable to ACME-1.",
                  "package": "left-pad", "version": "1.0.0", "fixed_in": "1.3.0", "file": "package-lock.json" },
                { "rule": "ACME-2", "severity": "low", "message": "A style problem", "file": "a.js", "line": 3 } ] }
```

Also read: shellcheck's `json1`, and the license report the `license` check writes. Findings of a step that is not one of the checks are tracked and shown like the others, but a check's gate settings apply only to the checks above. Support for commercial scanners is later work, through adapters of the same shape.

## Tools used and what leaves the machine

The checks that need the network read public data only: advisories (OSV, the Go vulnerability database), license facts (deps.dev), Go modules and Trivy's rule bundle. No source code or finding leaves the Station (runner). Hosted installations run the checks on the customer's own Stations.
