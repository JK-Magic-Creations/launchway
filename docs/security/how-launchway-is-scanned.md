# How Launchway is scanned

Every check Launchway runs on its own code, dependencies, images and charts, when it runs, whether
it blocks, and how an exception is made. For security reviewers, contributors and customers who
ask what is behind the word "secure". The scanners are development and CI tools only: none is
installed on your machine by Launchway, and none is bundled into the binary, the packages or the
images.

## Run them yourself

```sh
make check        # the fast gates (seconds): format, vet, lint with gosec, architecture, web lint and tests
make security     # govulncheck, osv-scanner and npm audit, secret scan of the history, charts and Dockerfile, fuzzing
make image-scan   # Trivy over the built image (make image first)
make web-csp      # the web UI's Content-Security-Policy in a real browser
make dast         # an OWASP ZAP baseline scan of a throwaway local server (optional)
make sbom         # CycloneDX SBOMs of the source and of the binary
make scorecard    # the OpenSSF Scorecard of the public repository (informational)
```

Scanners run from containers (Podman or Docker) or with `go run` at a pinned version, so nothing is
installed system-wide. The versions are in `scripts/security/lib.sh` and the Makefile.

## The gates

| Gate | What it finds | Tool | Where it runs | Blocks? |
|---|---|---|---|---|
| Go vulnerabilities | known vulnerabilities in code we call | govulncheck | `make govulncheck`, the product's pipeline, the Security workflow, release | yes, if reachable; unreachable ones are listed |
| All locked dependencies | known vulnerabilities in Go modules, the web UI's and the docs tools' npm packages | osv-scanner, npm audit | `make osv`, Security workflow | yes (npm audit: high and above) |
| Dependency changes | new vulnerable or unapproved-license dependencies in a pull request | dependency review | Dependency review workflow | yes, high and above |
| Licenses | a module compiled into the binary with a license outside the allow-list | go-licenses | `make license-check`, CI, release | yes |
| Static analysis | injection, unsafe file and crypto use, and more | gosec (in the linter), CodeQL | `make lint`, CodeQL workflow | gosec: yes beyond the baseline; CodeQL: reported to code scanning |
| Secrets | credentials committed to any commit of any branch | gitleaks | `make secrets`, Security workflow, optional pre-commit hook | yes beyond the shrink-only baseline |
| Charts | wrong or unknown fields, bad practice, misconfiguration | kubeconform, kube-linter, Trivy config | `make iac`, Security workflow | yes (HIGH and CRITICAL for Trivy) |
| Pod Security | a chart pod that would not pass the "restricted" profile | a Go test over the rendered charts | `make chart-pss`, `go test` | yes |
| Dockerfile | unsafe or fragile build instructions | hadolint | `make iac` | yes (warning and above) |
| Image | vulnerabilities in the OS packages and the binary, secrets | Trivy | `make image-scan`, the image step of the product's pipeline, Security workflow | yes (fixable HIGH and CRITICAL) |
| Fuzzing | crashes and hangs in parsers fed by outsiders | native Go fuzzing | `make fuzz`, the product's pipeline, Security workflow | yes |
| Response headers and CSP | a missing or weakened security header | Go tests, a browser test | `go test`, `make web-csp` | yes |
| Web application | passive findings on a running server | OWASP ZAP baseline | `make dast` (optional) | yes when run |
| Workflows and scripts | unsafe workflow files on the public code host, shell mistakes | actionlint, shellcheck | `make lint-workflows lint-scripts` | yes |
| Reproducible release | a release that cannot be rebuilt byte for byte | `make release-reproduce` | CI | yes |
| Project posture | repository settings, review, pinning | OpenSSF Scorecard | `make scorecard`, Scorecard workflow | no |

### Choices and why

