#!/bin/sh
# The Helm chart's manifests must be the same every time they are rendered, and must hold no Secret
# material: a GitOps controller renders the chart on every sync, and a value that changes per render
# (a generated key or password) would replace the real one. The secrets are made once, in the
# cluster, by the chart's pre-install/pre-upgrade hook Job. See docs/reference/helm-values.md.
#
#   scripts/chart-determinism.sh        (needs helm; part of make chart-lint)
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
command -v helm >/dev/null 2>&1 || { echo "chart-determinism: needs helm" >&2; exit 2; }
tmp=$(mktemp -d "${TMPDIR:-/tmp}/chart-determinism.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
chart=deploy/helm/launchway
status=0
check() { # name, helm arguments...
	name=$1; shift
	helm template t "$chart" "$@" >"$tmp/a.yaml"
	helm template t "$chart" "$@" >"$tmp/b.yaml"
	if ! cmp -s "$tmp/a.yaml" "$tmp/b.yaml"; then
		echo "chart-determinism: FAIL $name: two renders differ" >&2
		diff "$tmp/a.yaml" "$tmp/b.yaml" | head -20 >&2
		status=1
	fi
	if grep -qE '^kind: Secret$' "$tmp/a.yaml" && [ "$name" != "own-database-url" ]; then
		echo "chart-determinism: FAIL $name: the render holds a Secret; secrets are made by the hook Job" >&2
		status=1
	fi
	# No hook Job means no secrets at all: the default render must have one.
	if [ "$name" = default ] && ! grep -q 'helm.sh/hook": pre-install,pre-upgrade' "$tmp/a.yaml"; then
		echo "chart-determinism: FAIL $name: no pre-install/pre-upgrade hook found" >&2
		status=1
	fi
	echo "chart-determinism: ok $name"
}
check default --set externalURL=https://launchway.example.com
check with-ingress --set externalURL=https://launchway.example.com --set ingress.enabled=true
check own-database-url --set externalURL=https://launchway.example.com --set database.url=postgres://u:p@db.example.com:5432/launchway
check high-availability -f "$chart/examples/ha.yaml"
check deploy-runner --set externalURL=https://launchway.example.com --set delivery.environments[0].name=prod --set delivery.environments[0].target=prod --set delivery.environments[0].projects[0]=shop \
	--set delivery.deployRunner.enabled=true --set delivery.deployRunner.target=prod --set delivery.deployRunner.applications[0].projects[0]=shop --set delivery.deployRunner.applications[0].names[0]=shop --set delivery.deployRunner.repos.control.url=http://example.invalid/control.git
# No template may generate randomness or read the cluster: both break the rule above.
if grep -rnE '(randAlphaNum|randBytes|randAlpha|randNumeric|randAscii|genPrivateKey|genCA|genSelfSignedCert) *[0-9("]|[^a-zA-Z]lookup +"' "$chart/templates"; then
	echo "chart-determinism: FAIL a template generates values or reads the cluster (above)" >&2
	status=1
fi
exit $status
