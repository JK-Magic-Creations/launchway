#!/bin/sh
# Runs `launchway validate` on every complete pipeline example in a reference page: each
# ```yaml block preceded by a line <!-- validate --> (blank lines between are allowed). A block
# marked <!-- try --> (the Learn pages' "Try it in the editor" examples) is checked the same way.
# Template examples (<!-- validate-template -->) and fragments are not checked here; the Go
# test internal/pipeline/docs_reference_test.go reads templates as well.
#
# Usage: scripts/docs/check-pipeline-examples.sh [page.md] [launchway binary]
# Defaults: docs/reference/pipeline.md and bin/launchway (build it with `make build`).
set -eu

page=${1:-docs/reference/pipeline.md}
bin=${2:-${LAUNCHWAY:-bin/launchway}}

if [ ! -x "$bin" ]; then
	echo "check-pipeline-examples: $bin is not an executable; run make build first" >&2
	exit 2
fi
if [ ! -f "$page" ]; then
	echo "check-pipeline-examples: $page does not exist" >&2
	exit 2
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

# Write each marked block to $dir/<n>-line<L>.yaml, where L is the block's first line.
awk -v dir="$dir" '
	/^<!-- (validate|try) -->[ \t]*$/ { want = 1; next }
	want && /^[ \t]*$/ { next }
	want && /^```yaml[ \t]*$/ { n++; file = sprintf("%s/%03d-line%d.yaml", dir, n, NR + 1); inblock = 1; want = 0; next }
	want { printf "%s:%d: <!-- validate --> is not followed by a ```yaml block\n", FILENAME, NR > "/dev/stderr"; bad = 1; want = 0 }
	inblock && /^```[ \t]*$/ { inblock = 0; close(file); next }
	inblock { print > file }
	END { exit bad }
' "$page"

count=0
failed=0
for f in "$dir"/*.yaml; do
	[ -e "$f" ] || break
	count=$((count + 1))
	line=${f##*-line}
	line=${line%.yaml}
	if ! out=$("$bin" validate "$f" 2>&1); then
		failed=$((failed + 1))
		echo "$page: the example starting on line $line is not valid:" >&2
		echo "$out" | sed "s|$f|  example|" >&2
	fi
done

if [ "$count" -eq 0 ]; then
	echo "check-pipeline-examples: $page has no examples marked <!-- validate -->" >&2
	exit 1
fi
if [ "$failed" -gt 0 ]; then
	echo "check-pipeline-examples: $failed of $count examples in $page are not valid" >&2
	exit 1
fi
echo "check-pipeline-examples: all $count examples in $page are valid"
