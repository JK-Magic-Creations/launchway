#!/bin/sh
# Lists the licenses of the dependencies with go-licenses; fails only when one
# is on PARAM_LICENSE_DENY (go-licenses license types: forbidden, restricted, ...).
set -e
. "$(dirname "$0")/../common.sh"
enter
GOBIN=${GOBIN:-$(go env GOPATH)/bin}
export GOBIN PATH="$GOBIN:$PATH"
command -v go-licenses >/dev/null 2>&1 || run go install github.com/google/go-licenses@latest || {
  [ -z "${PARAM_LICENSE_DENY:-}" ] && { say "go-licenses could not be installed, so the licenses are not listed"; exit 0; }
}
run go-licenses report ./... || say "could not list the licenses"
if [ -n "${PARAM_LICENSE_DENY:-}" ]; then
  run go-licenses check ./... --disallowed_types="$PARAM_LICENSE_DENY"
else
  say "license_deny is empty, so licenses are listed and nothing fails (set license_deny: [forbidden, restricted] to block)"
fi
