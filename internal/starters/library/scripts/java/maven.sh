#!/bin/sh
# mvn verify: compile, test, package. Uses the repository's ./mvnw when it
# has one. Test results are Surefire's and Failsafe's XML; coverage is
# JaCoCo's if the build configures it.
set -e
. "$(dirname "$0")/../common.sh"
enter
[ -f pom.xml ] || { echo "no pom.xml in ${PARAM_WORKING_DIRECTORY:-.}" >&2; exit 1; }
mvn=mvn
[ -x ./mvnw ] && mvn=./mvnw
mkdir -p .cache/m2
cmd=${PARAM_BUILD_COMMAND:-}
[ -n "$cmd" ] || cmd="$mvn -B -ntp -Dmaven.repo.local=.cache/m2 ${LW_GOAL:-verify}"
run "$cmd"
