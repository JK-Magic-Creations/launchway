#!/bin/sh
# Counts TODO and FIXME notes. With the template's strict parameter, any
# note fails the step.
set -e
count=$(grep -rIn -e TODO -e FIXME --exclude-dir=.git --exclude-dir=.launchway . 2>/dev/null | wc -l | tr -d ' ')
echo "$count note(s) left in the code"
echo "count=$count" >> "$LAUNCHWAY_OUTPUTS"
if [ "$PARAM_STRICT" = "true" ] && [ "$count" -gt 0 ]; then
  echo "strict is on, and notes are left" >&2
  exit 1
fi
