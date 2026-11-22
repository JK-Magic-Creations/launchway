#!/bin/sh
# Misconfiguration scan of infrastructure code with trivy config; SARIF in .launchway/scan/iac.sarif.
#
# PARAM_MODE: report (findings are recorded, the step passes) or enforce (findings fail it).
# PARAM_SEVERITY: the severities reported. PARAM_CHECK_UPDATES: true downloads the newest checks.
#
# The scanner reads its checks from the copy built into it, which is the one written for this version. The checks it
# would download are the newest, and a pinned scanner is older than they are: it cannot type-check some of them and
# prints "[rego] Error occurred while parsing" for each, on every run, and still exits 0. A scan that says it errored is
# not a scan that passed, so any error line of the scanner's own fails the step, in report mode too.
set -u
mkdir -p .launchway/scan
log=.launchway/scan/iac.log
ec=0
[ "${PARAM_MODE:-report}" = enforce ] && ec=1
update=--skip-check-update
[ "${PARAM_CHECK_UPDATES:-false}" = true ] && update=
echo "+ trivy config $update --format sarif --severity ${PARAM_SEVERITY:-HIGH,CRITICAL} --exit-code $ec ."
# shellcheck disable=SC2086
trivy config $update --format sarif --output .launchway/scan/iac.sarif --severity "${PARAM_SEVERITY:-HIGH,CRITICAL}" --exit-code "$ec" . 2> "$log"
rc=$?
cat "$log" >&2
if grep -Eq '[[:space:]](ERROR|FATAL)[[:space:]]' "$log"; then
  echo "launchway: the scanner reported errors of its own (above), so the scan is not trusted and the step fails" >&2
  [ "$rc" -ne 0 ] || rc=1
fi
exit "$rc"
