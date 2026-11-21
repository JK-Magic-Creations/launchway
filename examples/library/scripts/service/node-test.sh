#!/bin/sh
# The tests of a Node service. The default asks the test script for JUnit XML
# (jest-junit or Vitest's junit reporter, as the repository has them) and an
# LCOV coverage file; a test_command of your own writes them where the
# template looks: reports/unit/*.xml and reports/coverage/*.info.
set -e
. "$(dirname "$0")/enter.sh"
mkdir -p reports/unit reports/coverage
CMD=${PARAM_TEST_COMMAND:-}
if [ -z "$CMD" ]; then
  CMD='npm test --if-present'
fi
echo "+ $CMD"
eval "$CMD"
