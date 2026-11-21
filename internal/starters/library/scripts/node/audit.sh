#!/bin/sh
# Known vulnerabilities in the dependencies, with the package manager's own audit.
# PARAM_AUDIT_MODE: enforce (high or critical fails the step) or report.
set -e
. "$(dirname "$0")/../common.sh"
enter
if [ -f pnpm-lock.yaml ]; then cmd="pnpm audit --audit-level high"
elif [ -f yarn.lock ]; then cmd="yarn npm audit --severity high 2>/dev/null || yarn audit --level high"
else cmd="npm audit --audit-level=high"
fi
rc=0
run "$cmd" || rc=$?
if [ "$rc" != 0 ] && [ "${PARAM_AUDIT_MODE:-enforce}" = report ]; then
  say "the audit found vulnerable dependencies (audit_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
