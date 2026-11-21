# Sourced by the other scripts of service-ci: goes to the working directory,
# and says where the Python environment is. Not run on its own.
#
# A template step gets every parameter as PARAM_<NAME>. The directory is a
# parameter, so a monorepo calls the template once for each directory.
ROOT=$PWD
WORKDIR=${PARAM_WORKING_DIRECTORY:-.}
case "$WORKDIR" in
  /*|*..*) echo "working_directory must be inside the repository: $WORKDIR" >&2; exit 2 ;;
esac
cd "$WORKDIR"
# The environment lives in the workspace, because the steps of a run may run in
# containers of their own and the workspace is what they share.
VENV="$PWD/.launchway-venv"
# The caches are at the root of the workspace, where the template says to keep them.
export PIP_CACHE_DIR="$ROOT/.cache/pip" npm_config_cache="$ROOT/.cache/npm"
