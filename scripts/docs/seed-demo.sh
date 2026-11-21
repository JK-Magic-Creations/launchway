#!/bin/sh
# Demo data for the screenshot tour (scripts/docs-screenshots.sh).
#
#   seed-demo.sh repos DIR       creates the demo repositories and a server
#                                configuration, DIR/launchway.yaml
#   seed-demo.sh runs URL DIR    drives a running server over its API until
#                                it shows realistic activity
#
# Everything is made up: the projects, the people and the example.com names.
# Steps run in docker.io/library/alpine:3.22 (and redis:7-alpine, as the
# demo-app's service), with plain shell; nothing is fetched from the network
# but the images.
set -eu
# Nothing here may ask for a password or reach for an agent.
GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false
export GIT_TERMINAL_PROMPT GIT_ASKPASS
unset SSH_ASKPASS
here=$(cd "$(dirname "$0")/../.." && pwd)

commit() {
  git -c user.name="${2:-Sam Rivera}" -c user.email="${3:-sam.rivera@example.com}" commit -q -m "$1"
}

# ---------------------------------------------------------------- repositories

repos() {
  dir=$1
  mkdir -p "$dir"
  dir=$(cd "$dir" && pwd)
  # demo-app is the repository `make demo` makes; the library and the
  # repository without a pipeline file are `make demo`'s too.
  "$here/scripts/make-demo-repo.sh" "$dir/demo-app" >/dev/null
  "$here/scripts/make-demo-library.sh" "$dir/platform-library" "$dir/.unused-lib-app" "$dir/docs-site" >/dev/null
  web_frontend "$dir/web-frontend"
  payments_api "$dir/payments-api"
  config "$dir"
  echo "Demo repositories and configuration in $dir"
}

