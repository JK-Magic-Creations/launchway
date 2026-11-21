#!/bin/sh
# A virtual environment for the tests and linters, and the dependencies in it.
set -e
. "$(dirname "$0")/enter.sh"
python3 -m venv "$VENV"
PATH="$VENV/bin:$PATH"
CMD=${PARAM_INSTALL_COMMAND:-}
if [ -z "$CMD" ]; then
  if [ -f requirements.txt ]; then
    CMD='pip install --quiet -r requirements.txt pytest pytest-cov'
  else
    CMD='pip install --quiet pytest pytest-cov'
  fi
fi
echo "+ $CMD"
eval "$CMD"
