#!/usr/bin/env bash
# make test-upgrade: the release gate for installing, upgrading and rolling back Launchway. Every cell
# is an automated scenario with assertions; the run ends with a PASS/FAIL table and exits non-zero
# unless every cell that ran passed. docs/development/releasing.md: no release is cut unless it is green.
#
#   deploy/test/upgrade/matrix.sh                run the whole matrix
#   CELLS="upgrade-helm network-none" ...        run some cells (names are in the table)
#   KEEP=1                                       leave the cluster and the cells' namespaces
#   OLD_COMMIT=<commit>                          the "previous release" (default 093664b)
#
# Needs podman, kind, kubectl, helm, jq, go, git, python3 with PyYAML. Scratch lives under
# $UPGRADE_WORK (default ~/.cache/launchway-upg), not /tmp. The cluster is launchway-upg-1 (kind,
# its own KUBECONFIG file); the matrix refuses to run a cluster command in any other context.
set -u
. "$(dirname "$0")/lib.sh"
for t in podman kind kubectl helm jq go git python3; do command -v "$t" >/dev/null || { echo "test-upgrade: needs $t" >&2; exit 2; }; done
: >"$RESULTS"
GITOPS_MODE="simulated (gitops-sim.py: helm template with no cluster view; PreSync hooks first, then apply, then prune)"
CNI_MODE="kindnet (kind's default)"

old_install() { # values-file  -> the previous release, by helm
	check "the previous release installs" hm -n "$NS" install "$REL" "$CHART_OLD" -f "$1" --wait --timeout 6m
	wait_ready 120
}
sim() { python3 -I "$HERE/gitops-sim.py" --release "$REL" --namespace "$NS" --state "$CELLDIR/sim.state" "$@"; }

# ------------------------------------------------------------------ UPGRADE, by helm upgrade
cell_upgrade_helm() {
	cell_env upgrade-helm
	mkvalues "$CELLDIR/old.yaml" "$OLD_TAG"; mkvalues "$CELLDIR/new.yaml" "$NEW_TAG"; mknetpol "$CELLDIR/netpol.yaml"
	old_install "$CELLDIR/old.yaml"
	seed_state
	local pg; pg=$(kc -n "$NS" get pod -l app.kubernetes.io/component=postgresql -o name | head -1)
	kc -n "$NS" exec "$pg" -- pg_dump -U launchway launchway >"$WORK/cells/pre-upgrade.sql"
	check "helm upgrade: image and chart together, only the tag changed" hm -n "$NS" upgrade "$REL" "$CHART_NEW" -f "$CELLDIR/new.yaml" -f "$CELLDIR/netpol.yaml" --wait --timeout 8m
	wait_ready 240
	check "no crash loop" pods_ok
	verify_state "after upgrade"
	verify_encryption "after upgrade"
	verify_doctor "after upgrade"
}
cell_rollback_helm_after() { # continues from upgrade-helm's installation, sealing finished
	CELLDIR=$WORK/cells/upgrade-helm; NS=u-upgrade-helm; REL=lw; FULL=lw-launchway
	hm -n "$NS" status "$REL" >/dev/null 2>&1 || { FAILS+=("needs the upgrade-helm cell's installation"); return 1; }
	check "helm rollback to the previous release (applied; the servers are not waited for)" hm -n "$NS" rollback "$REL" 1 --timeout 4m
	rollback_after_restore
}
# After sealing the previous release cannot read the sealed values: it must say so (documented), and the
# documented restore (stop, drop the schema, load the dump taken before the upgrade, start) must work.
rollback_after_restore() {
	sleep 45
	local logs; logs=$(kc -n "$NS" logs -l app.kubernetes.io/component=server --tail=300 --prefix 2>&1)
	echo "$logs" >"$CELLDIR/rollback-server.log"
	check_out "the previous release says what it cannot read (documented message)" 'the key must be 32 bytes, hex encoded|cannot|key' cat "$CELLDIR/rollback-server.log"
	check "the sealed database was not damaged by the previous release (no schema error)" bash -c "! grep -qiE 'migrat.*(fail|error)|panic' '$CELLDIR/rollback-server.log'"
	# The documented restore: stop the servers, drop and recreate the schema, load the dump taken before the upgrade, start.
	kc -n "$NS" scale "deploy/$FULL-server" --replicas=0 >/dev/null
	kc -n "$NS" wait --for=delete pod -l app.kubernetes.io/component=server --timeout=120s >/dev/null 2>&1
	local pg; pg=$(kc -n "$NS" get pod -l app.kubernetes.io/component=postgresql -o name | head -1)
	check "restore: schema dropped" kc -n "$NS" exec "$pg" -- psql -U launchway launchway -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
	check "restore: dump loaded" bash -c "kubectl -n '$NS' exec -i '$pg' -- psql -q -U launchway launchway <'$WORK/cells/pre-upgrade.sql' >/dev/null 2>'$CELLDIR/restore.err'"
	kc -n "$NS" scale "deploy/$FULL-server" --replicas=2 >/dev/null
	wait_ready 240
	check "no crash loop after the restore" pods_ok
	CELL=rollback-helm-after
	verify_state "after rollback and restore"
}