web_frontend() {
  d=$1
  [ -d "$d/.git" ] && return 0
  mkdir -p "$d/.launchway" "$d/src" "$d/ci"
  cat > "$d/.launchway/pipeline.yaml" <<'YAML'
pipeline: web-frontend
on:
  push: { branches: [main] }

inputs:
  browsers: { type: list, default: [chromium, firefox, webkit], description: Browsers the end-to-end tests run in }

steps:
  install:
    description: Install the dependencies
    image: docker.io/library/alpine:3.22
    run: |
      echo "Installing 412 packages from package-lock.json"
      sleep 1
      echo "packages=412" >> "$LAUNCHWAY_OUTPUTS"

  unit-tests:
    description: Component and unit tests
    image: docker.io/library/alpine:3.22
    needs: install
    retry: 1
    run: sh ci/unit-tests.sh
    reports:
      tests: reports/junit.xml

  audit:
    description: Lint for security problems and audit the dependencies
    image: docker.io/library/alpine:3.22
    needs: install
    run: sh ci/audit.sh
    reports:
      findings: reports/*.sarif
      sbom: reports/sbom.cdx.json

  e2e:
    description: End-to-end tests in each browser
    needs: unit-tests
    for_each: { browser: { input: browsers } }
    max_parallel: 3
    image: docker.io/library/alpine:3.22
    run: sh ci/e2e.sh
    reports:
      tests: reports/e2e.xml

  bundle:
    description: Build the production bundle
    image: docker.io/library/alpine:3.22
    needs: [e2e, audit]
    run: |
      mkdir -p dist/assets
      for f in index app vendor; do head -c 20000 /dev/urandom | base64 > dist/assets/$f.js; done
      echo '<!doctype html><title>Web frontend</title>' > dist/index.html
      tar -czf web-frontend.tar.gz dist
      echo "Bundle: $(du -sh dist | cut -f1)"
    artifacts:
      - { name: bundle, paths: [web-frontend.tar.gz], retain: 14d }
      - { name: site, paths: ["dist/**/*"], retain: 7d }
YAML
  cat > "$d/ci/unit-tests.sh" <<'SH'
#!/bin/sh
# Writes JUnit XML as a JavaScript test runner would. The date picker's two
# tests fail while src/delivery.js offers four days instead of five; the toast test is
# flaky: it fails on a step's first attempt in every other run.
set -e
mkdir -p reports
ok() { printf '  <testcase classname="%s" name="%s" file="%s" time="%s"/>\n' "$1" "$2" "$3" "$4"; }
failed=0
flaky=0
if [ "$LAUNCHWAY_ATTEMPT" = 1 ] && [ $(( ${LAUNCHWAY_RUN_NUMBER:-1} % 2 )) = 0 ]; then flaky=1; fi
{
  echo '<?xml version="1.0" encoding="UTF-8"?>'
  echo '<testsuites name="unit">'
  echo ' <testsuite name="components" tests="12">'
  for t in "renders the header" "renders the footer links" "opens the menu" "closes the menu on escape" \
           "shows the cart count" "formats prices in EUR" "formats prices in USD" "hides empty banners"; do
    ok "components.Layout" "$t" src/components/Layout.test.tsx 0.0$((${#t} % 9))4
  done
  ok components.SearchBox "debounces typing" src/components/SearchBox.test.tsx 0.212
  ok components.SearchBox "shows suggestions" src/components/SearchBox.test.tsx 0.087
  if [ "$flaky" = 1 ]; then
    echo '  <testcase classname="components.Toast" name="disappears after five seconds" file="src/components/Toast.test.tsx" time="5.004">'
    echo '   <failure message="Timed out waiting for the toast to disappear">Error: Timed out in waitFor after 5000ms
    at waitFor (node_modules/@testing-library/dom/dist/wait-for.js:166:27)
    at Object.&lt;anonymous&gt; (src/components/Toast.test.tsx:31:11)</failure>'
    echo '  </testcase>'
  else
    ok components.Toast "disappears after five seconds" src/components/Toast.test.tsx 1.204
  fi
  ok components.Toast "can be dismissed" src/components/Toast.test.tsx 0.041
  echo ' </testsuite>'
  echo ' <testsuite name="checkout" tests="9">'
  for t in "adds an item" "removes an item" "applies a discount code" "rejects an expired code" \
           "totals with tax" "keeps the cart after reload" "validates the postcode"; do
    ok "checkout.Cart" "$t" src/checkout/Cart.test.ts 0.0$((${#t} % 7))2
  done
  if [ -f src/delivery.js ] && grep -q 'WORKING_DAYS = 4' src/delivery.js; then
    failed=1
    echo '  <testcase classname="checkout.DeliveryDate" name="offers the next five working days" file="src/checkout/DeliveryDate.test.ts" time="0.034">'
    echo '   <failure message="expect(received).toHaveLength(expected)">Error: expect(received).toHaveLength(expected)

Expected length: 5
Received length: 4
Received array:  ["2026-11-30", "2026-12-01", "2026-12-02", "2026-12-03"]
    at Object.&lt;anonymous&gt; (src/checkout/DeliveryDate.test.ts:22:30)</failure>'
    echo '   <system-out>today: 2026-11-27 (Friday)</system-out>'
    echo '  </testcase>'
    echo '  <testcase classname="checkout.DeliveryDate" name="skips public holidays" file="src/checkout/DeliveryDate.test.ts" time="0.019">'
    echo '   <failure message="expected 2026-12-28 to be offered">Error: expected 2026-12-28 to be offered after the holidays
    at Object.&lt;anonymous&gt; (src/checkout/DeliveryDate.test.ts:41:28)</failure>'
    echo '  </testcase>'
  else
    ok checkout.DeliveryDate "offers the next five working days" src/checkout/DeliveryDate.test.ts 0.031
    ok checkout.DeliveryDate "skips public holidays" src/checkout/DeliveryDate.test.ts 0.017
  fi
  echo '  <testcase classname="checkout.Payment" name="supports saved cards" file="src/checkout/Payment.test.ts" time="0">'
  echo '   <skipped message="waiting for the payments sandbox"/>'
  echo '  </testcase>'
  echo ' </testsuite>'
  echo '</testsuites>'
} > reports/junit.xml
sleep 1
if [ "$failed" = 1 ]; then echo "2 tests failed in src/checkout/DeliveryDate.test.ts" >&2; exit 1; fi
if [ "$flaky" = 1 ]; then echo "1 test failed: Toast disappears after five seconds" >&2; exit 1; fi
echo "Tests: 21 passed, 1 skipped, 22 total"
SH
  cat > "$d/ci/e2e.sh" <<'SH'
#!/bin/sh
# Writes the end-to-end results of one browser as JUnit XML.
set -e
b=${LAUNCHWAY_EACH_BROWSER:-chromium}
mkdir -p reports
echo "Running 6 end-to-end tests in $b against http://localhost:4173"
sleep 2
{
  echo '<?xml version="1.0" encoding="UTF-8"?>'
  echo "<testsuite name=\"e2e-$b\" tests=\"6\">"
  for t in "home page loads" "search finds a product" "add to cart" "checkout as a guest" "sign in" "change the language"; do
    sleep 1; printf '  \033[32m✓\033[0m %s (%s)\n' "$t" "$b" >&2
    if [ "$b" = webkit ] && [ "$t" = "change the language" ]; then
      echo "<testcase classname=\"e2e.$b\" name=\"$t\" time=\"0\"><skipped message=\"not supported in webkit yet\"/></testcase>"
    else
      echo "<testcase classname=\"e2e.$b\" name=\"$t\" time=\"1.$((${#t} % 10))\"/>"
    fi
  done
  echo '</testsuite>'
} > reports/e2e.xml
echo "$b: all tests passed"
SH
  cat > "$d/ci/audit.sh" <<'SH'
#!/bin/sh
# Writes what a linter's security rules and a dependency audit would find,
# as SARIF 2.1.0, and a CycloneDX SBOM of the dependencies.
set -e
mkdir -p reports
echo "Linting 184 files and auditing 412 packages"
sleep 1
cat > reports/lint.sarif <<'JSON'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"eslint-security","version":"3.0.1","rules":[
{"id":"no-unsanitized/property","shortDescription":{"text":"Assignment to innerHTML with unsanitised input"},"helpUri":"https://example.com/rules/no-unsanitized","properties":{"security-severity":"6.5","tags":["CWE-79","security"]}},
{"id":"detect-non-literal-regexp","shortDescription":{"text":"RegExp built from a variable"},"properties":{"security-severity":"3.1","tags":["CWE-1333","security"]}}]}},
"results":[
{"ruleId":"no-unsanitized/property","level":"error","message":{"text":"Unsafe assignment to innerHTML in renderReview()"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/components/Review.tsx"},"region":{"startLine":48,"startColumn":5}}}]},
{"ruleId":"detect-non-literal-regexp","level":"warning","message":{"text":"RegExp() called with the search term"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/components/SearchBox.tsx"},"region":{"startLine":17,"startColumn":19}}}]}
]}]}
JSON
cat > reports/dependencies.sarif <<'JSON'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Trivy","version":"0.61.0","rules":[
{"id":"CVE-2024-4067","shortDescription":{"text":"micromatch: vulnerable to Regular Expression Denial of Service"},"helpUri":"https://avd.aquasec.com/nvd/cve-2024-4067","properties":{"security-severity":"5.3","tags":["vulnerability","security","MEDIUM"]}},
{"id":"CVE-2024-45296","shortDescription":{"text":"path-to-regexp: backtracking regular expressions cause ReDoS"},"helpUri":"https://avd.aquasec.com/nvd/cve-2024-45296","properties":{"security-severity":"7.5","tags":["vulnerability","security","HIGH"]}}]}},
"results":[
{"ruleId":"CVE-2024-4067","level":"warning","message":{"text":"Package: micromatch\nInstalled Version: 4.0.5\nFixed Version: 4.0.8"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"package-lock.json"},"region":{"startLine":1}}}]},
{"ruleId":"CVE-2024-45296","level":"error","message":{"text":"Package: path-to-regexp\nInstalled Version: 0.1.7\nFixed Version: 0.1.10"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"package-lock.json"},"region":{"startLine":1}}}]}
]}]}
JSON
comp() { printf '{"type":"library","name":"%s","version":"%s","purl":"pkg:npm/%s@%s","licenses":[{"license":{"id":"%s"}}]}' "$1" "$2" "$1" "$2" "$3"; }
{
  printf '{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"metadata":{"component":{"type":"application","name":"web-frontend"}},"components":['
  comp react 19.1.0 MIT; printf ,
  comp react-dom 19.1.0 MIT; printf ,
  comp react-router 7.1.1 MIT; printf ,
  comp date-fns 4.1.0 MIT; printf ,
  comp micromatch 4.0.5 MIT; printf ,
  comp path-to-regexp 0.1.7 MIT; printf ,
  comp zod 3.24.1 MIT; printf ,
  comp clsx 2.1.1 MIT; printf ,
  comp dompurify 3.2.3 "MPL-2.0"; printf ,
  comp left-pad 1.1.3 WTFPL
  printf ']}\n'
} > reports/sbom.cdx.json
echo "2 lint findings, 2 vulnerable dependencies; see the Security tab"
SH
  cat > "$d/src/app.js" <<'JS'
