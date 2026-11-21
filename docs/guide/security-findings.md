# Security findings and SBOMs

Scanner results in SARIF and software bills of materials in CycloneDX: what is kept, what is never kept, and how runs are compared.

Steps can declare the results of security scanners and software bills of materials:

```yaml
steps:
  scan:
    image: registry.example.com/scanners:1
    run: |
      gitleaks detect --report-format sarif --report-path reports/security/gitleaks.sarif || true
      semgrep scan --sarif --output reports/security/semgrep.sarif || true
      trivy fs --format cyclonedx --output reports/sbom.cdx.json .
    reports:
      findings: reports/security/*.sarif   # SARIF 2.1.0, from any scanner
      sbom: reports/sbom.cdx.json           # CycloneDX JSON
```

- **Findings** are read from SARIF 2.1.0, as gitleaks, semgrep, CodeQL, bandit, hadolint, trivy,
  govulncheck and most other scanners write it, whether the step passed or failed. A file that is
  not SARIF is shown as such, with the reason; it never fails the step.
- **Severity** is one of critical, high, medium, low or unknown, taken from the first of: a
  `security-severity` score on the result or its rule (9.0 and over critical, 7.0 high, 4.0 medium,
  0.1 low); the tool's own severity word (`issue_severity`, `severity`, or a rule tag such as
  `HIGH`); the SARIF level (error high, warning medium, note low, none unknown). A finding about a
  secret is never below high.
- **Secrets that scanners find are never stored or shown**: only where they are and which rule
  found them. Code snippets in SARIF files are never kept for any finding, the message of a
  finding about a secret is replaced by its rule's description, and every message is cleared of
  anything shaped like a key or token. SARIF files themselves are not kept.
- **New, still there, fixed.** Each finding gets a fingerprint (its tool, rule and file, with the
  scanner's own fingerprints when it gives real ones, else the line and message), and a run's
  findings are compared with the previous run of the same branch: new, still there, or fixed.
  Only scanners that ran both times are compared. A finding whose code moved up or down a file
  shows as fixed and new again, unless its scanner gives line-independent fingerprints, as
  CodeQL does.
- **The Security tab** of a run shows a bar of counts by severity, then every finding, the most
  severe first, with filters by severity, tool and status and a search box; each opens to the
  rule, its description, the location and a link to the rule's documentation. Run lists show the
  counts. **A project's Security page** shows the counts over a branch's recent runs, and the
  findings still open, each with the run where it was first seen.
- **SBOMs** are kept whole as the step's artifact `sbom` and summarized: the number of components
  and the licenses. **Components** can be searched across projects (`/components` in the UI): which
  runs and projects contain a package, at a version.
- **API** (anyone who may see the run or project):
  `GET /api/v1/runs/{id}/findings?severity=&tool=&status=new|existing|fixed&q=&step=&limit=&offset=`,
  `GET /api/v1/projects/{name}/security?ref=&runs=`,
  `GET /api/v1/components?name=&version=&limit=` (only projects the caller may see). Run summaries
  carry `findings`, the counts by severity, for checks that later act on them.

| Setting | Default | Meaning |
|---|---|---|
| `findings.max_upload_mib` | 32 | MiB of SARIF files accepted per step attempt; runners are told |
| `findings.max_per_run` | 20000 | findings one run keeps; beyond it, only the counts |
| `findings.max_per_project` | 500000 | findings a project keeps; the oldest runs' go first |
| `findings.max_components_per_run` | 20000 | SBOM components one run keeps for searching |
| `findings.max_components_per_project` | 1000000 | SBOM components a project keeps |
| `findings.max_sbom_mib` | 64 | MiB of a step's SBOM files read to summarize them |

With the Helm chart these are `config.artifacts.*` and `config.findings.*` in the values, unset by
default. A server older than the one that brought these keys cannot read a pipeline that uses
`artifacts`, `fetch`, `reports.findings` or `reports.sbom`; use them once every server is upgraded.
