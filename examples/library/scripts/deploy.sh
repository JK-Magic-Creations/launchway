#!/bin/sh
# Pretends to deploy the build, and records where it can be reached.
set -e
echo "Deploying $LAUNCHWAY_PROJECT to $TARGET"
echo "url=https://$LAUNCHWAY_PROJECT.$TARGET" >> "$LAUNCHWAY_OUTPUTS"
echo "Deployed to https://$LAUNCHWAY_PROJECT.$TARGET"