export function start() { console.log("web frontend"); }
JS
  echo "# Web frontend" > "$d/README.md"
  (cd "$d" && git init -q -b main && git add -A && commit "Web frontend with unit and end-to-end tests" "Priya Shah" priya.shah@example.com)
}

payments_api() {
  d=$1
  [ -d "$d/.git" ] && return 0
  mkdir -p "$d/.launchway" "$d/ci"
  cat > "$d/.launchway/pipeline.yaml" <<'YAML'
pipeline: payments-api
on:
  push: { branches: [main] }

# The platform team's templates, pinned to a release.
libraries:
  platform: { repo: platform-library, version: v2 }

inputs:
  environment: { type: choice, options: [dev, prod], default: dev, description: Where the release goes }

steps:
  unit-tests:
    description: Go unit tests
    image: docker.io/library/alpine:3.22
    run: sh ci/test.sh
    reports:
      tests: reports/test.json

  release:
    uses: platform/release
    needs: unit-tests
    with: { environment: { input: environment }, approve: true }

  smoke-test:
    description: Check the deployed service answers
    image: docker.io/library/alpine:3.22
    needs: release
    run: |
      echo "GET $LAUNCHWAY_NEEDS_RELEASE_URL/healthz → 200 OK (image $LAUNCHWAY_NEEDS_RELEASE_DIGEST)"
YAML
  cat > "$d/ci/test.sh" <<'SH'
#!/bin/sh
# Writes go test -json output for the service's packages.
set -e
mkdir -p reports
: > reports/test.json
for p in ledger refunds webhooks; do
  for t in TestCapture TestRefundPartial TestIdempotencyKey TestCurrencyRounding; do
    printf '{"Action":"run","Package":"example.com/payments/%s","Test":"%s"}\n' "$p" "$t" >> reports/test.json
    printf '{"Action":"pass","Package":"example.com/payments/%s","Test":"%s","Elapsed":0.0%s}\n' "$p" "$t" "${#t}" >> reports/test.json
  done
  printf '{"Action":"pass","Package":"example.com/payments/%s","Elapsed":0.4}\n' "$p" >> reports/test.json
  echo "ok   example.com/payments/$p  0.4s"
done
SH
  printf '#!/bin/sh\n# TODO: read the port from the environment\necho "listening on 8080"\n' > "$d/start.sh"
  echo "# Payments API" > "$d/README.md"
  (cd "$d" && git init -q -b main && git add -A && commit "Payments API on the platform release template" "Jordan Lee" jordan.lee@example.com)
}

