#!/bin/sh
# The script of the check secrets of Standing orders, byte for byte (generated; see internal/standards/starters_wiring_test.go).
set +e
mkdir -p .standing
git config --global --add safe.directory /workspace >/dev/null 2>&1
opts=""
if [ -n "$LAUNCHWAY_STANDING_SINCE" ] && git cat-file -e "$LAUNCHWAY_STANDING_SINCE^{commit}" 2>/dev/null; then
  opts="--log-opts=$LAUNCHWAY_STANDING_SINCE..HEAD"
  echo "Reading the commits since $LAUNCHWAY_STANDING_SINCE."
else
  echo "Reading every commit in the history."
fi
if [ -d .git ]; then
  gitleaks git . $opts --no-banner --redact --exit-code 0 --report-format sarif --report-path /tmp/r.sarif
else
  echo "The workspace has no Git history; reading the files instead."
  gitleaks dir . --no-banner --redact --exit-code 0 --report-format sarif --report-path /tmp/r.sarif
fi
[ -s /tmp/r.sarif ] || { echo "gitleaks wrote no report, so this check did not run."; exit 1; }
cp /tmp/r.sarif .standing/secrets.sarif
