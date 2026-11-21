# Security scans

Run a secret scanner and a dependency scanner in your pipeline, see their findings on the run, and
watch them become **fixed** in the next run. You also make a software bill of materials (SBOM)
and search it. For developers. Takes about 15 minutes.

> **Verified** on 2026-11-19 against commit f717337: the steps below with a local server, Podman,
> `ghcr.io/gitleaks/gitleaks:v8.28.0` and `docker.io/aquasec/trivy:0.67.2`. **Not verified:** other
> scanners here (any scanner that writes SARIF 2.1.0 works the same way). Vulnerability databases
> change, so your counts will differ.

## What you need

- The repository and server from [First pipeline in 10 minutes](first-pipeline.md) (and
  [Tests and reports](tests-and-reports.md) if you did it), with the server running.
- Network access from steps: the dependency scanner downloads its vulnerability database.

## 1 Give the scanners something to find

In `~/hello-launchway`, add a file with a webhook address that looks real, and an old dependency
list. Both are fakes for the tutorial. The command joins the address from three pieces, so the
whole address is never written out in one place (this page included), and nothing in this
repository could be mistaken for a leaked Slack secret:

```sh
printf 'SLACK_WEBHOOK=https://hooks.slack.com/services/%s/%s/%s\n' \
  T00000000 B00000000 XXXXXXXXXXXXXXXXXXXXXXXX > config.env
printf 'requests==2.19.0\npyyaml==5.3\n' > requirements.txt
```

## 2 Add two scanning steps

Add these steps to `.launchway/pipeline.yaml`, beside `test`:

```yaml
  secret-scan:
    image: ghcr.io/gitleaks/gitleaks:v8.28.0
    needs: lint
    run: |
      mkdir -p reports/security
      gitleaks dir . --report-format sarif --report-path reports/security/gitleaks.sarif --exit-code 0
    reports:
      findings: reports/security/*.sarif

  dependencies:
    image: docker.io/aquasec/trivy:0.67.2
    needs: lint
    run: |
      mkdir -p reports/security
      trivy fs --scanners vuln --format sarif --output reports/security/trivy.sarif .
      trivy fs --format cyclonedx --output reports/sbom.cdx.json .
    reports:
      findings: reports/security/*.sarif
      sbom: reports/sbom.cdx.json
```

- `reports.findings` names SARIF 2.1.0 files. Launchway reads them whether the step passed or
  failed; a file that is not SARIF is reported as such and never fails the step.
- `reports.sbom` names a CycloneDX JSON file. It is kept as the step's `sbom` artifact and summarized.
- `--exit-code 0` keeps the step green while you look at the results. To fail the pipeline on a
  finding, let the scanner fail, or use a [policy](policy-gates.md).

Commit:

```sh
launchway validate
git add -A
git commit -m "Scan for secrets and vulnerable dependencies"
```

## 3 Read the findings

Open the run, then its **Security** tab: a bar of counts by severity, then each finding, most
severe first, with filters by severity, tool and status. On this run:

```text
critical  Trivy     CVE-2020-14343      requirements.txt   pyyaml 5.3
critical  Trivy     CVE-2020-1747       requirements.txt   pyyaml 5.3
high      gitleaks  slack-webhook-url   config.env
high      Trivy     CVE-2018-18074      requirements.txt   requests 2.19.0
...
```

Notice what is **not** shown: the webhook address itself. Secrets that scanners find are never
stored or shown, only where they are and which rule found them, and a finding about a secret is
never below high.

The run list shows the counts too, and so does the API:

```sh
curl -s "http://127.0.0.1:8080/api/v1/runs/<run-id>/findings?severity=critical"
```

## 4 Search the SBOM

Open **Components** in the menu and search for `requests`. Every run and project that contains
the package is listed, with its version. This answers "where do we use this library?" when a new
vulnerability is announced.

## 5 Fix them, and see them fixed

```sh
git rm config.env
printf 'requests==2.32.4\npyyaml==6.0.2\n' > requirements.txt
git add -A
git commit -m "Remove the webhook, update dependencies"
```

Each finding gets a fingerprint and is compared with the previous run of the same branch. On the
new run's Security tab, filter by status **fixed**: the webhook and the old pyyaml and requests
findings are there. Anything still found would be **existing**, and anything new, **new**. Only
scanners that ran both times are compared.

The project's **Security** page shows the counts over the branch's recent runs, and the findings
still open, each with the run where it was first seen.

## What you learned

- Any scanner that writes SARIF can report to Launchway with `reports.findings`.
- Findings are compared run to run: new, existing, fixed. Secret values are never kept.
- SBOMs make every component searchable across projects.

## What next

- [Policy gates and waivers](policy-gates.md): stop a deployment when findings are too severe.
- [Build and sign an image](build-and-sign-image.md): image steps scan what they build.
- [Security findings guide](../guide/security-findings.md): severities, fingerprints, limits.
