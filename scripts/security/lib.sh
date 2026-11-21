#!/bin/sh
# Shared settings for the security scans (make security). Each scanner runs from a container at a
# pinned version, or with `go run` at a pinned version, so nothing is installed on the machine and
# nothing is bundled into Launchway. See docs/security/how-launchway-is-scanned.md.
# Licenses of the scanners are recorded in docs/security/how-launchway-is-scanned.md.
CONTAINER="${CONTAINER:-podman}"
GITLEAKS_IMAGE='ghcr.io/gitleaks/gitleaks:v8.30.1@sha256:b109bc5f8f76a38196a3e413704fc5b9e3c32360bce4e4b603bd6f45b3721dbb'
TRIVY_IMAGE='ghcr.io/aquasecurity/trivy:0.76.2'
HADOLINT_IMAGE='ghcr.io/hadolint/hadolint:v2.15.1'
ZAP_IMAGE='ghcr.io/zaproxy/zaproxy:2.17.0'
OSV_SCANNER='github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.7.1'
KUBECONFORM='github.com/yannh/kubeconform/cmd/kubeconform@v0.8.0'
KUBE_LINTER='golang.stackrox.io/kube-linter/cmd/kube-linter@v0.8.3'
SCORECARD='github.com/ossf/scorecard/v5@v5.7.0'
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
export ROOT
