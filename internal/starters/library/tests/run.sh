#!/bin/sh
# Unit tests of the library's scripts. Plain sh, no framework: each case runs
# a script in a scratch directory with the PARAM_* variables the template
# would set, and checks what it did.
#
#   sh tests/run.sh            (from the library's directory, or anywhere)
#
# Exit status 0 when every case passes. Cases that need a tool the machine
# lacks say "skip" with the reason.
set -u
here=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/var/tmp}/starters-tests.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
pass=0; fail=0
ok()   { pass=$((pass + 1)); echo "ok   - $1"; }
bad()  { fail=$((fail + 1)); echo "FAIL - $1"; }
skip() { echo "skip - $1"; }
check() { name=$1; shift; if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name"; fi; }
refuse() { name=$1; shift; if "$@" >/dev/null 2>&1; then bad "$name"; else ok "$name"; fi; }
fresh() { rm -rf "$scratch/w"; mkdir -p "$scratch/w"; cd "$scratch/w" || exit 1; }

# --- common.sh: the working directory guard and the report directory
fresh
refuse "enter refuses a path that leaves the repository" sh -c "PARAM_WORKING_DIRECTORY=../x sh -c '. $here/scripts/common.sh; enter'"
refuse "enter refuses an absolute path" sh -c "PARAM_WORKING_DIRECTORY=/etc sh -c '. $here/scripts/common.sh; enter'"
refuse "enter refuses a directory that is not there" sh -c "PARAM_WORKING_DIRECTORY=nope sh -c '. $here/scripts/common.sh; enter'"
mkdir sub
check "enter goes into a subdirectory" sh -c "PARAM_WORKING_DIRECTORY=sub sh -c '. $here/scripts/common.sh; enter; [ \$(basename \$PWD) = sub ]'"
d=$(sh -c ". $here/scripts/common.sh; report_dir")
[ "$d" = reports ] && ok "report_dir is reports for a step that is not repeated" || bad "report_dir is $d"
d=$(LAUNCHWAY_EACH_GO=1.27 sh -c ". $here/scripts/common.sh; report_dir")
[ "$d" = reports/1.27 ] && ok "report_dir names the matrix member" || bad "report_dir for a member is $d"
d=$(LAUNCHWAY_EACH_GO=1.27 sh -c ". $here/scripts/common.sh; venv_dir")
[ "$d" = .venv-1.27 ] && ok "venv_dir names the matrix member" || bad "venv_dir for a member is $d"

# --- image/tag.sh: the tag is made from the commit, never the clock
fresh
export LAUNCHWAY_OUTPUTS="$scratch/out"; : > "$LAUNCHWAY_OUTPUTS"
LAUNCHWAY_COMMIT=0d990ef2fe0d625f19a509b190ed9471235c93a2 LAUNCHWAY_COMMIT_TIME=2026-11-20T06:12:56Z sh "$here/scripts/image/tag.sh" >/dev/null
check "tag.sh writes YYYYMMDDHHMMSS-sha8" grep -qx 'tag=20261120061256-0d990ef2' "$LAUNCHWAY_OUTPUTS"
refuse "tag.sh refuses a run without a commit" sh -c "env -u LAUNCHWAY_COMMIT LAUNCHWAY_COMMIT_TIME=x sh '$here/scripts/image/tag.sh'"

# --- secrets.sh
fresh
git init -q . 2>/dev/null
echo "hello" > a.txt; git add a.txt 2>/dev/null
check "secrets.sh passes a clean repository" sh "$here/scripts/secrets.sh"
printf '%s\n' "-----BEGIN RSA PRIVATE KEY-----" > key.pem; git add key.pem 2>/dev/null
refuse "secrets.sh finds a private key" sh "$here/scripts/secrets.sh"
out=$(sh "$here/scripts/secrets.sh" 2>&1 || true)
case "$out" in *"BEGIN RSA PRIVATE KEY-----"*) bad "secrets.sh prints the secret it found in full";; *) ok "secrets.sh does not print the whole line it found";; esac

