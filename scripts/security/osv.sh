#!/bin/sh
# Known vulnerabilities in every dependency Launchway locks: Go modules (go.mod), the web UI's npm
# lock file and the documentation site's npm lock file, from OSV.dev (Google's open vulnerability
# database, which aggregates GitHub advisories, the Go and npm databases and more) with osv-scanner.
# Policy: any finding fails. An exception goes in osv-scanner.toml with a reason and an expiry date
# (ignoreUntil); an expired exception stops applying. npm audit runs too, because the registry's
# own advisories sometimes arrive first: high and critical fail for the shipped web UI.
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
echo "== osv-scanner"
go run "$OSV_SCANNER" scan source --config osv-scanner.toml --lockfile go.mod --lockfile web/package-lock.json --lockfile docs/package-lock.json
echo "== npm audit, web UI (fails on high and above; the UI ships to browsers)"
(cd web && npm audit --package-lock-only --audit-level=high)
echo "== npm audit, documentation tools (build-time only; fails on high and above)"
(cd docs && npm audit --package-lock-only --audit-level=high)
