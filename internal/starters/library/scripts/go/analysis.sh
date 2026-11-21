#!/bin/sh
# Static analysis with staticcheck. PARAM_ANALYSIS_MODE: enforce or report.
set -e
. "$(dirname "$0")/../common.sh"
enter
GOBIN=${GOBIN:-$(go env GOPATH)/bin}
export GOBIN PATH="$GOBIN:$PATH"
command -v staticcheck >/dev/null 2>&1 || run go install honnef.co/go/tools/cmd/staticcheck@latest
rc=0
run staticcheck ./... || rc=$?
if [ "$rc" != 0 ] && [ "${PARAM_ANALYSIS_MODE:-report}" = report ]; then
  say "staticcheck reported findings (analysis_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
