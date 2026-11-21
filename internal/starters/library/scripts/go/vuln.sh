#!/bin/sh
# Reachable vulnerabilities in the dependencies, with govulncheck.
set -e
. "$(dirname "$0")/../common.sh"
enter
GOBIN=${GOBIN:-$(go env GOPATH)/bin}
export GOBIN PATH="$GOBIN:$PATH"
command -v govulncheck >/dev/null 2>&1 || run go install golang.org/x/vuln/cmd/govulncheck@latest
run govulncheck ./...
