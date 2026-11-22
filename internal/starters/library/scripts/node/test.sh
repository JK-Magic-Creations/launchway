#!/bin/sh
# Runs the tests and collects what the Tests and Coverage tabs show, without the repository listing a reporter:
#   jest        jest-junit and cobertura coverage, installed for the step (pinned) and added to the command
#   vitest      its own junit reporter, and its v8 coverage provider (installed at vitest's own version)
#   mocha       mocha-junit-reporter, and c8 for coverage
#   node --test its own junit reporter
# This holds for a command of your own too (test_command, or the package's test script, or a script that runs one of
# these), because the reporter is added to the command that runs the tests. A command it cannot read is run as it is,
# and the log says in plain words that no report was produced and the one line that would produce it.
# Reports go to reports/junit.xml and reports/coverage.xml (reports/<values>/ for a matrix member).
set -e
. "$(dirname "$0")/../common.sh"
enter
rd=$(report_dir)
mkdir -p "$rd"
if [ -n "${PARAM_TEST_ENV:-}" ]; then
  eval "$(printf '%s\n' "$PARAM_TEST_ENV" | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=\(.*\)$/export \1='"'"'\2'"'"'/p')"
fi
pm=npm
[ -f pnpm-lock.yaml ] && pm=pnpm
[ -f yarn.lock ] && pm=yarn
test_script=$(node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(String(s.test||""))')
cmd=${PARAM_TEST_COMMAND:-}
if [ -z "$cmd" ]; then
  case "$test_script" in
    ""|*"no test specified"*) say "package.json has no real test script, so no tests ran (add a \"test\" script)"; exit 0 ;;
  esac
  case "$pm" in yarn) cmd="yarn test" ;; *) cmd="$pm test" ;; esac
fi
want_cov=${PARAM_COVERAGE:-true}

# script_of NAME is the text of a package.json script.
script_of() { node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(String(s[process.argv[1]]||""))' "$1"; }
# The text that says which runner the command uses: the command, and the script it runs when it is "npm test",
# "yarn run test:unit" and the like.
shown=$cmd
case "$cmd" in
  "npm test"*|"npm t"|"npm run "*|"pnpm test"*|"pnpm run "*|"yarn test"*|"yarn run "*|"npm run-script "*)
    name=$(printf '%s\n' "$cmd" | sed -E 's/^(npm|pnpm|yarn)( run-script| run)? +//; s/ .*//')
    [ -n "$(script_of "$name")" ] && shown="$cmd $(script_of "$name")" ;;
esac
has_word() { printf '%s\n' "$shown" | grep -Eq "(^|[[:space:]/;&|\"'])$1([[:space:]\"';&|]|\$)"; }
kind=
if has_word vitest; then kind=vitest
elif has_word jest; then kind=jest
elif has_word mocha; then kind=mocha
elif printf '%s\n' "$shown" | grep -Eq '(^|[[:space:]])node([[:space:]]+[^;&|]*)?--test'; then kind=nodetest
fi

tools=$(lw_tools)
install_tool() {  # PACKAGE@VERSION: into the step's own tools directory, not into the repository
  mkdir -p "$tools"
  run npm install --prefix "$tools" --no-save --no-audit --no-fund --loglevel=error "$1" || return 1
}
cov_dir="$rd/cov"
flags=
case "$kind" in
  jest)
    if install_tool jest-junit@17.0.0; then
      export JEST_JUNIT_OUTPUT_DIR="$rd" JEST_JUNIT_OUTPUT_NAME=junit.xml
      flags="--reporters=default --reporters=$tools/node_modules/jest-junit"
    fi
    [ "$want_cov" = true ] && flags="$flags --coverage --coverageReporters=cobertura --coverageReporters=text-summary --coverageDirectory=$cov_dir" ;;
  vitest)
    flags="--reporter=default --reporter=junit --outputFile.junit=$rd/junit.xml"
    if [ "$want_cov" = true ]; then
      v=$(node -e 'try{process.stdout.write(require("vitest/package.json").version)}catch(e){}')
      if [ -n "$v" ] && { [ -d node_modules/@vitest/coverage-v8 ] || run npm install --no-save --no-audit --no-fund --loglevel=error "@vitest/coverage-v8@$v"; }; then
        flags="$flags --coverage.enabled --coverage.reporter=cobertura --coverage.reporter=text-summary --coverage.reportsDirectory=$cov_dir"
      else
        say "coverage was not collected: the v8 coverage provider for vitest $v could not be installed"
      fi
    fi ;;
  mocha)
    if install_tool mocha-junit-reporter@2.2.1; then
      export MOCHA_FILE="$rd/junit.xml"
      flags="--reporter spec --reporter $tools/node_modules/mocha-junit-reporter"
      # Two reporters at once need mocha-multi-reporters; with one, the junit file is written and the log carries dots.
      flags="--reporter $tools/node_modules/mocha-junit-reporter"
    fi ;;
  nodetest)
    flags="--test-reporter=spec --test-reporter-destination=stdout --test-reporter=junit --test-reporter-destination=$rd/junit.xml" ;;
esac

# add_flags puts the flags into the command where they belong: right after the runner's name, or after "--" when
# the command is a package script.
final=$cmd
if [ -n "$flags" ]; then
  case "$cmd" in
    "npm test"*|"npm t"|"npm run "*|"pnpm test"*|"pnpm run "*|"npm run-script "*) final="$cmd -- $flags" ;;
    "yarn test"*|"yarn run "*) final="$cmd $flags" ;;
    *)
      word=$kind; [ "$kind" = nodetest ] && word=--test
      final=$(printf '%s\n' "$cmd" | sed -E "s#(^|[[:space:]/])$word([[:space:]]|\$)#\1$word $flags\2#g") ;;
  esac
fi
if [ "$kind" = mocha ] || [ "$kind" = nodetest ]; then
  if [ "$want_cov" = true ] && install_tool c8@12.0.0; then
    final="$tools/node_modules/.bin/c8 --reporter=cobertura --reporter=text-summary --reports-dir=$cov_dir sh -c '$(printf '%s' "$final" | sed "s/'/'\\\\''/g")'"
  fi
fi

status=0
run "$final" || status=$?

# What came out of it. The coverage tools write cobertura-coverage.xml (jest, c8) or coverage.xml in their directory.
for f in "$cov_dir/cobertura-coverage.xml" "$cov_dir/coverage.xml"; do
  [ -s "$f" ] && { mv "$f" "$rd/coverage.xml"; break; }
done
rm -rf "$cov_dir"
if [ ! -s "$rd/junit.xml" ]; then
  case "$kind" in
    "") no_report "TEST REPORT" "The command \"$cmd\" is not a runner this starter can read results from (it knows jest, vitest, mocha and node --test)." "make the command write JUnit XML to $rd/junit.xml (and Cobertura coverage to $rd/coverage.xml), or run the tests with one of those runners." ;;
    *) no_report "TEST REPORT" "The $kind run ended without writing $rd/junit.xml." "check that the command runs $kind itself (a wrapper script that does not pass its arguments on drops the reporter); the arguments added were: $flags" ;;
  esac
elif [ "$want_cov" = true ] && [ ! -s "$rd/coverage.xml" ]; then
  say "tests were reported, but no coverage was produced (the runner or its coverage tool wrote none); set coverage: false to stop asking for it"
fi
exit "$status"
