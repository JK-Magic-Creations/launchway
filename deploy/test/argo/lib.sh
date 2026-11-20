# shellcheck shell=bash
# shellcheck disable=SC2034  # the variables here are read by the scripts that source this file
# Shared by the scripts in deploy/test/argo. Source it; do not run it.
#
# The cluster context comes from KUBE_CONTEXT (default kind-launchway-test) or
# from a leading "--context NAME" argument, which the scripts strip with
# parse_context "$@" before reading their own arguments. Any context whose name
# does not start with "kind-" is refused: these scripts install and delete
# cluster-wide objects and must never touch a real cluster.

set -euo pipefail

ARGO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Pinned versions. The manifests are downloaded from these URLs and checked
# against these SHA-256 sums; a mismatch stops the install.
ARGOCD_VERSION=v3.3.7
ARGOCD_MANIFEST_URL=https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml
ARGOCD_MANIFEST_SHA256=bf1844394bf6213a60d316c2e3b0229495eb1d97689347f427bb2db69c1b510e
ROLLOUTS_VERSION=v1.8.3
ROLLOUTS_MANIFEST_URL=https://github.com/argoproj/argo-rollouts/releases/download/${ROLLOUTS_VERSION}/install.yaml
ROLLOUTS_MANIFEST_SHA256=6f4f581dab41450ca623a03d246156fa7fa79901f7f01693c5df66f7557fc145

# Test-only Git server values. They are also in git-server.yaml.
GIT_NAMESPACE=${LAUNCHWAY_TEST_ARGO_GIT_NAMESPACE:-lw-git}
GIT_SERVICE=${LAUNCHWAY_TEST_ARGO_GIT_SERVICE:-git}
GIT_PORT=8080
GIT_USER=${LAUNCHWAY_TEST_ARGO_GIT_USER:-lwtest}
GIT_PASSWORD=${LAUNCHWAY_TEST_ARGO_GIT_PASSWORD:-lwtest-not-a-secret}
GIT_REPO_PATH=${GIT_USER}/gitops.git
GIT_INCLUSTER_URL=http://${GIT_SERVICE}.${GIT_NAMESPACE}.svc:${GIT_PORT}/${GIT_REPO_PATH}

# Where downloads are cached between runs.
ARGO_CACHE=${LAUNCHWAY_TEST_ARGO_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/launchway-test-argo}

CTX=${KUBE_CONTEXT:-kind-launchway-test}

# parse_context strips "--context NAME" from the front of the arguments and
# leaves the rest in REST_ARGS.
parse_context() {
	REST_ARGS=()
	while [ $# -gt 0 ]; do
		case "$1" in
		--context) CTX=$2; shift 2 ;;
		--context=*) CTX=${1#--context=}; shift ;;
		*) REST_ARGS+=("$1"); shift ;;
		esac
	done
}

require_kind_context() {
	case "$CTX" in
	kind-*) ;;
	*)
		echo "refusing to use context '$CTX': these scripts only run against kind clusters (context names starting with 'kind-')" >&2
		exit 2
		;;
	esac
	if ! kubectl config get-contexts -o name | grep -qx -- "$CTX"; then
		echo "context '$CTX' is not in your kubeconfig" >&2
		exit 2
	fi
}

K() { kubectl --context "$CTX" "$@"; }

# fetch_pinned URL SHA256 FILE downloads URL to FILE once and checks its sum.
fetch_pinned() {
	local url=$1 sum=$2 out=$3
	mkdir -p "$(dirname "$out")"
	if [ ! -f "$out" ] || ! echo "$sum  $out" | sha256sum -c --status; then
		curl -fsSL --retry 3 -o "$out.tmp" "$url"
		if ! echo "$sum  $out.tmp" | sha256sum -c --status; then
			echo "checksum mismatch for $url (got $(sha256sum "$out.tmp" | cut -d' ' -f1), want $sum)" >&2
			rm -f "$out.tmp"
			exit 1
		fi
		mv "$out.tmp" "$out"
	fi
}

basic_auth_header() {
	printf 'Authorization: Basic %s' "$(printf '%s:%s' "$GIT_USER" "$GIT_PASSWORD" | base64 -w0)"
}

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }

# start_git_port_forward forwards a free local port to the Git Service and sets
# GIT_PF_PID and GIT_HOST_URL. The caller stops it with: kill "$GIT_PF_PID".
start_git_port_forward() {
	local out
	out=$(mktemp)
	# kubectl itself, not the K function: $! must be kubectl's PID, or killing
	# it would leave the port-forward running.
	kubectl --context "$CTX" -n "$GIT_NAMESPACE" port-forward --address 127.0.0.1 "svc/$GIT_SERVICE" ":$GIT_PORT" >"$out" 2>&1 &
	GIT_PF_PID=$!
	local port=""
	for _ in $(seq 1 50); do
		port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) .*/\1/p' "$out")
		[ -n "$port" ] && break
		if ! kill -0 "$GIT_PF_PID" 2>/dev/null; then break; fi
		sleep 0.2
	done
	if [ -z "$port" ]; then
		cat "$out" >&2
		rm -f "$out"
		kill "$GIT_PF_PID" 2>/dev/null || true
		echo "port-forward to $GIT_NAMESPACE/$GIT_SERVICE did not start" >&2
		return 1
	fi
	rm -f "$out"
	GIT_HOST_URL=http://127.0.0.1:${port}/${GIT_REPO_PATH}
}

# wait_apps NAME... waits until every named Application is Synced and Healthy
# with no sync operation running ($1 may be "--sha SHA" to require that
# commit).
wait_apps() {
	local sha="" deadline=$((SECONDS + ${WAIT_TIMEOUT:-300}))
	if [ "${1:-}" = --sha ]; then sha=$2; shift 2; fi
	local app state
	for app in "$@"; do
		while :; do
			state=$(K -n argocd get application "$app" -o json 2>/dev/null | jq -r --arg sha "$sha" '
				[ .status.sync.status, .status.health.status,
				  ((.status.sync.revision // "") as $r | (.status.sync.revisions // []) as $rs |
				   if $sha == "" then "any" elif $r == $sha or ($rs | length > 0 and all(. == $sha)) then "at-sha" else "old" end),
				  (.status.operationState.phase // "none") ] | join(" ")') || state="missing"
			case "$state" in
			"Synced Healthy any Running" | "Synced Healthy at-sha Running") ;;
			"Synced Healthy any "* | "Synced Healthy at-sha "*) log "$app: $state"; break ;;
			esac
			if [ $SECONDS -ge $deadline ]; then
				echo "timed out waiting for Application $app (last state: $state)" >&2
				return 1
			fi
			sleep 2
		done
	done
}

# request_sync APP [REVISION] sets .operation on an Application, the way
# "argocd app sync" does (the controller picks it up and runs the sync).
request_sync() {
	local app=$1 rev=${2:-}
	local body
	body=$(jq -cn --arg rev "$rev" '{operation: {initiatedBy: {username: "launchway-test"}, sync: ({} + (if $rev == "" then {} else {revision: $rev} end))}}')
	K -n argocd patch application "$app" --type merge -p "$body" >/dev/null
}