config() {
  d=$1
  cat > "$d/launchway.yaml" <<'YAML'
# The demo configuration of the screenshot tour. Paths are relative to this
# file. No sign-in: listen on a loopback address only.
listen: 127.0.0.1:8080
data: data
user: ana
egress: { allow_local: true }
forge: { hosting: true }

projects:
  - name: demo-app
    repo: demo-app
    branch: main
    poll: 5s
    push: { branch_prefix: launchway/ }
  - name: payments-api
    repo: payments-api
    branch: main
    poll: 5s
  - name: web-frontend
    repo: web-frontend
    branch: main
    poll: 5s
  - name: docs-site
    repo: docs-site
    branch: main
    poll: 5s
    pipeline:
      library: platform-library
      version: v2
      template: check
      with: { strict: false }
      on: { push: { branches: [main] } }

libraries:
  - name: platform-library
    repo: platform-library

drafts: { allow: operator }

# Policies in report mode: judged and shown on every run, never blocking.
policy:
  policies:
    - name: pinned-libraries
      description: Shared libraries are pinned to a release, and no step may run longer than an hour.
      mode: report
      rules:
        library_versions: { pinned: tag }
        max_timeout: 1h
    - name: image-digests
      description: Steps run in images pinned by digest, from the approved registry.
      mode: report
      scope: { projects: [payments-api, web-frontend] }
      rules:
        step_images: { digest: true, registries: ["docker.io/library/**"] }

# Steps run on a separate runner process, build-1 (see
# scripts/docs-screenshots.sh), so that no host name shows in the tour.
runner:
  enabled: false
YAML
}

