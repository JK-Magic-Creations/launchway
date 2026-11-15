#!/usr/bin/env bash
# Removes what control/up.sh added (and nothing else).
source "$(dirname "$0")/../lib.sh"
parse_context "$@"
require_kind_context
CTL_DIR=$(cd "$(dirname "$0")" && pwd)
K delete --ignore-not-found validatingadmissionpolicybinding lw-ctl-deployer-rollouts >/dev/null
K delete --ignore-not-found validatingadmissionpolicy lw-ctl-deployer-rollouts >/dev/null
K delete --ignore-not-found -f "$CTL_DIR/cluster.yaml" >/dev/null
log "control test objects removed"
