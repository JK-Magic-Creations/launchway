#!/bin/sh
# Builds every package. When there are programs (main packages) they are
# written to bin/; a library has none, and building it is then a compile check.
set -e
. "$(dirname "$0")/../common.sh"
enter
if go list -f '{{.Name}}' ./... 2>/dev/null | grep -qx main; then
  mkdir -p bin
  run go build -o bin/ ./...
  ls bin | sed 's/^/built: /'
else
  say "no main package here: this is a library, so the build is a compile check"
  run go build ./...
fi
