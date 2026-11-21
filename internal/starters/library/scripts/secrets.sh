#!/bin/sh
# A quick look for secrets committed by mistake: private keys and the token
# shapes of common providers, in the files Git tracks. It is a safety net,
# not a replacement for a full scanner (coming: findings in the Security tab).
set -e
. "$(dirname "$0")/common.sh"
enter
files=$(git ls-files 2>/dev/null || find . -type f -not -path './.git/*')
pattern='-----BEGIN ([A-Z]+ )?PRIVATE KEY-----|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36}|glpat-[A-Za-z0-9_-]{20}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-[A-Za-z0-9_-]{20,}'
hits=$(printf '%s\n' "$files" | while read -r f; do
  [ -f "$f" ] || continue
  case "$f" in *.png|*.jpg|*.gif|*.ico|*.pdf|*.zip|*.gz|*.woff*|*/testdata/*|*_test.go) continue ;; esac
  grep -IEHn -e "$pattern" "$f" 2>/dev/null | cut -c1-120 || true
done)
if [ -n "$hits" ]; then
  echo "These lines look like secrets (the values are not shown in full):" >&2
  printf '%s\n' "$hits" | sed -E 's/(.{30}).*/\1.../' >&2
  exit 1
fi
say "no secrets found in $(printf '%s\n' "$files" | wc -l) files"
