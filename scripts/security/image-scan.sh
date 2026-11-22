#!/bin/sh
# Scans the Launchway image (build it first: make image) with Trivy: vulnerabilities in the
# operating system packages and in the Go binary, secrets, and image misconfigurations. Policy:
# HIGH and CRITICAL with a fix available fail; the image's own pipeline step (.launchway/pipeline.yaml,
# scan: fail_on: high) applies the same rule before anything is pushed. Exceptions are in
# quality/trivy-ignore.yaml with a reason and an expiry date.
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
IMAGE="${IMAGE:-launchway:$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
work="$(mktemp -d)"; chmod 755 "$work"; trap 'rm -rf "$work"' EXIT
$CONTAINER save -o "$work/image.tar" "$IMAGE"; chmod 644 "$work/image.tar"
cache="${TRIVY_CACHE_DIR:-$HOME/.cache/launchway-trivy}"; mkdir -p "$cache"
$CONTAINER run --rm --security-opt label=disable -v "$work:/work" -v "$ROOT/quality/trivy-ignore.yaml:/ignore.yaml:ro" -v "$cache:/root/.cache" \
  "$TRIVY_IMAGE" image --skip-version-check --input /work/image.tar --scanners vuln,secret --ignore-unfixed --ignorefile /ignore.yaml \
  --quiet --format json --output /work/r.json
# The scan only means something if the scanner recognized the distribution: an image without
# /etc/os-release is reported as having no operating system, and so no OS packages to check.
family="$(jq -r '.Metadata.OS.Family // "none"' "$work/r.json")"
[ "$family" != none ] || { echo "image-scan: the scanner could not tell the operating system; OS packages were NOT checked" >&2; exit 1; }
jq -r '[.Results[]? | (.Vulnerabilities[]?, .Secrets[]?)] | group_by(.Severity) | map("  \(.[0].Severity): \(length)") | .[]' "$work/r.json"
bad="$(jq '[.Results[]? | (.Vulnerabilities[]?, .Secrets[]?) | select(.Severity == "HIGH" or .Severity == "CRITICAL")] | length' "$work/r.json")"
if [ "$bad" -gt 0 ]; then
  jq -r '.Results[]? | .Target as $t | (.Vulnerabilities[]?, .Secrets[]?) | select(.Severity == "HIGH" or .Severity == "CRITICAL") | "  \(.Severity) \(.VulnerabilityID // .RuleID) \(.PkgName // $t) \(.InstalledVersion // "") fixed in \(.FixedVersion // "-")"' "$work/r.json" >&2
  echo "image-scan: $bad fixable HIGH or CRITICAL findings in $IMAGE" >&2
  exit 1
fi
echo "image-scan: $IMAGE ($family $(jq -r '.Metadata.OS.Name' "$work/r.json")): no fixable HIGH or CRITICAL findings"
