#!/bin/sh
# Creates a small Git repository with a pipeline that shows what Launchway does.
set -e
dir=${1:-../launchway-demo}
if [ -d "$dir/.git" ]; then
  echo "$dir already exists; leaving it alone."
  exit 0
fi
mkdir -p "$dir/.launchway"
cd "$dir"
git init -q -b main
cat > .launchway/pipeline.yaml <<'EOF'
pipeline: demo-app
on:
  push: { branches: [main, "feature/*"] }
  # Every morning at 06:00 in Chicago, the main branch with a full scan.
  schedule:
    - { cron: "0 6 * * *", timezone: America/Chicago, inputs: { scan: full } }

# Values a run is started with: a person starting it sees a form; pushes
# and the schedule above use the defaults (or the schedule's values).
inputs:
  scan: { type: choice, options: [quick, full], default: quick, description: How deep the security scan goes }
  alpine: { type: list, default: ["3.21", "3.22"], description: Alpine versions to check the build on }

# Named containers: a toolbox, and a cache that runs beside the steps.
containers:
  tools: { image: docker.io/library/alpine:3.22 }
  cache: { image: docker.io/library/redis:7-alpine, service: true }

steps:
  lint:
    description: Check the code style
    image: docker.io/library/alpine:3.22
    run: |
      echo "Checking style in $(ls | wc -l) files"
      sleep 1
      printf '\033[32m✓ no problems found\033[0m\n'

  unit-tests:
    description: Run the fast tests
    image: docker.io/library/alpine:3.22
    run: |
      sh tests/unit.sh
      echo "coverage=84%" >> "$LAUNCHWAY_OUTPUTS"
    # Read after the step, passed or failed: JUnit XML and go test -json.
    reports:
      tests: [reports/unit.xml, reports/api.json]

  integration-tests:
    description: Test against the cache service
    container: tools
    needs: unit-tests
    retry: 1
    run: |
      echo "Talking to the cache service at localhost:6379"
      for i in $(seq 1 30); do
        reply=$(printf 'PING\r\n' | nc -w 1 localhost 6379 2>/dev/null | tr -d '\r') || true
        if [ "$reply" = "+PONG" ]; then printf '\033[32mPASS\033[0m  cache answered %s\n' "$reply"; break; fi
        sleep 1
      done
      # One test is flaky: it fails on the first attempt and passes on the
      # retry, so the run shows it as flaky.
      sh tests/integration.sh
    reports:
      tests: reports/integration.xml

  security-scan:
    description: Scan the code, its secrets and its dependencies
    image: docker.io/library/alpine:3.22
    run: sh tests/scan.sh        # the scan input says how deep: quick or full
    # SARIF from any scanner, and a CycloneDX SBOM. The SBOM is kept as the
    # step's artifact "sbom"; secrets a scanner finds are never shown.
    reports:
      findings: reports/security/*.sarif
      sbom: reports/sbom.cdx.json

  # Repeated once for each Alpine version in the alpine input: the run
  # shows them as one node with a status grid.
  compat:
    description: Check the build on each Alpine version
    needs: lint
    for_each: { alpine: { input: alpine } }
    max_parallel: 2
    image: { each: alpine, format: "docker.io/library/alpine:%s" }
    run: |
      echo "Checking on Alpine $LAUNCHWAY_EACH_ALPINE: $(cat /etc/alpine-release 2>/dev/null || echo unknown)"
      echo "alpine $LAUNCHWAY_EACH_ALPINE: ok" > compat.txt
    # Each member keeps its own report, under its own step.
    artifacts:
      - { name: report, paths: [compat.txt], retain: 7d }

  build:
    description: Build and push the image
    image: docker.io/library/alpine:3.22
    needs: [lint, integration-tests, security-scan, compat]
    run: |
      echo "Building $LAUNCHWAY_PROJECT at $LAUNCHWAY_COMMIT (coverage $LAUNCHWAY_NEEDS_UNIT_TESTS_COVERAGE)"
      sleep 2
      digest=$(echo "$LAUNCHWAY_COMMIT" | sha256sum | cut -c1-16)
      echo "image=registry.example.com/demo-app@sha256:$digest" >> "$LAUNCHWAY_OUTPUTS"
      echo "Pushed registry.example.com/demo-app@sha256:$digest"
      mkdir -p dist
      printf '#!/bin/sh\necho "demo-app %s: healthy"\n' "$digest" > dist/demo-app
      chmod +x dist/demo-app
      tar -czf dist/demo-app.tar.gz -C dist demo-app
    # Kept for 14 days: download them from the run, or fetch them in a
    # later step, on whichever runner it runs.
    artifacts:
      - name: binary
        paths: [dist/demo-app, "dist/*.tar.gz"]
        retain: 14d

  smoke-test:
    description: Check that the built image answers
    image: docker.io/library/alpine:3.22
    needs: build
    fetch:
      - { from: build, name: binary, into: smoke }
    run: |
      echo "Testing $LAUNCHWAY_NEEDS_BUILD_IMAGE"
      ./smoke/dist/demo-app
      sleep 1
      # Fails on the first attempt while a file called FLAKY exists, so you
      # can try "Resume". Delete FLAKY and push to make it pass first time.
      if [ "$LAUNCHWAY_ATTEMPT" = "1" ] && [ -f FLAKY ]; then
        echo "GET /healthz → 503 Service Unavailable" >&2
        exit 1
      fi
      echo "GET /healthz → 200 OK"

  # Pauses the run until someone approves it, on main only.
  approve-dev:
    description: A person decides whether this build goes to dev
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy this build to dev?
    timeout: 24h

  deploy-dev:
    description: Deploy the image to the dev environment
    image: docker.io/library/alpine:3.22
    needs: approve-dev
    when: { branch: main }
    run: |
      echo "Would deploy $LAUNCHWAY_NEEDS_BUILD_IMAGE to dev"
EOF
mkdir -p tests
# The demo's tests only print results: a real project runs its own, such as
# pytest --junitxml=reports/unit.xml or go test -json ./... > reports/api.json.
cat > tests/unit.sh <<'EOF2'
#!/bin/sh
# Writes the unit test results as JUnit XML (as pytest does) and the API
# tests' as go test -json output. On a branch with button.txt, the new
# button's test fails.
set -e
mkdir -p reports
fail=0
case_ok() { printf '    <testcase classname="%s" name="%s" file="%s" time="%s"/>\n' "$1" "$2" "$3" "$4"; }
{
  echo '<?xml version="1.0" encoding="utf-8"?>'
  echo '<testsuites><testsuite name="pytest" tests="14">'
  case_ok tests.test_parser test_reads_empty_file tests/test_parser.py 0.004
  case_ok tests.test_parser test_reads_nested_lists tests/test_parser.py 0.011
  case_ok tests.test_parser test_rejects_tabs tests/test_parser.py 0.003
  case_ok tests.test_parser test_unicode_names tests/test_parser.py 0.006
  case_ok tests.test_engine test_runs_in_order tests/test_engine.py 0.240
  case_ok tests.test_engine test_retries_failed_steps tests/test_engine.py 0.512
  case_ok tests.test_engine test_resumes_after_restart tests/test_engine.py 1.204
  case_ok tests.test_api test_lists_runs tests/test_api.py 0.087
  case_ok tests.test_api test_paginates tests/test_api.py 0.065
  case_ok tests.test_api test_hides_other_projects tests/test_api.py 0.091
  case_ok tests.test_ui test_renders_run_list tests/test_ui.py 0.430
  echo '    <testcase classname="tests.test_ui" name="test_dark_mode_contrast" file="tests/test_ui.py" time="0.000">'
  echo '      <skipped type="pytest.skip" message="needs a browser with forced colours">tests/test_ui.py:41: needs a browser with forced colours</skipped>'
  echo '    </testcase>'
  if [ -f button.txt ]; then
    fail=1
    echo '    <testcase classname="tests.test_ui" name="test_button_renders" file="tests/test_ui.py" time="0.318">'
    echo '      <failure message="AssertionError: expected &quot;Save&quot;, got &quot;Sav&quot;">def test_button_renders():'
    echo '        html = render(Button(label="Save"))'
    echo '&gt;       assert "Save" in html'
    echo 'E       AssertionError: expected "Save", got "Sav"'
    echo ''
    echo 'tests/test_ui.py:58: AssertionError</failure>'
    echo '      <system-out>rendering Button(label=Save) at width 320
truncating label to fit: Sav</system-out>'
    echo '    </testcase>'
  else
    case_ok tests.test_ui test_button_renders tests/test_ui.py 0.302
  fi
  case_ok tests.test_ui test_keyboard_navigation tests/test_ui.py 0.188
  echo '</testsuite></testsuites>'
} > reports/unit.xml
{
  for t in TestHealth TestCreateRun TestListRuns TestCancel; do
    printf '{"Action":"run","Package":"example.com/demo/api","Test":"%s"}\n' "$t"
    printf '{"Action":"output","Package":"example.com/demo/api","Test":"%s","Output":"=== RUN   %s\\n"}\n' "$t" "$t"
    printf '{"Action":"pass","Package":"example.com/demo/api","Test":"%s","Elapsed":0.0%s}\n' "$t" "${#t}"
  done
  printf '{"Action":"pass","Package":"example.com/demo/api","Elapsed":0.21}\n'
} > reports/api.json
for t in parser engine api ui; do sleep 0.4; printf '\033[32mPASS\033[0m  %s\n' "$t"; done
if [ "$fail" = 1 ]; then echo "1 test failed: tests/test_ui.py::test_button_renders" >&2; exit 1; fi
EOF2
cat > tests/integration.sh <<'EOF2'
#!/bin/sh
# Writes the integration test results. test_cache_survives_restart fails on
# the first attempt of each run and passes on the retry: a flaky test.
set -e
mkdir -p reports
if [ "$LAUNCHWAY_ATTEMPT" = 1 ]; then
  restart='<testcase classname="tests.integration.CacheTest" name="test_cache_survives_restart" time="5.012"><failure message="TimeoutError: the cache did not come back within 5s">Traceback (most recent call last):
  File "tests/integration/test_cache.py", line 33, in test_cache_survives_restart
    wait_for(cache.ping, timeout=5)
TimeoutError: the cache did not come back within 5s</failure></testcase>'
else
  restart='<testcase classname="tests.integration.CacheTest" name="test_cache_survives_restart" time="2.418"/>'
fi
cat > reports/integration.xml <<XML
<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="integration" tests="4">
  <testcase classname="tests.integration.CacheTest" name="test_ping" time="0.012"/>
  <testcase classname="tests.integration.CacheTest" name="test_set_and_get" time="0.031"/>
  $restart
  <testcase classname="tests.integration.CacheTest" name="test_expiry" time="1.507"/>
</testsuite>
XML
if [ "$LAUNCHWAY_ATTEMPT" = 1 ]; then echo "test_cache_survives_restart failed; the step will be retried" >&2; exit 1; fi
echo "4 integration tests passed"
EOF2
cat > tests/scan.sh <<'EOF2'
#!/bin/sh
# Writes what three scanners would: secrets, code and dependencies, as
# SARIF 2.1.0, and a CycloneDX SBOM. On a branch with button.txt the code
# scan finds one more problem, which the run shows as new.
set -e
mkdir -p reports/security
echo "Scanning for secrets, code problems and vulnerable dependencies (${LAUNCHWAY_INPUT_SCAN:-quick} scan)…"
sleep 1
cat > reports/security/secrets.sarif <<'JSON'
{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[{"tool":{"driver":{"name":"gitleaks","version":"8.24.0",
"rules":[{"id":"aws-access-token","shortDescription":{"text":"Identified a pattern that may indicate AWS credentials, risking unauthorized cloud resource access and data breaches on AWS platforms."}}]}},
"results":[{"ruleId":"aws-access-token","message":{"text":"aws-access-token has detected secret for file config/settings.py: AKIAIOSFODNN7EXAMPLE"},
"locations":[{"physicalLocation":{"artifactLocation":{"uri":"config/settings.py"},"region":{"startLine":12,"startColumn":19,"endLine":12,"endColumn":39,"snippet":{"text":"AWS_KEY = \"AKIAIOSFODNN7EXAMPLE\""}}}}],
"partialFingerprints":{"commitSha":"","email":"","author":"","date":"","commitMessage":""}}]}]}
JSON
extra=""
if [ -f button.txt ]; then
  extra=',{"ruleId":"python.lang.security.audit.eval-detected.eval-detected","message":{"text":"Detected the use of eval(). eval() can be dangerous if used to evaluate dynamic content."},"level":"warning","locations":[{"physicalLocation":{"artifactLocation":{"uri":"app/button.py"},"region":{"startLine":7,"startColumn":12,"endLine":7,"endColumn":30}}}],"properties":{}}'
fi
cat > reports/security/code.sarif <<JSON
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Semgrep OSS","semanticVersion":"1.182.1","rules":[
{"id":"python.lang.security.audit.subprocess-shell-true.subprocess-shell-true","name":"python.lang.security.audit.subprocess-shell-true.subprocess-shell-true","shortDescription":{"text":"Semgrep Finding: subprocess shell=True"},"fullDescription":{"text":"Found 'subprocess' function with 'shell=True'. This is dangerous because this call will spawn the command using a shell process."},"help":{"text":"Use shell=False and pass the arguments as a list."},"helpUri":"https://semgrep.dev/r/python.lang.security.audit.subprocess-shell-true.subprocess-shell-true","defaultConfiguration":{"level":"error"},"properties":{"precision":"very-high","security-severity":"7.5","tags":["CWE-78: OS Command Injection","security"]}},
{"id":"python.lang.security.deserialization.avoid-pyyaml-load.avoid-pyyaml-load","name":"python.lang.security.deserialization.avoid-pyyaml-load.avoid-pyyaml-load","shortDescription":{"text":"Semgrep Finding: avoid-pyyaml-load"},"fullDescription":{"text":"Detected a possible YAML deserialization vulnerability. yaml.load can create arbitrary Python objects."},"help":{"text":"Use yaml.safe_load instead."},"helpUri":"https://semgrep.dev/r/python.lang.security.deserialization.avoid-pyyaml-load.avoid-pyyaml-load","defaultConfiguration":{"level":"warning"},"properties":{"security-severity":"5.0","tags":["CWE-502: Deserialization of Untrusted Data","security"]}},
{"id":"python.lang.security.audit.eval-detected.eval-detected","shortDescription":{"text":"Semgrep Finding: eval-detected"},"fullDescription":{"text":"Detected the use of eval(). eval() can be dangerous if used to evaluate dynamic content."},"helpUri":"https://semgrep.dev/r/python.lang.security.audit.eval-detected.eval-detected","defaultConfiguration":{"level":"warning"},"properties":{"security-severity":"5.1","tags":["CWE-95","security"]}}]}},
"results":[
{"ruleId":"python.lang.security.audit.subprocess-shell-true.subprocess-shell-true","message":{"text":"Found 'subprocess' function 'run' with 'shell=True'."},"level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"app/deploy.py"},"region":{"startLine":21,"startColumn":5,"endLine":21,"endColumn":52}}}],"properties":{}},
{"ruleId":"python.lang.security.deserialization.avoid-pyyaml-load.avoid-pyyaml-load","message":{"text":"Detected a possible YAML deserialization vulnerability: yaml.load(config_text)."},"level":"warning","locations":[{"physicalLocation":{"artifactLocation":{"uri":"app/config.py"},"region":{"startLine":9,"startColumn":12,"endLine":9,"endColumn":35}}}],"properties":{}}$extra
]}]}
JSON
# A full scan also checks the dependencies.
if [ "${LAUNCHWAY_INPUT_SCAN:-quick}" = full ]; then
cat > reports/security/dependencies.sarif <<'JSON'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Trivy","version":"0.61.0","informationUri":"https://github.com/aquasecurity/trivy","rules":[
{"id":"CVE-2023-32681","name":"LanguageSpecificPackageVulnerability","shortDescription":{"text":"python-requests: Unintended leak of Proxy-Authorization header"},"fullDescription":{"text":"Requests is a HTTP library. Since Requests 2.3.0, Requests has been leaking Proxy-Authorization headers to destination servers when redirected to an HTTPS endpoint."},"defaultConfiguration":{"level":"warning"},"helpUri":"https://avd.aquasec.com/nvd/cve-2023-32681","help":{"text":"Vulnerability CVE-2023-32681\nSeverity: MEDIUM\nPackage: requests\nFixed Version: 2.31.0"},"properties":{"precision":"very-high","security-severity":"6.1","tags":["vulnerability","security","MEDIUM"]}},
{"id":"CVE-2019-11324","name":"LanguageSpecificPackageVulnerability","shortDescription":{"text":"python-urllib3: Certification mishandles certain cases"},"fullDescription":{"text":"The urllib3 library before 1.24.2 for Python mishandles certain cases where the desired set of CA certificates is different from the OS store of CA certificates."},"defaultConfiguration":{"level":"error"},"helpUri":"https://avd.aquasec.com/nvd/cve-2019-11324","properties":{"security-severity":"7.5","tags":["vulnerability","security","HIGH"]}},
{"id":"CVE-2022-42969","name":"LanguageSpecificPackageVulnerability","shortDescription":{"text":"python-py: ReDoS in the py library"},"defaultConfiguration":{"level":"note"},"helpUri":"https://avd.aquasec.com/nvd/cve-2022-42969","properties":{"security-severity":"2.0","tags":["vulnerability","security","LOW"]}}]}},
"results":[
{"ruleId":"CVE-2023-32681","ruleIndex":0,"level":"warning","message":{"text":"Package: requests\nInstalled Version: 2.25.1\nVulnerability CVE-2023-32681\nSeverity: MEDIUM\nFixed Version: 2.31.0"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"requirements.txt","uriBaseId":"ROOTPATH"},"region":{"startLine":3,"startColumn":1,"endLine":3,"endColumn":1}}}]},
{"ruleId":"CVE-2019-11324","ruleIndex":1,"level":"error","message":{"text":"Package: urllib3\nInstalled Version: 1.24.1\nVulnerability CVE-2019-11324\nSeverity: HIGH\nFixed Version: 1.24.2"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"requirements.txt","uriBaseId":"ROOTPATH"},"region":{"startLine":5,"startColumn":1,"endLine":5,"endColumn":1}}}]},
{"ruleId":"CVE-2022-42969","ruleIndex":2,"level":"note","message":{"text":"Package: py\nInstalled Version: 1.11.0\nVulnerability CVE-2022-42969\nSeverity: LOW"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"requirements.txt","uriBaseId":"ROOTPATH"},"region":{"startLine":7,"startColumn":1,"endLine":7,"endColumn":1}}}]}
]}]}
JSON
fi
comp() { printf '{"type":"library","name":"%s","version":"%s","purl":"pkg:pypi/%s@%s","licenses":[{"license":{"id":"%s"}}]}' "$1" "$2" "$1" "$2" "$3"; }
{
  printf '{"bomFormat":"CycloneDX","specVersion":"1.5","version":1,"metadata":{"component":{"type":"application","name":"demo-app"}},"components":['
  comp requests 2.25.1 Apache-2.0; printf ,
  comp urllib3 1.24.1 MIT; printf ,
  comp py 1.11.0 MIT; printf ,
  comp pyyaml 6.0.1 MIT; printf ,
  comp flask 3.0.3 BSD-3-Clause; printf ,
  comp werkzeug 3.0.4 BSD-3-Clause; printf ,
  comp jinja2 3.1.4 BSD-3-Clause; printf ,
  comp click 8.1.7 BSD-3-Clause; printf ,
  comp certifi 2024.8.30 MPL-2.0; printf ,
  comp idna 3.10 BSD-3-Clause; printf ,
  comp charset-normalizer 3.4.0 MIT; printf ,
  printf '{"type":"library","name":"left-pad","version":"1.3.0","purl":"pkg:npm/left-pad@1.3.0","licenses":[{"license":{"id":"WTFPL"}}]}'
  printf ']}\n'
} > reports/sbom.cdx.json
printf '\033[33mFound problems; see the Security tab\033[0m\n'
EOF2
echo "A demo app for trying Launchway." > README.md
echo "Delete this file to make the smoke test pass first time." > FLAKY
git add -A
git -c user.name=Launchway -c user.email=demo@localhost commit -q -m "Initial demo app"
git checkout -q -b feature/new-button
echo "button" > button.txt
git add -A
git -c user.name=Launchway -c user.email=demo@localhost commit -q -m "Add a new button"
git checkout -q main
echo "Created $dir"
