#!/bin/sh
# Builds a static site if a build command is given, then checks that the
# output has an index.html and no links to files that are not there.
set -e
. "$(dirname "$0")/../common.sh"
enter
[ -z "${PARAM_BUILD_COMMAND:-}" ] || run "$PARAM_BUILD_COMMAND"
dir=${PARAM_SITE_DIR:-.}
[ -f "$dir/index.html" ] || { echo "$dir/index.html is missing: set site_dir: to the folder the site is built into" >&2; exit 1; }
missing=0
for page in $(find "$dir" -name '*.html' -not -path '*/node_modules/*' | head -500); do
  base=$(dirname "$page")
  for link in $(sed -n 's/.*\(href\|src\)="\([^"#?:]*\)".*/\2/p' "$page" | sort -u); do
    case "$link" in ""|http*|//*|mailto*|data*) continue ;; esac
    case "$link" in /*) target="$dir$link" ;; *) target="$base/$link" ;; esac
    [ -e "$target" ] || [ -e "$target/index.html" ] || { echo "$page links to $link, which is not in the site" >&2; missing=1; }
  done
done
[ "$missing" -eq 0 ] || exit 1
say "site ok: $(find "$dir" -type f | wc -l) files"
