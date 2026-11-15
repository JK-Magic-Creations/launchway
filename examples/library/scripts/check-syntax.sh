#!/bin/sh
# Parses every shell script in the repository, without running it.
set -e
found=0
for f in $(find . -name '*.sh' -not -path './.git/*' -not -path './.launchway/lib/*'); do
  sh -n "$f"
  echo "ok  $f"
  found=$((found + 1))
done
echo "$found shell script(s) parse"
