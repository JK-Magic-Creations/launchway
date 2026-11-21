#!/bin/sh
# make test-like-ci: runs the pipeline's own `test` step on this machine, the way the pipeline runs it,
# so "passes on my machine" cannot hide an environment difference. Three pushes in a row once failed
# only in the pipeline: an older git in the build image, a stale generated file (only part of the suite
# had been run), and a user with no account entry.
#
# The image and the command are read from .launchway/pipeline.yaml (step `test`), so they cannot drift
# from what the pipeline runs. The container differs from a normal local run on purpose:
#   - numeric user 1000:1000 with no /etc/passwd entry (podman: --passwd=false), HOME on a temp dir; then, as the
#     runner's step pods do (kubernetes.user_entry, internal/executor/user_entry.sh), the step user is given an account
#     entry in a copy of /etc/passwd and /etc/group mounted read-only (TEST_LIKE_CI_USER_ENTRY=0 leaves it out);
#   - a real Git CHECKOUT of HEAD (a clone, so .git is there and Go's build-time version stamping reads it), owned
#     by another user than the step's (under podman: a subuid, as the pod's workspace is), so a file that is
#     uncommitted or generated and not committed is missing, and "error obtaining VCS status" or "dubious
#     ownership" problems appear here and not only on the cluster. GOFLAGS is NOT set to -buildvcs=false: the
#     pod does not set it either;
#   - the step's CPU request as the CPU cap (--cpus, and GOMAXPROCS the same: the pod's cgroup is wider, so
#     this is the tighter of the two) and the step's memory_limit as the memory cap, both read from the pipeline
#     file; HOME, GOCACHE and the other variables as the executor sets them (internal/executor/kubernetes.go);
#   - the module cache mounted read-only, the container's root filesystem read-only.
# The checkout is a throwaway, writable (the step writes reports/ in its workspace); your tree is untouched.
# The failure summary (scripts/ci/go-test-summary.sh) is part of the step, so failures read the same.
#
# Skips, saying so, when neither podman nor docker is installed. Exit status is the step's.
# CONTAINER=<tool> chooses the tool; TEST_LIKE_CI_MEMORY and TEST_LIKE_CI_CPUS override the pipeline file's values.
set -eu
cd "$(dirname "$0")/../.."
pipeline=.launchway/pipeline.yaml
tool=${CONTAINER:-}
if [ -z "$tool" ]; then
	for t in podman docker; do command -v "$t" >/dev/null 2>&1 && { tool=$t; break; }; done
fi
if [ -z "$tool" ] || ! command -v "$tool" >/dev/null 2>&1; then
	echo "test-like-ci: skipped: neither podman nor docker is installed (set CONTAINER=<tool> to use another)"
	exit 0
fi

work=$(mktemp -d)
# Files the container wrote belong to a mapped user under rootless podman: remove them from inside
# that mapping when plain removal cannot, and never let cleanup change the exit status.
cleanup() {
	st=$?
	chmod -R u+rwX "$work" 2>/dev/null || true
	rm -rf "$work" 2>/dev/null || { [ "$tool" = podman ] && podman unshare rm -rf "$work" 2>/dev/null; } || true
	exit "$st"
}
trap cleanup EXIT

# The test step's image and run block, from the pipeline file (a two-space step key, its fields at six).
awk -v out="$work/cmd.sh" -v imgout="$work/image" '
	/^  [A-Za-z0-9_-]+:/ { instep = ($1 == "test:"); inrun = 0; next }
	!instep { next }
	inrun && (/^      / || /^[ \t]*$/) { sub(/^      /, ""); print > out; next }
	{ inrun = 0 }
	/^    image:/ { print $2 > imgout }
	/^    run: *\|/ { inrun = 1 }
' "$pipeline"
if [ ! -s "$work/image" ] || [ ! -s "$work/cmd.sh" ]; then
	echo "test-like-ci: could not read the image and run block of step 'test' from $pipeline" >&2
	exit 2
fi
image=$(cat "$work/image")

