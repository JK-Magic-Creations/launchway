#!/bin/sh
# DAST baseline: the OWASP ZAP baseline scan (passive checks plus a short spider; it sends no
# attack payloads) against a throwaway Launchway with demo data on 127.0.0.1, started by
# scripts/a11y-server.sh (needs make all and Git). Writes bin/zap-report.json and bin/zap-report.html.
# Optional: it needs a container runtime and the ZAP image. Findings of level WARN fail unless
# quality/zap-rules.tsv lowers or ignores the rule (with a reason in the file).
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
mkdir -p bin
export CONTAINER ZAP_IMAGE ROOT
exec scripts/a11y-server.sh sh -c '
  target="$LAUNCHWAY_A11Y_URL"
  port="${target##*:}"
  out="$ROOT/bin/zap"; mkdir -p "$out"; chmod 777 "$out"
  # The container reaches the host loopback through the host network.
  $CONTAINER run --rm --network host --security-opt label=disable -v "$out:/zap/wrk:rw" -v "$ROOT/quality/zap-rules.tsv:/zap/wrk/rules.tsv:ro" \
    "$ZAP_IMAGE" zap-baseline.py -t "$target" -c rules.tsv -J zap-report.json -r zap-report.html || rc=$?
  echo "zap exit status: ${rc:-0} (0 = clean, 2 = warnings, 1 = failures)"
  exit "${rc:-0}"
'
