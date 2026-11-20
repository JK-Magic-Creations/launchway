#!/usr/bin/env bash
# Installs the Argo CD test environment into a kind cluster and waits until it
# is ready. Safe to run again: everything is applied idempotently.
#
#   deploy/test/argo/up.sh [--context kind-NAME]
#
# Environment:
#   KUBE_CONTEXT                    context (default kind-launchway-test; must start with kind-)
#   LAUNCHWAY_TEST_ARGO_RESEED=1    recreate the test repository even if it exists
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
parse_context "$@"
require_kind_context

ARGOCD_YAML=$ARGO_CACHE/argocd-$ARGOCD_VERSION-install.yaml
ROLLOUTS_YAML=$ARGO_CACHE/argo-rollouts-$ROLLOUTS_VERSION-install.yaml
log "context $CTX; manifests cached in $ARGO_CACHE"
fetch_pinned "$ARGOCD_MANIFEST_URL" "$ARGOCD_MANIFEST_SHA256" "$ARGOCD_YAML"
fetch_pinned "$ROLLOUTS_MANIFEST_URL" "$ROLLOUTS_MANIFEST_SHA256" "$ROLLOUTS_YAML"

label_ns() { K label namespace "$1" app.kubernetes.io/part-of=launchway-test-argo --overwrite >/dev/null; }

# Argo CD. Server-side apply: the CRDs are too large for the last-applied
# annotation of a client-side apply.
log "installing Argo CD $ARGOCD_VERSION"
K create namespace argocd --dry-run=client -o yaml | K apply -f - >/dev/null
label_ns argocd
K apply -n argocd --server-side --force-conflicts -f "$ARGOCD_YAML" >/dev/null

# Poll Git every 10 s instead of every 3 min (plus up to 60 s jitter), so a
# pushed commit is noticed quickly without a webhook. The controller and the
# repo server read these keys only at start, so their pod templates carry the
# values as an annotation: a change restarts them, a re-run does not.
RECONCILE=10s
JITTER=0s
K -n argocd patch configmap argocd-cm --type merge \
	-p "{\"data\":{\"timeout.reconciliation\":\"$RECONCILE\",\"timeout.reconciliation.jitter\":\"$JITTER\"}}" >/dev/null
STAMP="{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"launchway-test/reconciliation\":\"$RECONCILE-$JITTER\"}}}}}"
K -n argocd patch statefulset argocd-application-controller --type merge -p "$STAMP" >/dev/null
K -n argocd patch deployment argocd-repo-server --type merge -p "$STAMP" >/dev/null

# Argo Rollouts. The upstream manifest has no Namespace object.
log "installing Argo Rollouts $ROLLOUTS_VERSION"
K create namespace argo-rollouts --dry-run=client -o yaml | K apply -f - >/dev/null
label_ns argo-rollouts
K apply -n argo-rollouts --server-side --force-conflicts -f "$ROLLOUTS_YAML" >/dev/null

log "installing the Git server"
K apply -f "$ARGO_DIR/git-server.yaml" >/dev/null

log "creating workload namespaces, the repository credentials and the AppProject"
K apply -f "$ARGO_DIR/argocd/namespaces.yaml" >/dev/null
K apply -f "$ARGO_DIR/argocd/repo-secret.yaml" >/dev/null
K apply -f "$ARGO_DIR/argocd/project.yaml" >/dev/null

# The deploy runner's identity in tests (impersonated as
# system:serviceaccount:argocd:lw-deployer): least-privilege RBAC, and the
# admission policy that limits its Application patches to .operation and the
# refresh annotation.
log "applying the deployer RBAC and admission policy"
K apply -f "$ARGO_DIR/deployer-rbac.yaml" >/dev/null
K apply -f "$ARGO_DIR/admission-policy.yaml" >/dev/null

log "waiting for the controllers and the Git server"
K -n argocd rollout status statefulset/argocd-application-controller --timeout=300s >/dev/null
for d in $(K -n argocd get deployments -o name); do
	K -n argocd rollout status "$d" --timeout=300s >/dev/null
done
K -n argo-rollouts rollout status deployment/argo-rollouts --timeout=300s >/dev/null
K -n "$GIT_NAMESPACE" rollout status "deployment/$GIT_SERVICE" --timeout=300s >/dev/null

start_git_port_forward
trap 'kill "$GIT_PF_PID" 2>/dev/null || true' EXIT
if [ "${LAUNCHWAY_TEST_ARGO_RESEED:-0}" = 1 ] || ! git -c http.extraHeader="$(basic_auth_header)" ls-remote --exit-code "$GIT_HOST_URL" main >/dev/null 2>&1; then
	"$ARGO_DIR/seed.sh" --context "$CTX" "$GIT_HOST_URL"
else
	log "repository exists; applying the Applications and waiting for them (LAUNCHWAY_TEST_ARGO_RESEED=1 recreates it)"
	K apply -f "$ARGO_DIR/argocd/applications.yaml" >/dev/null
	for app in demo-manual demo-multi; do
		if [ "$(K -n argocd get application "$app" -o jsonpath='{.status.sync.status}')" != Synced ]; then
			request_sync "$app"
		fi
	done
	wait_apps demo demo-manual plain demo-multi demo-wref
fi
log "ready. In-cluster Git URL: $GIT_INCLUSTER_URL"
