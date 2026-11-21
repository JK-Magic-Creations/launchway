#!/bin/sh
# pytest with a JUnit report (Tests tab) and a Cobertura coverage report
# (Coverage tab), both in reports/ (reports/<values>/ for a matrix member). No tests at all is not a failure: it is said in the log.
set -e
. "$(dirname "$0")/../common.sh"
enter
. "$(venv_dir)/bin/activate"
rd=$(report_dir)
mkdir -p "$rd"
if [ -n "${PARAM_TEST_ENV:-}" ]; then
  # KEY=VALUE lines, for settings such as a database address beside the tests.
  eval "$(printf '%s\n' "$PARAM_TEST_ENV" | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=\(.*\)$/export \1='"'"'\2'"'"'/p')"
fi
cmd=${PARAM_TEST_COMMAND:-}
[ -n "$cmd" ] || cmd="pytest -q --junitxml=$rd/junit.xml --cov=. --cov-report=xml:$rd/coverage.xml"
echo "+ $cmd"
status=0
eval "$cmd" || status=$?
if [ "$status" -eq 5 ]; then
  say "pytest collected no tests; nothing to report (add tests under tests/)"
  status=0
fi
exit "$status"
