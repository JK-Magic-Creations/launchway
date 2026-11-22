# shellcheck shell=bash
# Shared pieces of the upgrade matrix (matrix.sh). TEST ONLY: every command here names the kind
# cluster launchway-upg-* it created; nothing touches any other cluster.

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HERE=$ROOT/deploy/test/upgrade
WORK=${UPGRADE_WORK:-$HOME/.cache/launchway-upg}
OLD_COMMIT=${OLD_COMMIT:-093664b}     # the previous release; later: the last N tags
OLD_TAG=0.1.0                         # what that release's image is called (its appVersion)
NEW_TAG=${NEW_TAG:-$(sed -n 's/^appVersion: "\(.*\)"/\1/p' "$ROOT/deploy/helm/launchway/Chart.yaml")}
CLUSTER=${UPGRADE_CLUSTER:-launchway-upg-1}
export KIND_EXPERIMENTAL_PROVIDER=podman
export KUBECONFIG=$WORK/kubeconfig
export TMPDIR=$WORK/tmp GOTMPDIR=$WORK/gotmp
RESULTS=$WORK/results.tsv
CHART_NEW=$ROOT/deploy/helm/launchway
CHART_OLD=$WORK/charts/old/deploy/helm/launchway
BIN=$WORK/bin/launchway
mkdir -p "$WORK"/{tmp,gotmp,bin,img,charts,cells}

log() { printf '\n==== %s\n' "$*" >&2; }
note() { printf '  %s\n' "$*" >&2; }

# kube guards every call: only our own kind cluster, never another context.
ctx_ok() { [ "$(kubectl config current-context 2>/dev/null)" = "kind-$CLUSTER" ]; }
kc() { ctx_ok || { echo "refusing: current context is not kind-$CLUSTER" >&2; return 99; }; kubectl "$@"; }
hm() { ctx_ok || { echo "refusing: current context is not kind-$CLUSTER" >&2; return 99; }; helm "$@"; }

