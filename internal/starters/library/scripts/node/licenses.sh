#!/bin/sh
# Lists the licenses of the dependencies; fails only when one is on PARAM_LICENSE_DENY
# (comma separated license names, such as "GPL-3.0,AGPL-3.0").
set -e
. "$(dirname "$0")/../common.sh"
enter
run npx --yes license-checker --summary
if [ -n "${PARAM_LICENSE_DENY:-}" ]; then
  run npx --yes license-checker --failOn "$(printf '%s' "$PARAM_LICENSE_DENY" | tr ',' ';')" --summary
else
  say "license_deny is empty, so licenses are listed and nothing fails (set license_deny: [GPL-3.0] to block)"
fi
