#!/bin/sh
# Lists the licenses of the installed dependencies; fails only when one is on
# PARAM_LICENSE_DENY (comma separated license names, such as "GPL-3.0,AGPL-3.0").
set -e
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
. "$(venv_dir)/bin/activate"
command -v pip-licenses >/dev/null 2>&1 || py_install pip-licenses
run pip-licenses --format=plain --summary
if [ -n "${PARAM_LICENSE_DENY:-}" ]; then
  run pip-licenses --fail-on="$(printf '%s' "$PARAM_LICENSE_DENY" | tr ',' ';')"
else
  say "license_deny is empty, so licenses are listed and nothing fails (set license_deny: [GPL-3.0] to block)"
fi
