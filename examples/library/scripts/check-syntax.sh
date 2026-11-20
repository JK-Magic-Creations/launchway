#!/bin/sh
# Parses every shell script in the repository, without running it.
set -e
found=0
while IFS= read -r f; do
  [ -n "$f" ] || continue
  sh -n "$f"
  echo "ok  $f"
  found=$((found + 1))
done <<LIST
$(find . -name '*.sh' -not -path './.git/*' -not -path './.launchway/lib/*')
LIST
echo "$found shell script(s) parse"
