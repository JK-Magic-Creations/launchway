#!/bin/sh
# Known vulnerabilities in the Go code, with govulncheck (the Go team's tool, backed by the Go
# vulnerability database). Policy:
#   - a vulnerability the code can REACH (a call path from our code to the vulnerable function)
#     fails the build, in the source and in the built binary;
#   - one in a package we import or a module we require but never call is advisory: it is printed,
#     and the dependency is updated at the next routine bump (it cannot be exploited through us
#     today, but a later change could start calling it).
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
GOVULNCHECK='golang.org/x/vuln/cmd/govulncheck@v1.10.0'
echo "== source (fails on reachable findings)"
go run "$GOVULNCHECK" ./...
echo "== advisory: imported or required but not called"
go run "$GOVULNCHECK" -format json ./... | jq -r 'select(.finding != null) | select(.finding.trace[0].function == null) | "  \(.finding.osv) in \(.finding.trace[0].module) \(.finding.trace[0].version) (fixed in \(.finding.fixed_version // "no fix yet"))"' | sort -u
if [ -x bin/launchway ]; then
  echo "== built binary (what ships)"
  go run "$GOVULNCHECK" -mode=binary bin/launchway
fi
