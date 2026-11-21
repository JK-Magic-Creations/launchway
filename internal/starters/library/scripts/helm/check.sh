#!/bin/sh
# helm lint, helm template with the chart's own values, and helm package.
set -e
. "$(dirname "$0")/../common.sh"
enter
chart=${PARAM_CHART:-.}
[ -f "$chart/Chart.yaml" ] || { echo "no Chart.yaml in $chart: set chart: to the chart's directory" >&2; exit 1; }
run helm lint "$chart" --strict
run helm template check "$chart" > /dev/null
mkdir -p dist
run helm package "$chart" --destination dist
