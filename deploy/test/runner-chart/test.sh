#!/bin/sh
# make test-runner-chart: the launchway chart (servers only, with the development PostgreSQL) in
# one namespace and the launchway-runner chart in another, on a kind cluster; a pipeline run
# end to end through the remote runner, its step pod in a third namespace.
#
#   deploy/test/runner-chart/test.sh --context kind-NAME
#
# Checks: the runner reaches the server through the chart's NetworkPolicy; a run's step runs in
# a pod of the step namespace (Pod Security "restricted"); the step pod's NetworkPolicy blocks
# its egress; the runner's ServiceAccount may manage pods in the step namespace and nothing else;
# then the runners switch to a join token (runner.join.existingSecret), enrol, and run again.
#
# Images: built from a linux/amd64 release binary with packaging/release/Containerfile and
# loaded into the cluster (no registry). RELEASE_DIR names a release directory to take the
# binary from (default: one is built in dist/runner-chart-test). Namespaces: lw-rc-server,
# lw-rc-runners, lw-rc-steps; deleted afterwards unless KEEP=1. Refuses any context not kind-*.
set -eu

ctx=
while [ $# -gt 0 ]; do
	case $1 in
	--context) ctx=$2; shift 2 ;;
	*) echo "usage: $0 --context kind-NAME" >&2; exit 2 ;;
	esac
