#!/bin/sh
# detect-secrets over the files git tracks. With a .secrets.baseline in the
# repository a new finding fails the step; without one findings are printed as
# warnings, so a repository starts with a report and gets strict by committing
# a baseline.
set -e
. "$(dirname "$0")/enter.sh"
python3 -m venv /tmp/secrets-venv
/tmp/secrets-venv/bin/pip install --quiet detect-secrets
files=$(git ls-files 2>/dev/null || true)
if [ -z "$files" ]; then
  echo "no tracked files: nothing to scan"
  exit 0
fi
# shellcheck disable=SC2086 # file names without spaces, as git lists them
if [ -f .secrets.baseline ]; then
  echo "+ detect-secrets-hook --baseline .secrets.baseline"
  /tmp/secrets-venv/bin/detect-secrets-hook --baseline .secrets.baseline $files
else
  echo "no .secrets.baseline: findings are warnings"
  /tmp/secrets-venv/bin/detect-secrets scan $files | grep -q '"results": {}' || echo "warning: possible secrets found; commit a .secrets.baseline to make this blocking" >&2
fi