# ---- assertions and results ----
FAILS=()
check() { # description, command...
	local d=$1; shift
	if "$@" >"$CELLDIR/last.out" 2>&1; then note "ok   $d"; else note "FAIL $d"; sed 's/^/       | /' "$CELLDIR/last.out" | tail -8 >&2; FAILS+=("$d"); fi
}
check_out() { # description, expected-regexp, command...   (the command's output must match)
	local d=$1 re=$2; shift 2
	local out; out=$("$@" 2>&1)
	if printf '%s' "$out" | grep -Eq -- "$re"; then note "ok   $d"; else note "FAIL $d (wanted /$re/)"; printf '%s\n' "$out" | tail -6 | sed 's/^/       | /' >&2; FAILS+=("$d"); fi
}
record() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$RESULTS"; }
run_cell() { # name, function
	local name=$1 fn=$2
	[ -z "${CELLS:-}" ] || case " $CELLS " in *" $name "*) ;; *) return 0;; esac
	log "CELL $name"
	CELL=$name; FAILS=(); CELLDIR=$WORK/cells/$name; rm -rf "$CELLDIR"; mkdir -p "$CELLDIR"
	local t0=$SECONDS
	"$fn" || FAILS+=("the cell stopped early")
	local dt=$((SECONDS - t0))
	if [ ${#FAILS[@]} -eq 0 ]; then record "$name" PASS "${dt}s"; else record "$name" FAIL "$(IFS=';'; echo "${FAILS[*]}")"; fi
	pf_stop
}
skip_cell() { record "$1" "not run" "$2"; }

# ---- images and cluster ----
build_images() {
	log "images"
	if ! podman image exists "localhost/launchway:$OLD_TAG" || [ "$(podman image inspect "localhost/launchway:$OLD_TAG" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" != "$(git -C "$ROOT" rev-parse "$OLD_COMMIT")" ]; then
		rm -rf "$WORK/old-src"; mkdir -p "$WORK/old-src"
		git -C "$ROOT" archive "$OLD_COMMIT" | tar -x -C "$WORK/old-src"
		podman build -q --build-arg VERSION=$OLD_TAG --build-arg REVISION="$(git -C "$ROOT" rev-parse "$OLD_COMMIT")" --build-arg CREATED=2026-11-01T00:00:00Z -t "launchway:$OLD_TAG" "$WORK/old-src" >/dev/null || return 1
	fi
	rev=$(git -C "$ROOT" rev-parse HEAD)
	podman build -q --build-arg VERSION="$NEW_TAG" --build-arg REVISION="$rev" --build-arg CREATED="$(TZ=UTC0 git -C "$ROOT" log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)" -t "launchway:$NEW_TAG" "$ROOT" >/dev/null || return 1
	rm -rf "$WORK/charts/old"; mkdir -p "$WORK/charts/old"
	git -C "$ROOT" archive "$OLD_COMMIT" deploy/helm/launchway | tar -x -C "$WORK/charts/old"
	( cd "$ROOT" && go build -o "$BIN" ./cmd/launchway ) || return 1
}
cluster_up() {
	if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
		log "creating kind cluster $CLUSTER"
		local node; node=$(podman images --format '{{.Repository}} {{.ID}}' | awk '$1=="docker.io/kindest/node"{print $2; exit}')
		kind create cluster --name "$CLUSTER" ${node:+--image "$(podman image inspect "$node" --format '{{index .RepoDigests 0}}')"} --wait 180s >&2 || return 1
	fi
	kubectl config use-context "kind-$CLUSTER" >/dev/null
	ctx_ok || return 1
	for img in "launchway:$OLD_TAG" "launchway:$NEW_TAG" docker.io/library/postgres:17.12-alpine docker.io/library/busybox:1.37; do
		local ref=$img; case $img in launchway:*) ref=localhost/$img;; esac
		local id; id=$(podman image inspect "$ref" --format '{{.Id}}' 2>/dev/null) || { podman pull -q "$ref" >/dev/null; id=$(podman image inspect "$ref" --format '{{.Id}}'); }
		local tar=$WORK/img/$(echo "$img" | tr ':/' '__')-${id:0:12}.tar
		if [ ! -f "$tar" ]; then rm -f "$WORK/img/$(echo "$img" | tr ':/' '__')"-*.tar; podman save -o "$tar" "$ref" >/dev/null; fi
		kind load image-archive "$tar" --name "$CLUSTER" >/dev/null 2>&1 || true
	done
}

