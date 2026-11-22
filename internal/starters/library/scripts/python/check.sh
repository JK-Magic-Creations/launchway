#!/bin/sh
# Lint with ruff; also check formatting when the project configures ruff.
set -e
# Style is advice by default (lint_mode: advise): findings are printed and do not fail the run.
if [ "${PARAM_LINT_MODE:-advise}" = advise ] && [ -z "${LW_ENFORCED:-}" ]; then
  if LW_ENFORCED=1 sh "$0"; then exit 0; fi
  echo "launchway: the style findings above are advice (lint_mode: advise); they do not fail the run. Set lint_mode: enforce to block on them." >&2
  exit 0
fi
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
. "$(venv_dir)/bin/activate"
if [ -n "${PARAM_LINT_COMMAND:-}" ]; then run "$PARAM_LINT_COMMAND"; exit 0; fi
command -v ruff >/dev/null 2>&1 || py_install ruff
run ruff check .
if [ -f ruff.toml ] || [ -f .ruff.toml ] || grep -q '^\[tool\.ruff' pyproject.toml 2>/dev/null; then
  run ruff format --check .
else
  say "no ruff configuration, so formatting is not checked (add [tool.ruff] to pyproject.toml to turn it on)"
fi