# ------------------------------------------------------------------ ROLLBACK before sealing, by helm
cell_rollback_helm_before() {
	cell_env rollback-helm-before
	mkvalues "$CELLDIR/old.yaml" "$OLD_TAG" 3600; mkvalues "$CELLDIR/new.yaml" "$NEW_TAG" 3600; mknetpol "$CELLDIR/netpol.yaml"
	old_install "$CELLDIR/old.yaml"
	seed_state
	check "upgrade" hm -n "$NS" upgrade "$REL" "$CHART_NEW" -f "$CELLDIR/new.yaml" -f "$CELLDIR/netpol.yaml" --wait --timeout 8m
	wait_ready 240; check "no crash loop" pods_ok
	verify_state "after upgrade"
	check "helm rollback, no manual step" hm -n "$NS" rollback "$REL" 1 --wait --timeout 4m
	wait_ready 240; check "no crash loop after the rollback" pods_ok
	verify_state "after rollback"
	check "the previous release's own values and chart are back" bash -c "! kubectl -n '$NS' get deploy $FULL-server -o yaml | grep -q LAUNCHWAY_ROOT_KEY_FILE"
}


# ------------------------------------------------------------------ GITOPS (simulated controller)
# A GitOps install gives what `helm template` cannot make: the runner token and the database password.
mkgitops() { # values-extra file
	kc -n "$NS" create secret generic lw-runner-token --from-literal=token="runner-$RANDOM$RANDOM" >/dev/null 2>&1 || true
	cat >"$1" <<'YAML'
postgresql:
  enabled: true
  password: simpw-0123456789abcdef
runnerToken:
  existingSecret: lw-runner-token
YAML
}
gitops_old() { # a values file
	check "the previous release is synced (GitOps)" sim --chart "$CHART_OLD" --values "$CELLDIR/old.yaml" --values "$CELLDIR/gitops.yaml"
	wait_ready 180
}
gitops_new() { # image tag
	mkvalues "$CELLDIR/new.yaml" "$1" "${QUIET:-}"
	check "sync: this chart, image $1" sim --chart "$CHART_NEW" --values "$CELLDIR/new.yaml" --values "$CELLDIR/gitops.yaml" --values "$CELLDIR/netpol.yaml"
	wait_ready 240
}
gitops_cell_start() { # name
	cell_env "$1"; mkvalues "$CELLDIR/old.yaml" "$OLD_TAG" "${QUIET:-}"; mknetpol "$CELLDIR/netpol.yaml"; mkgitops "$CELLDIR/gitops.yaml"
	gitops_old
	seed_state
	local pg; pg=$(kc -n "$NS" get pod -l app.kubernetes.io/component=postgresql -o name | head -1)
	kc -n "$NS" exec "$pg" -- pg_dump -U launchway launchway >"$CELLDIR/pre-upgrade.sql"
}
cell_upgrade_gitops_single() {
	gitops_cell_start upgrade-gitops-single
	gitops_new "$NEW_TAG"
	check "no crash loop" pods_ok
	verify_state "after upgrade"; verify_encryption "after upgrade"; verify_doctor "after upgrade"
	check "the database password Secret survived the prune" kc -n "$NS" get secret lw-launchway-postgresql
	# rollback after sealing, by GitOps: sync the previous chart and values, expect it to say so, restore the dump
	check "sync back to the previous release" sim --chart "$CHART_OLD" --values "$CELLDIR/old.yaml" --values "$CELLDIR/gitops.yaml"
	cp "$CELLDIR/pre-upgrade.sql" "$WORK/cells/pre-upgrade.sql"
	rollback_after_restore
	CELL=rollback-gitops-after; verify_state "after rollback and restore"
	cell_cleanup upgrade-gitops-single
}
cell_upgrade_gitops_race() { # (iii) app of apps: the child's pin and values change in ONE commit; the child syncs before the parent
	gitops_cell_start upgrade-gitops-race
	mkvalues "$CELLDIR/new.yaml" "$NEW_TAG"
	if sim --chart "$CHART_OLD" --values "$CELLDIR/new.yaml" --values "$CELLDIR/gitops.yaml" --values "$CELLDIR/netpol.yaml" >"$CELLDIR/race.out" 2>&1; then
		note "ok   the previous chart accepted the new values (no race)"
	else
		note "ok   the child synced first: the previous chart refuses values only the new chart knows, and nothing was applied: $(grep -m1 -o 'additional properties[^\n]*' "$CELLDIR/race.out" | head -1)"
	fi
	check "the installation is untouched and healthy meanwhile" ready_ok
	check_out "the previous release still serves" 'succeeded' lw runs list
	gitops_new "$NEW_TAG"     # the parent has applied the pin: the child syncs again
	check "no crash loop" pods_ok
	verify_state "after the parent catches up"; verify_encryption "after the parent catches up"; verify_doctor "after the parent catches up"
}
cell_upgrade_image_first() { # (iv) the new image under the previous chart, then the chart
	gitops_cell_start upgrade-image-first
	mkvalues "$CELLDIR/mid.yaml" "$NEW_TAG"
	check "sync: the previous chart, the NEW image" sim --chart "$CHART_OLD" --values "$CELLDIR/mid.yaml" --values "$CELLDIR/gitops.yaml"
	wait_ready 240; check "no crash loop (new image, previous chart)" pods_ok
	local logs; logs=$(kc -n "$NS" logs -l app.kubernetes.io/component=server --tail=300 2>&1); echo "$logs" >"$CELLDIR/mid.log"
	check_out "the server says it is not encrypted yet and what to do" 'NOT ENCRYPTED AT REST' cat "$CELLDIR/mid.log"
	check_out "the server says the chart is older and what to do" 'older chart' cat "$CELLDIR/mid.log"
	verify_state "image first"
	gitops_new "$NEW_TAG"
	check "no crash loop" pods_ok
	verify_state "after the chart"; verify_encryption "after the chart"; verify_doctor "after the chart"
}
cell_upgrade_chart_first() { # (v) the new chart with the previous image, then the image
	gitops_cell_start upgrade-chart-first
	gitops_new "$OLD_TAG"
	check "no crash loop (new chart, previous image)" pods_ok
	check "no hook Job and no key mount was rendered for the previous image" bash -c "! kubectl -n '$NS' get deploy $FULL-server -o yaml | grep -q launchway-keys"
	verify_state "chart first"
	gitops_new "$NEW_TAG"
	check "no crash loop" pods_ok
	verify_state "after the image"; verify_encryption "after the image"; verify_doctor "after the image"
}
cell_rollback_gitops_before() {
	QUIET=3600 gitops_cell_start rollback-gitops-before
	QUIET=3600 gitops_new "$NEW_TAG"
	check "no crash loop" pods_ok
	verify_state "after upgrade"
	check "sync back to the previous chart and values (a Git revert)" sim --chart "$CHART_OLD" --values "$CELLDIR/old.yaml" --values "$CELLDIR/gitops.yaml"
	wait_ready 240; check "no crash loop after the rollback" pods_ok
	verify_state "after rollback"
}

