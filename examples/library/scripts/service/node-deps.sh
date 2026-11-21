#!/bin/sh
# The Node dependencies: npm ci with the lock file, else npm install.
set -e
. "$(dirname "$0")/enter.sh"
CMD=${PARAM_INSTALL_COMMAND:-}
if [ -z "$CMD" ]; then
  if [ -f package-lock.json ]; then CMD='npm ci'; else CMD='npm install'; fi
fi
echo "+ $CMD"
eval "$CMD"
