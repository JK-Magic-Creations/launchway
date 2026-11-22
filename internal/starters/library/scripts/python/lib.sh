#!/bin/sh
# Shared by the Python scripts: source it after common.sh and after the project's environment is active.
#
# The project's environment is whatever the package manager made: a venv with pip (pip, Poetry, PDM), or the one uv makes,
# which has no pip at all. Every tool a step needs is installed into that environment and run from it, so that
# "python -m pytest --cov" and the tool it needs always see the same packages.

# Where these scripts are, whatever directory a step has changed to by the time it uses them.
LW_PYDIR=$(cd "$(dirname "$0")" && pwd)

# ensure_pip makes "python -m pip" work in the active environment. uv leaves pip out of the environment it makes; pip
# that ships with Python is put in, with ensurepip, or with uv when Python has no ensurepip (some distributions).
ensure_pip() {
  python -m pip --version >/dev/null 2>&1 && return 0
  say "the environment ${VIRTUAL_ENV:-in use} has no pip (uv leaves it out), so the tools are installed with the pip that ships with Python"
  python -m ensurepip --upgrade --default-pip >/dev/null 2>&1 && return 0
  if command -v uv >/dev/null 2>&1 && uv pip install --quiet --python "$(command -v python)" pip; then return 0; fi
  echo "launchway: could not make pip available in ${VIRTUAL_ENV:-the active environment}; install it in the image, or give install_command" >&2
  return 1
}

# py_install installs packages into the active environment.
py_install() {
  ensure_pip || return 1
  echo "+ python -m pip install --quiet $*"
  python -m pip install --quiet "$@"
}

# py_need MODULE PACKAGE... installs the packages unless the module imports.
py_need() {
  mod=$1; shift
  python -c "import $mod" 2>/dev/null || py_install "$@"
}

# deps_file is where the project's own dependencies are recorded after they were installed, before any tool.
deps_file() { echo "$(lw_tools)/project-deps$(venv_dir | sed 's/^\.venv//').txt"; }

# record_deps writes what the project's package manager installed, as NAME==VERSION lines, to deps_file: the project's
# dependencies, without the pip, setuptools and wheel that the image or the environment seeded (unless the project's own
# lock or requirements name them), without the project itself and anything installed from a directory or a repository.
record_deps() {
  mkdir -p "$(lw_tools)"
  files=
  for f in uv.lock poetry.lock pdm.lock requirements.lock requirements.txt requirements-dev.txt requirements/*.txt pyproject.toml; do
    [ -f "$f" ] && files="$files $f"
  done
  # shellcheck disable=SC2086
  python "$LW_PYDIR/project_deps.py" "$(deps_file)" $files
}
