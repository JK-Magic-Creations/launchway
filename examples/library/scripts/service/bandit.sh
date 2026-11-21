#!/bin/sh
# bandit over the Python code, with SARIF for the Security tab. In enforce mode
# a finding of high severity and high confidence fails the step; in report mode
# it is recorded and the step passes.
set -e
. "$(dirname "$0")/enter.sh"
if ! find . -name '*.py' -not -path './.launchway-venv/*' -not -path './.cache/*' | grep -q .; then
  echo "no Python files: nothing to scan"
  exit 0
fi
python3 -m venv /tmp/bandit-venv
/tmp/bandit-venv/bin/pip install --quiet 'bandit[sarif]'
mkdir -p reports/security
status=0
# shellcheck disable=SC2086 # scan_paths is a list of paths
/tmp/bandit-venv/bin/bandit -r ${PARAM_SCAN_PATHS:-.} -x ./.launchway-venv,./.cache,./node_modules \
  --severity-level high --confidence-level high -f sarif -o reports/security/bandit.sarif || status=$?
if [ "$status" -ne 0 ] && [ "${PARAM_SCAN_MODE:-enforce}" = report ]; then
  echo "bandit found problems; scan_mode is report, so the step passes"
  exit 0
fi
exit "$status"
