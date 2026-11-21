#!/bin/sh
# Makes the drafted release a candidate, or publishes it (which creates the annotated tag).
set -eu
export LAUNCHWAY_SERVER=$RELEASE_SERVER LAUNCHWAY_TOKEN=$RELEASE_TOKEN
version=$LAUNCHWAY_NEEDS_DRAFT_VERSION
if [ "${RELEASE_PUBLISH:-false}" = "true" ]; then
  launchway release publish "$LAUNCHWAY_PROJECT" "$version"
else
  launchway release candidate "$LAUNCHWAY_PROJECT" "$version"
fi