- **Go vulnerabilities: govulncheck.** It is the Go team's tool and reads the Go vulnerability
  database; it reports a finding only when our code can reach the vulnerable function, which keeps
  the signal high ([Go vulnerability management](https://go.dev/doc/security/vuln/)). Policy: a
  reachable finding fails the build. A vulnerability in a package we import or a module we require
  but never call is printed as advisory and removed at the next routine update.
- **All dependencies: osv-scanner.** OSV.dev is the open database that gathers GitHub advisories and
  the Go and npm databases; one tool covers Go and both npm lock files.
- **Static analysis: gosec plus CodeQL, not a second rule-set tool.** gosec already runs in
  `make lint` (excluding only G104, covered by errcheck, and G304, because file paths come from
  configuration; tests are exempt) and is the Go-specific security rule set. CodeQL adds data-flow
  analysis for Go and TypeScript, is maintained by GitHub, is free for public repositories and
  reports into the Security tab, so no extra rule-set has to be maintained here. A third tool
  would add noise before it adds coverage.
- **Secrets: gitleaks.** It is a single static program, scans the full history in about two
  seconds, and its baseline format makes the "may only shrink" rule enforceable. A finding beyond
  the baseline fails; a baseline entry that no longer matches also fails, so the list shrinks. The
  18 entries today are fake credentials in test fixtures and documentation examples (checked one by
  one). History rewrites change commit ids, so after one the baseline is regenerated with
  `scripts/security/gitleaks.sh --update`, which refuses to add anything.
- **Images, charts and Dockerfile: Trivy, kubeconform, kube-linter, hadolint.** Trivy is the
  scanner the product's own image step already uses, so the severity policy is the same in both
  places. kubeconform checks every rendered object against the Kubernetes schemas, strictly.
- **Fuzzing.** Native Go fuzz targets exist for the pipeline parser, schedules, templates, webhook
  signature checks, the Git service path, and the test, coverage, SBOM and security report parsers,
  and the GitOps file editor. A short run in CI finds shallow problems. A continuous fuzzing service
  such as OSS-Fuzz needs the project to have a significant user base and a committer contact
  ([OSS-Fuzz criteria](https://google.github.io/oss-fuzz/getting-started/accepting-new-projects/));
  ClusterFuzzLite runs the same engines inside the public code host's workflow runner in the meantime.
- **DAST: OWASP ZAP baseline.** It is the OWASP flagship scanner, its baseline mode is passive plus
  a short spider and sends no attack payloads, and it runs from one container against a throwaway
  server. It is optional because it needs a container runtime and a built UI.
- **Subresource Integrity.** The UI loads no script or style from another origin (the policy
  forbids it), so there is nothing to attach an integrity hash to.

## Severity policy and exceptions

| Severity | Rule |
|---|---|
| Critical, high (reachable or fixable) | blocks; fix before merging |
| Medium | fix in the next release; listed in the release notes if still open |
| Low, informational | listed; fixed when convenient |

An exception is never silent. It lives in a file, with a reason and an end date:

| Scanner | Exception file | Expiry |
|---|---|---|
| Trivy (charts, Dockerfile, image) | `quality/trivy-ignore.yaml`, `expired_at` | an expired entry stops hiding the finding |
| osv-scanner | `osv-scanner.toml`, `ignoreUntil` | same |
| gitleaks | `quality/gitleaks-baseline.json` | may only shrink |
| ZAP | `quality/zap-rules.tsv`, a reason above each rule | reviewed with each release |
| golangci-lint | `quality/lint-baseline.json` | may only shrink |

## What the scans found, and what was done (2026-11-21)

| Scan | Result | Action |
|---|---|---|
| govulncheck | 0 reachable, 0 other | none needed |
| osv-scanner, Go and web UI | 0 | none needed |
| osv-scanner, docs tools | 9 (1 high, 7 medium, 1 low), all in the documentation site's build tool (Vite 5, dev server only) | fixed: `overrides` to Vite 6.4.x in `docs/package.json`; the docs build and Learn build pass |
| npm audit | web 0; docs 3 before the fix, 0 after | same fix |
| gitleaks, 382 commits, all branches | 18, all fake credentials in tests and documentation | baseline of 18, shrink-only |
| Trivy image | 0 vulnerabilities, 0 secrets, but see the next row | none |
| Trivy image, scanner blind spot | the image had no `/etc/os-release`, so Trivy reported "no operating system" and checked none of the Alpine packages (Git, OpenSSH, musl) | fixed in the Dockerfile; the same scan now reports Alpine 3.23.8 and 0 findings; `make image-scan` fails if the OS is not recognized |
| kubeconform | 54 objects valid | none |
| kube-linter | 5 findings: PodDisruptionBudget without `unhealthyPodEvictionPolicy` | fixed in the charts (`AlwaysAllow`) |
| Trivy config, charts | 1 HIGH: KSV-0053, the Kubernetes executor's Role in the step namespace allows `pods/exec` | exception with expiry 2027-05-21; the Role is limited to the step namespace; removal is a backlog item |
| Trivy config, rootless BuildKit manifest | 2 (privilege escalation, writable root) that the manifest's README explains | exception with expiry |
| hadolint | 3 warnings | `cd` in a RUN replaced by WORKDIR; two documented ignores |
| Pod Security "restricted" | every pod of both charts passes | test added |
| Fuzzing, 11 targets, 10 to 20 s each | 1 input found: a file path with trailing spaces in a security report was shown with them | fixed (`internal/findings/paths.go`); the input is kept as a regression test |
| ZAP baseline | 64 passes, 4 warnings: cacheable static files, `style-src 'unsafe-inline'`, "modern web application", no COEP | each recorded in `quality/zap-rules.tsv` with a reason |
| Security headers | the UI sent only `frame-ancestors`; no script policy, no HSTS | added a Content-Security-Policy (own scripts only), HSTS over HTTPS, Permissions-Policy and Cross-Origin-Opener-Policy; tests and a browser test over 22 pages |
| OpenSSF Scorecard (public repository, an older commit) | 4.6 of 10 | see below |

### OpenSSF Scorecard

| Check | Score | Fix |
|---|---|---|
| Binary-Artifacts, Dangerous-Workflow, Fuzzing, License, Security-Policy, Token-Permissions | 10 | none |
| Pinned-Dependencies | 8 | Dockerfile base images are pinned by tag; pin by digest (the Dependabot Docker updates added here keep digests current) |
| SAST | 0 | CodeQL workflow added |
| Dependency-Update-Tool | 0 | `.github/dependabot.yml` added |
| Vulnerabilities | 0 (96 found on the published commit) | the docs-tools part is fixed here; re-run after the next push |
| Branch-Protection | 0 | repository setting: protect `main`, require pull requests and status checks |
| Code-Review | 0 | requires a second reviewer; not possible with one maintainer until a second person joins |
| CII-Best-Practices | 0 | apply for the badge (checklist below) |
| Maintained | 0 | the repository is under 90 days old; resolves with time |
| Contributors | 0 | needs contributors from several organizations |
| CI-Tests, Packaging, Signed-Releases | not rated | no pull requests or releases published yet |

### OpenSSF Best Practices badge (passing level), status

Criteria: [bestpractices.dev/criteria](https://www.bestpractices.dev/en/criteria). Met today: a
public version-controlled repository; a license (AGPL-3.0); documentation of how to contribute,
build and test; a security reporting process (`SECURITY.md`, private reporting) and a stated
response; the automated test suite in CI; static analysis (gosec, CodeQL); dynamic checks (fuzzing,
ZAP); no known unfixed medium-or-higher vulnerabilities in the project's own code; reproducible
builds; signed releases; HTTPS for the project's sites; secure development knowledge documented
([hardening](hardening.md), [trust model](trust-model.md)). Unmet or unverified: the badge
application itself; a stated vulnerability-response time (added to `SECURITY.md`); a second
maintainer for review; a published release (none yet), so release-signing criteria are met in the
workflow but not yet observable. Silver and gold need two or more maintainers, a code-review
requirement and a documented assurance case; the plan is in the security assurance design.

## Sign what ships

Releases are built twice and compared byte for byte, signed keylessly with cosign, carry CycloneDX
SBOMs and signed provenance, and GitHub build-provenance attestations. That is SLSA Build level 2:
a hosted build platform that signs the provenance ([SLSA levels](https://slsa.dev/spec/v1.0/levels)).
Level 3 additionally needs the build isolated from other runs and the signing secrets out of reach
of the build steps; moving the release job into a dedicated reusable workflow is the path, and
until then Launchway claims level 2 and no more. See [Verifying downloads](verify-downloads.md).

## Licenses of the scanners

Development and CI tools, run from containers or `go run`, never linked into or shipped with
Launchway.

| Tool | License |
|---|---|
| govulncheck | BSD-3-Clause |
| osv-scanner | Apache-2.0 |
| gitleaks | MIT |
| Trivy | Apache-2.0 |
| kubeconform | Apache-2.0 |
| kube-linter | Apache-2.0 |
| hadolint | GPL-3.0 (run as a separate program only) |
| OWASP ZAP | Apache-2.0 |
| OpenSSF Scorecard | Apache-2.0 |
| Syft, cosign | Apache-2.0 |
| CodeQL | GitHub service; queries MIT |
| actionlint, shellcheck | MIT, GPL-3.0 (separate program) |
