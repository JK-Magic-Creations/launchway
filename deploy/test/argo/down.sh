#!/usr/bin/env bash
# Removes everything up.sh and the experiments created: the namespaces and the
# cluster-scoped objects listed in README.md. kind contexts only.
#
#   deploy/test/argo/down.sh [--context kind-NAME]
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
parse_context "$@"
require_kind_context

NAMESPACES=(lw-demo lw-demo-manual lw-demo-multi lw-demo-wref lw-git argocd argo-rollouts)
CRDS=(
	applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io
	analysisruns.argoproj.io analysistemplates.argoproj.io clusteranalysistemplates.argoproj.io
	experiments.argoproj.io rollouts.argoproj.io
)
CLUSTERROLES=(
	argocd-application-controller argocd-applicationset-controller argocd-server
	argo-rollouts argo-rollouts-aggregate-to-admin argo-rollouts-aggregate-to-edit argo-rollouts-aggregate-to-view
)
CLUSTERROLEBINDINGS=(
	argocd-application-controller argocd-applicationset-controller argocd-server
	argo-rollouts
)
VAPS=(launchway-deployer-applications)
VAPBINDINGS=(launchway-deployer-applications)

log "context $CTX"
# Applications first, with their finalizers removed, so nothing waits on a
# controller that is about to go away and no workload is pruned.
if K get crd applications.argoproj.io >/dev/null 2>&1; then
	for app in $(K -n argocd get applications -o name 2>/dev/null); do
		K -n argocd patch "$app" --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null || true
	done
	K -n argocd delete applications --all --wait=false >/dev/null 2>&1 || true
fi

K delete validatingadmissionpolicybinding "${VAPBINDINGS[@]}" --ignore-not-found
K delete validatingadmissionpolicy "${VAPS[@]}" --ignore-not-found
K delete namespace "${NAMESPACES[@]}" --ignore-not-found --wait=true --timeout=180s
K delete crd "${CRDS[@]}" --ignore-not-found
K delete clusterrolebinding "${CLUSTERROLEBINDINGS[@]}" --ignore-not-found
K delete clusterrole "${CLUSTERROLES[@]}" --ignore-not-found
log "removed. The Git server's volume (local-path, reclaim policy Delete) goes with its claim."
