#!/bin/sh
# pytest with a JUnit report (Tests tab) and a Cobertura coverage report (Coverage tab), both in reports/
# (reports/<values>/ for a matrix member). The repository need not list pytest-cov: it is installed for the step, at a
# pinned version. The options reach pytest through PYTEST_ADDOPTS, so they hold for a command of your own too
# (test_command: "make test", "python -m pytest -k fast", a script that calls pytest). No tests at all is not a failure:
# it is said in the log. When pytest is not what runs, the log says in plain words that no report was produced.
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
want_cov=${PARAM_COVERAGE:-true}
python -c "import pytest" 2>/dev/null || run pip install --quiet pytest==9.2.0
[ "$want_cov" = true ] && { python -c "import pytest_cov" 2>/dev/null || run pip install --quiet pytest-cov==7.1.0; }
# The tests import the project's own packages from where they stand, as "python -m pytest" does.
export PYTHONPATH="$PWD${PYTHONPATH:+:$PYTHONPATH}"
opts="--junitxml=$rd/junit.xml"
if [ "$want_cov" = true ]; then
  opts="$opts --cov=. --cov-report=xml:$rd/coverage.xml --cov-report=term"
  # Measure the project's own code: not the virtual environment, not the tools' caches, not the reports. A
  # repository that configures coverage itself keeps its own settings.
  if [ ! -f .coveragerc ] && ! grep -qs 'tool.coverage' pyproject.toml && ! grep -qs '^\[coverage:' setup.cfg tox.ini; then
    rc=$(lw_tools)/coveragerc
    mkdir -p "$(dirname "$rc")"
    printf '[run]\nomit =\n    .venv*/*\n    venv/*\n    */site-packages/*\n    .cache/*\n    reports/*\n    */tests/*\n    */test_*.py\n    */conftest.py\n' > "$rc"
    export COVERAGE_RCFILE="$rc"
  fi
fi
export PYTEST_ADDOPTS="$opts${PYTEST_ADDOPTS:+ $PYTEST_ADDOPTS}"
cmd=${PARAM_TEST_COMMAND:-python -m pytest -q}
echo "+ $cmd   (PYTEST_ADDOPTS=$PYTEST_ADDOPTS)"
status=0
eval "$cmd" || status=$?
none=
if [ "$status" -eq 5 ]; then
  none=1
  say "pytest collected no tests; nothing to report (add tests under tests/)"
  status=0
fi
if [ ! -s "$rd/junit.xml" ] && [ -z "$none" ]; then
  no_report "TEST REPORT" "The command \"$cmd\" did not write $rd/junit.xml, so it did not run pytest (or ran it without the options this starter hands it)." "run pytest from the command (for example: test_command: python -m pytest), or make the command write JUnit XML to $rd/junit.xml"
elif [ "$want_cov" = true ] && [ ! -s "$rd/coverage.xml" ]; then
  say "tests were reported, but no coverage was produced (pytest found no code to measure); set coverage: false to stop asking for it"
fi
exit "$status"
