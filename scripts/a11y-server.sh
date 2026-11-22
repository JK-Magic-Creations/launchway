#!/bin/sh
# Starts a throwaway Launchway with demo data for the browser accessibility scans and the keyboard tests, runs a
# command against it, and stops it again. Nothing is left behind.
#
#   scripts/a11y-server.sh COMMAND [ARGS...]
#
# COMMAND runs with LAUNCHWAY_A11Y_URL set to the server's address (a free port on 127.0.0.1), and
# LAUNCHWAY_A11Y_RUNS naming the runs it made (RUN_OK, RUN_FAILED, RUN_WAITING: ids, one per line, as KEY=value).
# Needs the built binary (make all) and Git. Steps run on the host (executor: host); nothing is pulled.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
bin=$root/bin/launchway
[ -x "$bin" ] || { echo "$bin is missing: run make all first" >&2; exit 1; }
work=$(mktemp -d "${TMPDIR:-/tmp}/launchway-a11y.XXXXXX")
server_pid=
cleanup() {
  status=$?
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill "$server_pid" 2>/dev/null || true
    i=0
    while kill -0 "$server_pid" 2>/dev/null && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
    kill -9 "$server_pid" 2>/dev/null || true
  fi
  [ "$status" = 0 ] || tail -n 40 "$work/server.log" 2>/dev/null >&2 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

git_repo() { # name, pipeline text
  d=$work/repos/$1
  mkdir -p "$d/.launchway"
  printf '%s\n' "$2" > "$d/.launchway/pipeline.yaml"
  (cd "$d" && git init -q -b main && git add -A && git -c user.name="Sam Rivera" -c user.email="sam@example.com" commit -q -m "$3")
}
git_repo shop-api 'pipeline: shop-api
on: { push: { branches: [main] } }
steps:
  build:
    description: Compile the service
    image: docker.io/library/alpine:3.22
    run: echo building
  test:
    description: Run the tests
    image: docker.io/library/alpine:3.22
    needs: build
    run: |
      echo "FAIL TestPriceRounding: want 10.01, got 10.00"
      exit 1' "Round prices per line item"
git_repo shop-web 'pipeline: shop-web
on: { push: { branches: [main] } }
steps:
  build:
    image: docker.io/library/alpine:3.22
    run: echo building
  approve:
    kind: approval
    message: Release shop-web to production
    needs: build' "Release the new checkout"
git_repo shop-docs 'pipeline: shop-docs
on: { push: { branches: [main] } }
steps:
  check:
    image: docker.io/library/alpine:3.22
    run: echo ok' "Fix a typo"

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
cat > "$work/launchway.yaml" <<YAML
listen: 127.0.0.1:$port
data: data
user: sam.rivera
egress: { allow_local: true }
forge: { hosting: true, allow_insecure_http: true }
projects:
  - { name: shop-api, repo: repos/shop-api, branch: main, poll: 5s }
  - { name: shop-web, repo: repos/shop-web, branch: main, poll: 5s }
  - { name: shop-docs, repo: repos/shop-docs, branch: main, poll: 5s }
runner: { concurrency: 3, executor: host }
YAML
url=http://127.0.0.1:$port
"$bin" server --config "$work/launchway.yaml" --listen "127.0.0.1:$port" >"$work/server.log" 2>&1 &
server_pid=$!
i=0
until curl -fsS -o /dev/null "$url/readyz" 2>/dev/null; do
  i=$((i + 1))
  if [ $i -gt 120 ] || ! kill -0 "$server_pid" 2>/dev/null; then echo "the server did not start" >&2; exit 1; fi
  sleep 0.5
done

# A hosted repository to read, with a few commits, a branch and a README (the pages of the code browser are scanned too).
curl -fsS -X POST -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' -d '{"home":"demo","name":"shop"}' "$url/api/v1/repositories" >/dev/null
hosted=$work/hosted
mkdir -p "$hosted/internal/price" && (
  cd "$hosted" && git init -q -b main && printf '# Shop\n\nSee the [guide](docs/guide.md).\n' > README.md
  printf 'package price\n\n// Round rounds a price to cents.\nfunc Round(v float64) float64 { return float64(int(v*100+0.5)) / 100 }\n' > internal/price/price.go
  git add -A && git -c user.name="Sam Rivera" -c user.email="sam@example.com" commit -q -m "Start the shop"
  printf '// Total sums lines.\nfunc Total(l []float64) float64 { return 0 }\n' >> internal/price/price.go
  git -c user.name="Priya Nair" -c user.email="priya@example.com" commit -q -am "Add Total, rounding each line first"
  git push -q "$url/git/launchway/shop.git" main 2>/dev/null; git checkout -q -b fix && printf '// fix\n' >> README.md && git -c user.name="Sam Rivera" -c user.email="sam@example.com" commit -q -am "Fix the readme" && git push -q "$url/git/launchway/shop.git" fix 2>/dev/null
) || true

start() { curl -fsS -X POST -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' -d '{"ref":"main"}' "$url/api/v1/projects/$1/runs" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'; }
ok=$(start shop-docs); failed=$(start shop-api); waiting=$(start shop-web)
printf 'RUN_OK=%s\nRUN_FAILED=%s\nRUN_WAITING=%s\n' "$ok" "$failed" "$waiting" > "$work/runs.env"
# Wait until the failed run has failed and the other is waiting for a person.
i=0
until [ "$(curl -fsS "$url/api/v1/approvals" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')" -ge 1 ] \
   && [ "$(curl -fsS "$url/api/v1/runs/$failed" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')" = failed ]; do
  i=$((i + 1)); [ $i -gt 120 ] && { echo "the demo runs did not reach their states" >&2
    for r in $ok $failed $waiting; do curl -fsS "$url/api/v1/runs/$r" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["project"], d["status"], [(s["id"], s["status"], s.get("waiting_for","")) for s in d.get("steps", [])], file=sys.stderr)'; done
    exit 1; }
  sleep 0.5
done
echo "Server at $url (runs: $ok $failed $waiting)" >&2
LAUNCHWAY_A11Y_URL=$url LAUNCHWAY_A11Y_RUNS=$work/runs.env "$@"
