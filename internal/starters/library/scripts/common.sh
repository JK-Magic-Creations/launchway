#!/bin/sh
# Shared by the starter scripts: source it, then call enter.
#
#   . "$(dirname "$0")/../common.sh"
#   enter
#
# Every script reads its settings from PARAM_* variables (a template's
# parameters), never from arguments, so a script can be run by hand the same
# way a step runs it: PARAM_WORKING_DIRECTORY=sub sh scripts/go/test.sh

# enter changes into the directory the code is in (PARAM_WORKING_DIRECTORY,
# default: the repository's root). It refuses paths that leave the workspace.
enter() {
  dir=${PARAM_WORKING_DIRECTORY:-.}
  case "$dir" in
    /*|..|../*|*/..|*/../*)
      echo "working_directory must be a directory inside the repository, not $dir" >&2
      exit 2 ;;
  esac
  [ -d "$dir" ] || { echo "working_directory $dir does not exist in this repository" >&2; exit 2; }
  # Package caches live in the workspace's .cache/, which the steps' cache:
  # keys save and restore between runs.
  LW_ROOT=$PWD
  export GOMODCACHE="$LW_ROOT/.cache/go-mod" GOCACHE="$LW_ROOT/.cache/go-build" GOFLAGS="${GOFLAGS:-} -modcacherw"
  cd "$dir"
}

# report_dir is where a step writes its reports. A step repeated by for_each
# (a matrix) gets a directory of its own per member, reports/<values>, so
# that members running side by side never overwrite each other's files.
report_dir() {
  vals=$(env | sed -n 's/^LAUNCHWAY_EACH_[A-Za-z0-9_]*=//p' | tr '\n' - | sed 's/-$//')
  if [ -n "$vals" ]; then echo "reports/$vals"; else echo reports; fi
}

# venv_dir is the Python environment of the step, one per matrix member.
venv_dir() {
  vals=$(env | sed -n 's/^LAUNCHWAY_EACH_[A-Za-z0-9_]*=//p' | tr '\n' - | sed 's/-$//')
  if [ -n "$vals" ]; then echo ".venv-$vals"; else echo .venv; fi
}

# say prints a line the log shows as what the starter decided.
say() { printf '%s\n' "launchway: $*"; }

# run prints a command and runs it through the shell, so that a command a
# person wrote as a parameter ("make test") works as it does at a prompt.
run() {
  echo "+ $*"
  eval "$*"
}

# no_report prints the notice a step gives when it ran the tests but no report came out of them, so that an empty
# Tests tab is explained where a person looks: the log of the step, which the Tests tab points to.
#   no_report WHAT WHY ADD
no_report() {
  echo "launchway: ============================================================"
  echo "launchway: TESTS RAN, BUT NO $1 WAS PRODUCED"
  echo "launchway: $2"
  echo "launchway: To get it, $3"
  echo "launchway: ============================================================"
}

# lw_tools is a directory in the workspace's cache where the starter installs the small tools it needs for a step
# (a test reporter, a coverage tool), at pinned versions, so that the repository need not list them.
lw_tools() { echo "$LW_ROOT/.cache/lw-tools"; }
