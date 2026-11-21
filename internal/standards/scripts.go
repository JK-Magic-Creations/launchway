package standards

// The scripts of the checks. Each runs with /bin/sh in the tool's pinned
// image, in the workspace, and writes its report under .standing/. A
// script fails only when the tool did not do its work (no report), never
// because it found something: what blocks is decided by the gate, from
// the findings, against the target branch's. Reports are written to /tmp
// first and copied in when complete, so a half-written file is never read
// and a parallel scan never reads another check's report.

const scriptPrelude = `set +e
mkdir -p .standing
git config --global --add safe.directory /workspace >/dev/null 2>&1
`

const scriptSecrets = scriptPrelude + `opts=""
if [ -n "$LAUNCHWAY_STANDING_SINCE" ] && git cat-file -e "$LAUNCHWAY_STANDING_SINCE^{commit}" 2>/dev/null; then
  opts="--log-opts=$LAUNCHWAY_STANDING_SINCE..HEAD"
  echo "Reading the commits since $LAUNCHWAY_STANDING_SINCE."
else
  echo "Reading every commit in the history."
fi
if [ -d .git ]; then
  gitleaks git . $opts --no-banner --redact --exit-code 0 --report-format sarif --report-path /tmp/r.sarif
else
  echo "The workspace has no Git history; reading the files instead."
  gitleaks dir . --no-banner --redact --exit-code 0 --report-format sarif --report-path /tmp/r.sarif
fi
[ -s /tmp/r.sarif ] || { echo "gitleaks wrote no report, so this check did not run."; exit 1; }
cp /tmp/r.sarif .standing/secrets.sarif
`

const scriptVulnDeps = scriptPrelude + `/osv-scanner scan source -r --format sarif --output-file /tmp/r.sarif . ; rc=$?
if [ ! -s /tmp/r.sarif ]; then
  [ "$rc" = "128" ] && { echo "No dependency files were found; nothing to check."; exit 0; }
  echo "osv-scanner wrote no report (exit $rc), so this check did not run."; exit 1
fi
cp /tmp/r.sarif .standing/vuln-deps.sarif
`

const goModules = `find . -name go.mod -not -path './.standing/*' -not -path '*/vendor/*' -not -path '*/node_modules/*' | head -20`

const goEnv = `export GOFLAGS=-buildvcs=false GOMODCACHE=/workspace/.standing/cache/gomod GOCACHE=/workspace/.standing/cache/gobuild
`

const scriptVulnReachGo = scriptPrelude + goEnv + `n=0; bad=0
` + goModules + ` > /tmp/mods
while IFS= read -r mod; do
  d=$(dirname "$mod"); n=$((n+1))
  ( cd "$d" && go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 -format sarif ./... > "/tmp/g$n.sarif" 2> "/tmp/g$n.err" )
  if [ -s "/tmp/g$n.sarif" ]; then
    if [ "$d" != "." ]; then
      p=$(printf '%s' "${d#./}" | sed 's/[&#\\]/\\&/g')
      sed -E "s#\"uri\": ?\"([^:\"]+)\"#\"uri\": \"$p/\\1\"#g" "/tmp/g$n.sarif" > ".standing/vuln-reach-go-$n.sarif"
    else
      cp "/tmp/g$n.sarif" ".standing/vuln-reach-go-$n.sarif"
    fi
  else
    echo "govulncheck produced no report in $d:"; head -5 "/tmp/g$n.err"; bad=1
  fi
done < /tmp/mods
[ "$n" -gt 0 ] || { echo "No go.mod found; nothing to check."; exit 0; }
exit $bad
`

const scriptSastGo = scriptPrelude + goEnv + `n=0; bad=0
` + goModules + ` > /tmp/mods
while IFS= read -r mod; do
  d=$(dirname "$mod"); n=$((n+1))
  ( cd "$d" && gosec -fmt sarif -out "/tmp/s$n.sarif" -no-fail ./... > "/tmp/s$n.log" 2>&1 )
  if [ -s "/tmp/s$n.sarif" ]; then
    if [ "$d" != "." ]; then
      p=$(printf '%s' "${d#./}" | sed 's/[&#\\]/\\&/g')
      sed -E "s#\"uri\": ?\"([^:/\"][^:\"]*)\"#\"uri\": \"$p/\\1\"#g" "/tmp/s$n.sarif" > ".standing/sast-go-$n.sarif"
    else
      cp "/tmp/s$n.sarif" ".standing/sast-go-$n.sarif"
    fi
  else
    echo "gosec produced no report in $d:"; tail -5 "/tmp/s$n.log"; bad=1
  fi
done < /tmp/mods
[ "$n" -gt 0 ] || { echo "No go.mod found; nothing to check."; exit 0; }
exit $bad
`

