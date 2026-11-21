#!/bin/sh
# Secret scan of the whole Git history (every branch, so every commit that was ever made), with gitleaks.
# What is staged is checked by the optional pre-commit hook (scripts/security/pre-commit).
# quality/gitleaks-baseline.json holds the findings known to be test fixtures and documentation
# examples. A finding beyond it fails; an entry that no longer matches anything also fails, so the
# baseline can only shrink. History rewrites change commit ids: regenerate with
#   scripts/security/gitleaks.sh --update   (refuses to add an entry; only BASELINE_INIT=1 does, once)
# GITLEAKS_SCOPE=head scans only the history of the checked-out commit (what a push publishes, and what
# the hosted checkout holds): findings on other local branches are not judged, and a baseline entry
# whose commit is not an ancestor of HEAD is left alone. `make prepush` uses it.
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
gitdir="$(git rev-parse --path-format=absolute --git-common-dir)"
out="$(mktemp -d)"; chmod 777 "$out"; trap 'rm -rf "$out"' EXIT
run() { # run <report> [extra gitleaks args]
  report="$1"; shift
  $CONTAINER run --rm --security-opt label=disable -v "$gitdir:/gitdir:ro" -v "$out:/out" \
    -v "$ROOT/quality/gitleaks.toml:/gitleaks.toml:ro" "$GITLEAKS_IMAGE" "$@" \
    --no-banner --redact --config /gitleaks.toml --report-format json --report-path "/out/$report" --exit-code 0 >/dev/null 2>"$out/err" \
    || { cat "$out/err" >&2; exit 2; }
}
scope=--all
[ "${GITLEAKS_SCOPE:-}" != head ] || scope="$(git rev-parse HEAD)"
run all.json git /gitdir --log-opts="$scope"
cp "$out/all.json" "$out/found.json"
if [ "${1:-}" = "--update" ]; then
  jq -r '.[].Fingerprint' quality/gitleaks-baseline.json | sort -u > "$out/old"
  jq -r '.[].Fingerprint' "$out/found.json" | sort -u > "$out/new"
  added="$(comm -13 "$out/old" "$out/new" || true)"
  [ -z "$added" ] || [ -n "${BASELINE_INIT:-}" ] || { echo "gitleaks: new findings; fix or remove them, the baseline does not grow:"; echo "$added"; exit 1; }
  jq '[.[] | {Fingerprint, RuleID, File, StartLine, Commit}]' "$out/found.json" > quality/gitleaks-baseline.json
  echo "gitleaks: baseline now has $(jq length quality/gitleaks-baseline.json) entries"; exit 0
fi
jq -r '.[].Fingerprint' quality/gitleaks-baseline.json | sort -u > "$out/base"
jq -r '.[].Fingerprint' "$out/found.json" | sort -u > "$out/now"
new="$(comm -13 "$out/base" "$out/now" || true)"
stale="$(comm -23 "$out/base" "$out/now" || true)"
# A clone that holds only some branches (the hosted checkout has main alone) cannot judge an entry
# whose commit it does not have; such an entry is judged where that commit exists.
if [ -n "$stale" ]; then
  keep=""
  for fp in $stale; do
    c="${fp%%:*}"
    if [ "${GITLEAKS_SCOPE:-}" = head ]; then
      git merge-base --is-ancestor "$c" HEAD 2>/dev/null || continue
    fi
    if git cat-file -e "$c^{commit}" 2>/dev/null; then keep="$keep$fp
"; fi
  done
  stale="$(printf '%s' "$keep" | sed '/^$/d')"
fi
n="$(jq length "$out/found.json")"
echo "gitleaks: $n findings, $(wc -l < "$out/base") in the baseline"
if [ -n "$new" ]; then
  echo "gitleaks: findings beyond the baseline (rule, file, line; values are redacted):" >&2
  jq -r --arg f "$new" '.[] | select(.Fingerprint as $x | ($f | split("\n") | index($x))) | "  \(.RuleID) \(.File):\(.StartLine) \(.Commit[0:8])"' "$out/found.json" >&2
  exit 1
fi
if [ -n "$stale" ]; then
  echo "gitleaks: the baseline lists findings that are gone; run scripts/security/gitleaks.sh --update so it shrinks:" >&2
  echo "$stale" | sed 's/^/  /' >&2
  exit 1
fi
echo "gitleaks: no secrets beyond the baseline"
