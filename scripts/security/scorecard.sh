#!/bin/sh
# The OpenSSF Scorecard of the PUBLIC repository (https://github.com/ossf/scorecard). Not a gate:
# the score depends on settings outside the code (branch protection, review) and on GitHub's
# view of the repository. Needs a GitHub token for the API (GITHUB_AUTH_TOKEN, or `gh auth login`).
# Only the public repository is sent to the tool.
set -eu
. "$(dirname "$0")/lib.sh"
REPO="${SCORECARD_REPO:-github.com/JK-Magic-Creations/launchway}"
token="${GITHUB_AUTH_TOKEN:-$(gh auth token 2>/dev/null || true)}"
[ -n "$token" ] || { echo "scorecard: set GITHUB_AUTH_TOKEN or sign in with gh" >&2; exit 2; }
GITHUB_AUTH_TOKEN="$token" go run "$SCORECARD" --repo="$REPO" --show-details "$@"
