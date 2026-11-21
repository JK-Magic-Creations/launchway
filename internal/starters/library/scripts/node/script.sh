#!/bin/sh
# Runs package.json's script NAME (PARAM_NAME) with the repository's package
# manager, or says it is not defined and succeeds when PARAM_OPTIONAL is true.
set -e
. "$(dirname "$0")/../common.sh"
enter
name=${LW_SCRIPT:-${PARAM_NAME:-}}
[ -n "$name" ] || { echo "which script? set LW_SCRIPT" >&2; exit 2; }
has=$(node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(String(s[process.argv[1]]||""))' "$name")
if [ -z "$has" ]; then
  say "package.json has no \"$name\" script, so there is nothing to run"
  [ "${PARAM_OPTIONAL:-true}" = true ] && exit 0
  exit 1
fi
if [ -f pnpm-lock.yaml ]; then run pnpm run "$name"
elif [ -f yarn.lock ]; then run yarn run "$name"
else run npm run "$name"
fi