# --- site/check.sh
fresh
refuse "site check needs an index.html" sh "$here/scripts/site/check.sh"
echo '<a href="gone.html">x</a><img src="logo.png">' > index.html
refuse "site check finds a link to a page that is not there" sh "$here/scripts/site/check.sh"
echo '<a href="about.html">x</a>' > index.html; echo '<p>about</p>' > about.html
check "site check passes a site whose links exist" sh "$here/scripts/site/check.sh"
mkdir -p out; echo '<p>ok</p>' > out/index.html
check "site check reads site_dir" sh -c "PARAM_SITE_DIR=out sh '$here/scripts/site/check.sh'"
check "site check runs the build command first" sh -c "PARAM_BUILD_COMMAND='mkdir -p built && echo ok > built/index.html' PARAM_SITE_DIR=built sh '$here/scripts/site/check.sh'"

# --- python/run.sh and node/run.sh need to be told the phases
refuse "python run.sh without PHASES says what is missing" sh "$here/scripts/python/run.sh"
refuse "node run.sh without PHASES says what is missing" sh "$here/scripts/node/run.sh"

# --- node scripts (need node)
if command -v node >/dev/null 2>&1; then
  fresh
  echo '{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}' > package.json
  out=$(sh "$here/scripts/node/test.sh" 2>&1) && case "$out" in *"no real test script"*) ok "node test.sh skips npm's default test script and says so";; *) bad "node test.sh output: $out";; esac || bad "node test.sh failed on the default script"
  echo '{"scripts":{"lint":"node -e 0"}}' > package.json
  check "node script.sh runs a script that exists" sh -c "LW_SCRIPT=lint sh '$here/scripts/node/script.sh'"
  check "node script.sh passes when an optional script is missing" sh -c "LW_SCRIPT=build sh '$here/scripts/node/script.sh'"
  refuse "node script.sh fails when a required script is missing" sh -c "PARAM_OPTIONAL=false LW_SCRIPT=build sh '$here/scripts/node/script.sh'"
  out=$(sh "$here/scripts/node/check.sh" 2>&1); case "$out" in *"lint"*) ok "node check.sh runs the lint script";; *) bad "node check.sh output: $out";; esac
  echo '{"scripts":{}}' > package.json
  out=$(sh "$here/scripts/node/check.sh" 2>&1); case "$out" in *"nothing was checked"*) ok "node check.sh says when there was nothing to check";; *) bad "node check.sh: $out";; esac
else
  skip "node scripts (node is not installed)"
fi

# --- go scripts (need go)
if command -v go >/dev/null 2>&1; then
  fresh
  printf 'module example.com/t\n\ngo 1.22\n' > go.mod
  printf 'package t\n\nfunc F() int { return 1 }\n' > t.go
  check "go build of a library is a compile check" sh "$here/scripts/go/build.sh"
  mkdir -p cmd/t && printf 'package main\n\nfunc main() {}\n' > cmd/t/main.go
  check "go build of a program writes bin/" sh -c "sh '$here/scripts/go/build.sh' && [ -x bin/t ]"
  printf 'package t\n\nfunc   G() int {return 2}\n' > g.go
  refuse "go check fails on an unformatted file when lint_mode is enforce" env PARAM_LINT_MODE=enforce sh "$here/scripts/go/check.sh"
  check "go check only advises on an unformatted file by default" sh "$here/scripts/go/check.sh"
  out=$(PARAM_LINT_MODE=enforce sh "$here/scripts/go/check.sh" 2>&1 || true); case "$out" in *"gofmt -w ."*) ok "go check says how to fix formatting";; *) bad "go check: $out";; esac
  gofmt -w g.go
  check "go check passes a formatted module" sh "$here/scripts/go/check.sh"
  printf 'package t\n\nimport "testing"\n\nfunc TestF(t *testing.T) { if F() != 1 { t.Fatal() } }\n' > t_test.go
  check "go test writes the JSON report and the coverage profile" sh -c "sh '$here/scripts/go/test.sh' && [ -s reports/go-test.json ] && [ -s reports/cover.out ]"
  check "go test of a matrix member writes to its own directory" sh -c "LAUNCHWAY_EACH_GO=1.27 sh '$here/scripts/go/test.sh' && [ -s reports/1.27/go-test.json ]"
  printf 'package t\n\nimport "testing"\n\nfunc TestBad(t *testing.T) { t.Fatal("no") }\n' > bad_test.go
  refuse "go test fails when a test fails" sh "$here/scripts/go/test.sh"
else
  skip "go scripts (go is not installed)"
fi

# --- helm
if command -v helm >/dev/null 2>&1; then
  fresh
  refuse "helm check says when there is no chart" sh "$here/scripts/helm/check.sh"
else
  skip "helm scripts (helm is not installed)"
fi

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
