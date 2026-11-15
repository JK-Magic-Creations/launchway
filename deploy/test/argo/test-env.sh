#!/usr/bin/env bash
# Checks the context (kind-* only) and prints how the Go integration tests find
# the test environment. Used by "make test-argo" before up.sh.
#
#   deploy/test/argo/test-env.sh [--context kind-NAME]
source "$(dirname "$0")/lib.sh"
parse_context "$@"
require_kind_context
cat <<EOF
Argo CD test environment, context $CTX
  Git repository in the cluster: $GIT_INCLUSTER_URL
  From this machine: port-forward svc/$GIT_SERVICE in $GIT_NAMESPACE (deploy/test/argo/git-url.sh does it)
  User $GIT_USER, password $GIT_PASSWORD (test-only values)

The Go tests read (defaults shown):
  LAUNCHWAY_TEST_ARGO_CONTEXT=$CTX
  LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE=$GIT_NAMESPACE
  LAUNCHWAY_TEST_ARGO_GIT_SERVICE=$GIT_SERVICE
  LAUNCHWAY_TEST_ARGO_GIT_USER=$GIT_USER
  LAUNCHWAY_TEST_ARGO_GIT_PASSWORD=$GIT_PASSWORD
  repository path: $GIT_REPO_PATH, port $GIT_PORT
EOF