# ---------------------------------------------------------------- activity

URL=
api() { # api METHOD PATH [JSON]
  if [ $# -ge 3 ]; then
    curl -fsS -X "$1" -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' --data "$3" "$URL$2"
  else
    curl -fsS -X "$1" -H 'X-Launchway-CSRF: 1' "$URL$2"
  fi
}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
start() { # start PROJECT JSON → run id
  api POST "/api/v1/projects/$1/runs" "$2" | json 'd.get("id") or d["run"]["id"]'
}
wait_for() { # wait_for RUN STATUS... (up to 4 minutes); "waiting" means a step waits for a person
  run=$1; shift
  i=0
  while [ $i -lt 480 ]; do
    s=$(api GET "/api/v1/runs/$run" | json '"waiting" if d.get("waiting") and d["status"] == "running" else d["status"]')
    for want in "$@"; do [ "$s" = "$want" ] && return 0; done
    sleep 0.5; i=$((i + 1))
  done
  echo "run $run is $s, not $*" >&2
  return 1
}

# wait_head PROJECT DIR: until the server has seen the commit at the tip of
# main in DIR. A run started just after a commit may otherwise run the
# commit before it: the server reads the repository when it polls.
wait_head() {
  want=$(git -C "$2" rev-parse HEAD)
  i=0
  until [ "$(api GET "/api/v1/projects/$1/inputs?ref=main" | json 'd["sha"]')" = "$want" ]; do
    i=$((i + 1)); [ $i -gt 120 ] && { echo "the server never saw $want in $1" >&2; return 1; }
    sleep 0.5
  done
}

# ---------------------------------------------------------------- hosted repository

# forge URL DIR: a repository hosted by the server itself, acme-flight/flight-api,
# with a pipeline, a pull request with a review comment and checks, and a merge rule.
forge() {
  URL=$1
  dir=$(cd "$2" && pwd)
  # The demo server and its repositories are on this machine: say so.
  api PUT /api/v1/config/installation/installation '{"spec":{"drafts":{"allow":"operator"},"forge":{"hosting":true},"egress":{"allow":["127.0.0.1"]}}}' >/dev/null
  api POST /api/v1/repositories '{"owner":"acme-flight","name":"flight-api","description":"Booking and schedules for Acme Flight","default_branch":"main"}' >/dev/null
  remote=$URL/git/acme-flight/flight-api.git
  d=$dir/flight-api
  mkdir -p "$d/.launchway" "$d/src"
  cat > "$d/.launchway/pipeline.yaml" <<'YAML'
pipeline: flight-api
on:
  push: { branches: [main] }
  pull_request: {}
steps:
  lint:
    description: Check the code style
    image: docker.io/library/alpine:3.22
    run: |
      sleep 1
      echo "checked 14 files, no style problems"
  test:
    description: Run the unit tests
    image: docker.io/library/alpine:3.22
    run: |
      sleep 2
      echo "ok  flightapi/booking   0.412s"
      echo "ok  flightapi/schedule  0.221s"
  scan:
    description: Scan the dependencies
    image: docker.io/library/alpine:3.22
    run: |
      sleep 1
      echo "no known vulnerabilities in 23 dependencies"
  build:
    description: Build the service
    image: docker.io/library/alpine:3.22
    needs: [lint, test, scan]
    run: echo "built flight-api 1.4.0"
YAML
  printf '# Flight API\n\nBooking and schedules for Acme Flight.\n' > "$d/README.md"
  printf 'package flightapi\n\n// Book reserves a seat on a flight.\nfunc Book(flight, traveler string) error { return nil }\n' > "$d/src/booking.go"
  (cd "$d" && git init -q -b main . && git add -A && commit "Start the booking API" "Ana Torres" ana.torres@example.com && git push -q "$remote" main)
  api PUT /api/v1/config/projects/flight-api "{\"spec\":{\"repo\":\"$remote\",\"branch\":\"main\",\"poll\":\"5s\"}}" >/dev/null
  api PUT /api/v1/repositories/acme-flight/flight-api/protected-branches '{"patterns":["main"]}' >/dev/null
  api PUT /api/v1/repositories/acme-flight/flight-api/merge-rules '{"rules":[{"pattern":"main","required_approvals":1,"approval_role":"operator","required_checks":["flight-api/test"],"require_up_to_date":true,"require_resolved_threads":true,"no_self_approval":true,"allow_admin_bypass":false}]}' >/dev/null
  wait_head flight-api "$d"
  printf 'package flightapi\n\n// SelectSeat holds a seat for ten minutes.\nfunc SelectSeat(flight, seat string) error { return nil }\n' > "$d/src/seats.go"
  (cd "$d" && git checkout -q -b seat-selection && git add -A && commit "Let travelers pick a seat" "Priya Shah" priya.shah@example.com && git push -q "$remote" seat-selection)
  r=/api/v1/repositories/acme-flight/flight-api
  api POST $r/pulls '{"title":"Let travelers pick a seat","body":"Adds seat selection to booking. A seat is held for ten minutes.\n\n- the new `SelectSeat` call\n- tests follow in the next change","source_branch":"seat-selection","target_branch":"main"}' >/dev/null
  api POST $r/pulls/1/comments '{"body":"Should a held seat expire if the traveler closes the page? Ten minutes feels long.","path":"src/seats.go","line":3}' >/dev/null
  api POST $r/pulls/1/reviews '{"verdict":"comment","body":"Looks good overall. One question inline."}' >/dev/null
  i=0
  until [ "$(api GET /api/v1/runs | json 'sum(1 for r in d if r["project"] == "flight-api" and r["status"] not in ("succeeded", "failed", "canceled"))')" = 0 ] &&
        [ "$(api GET $r/pulls/1 | json 'sum(1 for c in d["readiness"]["checks"] if c["status"] in ("pending", "running"))')" = 0 ]; do
    i=$((i + 1)); [ $i -gt 240 ] && { echo "the pull request's checks never finished" >&2; return 1; }
    sleep 0.5
  done
}

runs() {
  URL=$1
  dir=$(cd "$2" && pwd)
  # Projects are read from Git in the background: wait until each has a
  # pipeline before starting runs.
  for p in demo-app payments-api web-frontend docs-site; do
    i=0
    until api GET "/api/v1/projects/$p/inputs" >/dev/null 2>&1; do
      i=$((i + 1)); [ $i -gt 120 ] && { echo "project $p never became ready" >&2; return 1; }
      sleep 0.5
    done
  done

  # web-frontend: a clean run, then a commit that breaks two tests, then
  # the fix. Every other run has a flaky test that passes on its retry.
  w1=$(start web-frontend '{"ref":"main"}')
  wait_for "$w1" succeeded failed
  printf '// How many working days the checkout offers for delivery.\nexport const WORKING_DAYS = 4;\n' > "$dir/web-frontend/src/delivery.js"
  (cd "$dir/web-frontend" && git add -A && commit "Add delivery date picker" "Priya Shah" priya.shah@example.com)
  wait_head web-frontend "$dir/web-frontend"
  w2=$(start web-frontend '{"ref":"main"}')
  wait_for "$w2" succeeded failed
  sed -i 's/WORKING_DAYS = 4/WORKING_DAYS = 5/' "$dir/web-frontend/src/delivery.js"
  (cd "$dir/web-frontend" && git add -A && commit "Offer five delivery days again" "Priya Shah" priya.shah@example.com)
  wait_head web-frontend "$dir/web-frontend"
  w3=$(start web-frontend '{"ref":"main"}')

  # docs-site: the server gives it the library's check template.
  d1=$(start docs-site '{"ref":"main"}')

  # payments-api: the library's release template; waits for approval.
  p1=$(start payments-api '{"ref":"main","inputs":{"environment":"dev"}}')

  # demo-app on its feature branch: a failing unit test and a new finding.
  f1=$(start demo-app '{"ref":"feature/new-button"}')

  # demo-app on main with a full scan: smoke-test fails on its first
  # attempt (the FLAKY file). Resumed, it waits for approval; approved, it
  # deploys.
  a1=$(start demo-app '{"ref":"main","inputs":{"scan":"full","alpine":["3.21","3.22"]}}')
  wait_for "$a1" failed
  api POST "/api/v1/runs/$a1/resume" '{}' >/dev/null
  wait_for "$a1" waiting
  api POST "/api/v1/runs/$a1/steps/approve-dev/approve" '{"comment":"Looks good"}' >/dev/null
  wait_for "$a1" succeeded

  wait_for "$p1" waiting
  api POST "/api/v1/runs/$p1/steps/release%2Fapprove/approve" '{"comment":"Release window open"}' >/dev/null || true
  wait_for "$p1" succeeded failed
  p2=$(start payments-api '{"ref":"main","inputs":{"environment":"prod"}}')

  # Another demo-app run on main, left failed: the run to resume.
  a2=$(start demo-app '{"ref":"main","inputs":{"scan":"quick","alpine":["3.22"]}}')

  # And one more, resumed, left waiting for approval.
  a3=$(start demo-app '{"ref":"main","inputs":{"scan":"quick","alpine":["3.21","3.22"]}}')
  wait_for "$a3" failed
  api POST "/api/v1/runs/$a3/resume" '{}' >/dev/null

  # A draft run of an edited pipeline.
  pipeline=$(cat "$dir/demo-app/.launchway/pipeline.yaml")
  draft=$(printf '%s' "$pipeline" | sed 's/Check the code style/Check the code style and spelling/' |
    python3 -c 'import json,sys; print(json.dumps({"ref":"main","pipeline":sys.stdin.read()}))')
  r1=$(api POST /api/v1/projects/demo-app/draft-runs "$draft" | json 'd.get("id") or d["run"]["id"]')

  for r in "$w3" "$d1" "$f1" "$a2" "$r1"; do wait_for "$r" succeeded failed waiting; done
  wait_for "$a3" waiting
  wait_for "$p2" waiting
  forge "$URL" "$dir"
  cat > "$dir/runs.env" <<EOF
WEB_OK=$w1
WEB_FAILED=$w2
WEB_FIXED=$w3
DOCS=$d1
PAY_DONE=$p1
PAY_WAITING=$p2
APP_FEATURE=$f1
APP_DONE=$a1
APP_FAILED=$a2
APP_WAITING=$a3
APP_DRAFT=$r1
EOF
  echo "Seeded runs:"; cat "$dir/runs.env"
}

case "${1:-}" in
  repos) repos "$2" ;;
  runs) runs "$2" "$3" ;;
  *) echo "usage: $0 repos DIR | runs URL DIR" >&2; exit 2 ;;
esac
