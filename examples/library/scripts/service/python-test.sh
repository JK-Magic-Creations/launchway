#!/bin/sh
# The tests, with JUnit XML under reports/unit and coverage under
# reports/coverage, which the template declares as the step's reports.
set -e
. "$(dirname "$0")/enter.sh"
PATH="$VENV/bin:$PATH"
mkdir -p reports/unit reports/coverage
CMD=${PARAM_TEST_COMMAND:-}
if [ -z "$CMD" ]; then
  # python -m pytest puts the repository root on sys.path, which tests that
  # import their own package from the root rely on.
  CMD='python -m pytest -q --junitxml=reports/unit/pytest.xml --cov=. --cov-report=xml:reports/coverage/coverage.xml'
fi
echo "+ $CMD"
eval "$CMD"
