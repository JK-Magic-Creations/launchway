#!/bin/sh
# A quick look for secrets committed by mistake: private keys and the token
# shapes of common providers, in the files Git tracks. A safety net, not a
# replacement for a full scanner.
. "$(dirname "$0")/launchway-step.sh"
dir=$(lw_input working_directory)
dir=${dir:-.}
case "$dir" in
  /*|..|../*|*/..|*/../*) lw_fail "working_directory must be a directory inside the repository, not $dir" ;;
esac
[ -d "$LAUNCHWAY_WORKSPACE/$dir" ] || lw_fail "working_directory $dir does not exist in this repository"
cd "$LAUNCHWAY_WORKSPACE/$dir" || lw_fail "cannot enter $dir"
files=$(git ls-files 2>/dev/null || find . -type f -not -path './.git/*')
pattern='-----BEGIN ([A-Z]+ )?PRIVATE KEY-----|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36}|glpat-[A-Za-z0-9_-]{20}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-[A-Za-z0-9_-]{20,}'
hits=$(printf '%s\n' "$files" | while read -r f; do
  [ -f "$f" ] || continue
  case "$f" in *.png|*.jpg|*.gif|*.ico|*.pdf|*.zip|*.gz|*.woff*|*/testdata/*|*_test.go) continue ;; esac
  grep -IEHn -e "$pattern" "$f" 2>/dev/null | cut -c1-120 || true
done)
n=$(printf '%s\n' "$files" | wc -l | tr -d ' ')
if [ -n "$hits" ]; then
  echo "These lines look like secrets (the values are not shown in full):" >&2
  printf '%s\n' "$hits" | sed -E 's/(.{30}).*/\1.../' >&2
  # Each hit is a finding: where it is, never what it says.
  printf '%s\n' "$hits" | while IFS=: read -r file line _rest; do
    lw_finding "committed-secret" high "${file#./}" "${line:-0}" "this line looks like a committed secret (a private key or a provider token)"
  done
  count=$(printf '%s\n' "$hits" | wc -l | tr -d ' ')
  lw_output_number files "$n"
  lw_output_number hits "$count"
  lw_summary "**$count line(s) look like secrets** in $n files. The values are not shown."
  lw_finish 1
fi
echo "launchway: no secrets found in $n files"
lw_output_number files "$n"
lw_output_number hits 0
lw_summary "No secrets found in $n files."
lw_finish
