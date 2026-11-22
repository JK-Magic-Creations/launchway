#!/bin/sh
# Known vulnerabilities in the project's dependencies, with pip-audit.
#
# What is audited is the list of packages the project's package manager installed (uv sync, poetry install, pdm install,
# pip install -r), recorded by the setup step before any tool was added: the project's locked dependencies. It is not the
# environment as it is now, which also holds the pytest, ruff and other tools of these steps, and not the pip, setuptools
# and wheel that the image seeded: a finding in those is the image's to fix, not the project's. pip-audit itself is installed
# in an environment of its own, so that it changes nothing the project runs on. The log says what was audited and what was left
# out. PARAM_AUDIT_MODE: enforce (a finding fails the step) or report.
set -e
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
. "$(venv_dir)/bin/activate"
deps=$(deps_file)
if [ ! -s "$deps" ]; then
  say "the setup step recorded no dependencies here; reading what is installed now"
  record_deps
fi
n=$(grep -c . "$deps" || true)
if [ "$n" -eq 0 ]; then
  say "the project has no dependencies installed, so there is nothing to audit"
  exit 0
fi
say "auditing the $n dependencies the project installed (listed in $deps): pip, setuptools and wheel of the image and the tools of these steps are not part of it"
auditenv="$(lw_tools)/audit-env"
if [ ! -x "$auditenv/bin/pip-audit" ]; then
  python3 -m venv "$auditenv"
  echo "+ pip install pip-audit (in its own environment, $auditenv)"
  "$auditenv/bin/python" -m pip install --quiet --disable-pip-version-check pip-audit
fi
rc=0
echo "+ pip-audit -r $deps --no-deps --disable-pip"
"$auditenv/bin/pip-audit" -r "$deps" --no-deps --disable-pip --progress-spinner off || rc=$?
if [ "$rc" != 0 ] && [ "${PARAM_AUDIT_MODE:-enforce}" = report ]; then
  say "pip-audit found vulnerable dependencies (audit_mode: report, so this does not fail the run)"; rc=0
fi
exit $rc
