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
#   - a copy of the committed tree (git archive of HEAD), so a file that is uncommitted, or generated
#     and not committed, is missing exactly as it is in the pipeline;
#   - the module cache mounted read-only, the container's root filesystem read-only, memory capped.
# The copy is a throwaway, writable (the step writes reports/ in its workspace); your tree is untouched.
# The failure summary (scripts/ci/go-test-summary.sh) is part of the step, so failures read the same.
#
# Skips, saying so, when neither podman nor docker is installed. Exit status is the step's.
# CONTAINER=<tool> chooses the tool; TEST_LIKE_CI_MEMORY (default 8g) caps the container.
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
trap 'chmod -R u+rwX "$work" 2>/dev/null; rm -rf "$work"' EXIT

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

mkdir "$work/src" "$work/home" "$work/acct"
git archive HEAD | tar -x -C "$work/src"
chmod -R a+rwX "$work"
mods=$(go env GOMODCACHE 2>/dev/null || true)
[ -n "$mods" ] || mods=$HOME/go/pkg/mod
mem=${TEST_LIKE_CI_MEMORY:-8g}

pass=""
[ "$tool" = podman ] && pass="--passwd=false"
account=""
if [ "${TEST_LIKE_CI_USER_ENTRY:-1}" != 0 ]; then
	# The same script the runner's init container runs, in the same image and as the same user.
	# shellcheck disable=SC2086
	"$tool" run --rm --user 1000:1000 $pass --memory 256m --cap-drop ALL \
		-v "$work/acct":/acct:z -v "$PWD/internal/executor/user_entry.sh":/user_entry.sh:ro,z "$image" \
		sh /user_entry.sh /acct 1000 1000 launchway /work/home
	account="-v $work/acct/passwd:/etc/passwd:ro,z -v $work/acct/group:/etc/group:ro,z -e USER=launchway -e LOGNAME=launchway"
fi
echo "test-like-ci: $image, as uid 1000${account:+ (given an account entry the way a step pod does)}${account:- with no account entry}, HOME on a temp dir, HEAD $(git rev-parse --short HEAD)"
echo "test-like-ci: running the pipeline's test step command from $pipeline"
status=0
# shellcheck disable=SC2086
"$tool" run --rm --user 1000:1000 $pass --memory "$mem" --read-only --security-opt no-new-privileges --cap-drop ALL \
	-e HOME=/work/home -e GOCACHE=/work/home/gocache -e GOFLAGS=-buildvcs=false -e GIT_TERMINAL_PROMPT=0 \
	-v "$work/src":/work/src:z -v "$work/home":/work/home:z -v "$work/cmd.sh":/work/cmd.sh:ro,z \
	$account -v "$mods":/go/pkg/mod:ro,z -w /work/src "$image" sh -e /work/cmd.sh || status=$?
if [ $status -ne 0 ]; then
	echo "test-like-ci: FAILED (exit $status): the pipeline's test step would fail the same way"
else
	echo "test-like-ci: the pipeline's test step passes here"
fi
exit $status
