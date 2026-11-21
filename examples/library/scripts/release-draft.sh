#!/bin/sh
# Drafts the release of this run's commit, with notes from the changes since the last release, and
# attaches what this run built. The version is RELEASE_VERSION, else the tag the run is for.
set -eu
export LAUNCHWAY_SERVER=$RELEASE_SERVER LAUNCHWAY_TOKEN=$RELEASE_TOKEN
version=${RELEASE_VERSION:-}
if [ -z "$version" ]; then
  version=${LAUNCHWAY_TAG:-}
  version=${version#v}
fi
[ -n "$version" ] || { echo "no version: give one, or run this for a tag such as v1.4.0" >&2; exit 2; }
echo "Drafting $LAUNCHWAY_PROJECT $version at $LAUNCHWAY_COMMIT"
launchway release create --project "$LAUNCHWAY_PROJECT" --version "$version" --from "$LAUNCHWAY_COMMIT" \
  --generate-notes --run "$LAUNCHWAY_RUN_ID"
launchway release assets add "$LAUNCHWAY_PROJECT" "$version" --run "$LAUNCHWAY_RUN_ID" --images
for pair in ${RELEASE_FILES:-}; do
  artifact=${pair%%:*}
  path=${pair#*:}
  launchway release assets add "$LAUNCHWAY_PROJECT" "$version" --run "$LAUNCHWAY_RUN_ID" --artifact "$artifact" --path "$path"
done
echo "version=$version" >> "$LAUNCHWAY_OUTPUTS"
