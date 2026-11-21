#!/bin/sh
# Fails when a tracked file is over 5 MB, or a known build output is tracked. Build outputs belong in .gitignore: a
# 111 MB binary once reached a commit and the code host refused the push.
set -eu
cd "$(dirname "$0")/.."
bad=0
big=$(git ls-files -z | xargs -0 -r stat -c '%s %n' 2>/dev/null | awk '$1 > 5000000 {print}' || true)
if [ -n "$big" ]; then
  echo "tracked files over 5 MB (keep binaries and large data out of Git):" >&2
  echo "$big" >&2
  bad=1
fi
for p in cmd/launchway/launchway launchway slipway; do
  if git ls-files --error-unmatch "$p" >/dev/null 2>&1; then echo "build output is tracked: $p" >&2; bad=1; fi
done
if git ls-files | grep -E '^(bin|dist|web/dist|web/node_modules|\.tools)/' | grep -v '^web/dist/\.keep$' | head -1 | grep -q .; then
  echo "files under a build output directory are tracked" >&2; bad=1
fi
[ "$bad" = 0 ] && echo "tracked files: none over 5 MB, no build outputs"
exit "$bad"
