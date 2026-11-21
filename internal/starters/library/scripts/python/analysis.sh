#!/bin/sh
# Static analysis for insecure code, with bandit: findings of high severity and
# high confidence fail the step in enforce mode. PARAM_ANALYSIS_EXCLUDE: paths to skip, comma separated.
set -e
. "$(dirname "$0")/../common.sh"
enter
. "$(venv_dir)/bin/activate"
command -v bandit >/dev/null 2>&1 || run pip install --quiet bandit
excl=${PARAM_ANALYSIS_EXCLUDE:-tests,.venv,.cache}
rc=0
run bandit -r . -lll -iii -x "$excl" -q || rc=$?
if [ "$rc" != 0 ] && [ "${PARAM_ANALYSIS_MODE:-enforce}" = report ]; then
  say "bandit reported findings (analysis_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
