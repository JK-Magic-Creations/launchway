#!/bin/sh
# Starts a Launchway in WORKDIR (a fresh directory, under your home, not /tmp) with 5,000 projects and 200,000 Sorties
# written into its database, then times the HTTP calls behind Home, the finder and the explorers. Stops the server again.
#   scripts/ui-fixtures/measure-scale.sh WORKDIR
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
work=${1:?usage: measure-scale.sh WORKDIR}
bin=$root/bin/launchway
[ -x "$bin" ] || { echo "$bin is missing: run make all first" >&2; exit 1; }
[ ! -e "$work/data" ] || { echo "$work already has data: use a fresh directory" >&2; exit 1; }
mkdir -p "$work/repos/demo/.launchway"
printf 'pipeline: demo\non: { push: { branches: [main] } }\nsteps:\n  build:\n    image: docker.io/library/alpine:3.22\n    run: echo building\n' > "$work/repos/demo/.launchway/pipeline.yaml"
(cd "$work/repos/demo" && git init -q -b main && git add -A && git -c user.name="Sam Rivera" -c user.email="sam@example.com" commit -q -m "Demo pipeline")
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
cat > "$work/launchway.yaml" <<YAML
listen: 127.0.0.1:$port
data: data
user: sam.rivera
egress: { allow_local: true }
runner: { concurrency: 0, executor: host }
YAML
cd "$work"
"$bin" server --config "$work/launchway.yaml" --listen "127.0.0.1:$port" >"$work/server.log" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null || true' EXIT
i=0; until curl -fsS -o /dev/null "http://127.0.0.1:$port/readyz" 2>/dev/null; do i=$((i+1)); [ $i -gt 120 ] && { echo "no start" >&2; exit 1; }; sleep 0.5; done
db=$(dirname "$(find "$work/data" -name '*.db' | head -1)")
python3 - "$port" "$db" <<'PY'
import json, random, sqlite3, sys, time, urllib.request
port, data = sys.argv[1], sys.argv[2]
base = f"http://127.0.0.1:{port}"
random.seed(3)
N, PER = 5000, 40
def call(method, path, body=None):
    req = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None, headers={"Content-Type": "application/json", "X-Launchway-CSRF": "1"})
    with urllib.request.urlopen(req, timeout=900) as r:
        t = r.read().decode(); return json.loads(t) if t else None
units = [f"squad{u:02d}" for u in range(40)]
lines = ["objects:"]
for u in units: lines.append(f"  - {{kind: unit, name: {u}, spec: {{display_name: {u}}}}}")
for i in range(N): lines.append(f"  - {{kind: project, name: svc-{i:04d}, unit: {units[i % 40]}, spec: {{repo: repos/demo, branch: main, poll: 24h}}}}")
t0 = time.time(); call("POST", "/api/v1/config/import", {"text": "\n".join(lines) + "\n", "apply": True}); print(f"imported {N} projects in {time.time()-t0:.0f}s")
time.sleep(5)
now = int(time.time() * 1000)
db = sqlite3.connect(f"{data}/launchway.db", timeout=120); db.execute("PRAGMA busy_timeout=120000")
rows = []; n = 0
for p in range(N):
    for i in range(1, PER + 1):
        n += 1
        st = "failed" if (i == PER and p % 50 == 0) else "running" if (i == PER and p % 97 == 0) else "failed" if i % 9 == 0 else "succeeded"
        br = "main" if i % 3 else f"feat-{i % 7}"
        at = now - (N * PER - n) * 2000
        rows.append((f"run_{n}", f"svc-{p:04d}", i, "ci", st, "push", "refs/heads/" + br, br, f"{n*7919:08x}", "ana", "msg", at, at, at + 300000, 0))
db.executemany("INSERT INTO runs(id,project,number,pipeline,status,event,ref,branch,sha,actor,message,created_at,started_at,finished_at,last_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", rows); db.commit()
print(f"wrote {n} Sorties")
def timed(path, k=8):
    ts = []
    for _ in range(k):
        t = time.time(); call("GET", path); ts.append((time.time() - t) * 1000)
    ts.sort(); return f"first {ts[0]:.0f} ms, median {ts[len(ts)//2]:.0f} ms, slowest {ts[-1]:.0f} ms"
# The home call is cached for 3 seconds: the first of a burst is the cost; vary the view to measure uncached calls.
cold = []
for k in range(6):
    t = time.time(); call("GET", "/api/v1/home?view=" + ("mine" if k % 2 else "all")); cold.append((time.time() - t) * 1000); time.sleep(3.2)
cold.sort(); print(f"GET /api/v1/home (uncached, {N} projects, {n} Sorties): median {cold[len(cold)//2]:.0f} ms, slowest {cold[-1]:.0f} ms")
print("GET /api/v1/home (cached):", timed("/api/v1/home"))
print("GET /api/v1/attention:", timed("/api/v1/attention"))
print("GET /api/v1/search?q=svc-04:", timed("/api/v1/search?q=svc-04"))
print("GET /api/v1/search?q=%234000:", timed("/api/v1/search?q=%234000"))
print("GET /api/v1/organize/pipelines:", timed("/api/v1/organize/pipelines"))
print("GET /api/v1/projects (the Explorer's list):", timed("/api/v1/projects", 3))
PY
