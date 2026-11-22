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
	check "helm rollback to the previous release" hm -n "$NS" rollback "$REL" 1 --wait --timeout 4m
	sleep 20
	local logs; logs=$(kc -n "$NS" logs "deploy/$FULL-server" --tail=200 2>&1)
	if printf '%s' "$logs" | grep -q 'the key must be 32 bytes, hex encoded'; then note "ok   the previous release says it cannot read sealed values: 'the key must be 32 bytes, hex encoded'"; else note "note the previous release did not print the documented message"; fi
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

main() {
	ensure_repo
	build_images || { echo "images failed" >&2; exit 2; }
	cluster_up || { echo "cluster failed" >&2; exit 2; }
	run_cell upgrade-helm cell_upgrade_helm
	run_cell rollback-helm-after cell_rollback_helm_after
	cell_cleanup upgrade-helm
	run_cell rollback-helm-before cell_rollback_helm_before
	cell_cleanup rollback-helm-before
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
