#!/bin/sh
# Infrastructure-as-code scans: the Helm charts are rendered with each example's values, then
#   kubeconform  checks every object against the Kubernetes schemas (strict: no unknown field),
#   kube-linter  checks workload practice (probes, limits, security context, RBAC),
#   trivy config checks misconfigurations (fails on HIGH and CRITICAL, with the exceptions in
#                quality/trivy-ignore.yaml, each with a reason and an expiry date),
#   hadolint     lints the Dockerfile.
# The Pod Security "restricted" check is a Go test (internal/config/chart_security_test.go).
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
command -v helm >/dev/null 2>&1 || { echo "iac: needs helm" >&2; exit 2; }
out="$(mktemp -d)"; chmod 755 "$out"; trap 'rm -rf "$out"' EXIT
mkdir "$out/rendered"
scripts/charts-sync.sh --check
helm template t deploy/helm/launchway --set externalURL=https://launchway.example.com > "$out/rendered/server-default.yaml"
helm template t deploy/helm/launchway --set externalURL=https://launchway.example.com --set ingress.enabled=true \
  --set httpRoute.enabled=true --set httpRoute.parentRefs[0].name=gateway > "$out/rendered/server-published.yaml"
helm template t deploy/helm/launchway -f deploy/helm/launchway/examples/ha.yaml > "$out/rendered/server-ha.yaml"
helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/remote.yaml \
  -f deploy/helm/launchway-runner/examples/networkpolicy.yaml > "$out/rendered/runner-remote.yaml"
helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/deploy-runner.yaml > "$out/rendered/runner-deploy.yaml"

echo "== kubeconform"
go run "$KUBECONFORM" -strict -summary -kubernetes-version "${KUBE_VERSION:-1.34.0}" -ignore-missing-schemas "$out/rendered" deploy/buildkit/buildkitd.yaml
echo "== kube-linter"
go run "$KUBE_LINTER" lint "$out/rendered"
echo "== trivy config (HIGH and CRITICAL fail)"
cache="${TRIVY_CACHE_DIR:-$HOME/.cache/launchway-trivy}"; mkdir -p "$cache"
cp deploy/buildkit/buildkitd.yaml "$out/rendered/buildkitd.yaml"
cp Dockerfile "$out/rendered/Dockerfile"
$CONTAINER run --rm --security-opt label=disable -v "$out/rendered:/work:ro" -v "$ROOT/quality/trivy-ignore.yaml:/ignore.yaml:ro" -v "$cache:/root/.cache" \
  "$TRIVY_IMAGE" config /work --severity HIGH,CRITICAL --exit-code 1 --ignorefile /ignore.yaml --quiet
echo "== hadolint"
$CONTAINER run --rm --security-opt label=disable -i -v "$ROOT/.hadolint.yaml:/.config/hadolint.yaml:ro" "$HADOLINT_IMAGE" hadolint --failure-threshold warning - < Dockerfile
echo "iac: all scans passed"
