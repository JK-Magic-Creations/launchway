#!/bin/sh
# Copies the templates the two charts share (deploy/helm/shared) into each chart, as
# templates/_shared-<name>.tpl. Run after editing deploy/helm/shared; internal/config/chart_test.go
# fails if a copy is out of date.
#
#   scripts/charts-sync.sh          copy
#   scripts/charts-sync.sh --check  only report copies that differ (exit 1)
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
check=no
[ "${1:-}" != --check ] || check=yes
status=0
for src in deploy/helm/shared/_*.tpl; do
	base=$(basename "$src")
	for chart in deploy/helm/launchway deploy/helm/launchway-runner; do
		dst=$chart/templates/_shared-${base#_}
		if cmp -s "$src" "$dst"; then
			continue
		fi
		if [ "$check" = yes ]; then
			echo "charts-sync: $dst differs from $src; run make charts-sync" >&2
			status=1
		else
			cp "$src" "$dst"
			echo "charts-sync: updated $dst"
		fi
	done
done
exit $status