const scriptSastPython = scriptPrelude + `ruff check --select S --ignore S101 --no-cache --exit-zero --output-format sarif --output-file /tmp/r.sarif .
[ -s /tmp/r.sarif ] || { echo "ruff wrote no report, so this check did not run."; exit 1; }
cp /tmp/r.sarif .standing/sast-python.sarif
`

const scriptLintPython = scriptPrelude + `ruff check --no-cache --exit-zero --output-format sarif --output-file /tmp/r.sarif .
[ -s /tmp/r.sarif ] || { echo "ruff wrote no report, so this check did not run."; exit 1; }
cp /tmp/r.sarif .standing/lint-python.sarif
`

// The license list comes in the environment (names only: letters, digits,
// dots, plus and minus, which the server checks before it puts them there).
const scriptLicense = scriptPrelude + `lic=""; [ -n "$STANDING_LICENSE_ALLOW" ] && lic="=$STANDING_LICENSE_ALLOW"
j() { printf '%s' "$1" | sed 's/[^,][^,]*/"&"/g'; }
/osv-scanner scan source -r --licenses$lic --format json --output-file /tmp/l.json . ; rc=$?
if [ ! -s /tmp/l.json ]; then
  [ "$rc" = "128" ] && { echo "No dependency files were found; nothing to check."; exit 0; }
  echo "osv-scanner wrote no report (exit $rc), so this check did not run."; exit 1
fi
{ printf '{"launchway_licenses":{"allow":[%s],"deny":[%s]},"osv":' "$(j "$STANDING_LICENSE_ALLOW")" "$(j "$STANDING_LICENSE_DENY")"; cat /tmp/l.json; printf '}\n'; } > /tmp/w.json
cp /tmp/w.json .standing/licenses.json
`

const scriptSBOM = scriptPrelude + `trivy fs --skip-dirs .standing --cache-dir .standing/cache/trivy --format cyclonedx --output /tmp/sbom.cdx.json . 
[ -s /tmp/sbom.cdx.json ] || { echo "trivy wrote no bill of materials, so this check did not run."; exit 1; }
cp /tmp/sbom.cdx.json .standing/sbom.cdx.json
`

const scriptDockerfile = scriptPrelude + `find . -type f \( -name Dockerfile -o -name 'Dockerfile.*' -o -name '*.Dockerfile' -o -name '*.dockerfile' -o -name Containerfile \) -not -path './.standing/*' -not -path '*/node_modules/*' | head -50 > /tmp/files
n=0; bad=0
while IFS= read -r f; do
  n=$((n+1))
  hadolint -f sarif "$f" > "/tmp/h$n.sarif" 2> "/tmp/h$n.err"
  if [ -s "/tmp/h$n.sarif" ]; then cp "/tmp/h$n.sarif" ".standing/dockerfile-$n.sarif"; else echo "hadolint produced no report for $f:"; head -3 "/tmp/h$n.err"; bad=1; fi
done < /tmp/files
[ "$n" -gt 0 ] || echo "No Dockerfile found; nothing to check."
exit $bad
`

const scriptMisconfig = scriptPrelude + `trivy config --skip-dirs .standing --cache-dir .standing/cache/trivy --format sarif --output /tmp/r.sarif .
[ -s /tmp/r.sarif ] || { echo "trivy wrote no report, so this check did not run."; exit 1; }
cp /tmp/r.sarif .standing/misconfig.sarif
`

const scriptK8s = scriptPrelude + `/kube-linter lint . --format sarif > /tmp/r.sarif 2> /tmp/r.err; rc=$?
if [ ! -s /tmp/r.sarif ]; then
  if grep -qi "no valid\|no objects\|no yaml" /tmp/r.err 2>/dev/null; then echo "No Kubernetes objects found; nothing to check."; exit 0; fi
  echo "kube-linter wrote no report (exit $rc):"; head -3 /tmp/r.err; exit 1
fi
cp /tmp/r.sarif .standing/k8s-practice.sarif
`

