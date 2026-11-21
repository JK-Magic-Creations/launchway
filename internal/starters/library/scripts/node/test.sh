#!/bin/sh
# Runs the tests and, where the runner can, writes a JUnit report to
# $rd/junit.xml for the Tests tab:
#   vitest         built in
#   jest           with jest-junit installed (otherwise the log says so)
#   node --test    built in
# Anything else runs unchanged, with no report.
set -e
. "$(dirname "$0")/../common.sh"
enter
rd=$(report_dir)
mkdir -p "$rd"
if [ -n "${PARAM_TEST_ENV:-}" ]; then
  eval "$(printf '%s\n' "$PARAM_TEST_ENV" | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=\(.*\)$/export \1='"'"'\2'"'"'/p')"
fi
if [ -n "${PARAM_TEST_COMMAND:-}" ]; then run "$PARAM_TEST_COMMAND"; exit 0; fi
test_script=$(node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(String(s.test||""))')
case "$test_script" in
  ""|*"no test specified"*) say "package.json has no real test script, so no tests ran (add a \"test\" script)"; exit 0 ;;
esac
dep() { node -e 'const p=require("./package.json");const d={...p.dependencies,...p.devDependencies};process.exit(d[process.argv[1]]?0:1)' "$1"; }
pm=npm
[ -f pnpm-lock.yaml ] && pm=pnpm
[ -f yarn.lock ] && pm=yarn
if dep vitest; then
  run "$pm" exec vitest run --reporter=default --reporter=junit --outputFile.junit="$rd/junit.xml"
elif dep jest && [ -d node_modules/jest-junit ]; then
  JEST_JUNIT_OUTPUT_DIR=$rd JEST_JUNIT_OUTPUT_NAME=junit.xml run "$pm" exec jest --ci --reporters=default --reporters=jest-junit
elif dep jest; then
  say "jest is used but jest-junit is not installed, so no test report is written (npm install --save-dev jest-junit)"
  run "$pm" test
else
  case "$test_script" in
    *"node --test"*)
      run "$pm" test -- --test-reporter=spec --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination=$rd/junit.xml ;;
    *) say "no test report: this runner is not one Launchway can read a report from by itself; write JUnit XML to reports/ and it will be shown"
       run "$pm" test ;;
  esac
fi
