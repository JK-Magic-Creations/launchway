#!/bin/sh
# Runs the phases named in PHASES, in order: setup, check, test, build (script).
# check, test, build. A step that needs its own environment (a matrix
# member) names several, so that it installs and then works in one step.
set -e
here=$(cd "$(dirname "$0")" && pwd)
for phase in ${PHASES:?name the phases to run, for example: setup test}; do
  echo "== $phase"
  case "$phase" in
    build) LW_SCRIPT=build sh "$here/script.sh" ;;
    *)     sh "$here/$phase.sh" ;;
  esac
done