const scriptShell = scriptPrelude + `find . -type f \( -name '*.sh' -o -name '*.bash' \) -not -path './.standing/*' -not -path '*/node_modules/*' -not -path './.git/*' | head -500 | tr '\n' '\0' > /tmp/files
[ -s /tmp/files ] || { echo "No shell script found; nothing to check."; exit 0; }
xargs -0 shellcheck -f json1 < /tmp/files > /tmp/r.json 2> /tmp/r.err
[ -s /tmp/r.json ] || { echo "shellcheck wrote no report:"; head -3 /tmp/r.err; exit 1; }
cp /tmp/r.json .standing/shell.json
`

const scriptPipelineLint = scriptPrelude + `find . -type f \( -path '*/.launchway/*.yaml' -o -path '*/.launchway/*.yml' \) -not -path './.standing/*' > /tmp/pf
find . -type f \( -name Dockerfile -o -name 'Dockerfile.*' -o -name '*.Dockerfile' -o -name Containerfile \) -not -path './.standing/*' -not -path '*/node_modules/*' > /tmp/df
cat /tmp/pf /tmp/df > /tmp/all
: > /tmp/hits
hit() { printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >> /tmp/hits; }
while IFS= read -r f; do
  grep -nE '(curl|wget)[^|;&]*\|[[:space:]]*(sudo[[:space:]]+)?(ba|z|da)?sh([[:space:]]|$)' "$f" 2>/dev/null | cut -d: -f1 | while read -r n; do
    hit unsafe-pipe-to-shell error "$f" "$n" "A download is piped straight into a shell: whatever the server sends is run, unchecked."
  done
done < /tmp/all
while IFS= read -r f; do
  awk '/^[ \t]*(- )?image:/ && !/@sha256:/ && !/[$]/ {print NR}' "$f" | while read -r n; do
    hit unpinned-image warning "$f" "$n" "An image is named by a tag, which can be moved, instead of by digest."
  done
  grep -niE '^[[:space:]]*[A-Za-z0-9_]*(password|secret|token|api_?key|private_?key)[A-Za-z0-9_]*:[[:space:]]*["'"'"']?[A-Za-z0-9/+=_.-]{8,}' "$f" 2>/dev/null | cut -d: -f1 | while read -r n; do
    hit secret-in-pipeline error "$f" "$n" "A value that looks like a secret is written in the file; keep it in Vault or a secret reference and refer to it by name."
  done
done < /tmp/pf
rules='{"id":"unsafe-pipe-to-shell","name":"unsafe-pipe-to-shell","shortDescription":{"text":"A download is piped into a shell"},"help":{"text":"Download to a file, check its checksum or signature, then run it. Or use an image that already contains the tool."}},{"id":"unpinned-image","name":"unpinned-image","shortDescription":{"text":"An image is not pinned by digest"},"help":{"text":"Write the image as name:tag@sha256:digest. The digest cannot be moved under you."}},{"id":"secret-in-pipeline","name":"secret-in-pipeline","shortDescription":{"text":"A secret is written in plain text"},"help":{"text":"Rotate the value, then keep it in Vault or a secret reference and use secrets: in the step."}}'
{
  printf '{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[{"tool":{"driver":{"name":"launchway-pipeline-lint","rules":[%s]}},"results":[' "$rules"
  awk -F'\t' 'BEGIN{first=1} {sub(/^\.\//,"",$3); gsub(/\\/,"\\\\",$3); gsub(/"/,"\\\"",$3); printf "%s{\"ruleId\":\"%s\",\"level\":\"%s\",\"message\":{\"text\":\"%s\"},\"locations\":[{\"physicalLocation\":{\"artifactLocation\":{\"uri\":\"%s\"},\"region\":{\"startLine\":%d}}}]}", (first?"":","), $1,$2,$5,$3,$4; first=0}' /tmp/hits
  printf ']}]}\n'
} > /tmp/r.sarif
cp /tmp/r.sarif .standing/pipeline-lint.sarif
`