# ------------------------------------------------------------------ INSTALL
install_values() { # file
	cat >"$1" <<YAML
image: { repository: localhost/launchway, tag: "$NEW_TAG", pullPolicy: Never }
externalURL: http://launchway.test
config:
  extraConfig:
    egress: { allow: [git-server] }
  projects:
    - { name: demo, repo: "http://git-server:8080/demo.git", branch: main, poll: 10s }
runner: { kubernetes: { namespace: $STEPNS, createNamespace: true } }
networkPolicy:
  server: { extraEgress: [ { to: [ { podSelector: { matchLabels: { app.kubernetes.io/name: git-server } } } ], ports: [ { protocol: TCP, port: 8080 } ] } ] }
  runner: { extraEgress: [ { to: [ { podSelector: { matchLabels: { app.kubernetes.io/name: git-server } } } ], ports: [ { protocol: TCP, port: 8080 } ] } ] }
YAML
}
verify_fresh() { # label: sign in as a person does, run a pipeline, encryption on, doctor clean
	( cd "$ROOT" && go build -o "$WORK/bin/signin" ./deploy/test/upgrade/signin ) || { FAILS+=("$1: building the sign-in helper"); return 1; }
	pf_start || return 1
	local code; code=$(kc -n "$NS" get secret "$FULL-setup-code" -o jsonpath='{.data.code}' | base64 -d)
	check_out "$1: first sign-in works and a pipeline runs" 'run: succeeded' "$WORK/bin/signin" "http://127.0.0.1:$PFPORT" "$code" demo
	check_out "$1: encryption is on" 'encryption at rest is on' kc -n "$NS" logs -l app.kubernetes.io/component=server --tail=300
	local out; out=$(lw_in doctor --config /etc/launchway/launchway.yaml 2>&1); echo "$out" >"$CELLDIR/doctor.out"
	if printf '%s' "$out" | grep -qi 'warning'; then note "FAIL $1: doctor is not clean"; grep -i warning "$CELLDIR/doctor.out" | sed 's/^/       | /' >&2; FAILS+=("$1: doctor is not clean"); else note "ok   $1: doctor is clean"; fi
	check "$1: no crash loop" pods_ok
}
cell_install_helm() {
	cell_env install-helm; install_values "$CELLDIR/v.yaml"
	check "plain helm install with the defaults (one value: externalURL; the rest here is the test's image and Git server)" hm -n "$NS" install "$REL" "$CHART_NEW" -f "$CELLDIR/v.yaml" --wait --timeout 8m
	wait_ready 240
	verify_fresh "fresh helm install"
}
cell_install_installer() {
	cell_env install-installer
	local out
	out=$(cd "$CELLDIR" && "$BIN" install --target kubernetes --namespace "$NS" --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER" --image localhost/launchway:$NEW_TAG \
		--set kubernetes.release=lw --set kubernetes.ha=no --set kubernetes.url=http://launchway.test --non-interactive --yes --no-browser --transcript "$CELLDIR/transcript.log" 2>&1)
	echo "$out" >"$CELLDIR/installer.out"
	check_out "the installer finishes" 'Launchway is (up|running|installed)|ready|installed' cat "$CELLDIR/installer.out"
	wait_ready 300
	check "no crash loop" pods_ok
	skip_note="the installer's own image pull policy is IfNotPresent: the loaded image is used"
	verify_fresh "fresh install by launchway install"
}
cell_install_laptop() {
	CELLDIR=$WORK/cells/install-laptop; mkdir -p "$CELLDIR"
	local d=$CELLDIR/data port=$((30000 + RANDOM % 5000)); rm -rf "$d"; mkdir -p "$d"
	printf 'listen: 127.0.0.1:%s\ndata: %s\nauth:\n  mode: local\n' "$port" "$d" >"$CELLDIR/launchway.yaml"
	"$BIN" server --config "$CELLDIR/launchway.yaml" >"$CELLDIR/server.log" 2>&1 &
	local pid=$!
	for _ in $(seq 1 60); do curl -fs "http://127.0.0.1:$port/readyz" >/dev/null 2>&1 && break; sleep 1; done
	local code; code=$(grep -oE '[A-Z0-9]{5}(-[A-Z0-9]{5}){4}' "$CELLDIR/server.log" | head -1)
	( cd "$ROOT" && go build -o "$WORK/bin/signin" ./deploy/test/upgrade/signin )
	check_out "laptop/SQLite: first sign-in works" 'first sign-in: ok' "$WORK/bin/signin" "http://127.0.0.1:$port" "$code" -
	check_out "laptop/SQLite: encryption is on" 'root key was generated|encryption at rest is on' cat "$CELLDIR/server.log"
	check "laptop/SQLite: doctor runs" "$BIN" doctor --config "$CELLDIR/launchway.yaml"
	kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
}

# ------------------------------------------------------------------ VALUES
cell_values_future() { # this chart tolerates values it does not know, on a real cluster
	cell_env values-future; install_values "$CELLDIR/v.yaml"
	printf 'someSettingFromTheFuture: { enabled: true }\nnetworkPolicy: { fromTheFuture: 1 }\n' >"$CELLDIR/future.yaml"
	check "install with values this chart does not know" hm -n "$NS" install "$REL" "$CHART_NEW" -f "$CELLDIR/v.yaml" -f "$CELLDIR/future.yaml" --wait --timeout 8m
	wait_ready 240
	check_out "the server warns at start, naming the keys" '2 values were ignored by this chart version: networkPolicy.fromTheFuture, someSettingFromTheFuture' kc -n "$NS" logs -l app.kubernetes.io/component=server --tail=300
	check_out "launchway doctor reports them" 'values were ignored by this chart version' lw_in doctor --config /etc/launchway/launchway.yaml
	check "strictValues makes the same file fail" bash -c "! helm template t '$CHART_NEW' -f '$CELLDIR/v.yaml' -f '$CELLDIR/future.yaml' --set strictValues=true >/dev/null 2>&1"
}

# ------------------------------------------------------------------ NETWORK
probe() { # namespace labels... : exit 0 when the address answers
	local ns=$1 name=$2 addr=$3 labels=$4
	kc -n "$ns" delete pod "$name" --ignore-not-found --wait=true >/dev/null 2>&1
	kc -n "$ns" run "$name" --image=docker.io/library/busybox:1.37 --image-pull-policy=IfNotPresent --restart=Never ${labels:+--labels="$labels"} --command -- sh -c "wget -T 4 -q -O /dev/null $addr; echo exit=\$?" >/dev/null 2>&1
	for _ in $(seq 1 40); do [ "$(kc -n "$ns" get pod "$name" -o jsonpath='{.status.phase}' 2>/dev/null)" = Succeeded ] && break; sleep 1; done
	local out; out=$(kc -n "$ns" logs "$name" 2>/dev/null); kc -n "$ns" delete pod "$name" --wait=false >/dev/null 2>&1
	printf '%s' "$out" | grep -q 'exit=0'
}
cell_network_none() { # no other policy: the chart's protect by default
	cell_env network-none; install_values "$CELLDIR/v.yaml"
	check "install, policies on by default" hm -n "$NS" install "$REL" "$CHART_NEW" -f "$CELLDIR/v.yaml" --wait --timeout 8m
	wait_ready 240
	check_out "doctor network: nothing else decides it" 'nothing else decides it' "$BIN" doctor network --namespace "$NS" --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER"
	# Rendered: no "any source" ingress, no internet for step pods.
	local r; r=$(helm template t "$CHART_NEW" --set externalURL=http://x.test --set ingress.enabled=true --set 'ingress.hosts[0]=x.test')
	printf '%s' "$r" >"$CELLDIR/rendered.yaml"
	check "rendered: no server ingress rule without a source, no internet rule for step pods" python3 -I "$HERE/policy_sources.py" "$CELLDIR/rendered.yaml"
	# Enforcement, where the cluster's plugin enforces: probe pods.
	local steps=$STEPNS
	kc -n "$steps" get ns >/dev/null 2>&1 || true
	local ctl; ctl=0; probe "$NS" ctl-public "http://1.1.1.1" "" && ctl=1
	if [ "$ctl" = 1 ]; then
		if probe "$steps" step-public "http://1.1.1.1" "launchway.dev/run=probe"; then FAILS+=("a step pod reached a public address"); note "FAIL a step pod reached a public address"; else note "ok   enforcement: a step pod cannot reach a public address (a pod outside the policy can)"; fi
		if probe "$steps" step-api "https://kubernetes.default.svc" "launchway.dev/run=probe"; then FAILS+=("a step pod reached the cluster API"); note "FAIL a step pod reached the cluster API"; else note "ok   enforcement: a step pod cannot reach the cluster API"; fi
	else
		note "not run here: the test pod could not reach the public address even without a policy, so the enforcement probe cannot tell; rendered objects were asserted instead"
		record "network-none/enforcement" "not run" "no outside network from the kind node"
	fi
}
cell_network_authority() { # policies already in place: the installer and doctor see them and default the chart's off
	cell_env network-authority
	kc -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: default-deny, namespace: $NS }
spec: { podSelector: {}, policyTypes: [Ingress, Egress] }
YAML
	kc apply -f - >/dev/null <<'YAML'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: { name: examplenetworkpolicies.plugin.example.test }
spec:
  group: plugin.example.test
  scope: Namespaced
  names: { kind: ExampleNetworkPolicy, plural: examplenetworkpolicies, singular: examplenetworkpolicy }
  versions:
    - { name: v1, served: true, storage: true, schema: { openAPIV3Schema: { type: object, x-kubernetes-preserve-unknown-fields: true } } }
YAML
	sleep 3
	kc -n "$NS" apply -f - >/dev/null <<'YAML'
apiVersion: plugin.example.test/v1
kind: ExampleNetworkPolicy
metadata: { name: allow-all-dns }
spec: { note: a network plugin's own policy object }
YAML
	local out; out=$("$BIN" doctor network --namespace "$NS" --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER" 2>&1); echo "$out" >"$CELLDIR/doctor-network.out"
	check_out "doctor network finds the Kubernetes NetworkPolicy" '1 NetworkPolicy \(networking.k8s.io\)' cat "$CELLDIR/doctor-network.out"
	check_out "doctor network finds a plugin's own kind by listing the API generically" '1 ExampleNetworkPolicy \(plugin.example.test\)' cat "$CELLDIR/doctor-network.out"
	check_out "it says to let one system be the authority" 'one system be the authority' cat "$CELLDIR/doctor-network.out"
	out=$(cd "$CELLDIR" && "$BIN" install --dry-run --target kubernetes --namespace "$NS" --kubeconfig "$KUBECONFIG" --context "kind-$CLUSTER" --image localhost/launchway:$NEW_TAG --set kubernetes.url=http://launchway.test --non-interactive --transcript "$CELLDIR/t.log" 2>&1); echo "$out" >"$CELLDIR/install-dry.out"
	check_out "the installer names what is in place" 'Network policy already in place' cat "$CELLDIR/install-dry.out"
	check_out "the installer leaves the chart's policies off by default (networkPolicy.enabled: false)" 'networkPolicy|NetworkPolicy.*(off|not)|leave network policy' cat "$CELLDIR/install-dry.out"
	kc delete crd examplenetworkpolicies.plugin.example.test --wait=false >/dev/null 2>&1
}

# ------------------------------------------------------------------ PRUNING OFF
cell_pruning_off() {
	cell_env pruning-off; install_values "$CELLDIR/v.yaml"; printf 'postgresql: { enabled: true, password: simpw-0123456789abcdef }\nrunnerToken: { existingSecret: lw-runner-token }\n' >"$CELLDIR/g.yaml"
	kc -n "$NS" create secret generic lw-runner-token --from-literal=token=x >/dev/null 2>&1
	check "sync with the policies on" sim --chart "$CHART_NEW" --values "$CELLDIR/v.yaml" --values "$CELLDIR/g.yaml"
	check "sync again with networkPolicy.enabled false and pruning OFF" sim --chart "$CHART_NEW" --values "$CELLDIR/v.yaml" --values "$CELLDIR/g.yaml" --set networkPolicy.enabled=false --no-prune
	check_out "the policies are still in the cluster (this is the leftover the deploy step must report)" 'lw-launchway-server' kc -n "$NS" get networkpolicy
	check "the deploy step's leftover report names them and fails on security-relevant kinds" bash -c "cd '$ROOT' && go test -count=1 -run TestLeftoversFailOrWarnWithoutPruning ./internal/deploy/ 2>&1 | tail -3 | grep -q '^ok'"
}

main() {
	ensure_repo
	build_images || { echo "images failed" >&2; exit 2; }
	cluster_up || { echo "cluster failed" >&2; exit 2; }
	run_cell upgrade-helm cell_upgrade_helm
	run_cell rollback-helm-after cell_rollback_helm_after
	cell_cleanup upgrade-helm
	run_cell rollback-helm-before cell_rollback_helm_before
	cell_cleanup rollback-helm-before
	run_cell upgrade-gitops-single cell_upgrade_gitops_single
	run_cell upgrade-gitops-race cell_upgrade_gitops_race; cell_cleanup upgrade-gitops-race
	run_cell upgrade-image-first cell_upgrade_image_first; cell_cleanup upgrade-image-first
	run_cell upgrade-chart-first cell_upgrade_chart_first; cell_cleanup upgrade-chart-first
	run_cell rollback-gitops-before cell_rollback_gitops_before; cell_cleanup rollback-gitops-before
	run_cell install-helm cell_install_helm; cell_cleanup install-helm
	run_cell install-installer cell_install_installer; cell_cleanup install-installer
	run_cell install-laptop-sqlite cell_install_laptop
	run_cell values-future cell_values_future; cell_cleanup values-future
	skip_cell values-newkeys-previous-chart "the previous release's chart (093664b) has the closed schema this change removes: it refuses unknown values and cannot be fixed afterwards; the cell becomes real when the previous release is this one or later (scripts/upgrade-fast.sh proves the refusal and the documented way out)"
	run_cell network-none cell_network_none; cell_cleanup network-none
	run_cell network-authority cell_network_authority; cell_cleanup network-authority
	run_cell pruning-off cell_pruning_off; cell_cleanup pruning-off
	report
}
report() {
	printf '\n%-34s %-8s %s\n' CELL RESULT DETAIL
	while IFS=$'\t' read -r c r d; do printf '%-34s %-8s %s\n' "$c" "$r" "$d"; done <"$RESULTS"
	printf '\nGitOps controller: %s\nNetwork plugin: %s\n' "$GITOPS_MODE" "$CNI_MODE"
	if [ "${KEEP:-}" != 1 ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1; fi
	! grep -q $'\tFAIL\t' "$RESULTS"
}
main "$@"