# ---- a cell's namespace, values, helpers ----
cell_env() { # name  (sets NS REL STEPNS and makes the namespace with the test Git server)
	NS=u-$1; REL=lw; STEPNS=lw-$1-steps; FULL=$REL-launchway
	kc delete namespace "$NS" "$STEPNS" --ignore-not-found --wait=true --timeout=180s >/dev/null 2>&1
	kc create namespace "$NS" >/dev/null
	kc -n "$NS" create configmap demo-repo --from-file="$WORK/repo/demo-repo.tar.gz" >/dev/null
	kc -n "$NS" apply -f "$ROOT/deploy/test/git-server.yaml" >/dev/null
	kc -n "$NS" rollout status deploy/git-server --timeout=120s >/dev/null
}
cell_cleanup() { # name
	[ "${KEEP:-}" = 2 ] && return 0   # KEEP=1 keeps the cluster; KEEP=2 also the cells' namespaces
	hm -n "u-$1" uninstall lw >/dev/null 2>&1 || true
	kc delete namespace "u-$1" "lw-$1-steps" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
# The new chart's policies default to deny, which an installation with an in-cluster Git server must
# be told about (docs/guide/upgrading.md, "Network policies"). The matrix says so in a values file that
# only the NEW chart accepts, applied together with the new version.
mknetpol() { # file
	cat >"$1" <<'YAML'
networkPolicy:
  server:
    extraEgress:
      - to: [{ podSelector: { matchLabels: { app.kubernetes.io/name: git-server } } }]
        ports: [{ protocol: TCP, port: 8080 }]
  runner:
    extraEgress:
      - to: [{ podSelector: { matchLabels: { app.kubernetes.io/name: git-server } } }]
        ports: [{ protocol: TCP, port: 8080 }]
YAML
}
mkvalues() { # file, image tag, [seal quiet seconds]
	local f=$1 tag=$2 quiet=${3:-}
	sed -e "s/__TAG__/$tag/" -e "s/__STEPNS__/$STEPNS/" "$HERE/values-base.yaml" >"$f"
	if [ -n "$quiet" ]; then printf 'server:\n  extraEnv:\n    - { name: LAUNCHWAY_SEAL_QUIET_SECONDS, value: "%s" }\n' "$quiet" >>"$f"; fi
}
ensure_repo() { [ -f "$WORK/repo/demo-repo.tar.gz" ] || sh "$ROOT/deploy/test/make-test-repo.sh" "$WORK/repo" >/dev/null; }

wait_ready() { # seconds
	kc -n "$NS" rollout status "deploy/$FULL-server" --timeout="${1:-240}s" >/dev/null 2>&1
}
ready_ok() { # the servers that are not being replaced are Ready (a restart at first start is the previous release's habit)
	local bad
	bad=$(kc -n "$NS" get pods -l app.kubernetes.io/component=server -o json | jq -r '.items[] | select(.metadata.deletionTimestamp==null) | select(.status.phase!="Running" or ([.status.containerStatuses[]?|select(.ready!=true)]|length>0)) | .metadata.name')
	[ -z "$bad" ] || { echo "not ready: $bad"; return 1; }
}
pods_ok() { # every server pod (not one that is being replaced) Running, Ready, no restarts
	local bad
	bad=$(kc -n "$NS" get pods -l app.kubernetes.io/component=server -o json | jq -r '.items[] | select(.metadata.deletionTimestamp==null) | select(.status.phase!="Running" or ([.status.containerStatuses[]?|select(.ready!=true or .restartCount>0)]|length>0)) | .metadata.name')
	[ -z "$bad" ] || { echo "not healthy: $bad"; return 1; }
	[ "$(kc -n "$NS" get pods -l app.kubernetes.io/component=server --no-headers | wc -l)" -ge 1 ]
}

# ---- talking to the server ----
PFPID=
PFPORT=$((20000 + RANDOM % 10000))
pf_stop() { [ -z "$PFPID" ] || { kill "$PFPID" 2>/dev/null; wait "$PFPID" 2>/dev/null; }; PFPID=; }
pf_start() {
	pf_stop
	kc -n "$NS" port-forward "svc/$FULL" "$PFPORT:8080" >"$CELLDIR/pf.log" 2>&1 &
	PFPID=$!
	for _ in $(seq 1 60); do curl -fs "http://127.0.0.1:$PFPORT/readyz" >/dev/null 2>&1 && return 0; sleep 1; done
	echo "no answer through the port-forward" >&2; return 1
}
lw() { LAUNCHWAY_SERVER=http://127.0.0.1:$PFPORT LAUNCHWAY_TOKEN=x "$BIN" "$@"; }
lw_in() { # run the pod's own binary: lw_in doctor encryption ...
	kc -n "$NS" exec "deploy/$FULL-server" -- launchway "$@"
}

# Everything an upgrade must keep: a project (from the config), a variable, a hosted repository with a
# pull request, a webhook with a secret, the SSH host keys and a run.
seed_state() {
	pf_start || return 1
	SEEDSECRET=SEEDSECRET-$RANDOM$RANDOM$RANDOM
	printf '%s' "$SEEDSECRET" >"$CELLDIR/hs"; echo "$SEEDSECRET" >"$CELLDIR/seedsecret"
	check "seed: variable" lw variables set foo --project demo --value bar
	check "seed: hosted repository" lw repos create demo
	local wc=$CELLDIR/wc; rm -rf "$wc"; mkdir -p "$wc"
	( cd "$wc" && git init -q -b main && git config user.email a@b.c && git config user.name t && echo hi >README && git add . && git commit -qm one &&
	  git remote add o "http://x:x@127.0.0.1:$PFPORT/git/launchway/demo.git" && git push -q o main &&
	  git checkout -qb feat && echo two >>README && git commit -qam two && git push -q o feat ) >"$CELLDIR/push.log" 2>&1 || { cat "$CELLDIR/push.log" >&2; FAILS+=("seed: push"); }
	check "seed: pull request" lw pr create -R launchway/demo -head feat -title "Seed PR"
	check "seed: webhook with a secret" lw repos hooks add launchway/demo --url http://git-server:8080/hook --events push --secret-file "$CELLDIR/hs"
	lw keys host 2>&1 | grep -o 'SHA256:[^ ]*' | sort >"$CELLDIR/hostkeys"
	[ -s "$CELLDIR/hostkeys" ] || FAILS+=("seed: SSH host keys")
	local out; out=$(lw runs start demo -wait -timeout 240s 2>&1); echo "$out" >"$CELLDIR/run.out"
	printf '%s' "$out" | grep -qiE 'succeeded|success' && note "ok   seed: a run succeeded" || { note "FAIL seed: run"; printf '%s\n' "$out" | tail -5 >&2; FAILS+=("seed: a run"); }
}

verify_state() { # label
	local label=$1
	pf_start || { FAILS+=("$label: no answer from the server"); return 1; }
	check_out "$label: project intact" '^demo' lw projects list
	check_out "$label: variable intact" 'foo = bar' lw variables list --project demo
	check_out "$label: pull request intact" 'Seed PR' lw pr list -R launchway/demo
	check_out "$label: webhook intact" 'git-server' lw repos hooks list launchway/demo
	local hid; hid=$(lw repos hooks list launchway/demo 2>/dev/null | grep -o 'hok_[a-z0-9]*' | head -1)
	check_out "$label: webhook secret readable (a delivery is signed and sent)" '[0-9]{3}' lw repos hooks test launchway/demo "$hid"
	lw keys host 2>&1 | grep -o 'SHA256:[^ ]*' | sort >"$CELLDIR/hostkeys.now"
	check "$label: SSH host keys unchanged" diff "$CELLDIR/hostkeys" "$CELLDIR/hostkeys.now"
	check_out "$label: an earlier run is still there" 'succeeded' lw runs list
	check_out "$label: a new pipeline run succeeds" 'succeeded|success' lw runs start demo -wait -timeout 240s
}
verify_encryption() { # label  (waits for sealing, then reads a dump)
	local label=$1 out i
	for i in $(seq 1 60); do
		out=$(lw_in doctor encryption --config /etc/launchway/launchway.yaml 2>&1); echo "$out" >"$CELLDIR/enc.out"
		printf '%s' "$out" | grep -qi 'complete' && break; sleep 5
	done
	check_out "$label: sealing completes" 'complete' cat "$CELLDIR/enc.out"
	local pg; pg=$(kc -n "$NS" get pod -l app.kubernetes.io/component=postgresql -o name | head -1)
	kc -n "$NS" exec "$pg" -- pg_dump -U launchway launchway >"$CELLDIR/dump.sql" 2>"$CELLDIR/dump.err"
	check "$label: a dump was taken" test -s "$CELLDIR/dump.sql"
	check "$label: no webhook secret readable in the dump" bash -c "! grep -q '$(cat "$CELLDIR/seedsecret")' '$CELLDIR/dump.sql'"
	check "$label: exactly one key-encryption key (one creation event, one root key Secret)" bash -c "[ \"\$(grep -c 'encryption.key_created' '$CELLDIR/dump.sql')\" = 1 ] && [ \"\$(kubectl -n '$NS' get secrets -o name | grep -c root-key)\" = 1 ]"
}
verify_doctor() { # label: doctor clean (the open sign-in of these installs is expected)
	local out; out=$(lw_in doctor --config /etc/launchway/launchway.yaml 2>&1); echo "$out" >"$CELLDIR/doctor.out"
	if printf '%s' "$out" | grep -i 'warning' | grep -viE 'sign-in|no sign' | grep -q .; then note "FAIL $1: doctor warns"; grep -i warning "$CELLDIR/doctor.out" | sed 's/^/       | /' >&2; FAILS+=("$1: doctor is not clean"); else note "ok   $1: doctor is clean"; fi
}
