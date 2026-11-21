#!/bin/sh
# The image tag: the commit's time (UTC, to the second) and the first eight
# characters of its id, such as 20261120061256-0d990ef1. It comes from the
# commit and not from the clock, so building the same commit twice, or in two
# steps of one run, gives the same tag.
set -e
: "${LAUNCHWAY_COMMIT:?this run has no commit}"
: "${LAUNCHWAY_COMMIT_TIME:?this run has no commit time}"
stamp=$(printf '%s' "$LAUNCHWAY_COMMIT_TIME" | tr -d ':TZ-')
tag="${stamp}-$(printf '%s' "$LAUNCHWAY_COMMIT" | cut -c1-8)"
echo "tag=$tag" >> "$LAUNCHWAY_OUTPUTS"
echo "tag $tag"
