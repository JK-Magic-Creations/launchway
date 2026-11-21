#!/bin/sh
# The script of the check vuln-reach-go of Standing orders, byte for byte (generated; see internal/standards/starters_wiring_test.go).
set +e
mkdir -p .standing
git config --global --add safe.directory /workspace >/dev/null 2>&1
export GOFLAGS="-buildvcs=false -modcacherw" GOMODCACHE=/workspace/.standing/cache/gomod GOCACHE=/workspace/.standing/cache/gobuild
n=0; bad=0
find . -name go.mod -not -path './.standing/*' -not -path '*/vendor/*' -not -path '*/node_modules/*' | head -20 > /tmp/mods
while IFS= read -r mod; do
  d=$(dirname "$mod"); n=$((n+1))
  ( cd "$d" && go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 -format sarif ./... > "/tmp/g$n.sarif" 2> "/tmp/g$n.err" )
  if [ -s "/tmp/g$n.sarif" ]; then
    if [ "$d" != "." ]; then
      p=$(printf '%s' "${d#./}" | sed 's/[&#\\]/\\&/g')
      sed -E "s#\"uri\": ?\"([^:\"]+)\"#\"uri\": \"$p/\\1\"#g" "/tmp/g$n.sarif" > ".standing/vuln-reach-go-$n.sarif"
    else
      cp "/tmp/g$n.sarif" ".standing/vuln-reach-go-$n.sarif"
    fi
  else
    echo "govulncheck produced no report in $d:"; head -5 "/tmp/g$n.err"; bad=1
  fi
done < /tmp/mods
[ "$n" -gt 0 ] || { echo "No go.mod found; nothing to check."; exit 0; }
exit $bad
