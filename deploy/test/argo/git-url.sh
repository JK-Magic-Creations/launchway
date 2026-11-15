#!/usr/bin/env bash
# Starts a port-forward to the test Git server and prints, as shell
# assignments, the repository URL on this machine and the port-forward's PID:
#
#   eval "$(deploy/test/argo/git-url.sh)"
#   git -c http.extraHeader="$LAUNCHWAY_TEST_ARGO_GIT_AUTH_HEADER" clone "$LAUNCHWAY_TEST_ARGO_GIT_URL"
#   kill "$LAUNCHWAY_TEST_ARGO_GIT_PF_PID"     # when done
#
# Options: [--context kind-NAME]  (or KUBE_CONTEXT)
source "$(dirname "$0")/lib.sh"
parse_context "$@"
require_kind_context
start_git_port_forward
disown "$GIT_PF_PID" 2>/dev/null || true
printf 'LAUNCHWAY_TEST_ARGO_GIT_URL=%q\n' "$GIT_HOST_URL"
printf 'LAUNCHWAY_TEST_ARGO_GIT_AUTH_HEADER=%q\n' "$(basic_auth_header)"
printf 'LAUNCHWAY_TEST_ARGO_GIT_PF_PID=%q\n' "$GIT_PF_PID"
printf 'export LAUNCHWAY_TEST_ARGO_GIT_URL LAUNCHWAY_TEST_ARGO_GIT_AUTH_HEADER LAUNCHWAY_TEST_ARGO_GIT_PF_PID\n'