done
case $ctx in
kind-*) ;;
*) echo "test-runner-chart: refusing context '$ctx': only kind-* clusters" >&2; exit 2 ;;
esac
cluster=${ctx#kind-}
root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"
here=deploy/test/runner-chart
k() { kubectl --context "$ctx" "$@"; }
h() { helm --kube-context "$ctx" "$@"; }
log() { printf '\n==== %s\n' "$*"; }
fail() {
	printf 'test-runner-chart: FAIL: %s\n' "$*" >&2
	exit 1
}
pass() { printf '  ok: %s\n' "$*"; }

SRV_NS=lw-rc-server
RUN_NS=lw-rc-runners
STEP_NS=lw-rc-steps
version=v0.0.0-chart-test
ALPINE=docker.io/library/alpine:3.23.8@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2
tag=chart-test

pf=
cleanup() {
	[ -z "$pf" ] || kill "$pf" 2>/dev/null || :
	[ "${KEEP:-}" = 1 ] && return
	h uninstall lw-rc-runners -n $RUN_NS --wait >/dev/null 2>&1 || :
	h uninstall lw-rc -n $SRV_NS --wait >/dev/null 2>&1 || :
	k delete namespace $RUN_NS $SRV_NS $STEP_NS --ignore-not-found --wait=false >/dev/null 2>&1 || :
}
trap cleanup EXIT

# ---- images ----
rel=${RELEASE_DIR:-}
if [ -z "$rel" ]; then
	rel=dist/runner-chart-test
	log "building a linux/amd64 release in $rel"
	RELEASE_PLATFORMS=linux/amd64 ALLOW_DIRTY=1 SIGN=none packaging/release/release.sh $version $rel >"$rel.log" 2>&1 ||
		{ tail -20 "$rel.log"; fail "release build"; }
fi
bin=$(find "$rel" -maxdepth 1 -name 'launchway-*-linux-amd64' | head -n 1)
[ -n "$bin" ] || fail "no linux/amd64 binary in $rel"
ver=$(basename "$bin" | sed 's/^launchway-\(.*\)-linux-amd64$/\1/')
log "images from $bin"
for target in server runner; do
	name=launchway
	[ $target = server ] || name=launchway-runner
	podman build -q --platform linux/amd64 -f packaging/release/Containerfile --target $target \
		--build-arg VERSION="$ver" --build-arg REVISION="$(git rev-parse HEAD)" \
		--build-arg CREATED="$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)" -t "localhost/$name:$tag" "$rel" >/dev/null
done
podman build -q -t "localhost/lw-rc-git:$tag" --build-arg BASE="$ALPINE" -f $here/git.Containerfile $here >/dev/null
archive=$(mktemp -d)
for img in launchway launchway-runner lw-rc-git; do
	podman save -q --format oci-archive -o "$archive/$img.tar" "localhost/$img:$tag"
	KIND_EXPERIMENTAL_PROVIDER=${KIND_EXPERIMENTAL_PROVIDER:-podman} kind load image-archive --name "$cluster" "$archive/$img.tar" >/dev/null
done
rm -rf "$archive"
pass "server, runner and git images loaded into $cluster"

# ---- the server ----
log "installing the launchway chart (server) in $SRV_NS"
h uninstall lw-rc-runners -n $RUN_NS --wait >/dev/null 2>&1 || :
h uninstall lw-rc -n $SRV_NS --wait >/dev/null 2>&1 || :
k delete namespace $RUN_NS $SRV_NS $STEP_NS --ignore-not-found --wait >/dev/null
k create namespace $SRV_NS >/dev/null
sed "s#image: lw-rc-git#image: localhost/lw-rc-git:$tag#" $here/git-server.yaml | k -n $SRV_NS apply -f - >/dev/null
k -n $SRV_NS rollout status deployment/lw-rc-git --timeout=120s >/dev/null || fail "the git server did not start"
h install lw-rc deploy/helm/launchway -n $SRV_NS --wait --timeout 5m \
	--set postgresql.enabled=true --set runner.enabled=false --set server.replicas=1 \
	--set image.repository=localhost/launchway --set image.tag=$tag --set image.pullPolicy=Never \
	-f $here/server-values.yaml >/dev/null || { k -n $SRV_NS get pods; fail "helm install launchway"; }
pass "server running"

# ---- the runners ----
log "installing the launchway-runner chart in $RUN_NS"
k create namespace $RUN_NS >/dev/null
token=$(k -n $SRV_NS get secret lw-rc-launchway-runner-token -o jsonpath='{.data.token}' | base64 -d)
k -n $RUN_NS create secret generic launchway-runner-token --from-literal=token="$token" >/dev/null
h install lw-rc-runners deploy/helm/launchway-runner -n $RUN_NS --wait --timeout 3m \
	--set image.repository=localhost/launchway-runner --set image.tag=$tag --set image.pullPolicy=Never \
	-f $here/runner-values.yaml >/dev/null || {
	k -n $RUN_NS get pods
	k -n $RUN_NS logs -l app.kubernetes.io/instance=lw-rc-runners --tail=20 || :
	fail "helm install launchway-runner"
}
pass "runner ready: it reached the server through its NetworkPolicy"
[ "$(k get namespace $STEP_NS -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}')" = restricted ] ||
	fail "the step namespace is not labelled restricted"
pass "step namespace $STEP_NS enforces Pod Security restricted"

sa=system:serviceaccount:$RUN_NS:lw-rc-runners-launchway-runner
for check in "yes create pods -n $STEP_NS" "yes create pods/exec -n $STEP_NS" "yes get pods/log -n $STEP_NS" \
	"no get secrets -n $STEP_NS" "no create pods -n $RUN_NS" "no create pods -n $SRV_NS" "no list namespaces" \
	"no create deployments -n $STEP_NS"; do
	want=${check%% *}
	# shellcheck disable=SC2086 # the check's words
	got=$(k auth can-i ${check#* } --as "$sa" 2>/dev/null || :)
	[ "$got" = "$want" ] || fail "can-i ${check#* }: $got, want $want"
done
pass "the runner's ServiceAccount: pods, pods/exec, pods/log in $STEP_NS only"

# enforces_policies: whether the cluster enforces NetworkPolicies at all: a pod under a deny-all
# egress policy in a namespace of its own tries to reach the Kubernetes API.
enforces_policies() {
	ns=lw-rc-netpol-probe
	k delete namespace $ns --ignore-not-found --wait >/dev/null 2>&1
	k create namespace $ns >/dev/null
	printf 'apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: deny}\nspec: {podSelector: {}, policyTypes: [Egress], egress: []}\n' |
		k -n $ns apply -f - >/dev/null
	api_ip=$(k -n default get endpointslices -l kubernetes.io/service-name=kubernetes \
		-o jsonpath='{.items[0].endpoints[0].addresses[0]}')
	api_port=$(k -n default get endpointslices -l kubernetes.io/service-name=kubernetes \
		-o jsonpath='{.items[0].ports[0].port}')
	sleep 2
	k -n $ns run probe --restart=Never --image=docker.io/library/busybox:1.37 -- \
		sh -c "nc -w 3 -z $api_ip $api_port && echo reached || echo blocked" >/dev/null
	k -n $ns wait --for=jsonpath='{.status.phase}'=Succeeded pod/probe --timeout=120s >/dev/null 2>&1 || :
	result=$(k -n $ns logs probe 2>/dev/null || :)
	k delete namespace $ns --wait=false >/dev/null 2>&1 || :
	[ "$result" = blocked ]
}

# run_pipeline: starts a run of demo, waits for it; sets $id, $status and $logs.
run_pipeline() {
	id=$(api -X POST -d '{}' "http://127.0.0.1:$port/api/v1/projects/demo/runs" | jq -r .id)
	i=0
	while :; do
		status=$(api "http://127.0.0.1:$port/api/v1/runs/$id" | jq -r .status)
		case $status in succeeded | failed | canceled) break ;; esac
		i=$((i + 1))
		[ $i -lt 300 ] || fail "run $id still $status after 5 minutes"
		sleep 1
	done
	logs=$(api "http://127.0.0.1:$port/api/v1/runs/$id/steps/hello/logs" | jq -r '.lines[].text')
}

# ---- a run ----
log "running a pipeline through the remote runner"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 18573)
k -n $SRV_NS port-forward service/lw-rc-launchway "$port:8080" >/dev/null 2>&1 &
pf=$!
api() { curl -fsS -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' "$@"; }
i=0
until api "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 30 ] || fail "port-forward"
	sleep 1
done
run_pipeline
printf '%s\n' "$logs" | sed 's/^/    | /'
[ "$status" = succeeded ] || fail "run $id $status"
printf '%s\n' "$logs" | grep -q '^Runner lw-rc-runners-launchway-runner-.*, executor kubernetes' || fail "not run by the chart's runner"
pass "run $id succeeded: step pod in $STEP_NS, run by the remote runner"
if printf '%s\n' "$logs" | grep -q 'egress blocked'; then
	pass "the step pod's egress was blocked by the step namespace's NetworkPolicy"
elif enforces_policies; then
	fail "the step pod reached the server although the step namespace's NetworkPolicy forbids it"
else
	echo "  NOTE: this cluster's network plugin does not enforce NetworkPolicies (a deny-all probe got out):"
	echo "        the policies were installed, but whether they block traffic is not tested here"
fi
runners=$(api "http://127.0.0.1:$port/api/v1/runners" | jq -r '.[] | select(.online) | "\(.name) \(.executor) \(.version)"')
printf '%s\n' "$runners" | grep -q "^lw-rc-runners-launchway-runner-.* kubernetes.* v$ver$" || fail "the server lists: $runners"
pass "the server lists the runner: $runners"

# ---- join tokens ----
log "switching the runners to a join token (runner.join.existingSecret)"
jt=$(api -X POST -d '{"pool":"default","ttl":"1h","max_uses":5,"ephemeral":true}' \
	"http://127.0.0.1:$port/api/v1/runner-join-tokens" | jq -r .token)
case $jt in lwj_*) ;; *) fail "no join token from the server ($jt)" ;; esac
k -n $RUN_NS create secret generic launchway-runner-join --from-literal=token="$jt" >/dev/null
h upgrade lw-rc-runners deploy/helm/launchway-runner -n $RUN_NS --wait --timeout 3m \
	--set image.repository=localhost/launchway-runner --set image.tag=$tag --set image.pullPolicy=Never \
	-f $here/runner-values.yaml --set token.existingSecret= --set runner.join.existingSecret=launchway-runner-join >/dev/null || {
	k -n $RUN_NS get pods
	k -n $RUN_NS logs -l app.kubernetes.io/instance=lw-rc-runners --tail=20 || :
	fail "helm upgrade with runner.join.existingSecret"
}
k -n $RUN_NS rollout status deployment -l app.kubernetes.io/instance=lw-rc-runners --timeout=180s >/dev/null 2>&1 || :
i=0
until api "http://127.0.0.1:$port/api/v1/runners" | jq -e '[.[] | select(.online and .auth == "identity")] | length > 0' >/dev/null; do
	i=$((i + 1))
	[ $i -lt 90 ] || fail "no enrolled runner: $(api "http://127.0.0.1:$port/api/v1/runners" | jq -c '[.[] | {name, online, auth}]')"
	sleep 2
done
pass "the runner pod enrolled with the join token"
# Only the enrolled runner may take the next run: wait for the shared-token one to leave.
i=0
until [ "$(api "http://127.0.0.1:$port/api/v1/runners" | jq '[.[] | select(.online and .auth != "identity")] | length')" = 0 ]; do
	i=$((i + 1))
	[ $i -lt 90 ] || fail "the shared-token runner is still online"
	sleep 2
done
run_pipeline
[ "$status" = succeeded ] || { printf '%s\n' "$logs"; fail "run $id $status with the enrolled runner"; }
pass "run $id succeeded on the enrolled runner"

log "test-runner-chart passed"
