#!/bin/sh
# gradle build: compile, test, package. Uses the repository's ./gradlew when
# it has one.
set -e
. "$(dirname "$0")/../common.sh"
enter
{ [ -f build.gradle ] || [ -f build.gradle.kts ]; } || { echo "no build.gradle in ${PARAM_WORKING_DIRECTORY:-.}" >&2; exit 1; }
g=gradle
[ -x ./gradlew ] && g=./gradlew
export GRADLE_USER_HOME="$PWD/.cache/gradle"
mkdir -p "$GRADLE_USER_HOME"
cmd=${PARAM_BUILD_COMMAND:-}
[ -n "$cmd" ] || cmd="$g --no-daemon --console=plain ${LW_GOAL:-build}"
run "$cmd"