# The step's resources, from its `resources: { cpu: 2, memory_limit: 8Gi, ... }` line.
res=$(awk '/^  [A-Za-z0-9_-]+:/ { instep = ($1 == "test:"); next } instep && /^    resources:/ { print; exit }' "$pipeline")
field() { printf '%s' "$res" | sed -n "s/.*[{ ,]$1: *\([^ ,}]*\).*/\1/p"; }
memlim=$(field memory_limit)
[ -n "$memlim" ] || memlim=$(field memory)
cpus=$(field cpu)
# Kubernetes quantities to the container tool's: 8Gi -> 8g, 512Mi -> 512m.
mem=${TEST_LIKE_CI_MEMORY:-$(printf '%s' "${memlim:-8Gi}" | sed 's/Gi$/g/; s/Mi$/m/')}
cpus=${TEST_LIKE_CI_CPUS:-${cpus:-2}}

mkdir "$work/src" "$work/home" "$work/acct"
git clone -q --no-hardlinks "$PWD" "$work/src"
git -C "$work/src" checkout -q --detach "$(git rev-parse HEAD)"
chmod -R a+rwX "$work"
differs=no
if [ "$tool" = podman ] && podman unshare chown -R 2000:2000 "$work/src" 2>/dev/null; then
	differs=yes
elif [ "$tool" != podman ]; then
	differs="no (only podman can give the checkout another owner)"
fi
mods=$(go env GOMODCACHE 2>/dev/null || true)
[ -n "$mods" ] || mods=$HOME/go/pkg/mod

pass=""
[ "$tool" = podman ] && pass="--passwd=false"
account=""
if [ "${TEST_LIKE_CI_USER_ENTRY:-1}" != 0 ]; then
	# The same script the runner's init container runs, in the same image and as the same user.
	# shellcheck disable=SC2086
	"$tool" run --rm --user 1000:1000 $pass --memory 256m --cap-drop ALL \
		-v "$work/acct":/acct:z -v "$PWD/internal/executor/user_entry.sh":/user_entry.sh:ro,z "$image" \
		sh /user_entry.sh /acct 1000 1000 launchway /home/launchway
	account="-v $work/acct/passwd:/etc/passwd:ro,z -v $work/acct/group:/etc/group:ro,z -e USER=launchway -e LOGNAME=launchway"
fi
echo "test-like-ci: $image, as uid 1000${account:+ (given an account entry the way a step pod does)}${account:- with no account entry}, HOME=/home/launchway, HEAD $(git rev-parse --short HEAD)"
echo "test-like-ci: cpus $cpus (GOMAXPROCS $cpus), memory limit $mem, checkout owned by another user: $differs"
echo "test-like-ci: running the pipeline's test step command from $pipeline"
status=0
# shellcheck disable=SC2086
"$tool" run --rm --user 1000:1000 $pass --memory "$mem" --cpus "$cpus" --read-only --security-opt no-new-privileges --cap-drop ALL \
	-e HOME=/home/launchway -e GOMODCACHE=/go/pkg/mod -e GOCACHE=/home/launchway/.cache/go-build -e PIP_CACHE_DIR=/home/launchway/.cache/pip -e npm_config_cache=/home/launchway/.npm \
	-e GOMAXPROCS="$cpus" -e GIT_TERMINAL_PROMPT=0 \
	-v "$work/src":/work/src:z -v "$work/home":/home/launchway:z -v "$work/cmd.sh":/work/cmd.sh:ro,z \
	$account -v "$mods":/go/pkg/mod:ro,z -w /work/src "$image" sh -e /work/cmd.sh || status=$?
echo "test-like-ci: conditions applied: cpu cap $cpus, memory cap $mem, real Git checkout (VCS stamping on) owned by another user: $differs, account entry: $([ -n "$account" ] && echo yes || echo no), step HOME/GOCACHE as the executor sets them"
if [ $status -ne 0 ]; then
	[ $status -ne 137 ] || echo "test-like-ci: exit 137: the step was killed, most likely for memory (cap $mem)"
	echo "test-like-ci: FAILED (exit $status): the pipeline's test step would fail the same way"
else
	echo "test-like-ci: the pipeline's test step passes here"
fi
exit $status
