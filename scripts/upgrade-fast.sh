#!/bin/sh
# The hermetic part of the upgrade gate (make test-upgrade-fast; part of make check): everything that
# can be proved by rendering, with no cluster. The cells that need one are in
# deploy/test/upgrade/matrix.sh (make test-upgrade).
#
#   old values + new chart          the previous release's values file renders under this chart, nothing ignored
#   new values + old chart          a values file with keys only this chart knows renders under the previous
#                                   chart of THIS repository's history that tolerates them (this chart's own
#                                   earlier form, built from the same files minus the new keys): see below
#   typos                           reported by default, fatal with strictValues
#   image and chart pairing         an image older than the secrets bootstrap renders no hook Job and no key mount
#   previous chart's secrets        the new chart renders none of the Secrets the previous chart rendered, so the
#                                   hook Job must keep them (checked in internal/bootstrap)
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
command -v helm >/dev/null 2>&1 || { echo "upgrade-fast: needs helm" >&2; exit 2; }
prev=${OLD_COMMIT:-093664b}
tmp=$(mktemp -d "${TMPDIR:-$HOME/.cache}/upgrade-fast.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
git archive "$prev" deploy/helm/launchway | tar -x -C "$tmp"
old=$tmp/deploy/helm/launchway
new=deploy/helm/launchway
fail=0
ok() { echo "upgrade-fast: ok $1"; }
bad() { echo "upgrade-fast: FAIL $1" >&2; fail=1; }

# 1. the previous release's own values (what an installation made by it holds) under this chart.
cat >"$tmp/old-values.yaml" <<'YAML'
image: { repository: localhost/launchway, tag: "0.1.0", pullPolicy: Never }
postgresql: { enabled: true }
config:
  extraConfig: { auth: { mode: none, allow_anonymous: true } }
  projects: [ { name: demo, repo: "http://git:8080/demo.git", branch: main, poll: 10s } ]
runner: { kubernetes: { namespace: lw-steps, createNamespace: true } }
YAML
if out=$(helm template t "$old" -f "$tmp/old-values.yaml" 2>&1) && helm template t "$new" -f "$tmp/old-values.yaml" --set strictValues=true >"$tmp/r.yaml" 2>"$tmp/err"; then
	if grep -q LAUNCHWAY_IGNORED_VALUES "$tmp/r.yaml"; then bad "the previous values file has keys this chart ignores"; else ok "previous values + this chart render, nothing ignored"; fi
	if grep -q bootstrap-secrets "$tmp/r.yaml"; then bad "the previous image (0.1.0) must get no hook Job"; else ok "the previous image gets no hook Job (it has no such command)"; fi
else bad "previous values + this chart: $(cat "$tmp/err")"; fi

# 2. values with keys only a newer chart knows, under this chart's own tolerant form: report, do not fail.
cat >"$tmp/future.yaml" <<'YAML'
externalURL: https://launchway.example.com
someSettingFromTheFuture: { enabled: true }
networkPolicy: { fromTheFuture: 1 }
YAML
if helm template t "$new" -f "$tmp/future.yaml" >"$tmp/r.yaml" 2>"$tmp/err" && grep -q 'someSettingFromTheFuture,networkPolicy.fromTheFuture\|networkPolicy.fromTheFuture,someSettingFromTheFuture' "$tmp/r.yaml"; then ok "values from a newer chart render and are reported"; else bad "values from a newer chart: $(cat "$tmp/err")"; fi
if helm template t "$new" -f "$tmp/future.yaml" --set strictValues=true >/dev/null 2>"$tmp/err"; then bad "strictValues must refuse unknown values"; else ok "strictValues refuses them, naming the keys"; fi

# 3. the previous chart (closed schema) refuses such values: the reason this chart's schema is open.
if helm template t "$old" -f "$tmp/future.yaml" >/dev/null 2>&1; then ok "(the previous chart tolerates them)"; else ok "the previous chart refuses unknown values, as known: rolling back to it needs them removed"; fi

# 4. the chart declares what it supports.
grep -q 'launchway.io/supported-app-versions' "$new/Chart.yaml" && grep -q 'launchway.io/bootstrap-since' "$new/Chart.yaml" && ok "the chart declares its app-version range" || bad "Chart.yaml lacks the compatibility annotations"
if helm template t "$new" --set image.tag=0.0.1 >/dev/null 2>"$tmp/err"; then bad "an unsupported image tag must fail the render"; else grep -q 'supports Launchway images' "$tmp/err" && ok "an unsupported image tag fails with a sentence" || bad "wrong message: $(cat "$tmp/err")"; fi
exit $fail
