#!/bin/sh
# Starts a Launchway in WORKDIR (a fresh directory, under your home, not /tmp), seeds a large believable installation into it
# (see seed-organize.py) and leaves it running: the address and PID are printed. Stop it with `kill PID`.
#   scripts/ui-fixtures/seed-organize.sh WORKDIR
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
work=${1:?usage: seed-organize.sh WORKDIR}
bin=$root/bin/launchway
[ -x "$bin" ] || { echo "$bin is missing: run make all first" >&2; exit 1; }
[ ! -e "$work/data" ] || { echo "$work already has data: use a fresh directory" >&2; exit 1; }
mkdir -p "$work/repos/demo/.launchway"
cat > "$work/repos/demo/.launchway/pipeline.yaml" <<'YAML'
pipeline: demo
on: { push: { branches: [main] } }
steps:
  build:
    image: docker.io/library/alpine:3.22
    run: echo building
YAML
(cd "$work/repos/demo" && git init -q -b main && git add -A && git -c user.name="Sam Rivera" -c user.email="sam@example.com" commit -q -m "Demo pipeline")
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
cat > "$work/launchway.yaml" <<YAML
listen: 127.0.0.1:$port
data: data
user: sam.rivera
egress: { allow_local: true }
forge: { hosting: true, allow_insecure_http: true }
runner: { concurrency: 0, executor: host }
YAML
cd "$work"
"$bin" server --config "$work/launchway.yaml" --listen "127.0.0.1:$port" >"$work/server.log" 2>&1 &
pid=$!
i=0
until curl -fsS -o /dev/null "http://127.0.0.1:$port/readyz" 2>/dev/null; do
  i=$((i + 1)); { [ $i -gt 120 ] || ! kill -0 "$pid" 2>/dev/null; } && { echo "the server did not start" >&2; tail "$work/server.log" >&2; exit 1; }
  sleep 0.5
done
db=$(find "$work/data" -name '*.db' | head -1)
python3 "$root/scripts/ui-fixtures/seed-organize.py" "http://127.0.0.1:$port" "$(dirname "$db")"
echo "http://127.0.0.1:$port  pid $pid  (kill $pid to stop)"
