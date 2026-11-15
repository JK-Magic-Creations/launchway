#!/bin/sh
# Pretends to build an image of the service, and records its digest.
set -e
echo "Building $LAUNCHWAY_PROJECT at $LAUNCHWAY_COMMIT for $PARAM_ENVIRONMENT"
digest=$(printf '%s' "$LAUNCHWAY_COMMIT" | sha256sum | cut -c1-16)
echo "digest=sha256:$digest" >> "$LAUNCHWAY_OUTPUTS"
echo "Built sha256:$digest"
