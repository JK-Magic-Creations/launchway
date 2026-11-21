#!/bin/sh
# Known vulnerabilities in the locked dependencies, with pip-audit.
# PARAM_AUDIT_MODE: enforce (a finding fails the step) or report.
set -e
. "$(dirname "$0")/../common.sh"
enter
. "$(venv_dir)/bin/activate"
command -v pip-audit >/dev/null 2>&1 || run pip install --quiet pip-audit
rc=0
run pip-audit --progress-spinner off || rc=$?
if [ "$rc" != 0 ] && [ "${PARAM_AUDIT_MODE:-enforce}" = report ]; then
  say "pip-audit found vulnerable dependencies (audit_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
