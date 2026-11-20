#!/bin/sh
# Regenerates the screenshot tour in docs/images/tour/ and the README's
# picture, docs/images/readme-hero.png, from a real running server with demo
# data (see docs/demo.md).
#
#   scripts/docs-screenshots.sh [--no-build] [--keep]
#
# It builds Launchway (make all), makes demo repositories and a demo
# configuration in a temporary directory (scripts/docs/seed-demo.sh), starts
# a server and a runner there on 127.0.0.1:$LAUNCHWAY_DOCS_PORT (default
# 18160), drives the API until there are runs of every kind, takes the
# screenshots with Playwright in light and dark mode, makes them smaller, and
# stops the server and the runner. Nothing is left running, and the temporary
# directory is removed (--keep keeps it, to look at the server's log).
#
# Needs: Go, Node and npm (as make all does), Git, Python 3 with Pillow,
# Podman or Docker with docker.io/library/alpine:3.22 and redis:7-alpine (or
# the network to pull them), and a Chromium for Playwright: the newest in
# ~/.cache/ms-playwright, or $LAUNCHWAY_DOCS_CHROME. Playwright itself is
# installed into the temporary directory from npm. With ffmpeg on the PATH it
# also makes docs/public/images/tour/start-a-run.webm (served as a file on the site).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
port=${LAUNCHWAY_DOCS_PORT:-18160}
build=1
keep=0
for a in "$@"; do
  case $a in
    --no-build) build=0 ;;
    --keep) keep=1 ;;
    *) echo "usage: $0 [--no-build] [--keep]" >&2; exit 2 ;;
  esac
done

url=http://127.0.0.1:$port
if curl -fsS -o /dev/null "$url/healthz" 2>/dev/null; then
  echo "Something already answers on $url; set LAUNCHWAY_DOCS_PORT to a free port." >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/launchway-docs.XXXXXX")
server_pid=
runner_pid=
cleanup() {
  status=$?
  for pid in $runner_pid $server_pid; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      i=0
      while kill -0 "$pid" 2>/dev/null && [ $i -lt 100 ]; do sleep 0.1; i=$((i + 1)); done
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
  if [ "$status" != 0 ]; then
    echo "Failed. The server's and runner's logs:" >&2
    tail -n 30 "$work/server.log" "$work/runner.log" 2>/dev/null >&2 || true
  fi
  if [ "$keep" = 1 ] || [ "$status" != 0 ]; then
    echo "Kept $work"
  else
    # Workspaces the runner made in containers may be owned by another user
    # of a rootless runtime's namespace; podman unshare can remove those.
    rm -rf "$work" 2>/dev/null || podman unshare rm -rf "$work" 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if [ "$build" = 1 ]; then
  echo "==> Building Launchway"
  make -C "$root" all
fi
bin=$root/bin/launchway
[ -x "$bin" ] || { echo "$bin is missing: run make all, or drop --no-build" >&2; exit 1; }

echo "==> Making the demo repositories in $work"
"$root/scripts/docs/seed-demo.sh" repos "$work/demo"

echo "==> Starting the server and a runner on $url"
"$bin" server --config "$work/demo/launchway.yaml" --listen "127.0.0.1:$port" >"$work/server.log" 2>&1 &
server_pid=$!
i=0
until curl -fsS -o /dev/null "$url/readyz" 2>/dev/null; do
  i=$((i + 1))
  if [ $i -gt 120 ] || ! kill -0 "$server_pid" 2>/dev/null; then echo "the server did not start" >&2; exit 1; fi
  sleep 0.5
done
LAUNCHWAY_RUNNER_TOKEN=$(cat "$work/demo/data/runner-token") "$bin" runner --server "$url" \
  --name build-1 --data "$work/runner" --concurrency 4 >"$work/runner.log" 2>&1 &
runner_pid=$!

echo "==> Seeding runs"
"$root/scripts/docs/seed-demo.sh" runs "$url" "$work/demo"

echo "==> Installing Playwright"
# shellcheck disable=SC2012  # sorted by version below, which find cannot do
chrome=${LAUNCHWAY_DOCS_CHROME:-$(ls -d "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | sort -V | tail -n 1)}
npm install --prefix "$work/pw" --no-audit --no-fund --prefer-offline --loglevel=error playwright@1.65.1 >/dev/null

echo "==> Taking the screenshots"
out=$work/shots
mkdir -p "$out"
LAUNCHWAY_DOCS_URL=$url LAUNCHWAY_DOCS_OUT=$out LAUNCHWAY_DOCS_CHROME=$chrome LAUNCHWAY_DOCS_PW=$work/pw \
  LAUNCHWAY_DOCS_RUNS=$work/demo/runs.env node "$root/scripts/docs/screenshots.mjs"

echo "==> Making them smaller"
mkdir -p "$root/docs/images/tour"
python3 "$root/scripts/docs/shrink.py" "$out" "$root/docs/images/tour"
mv "$root/docs/images/tour/readme-hero-light.png" "$root/docs/images/readme-hero.png"
if command -v ffmpeg >/dev/null 2>&1 && [ -d "$out/frames" ]; then
  mkdir -p "$root/docs/public/images/tour"
  echo "==> Making the animated tour"
  ffmpeg -loglevel error -y -framerate 2 -i "$out/frames/%03d.png" \
    -vf "scale=1080:-2" -c:v libvpx-vp9 -b:v 0 -crf 40 -pix_fmt yuv420p -an \
    "$root/docs/public/images/tour/start-a-run.webm"
fi
ls -l "$root/docs/images/tour" "$root/docs/images/readme-hero.png"
echo "Done."
