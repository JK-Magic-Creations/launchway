#!/bin/sh
# A virtual environment (.venv) with the project's dependencies and the test tools. The package manager is read from the
# repository's own files:
#   uv.lock          -> uv sync --frozen
#   poetry.lock      -> poetry install
#   pdm.lock         -> pdm install --frozen-lockfile
#   anything else    -> pip (requirements*.txt, or the project in pyproject.toml)
# Whatever installs the project, the tools of the later steps (pytest, pytest-cov, ruff) go into that same environment and
# run from it, and pip is there to install them with even where the manager leaves it out (uv). What the manager installed
# is recorded before any tool is added, so that the dependency audit looks at the project's dependencies and nothing else.
set -e
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
mkdir -p .cache/pip .cache/uv
export PIP_CACHE_DIR="$PWD/.cache/pip" UV_CACHE_DIR="$PWD/.cache/uv" PIP_DISABLE_PIP_VERSION_CHECK=1
if [ -f uv.lock ]; then tool=uv
elif [ -f poetry.lock ]; then tool=poetry
elif [ -f pdm.lock ]; then tool=pdm
else tool=pip
fi
say "package manager: $tool ($(python3 --version 2>&1))"
if [ -n "${PARAM_INSTALL_COMMAND:-}" ]; then
  python3 -m venv "$(venv_dir)"
  . "$(venv_dir)/bin/activate"
  run "$PARAM_INSTALL_COMMAND"
else
  case "$tool" in
    uv)
      command -v uv >/dev/null 2>&1 || python3 -m pip install --quiet uv
      export UV_PROJECT_ENVIRONMENT="$PWD/$(venv_dir)"
      run uv sync --frozen
      . "$(venv_dir)/bin/activate" ;;
    poetry)
      command -v poetry >/dev/null 2>&1 || python3 -m pip install --quiet poetry
      # Poetry installs into the environment that is active.
      python3 -m venv "$(venv_dir)"
      . "$(venv_dir)/bin/activate"
      run poetry install --no-interaction ;;
    pdm)
      command -v pdm >/dev/null 2>&1 || python3 -m pip install --quiet pdm
      python3 -m venv "$(venv_dir)"
      . "$(venv_dir)/bin/activate"
      run pdm install --frozen-lockfile ;;
    pip)
      python3 -m venv "$(venv_dir)"
      . "$(venv_dir)/bin/activate"
      found=
      # A lock file written by pip-compile (requirements.lock) is what the project runs on: it is installed in place of
      # requirements.txt, which it was made from.
      for f in requirements.lock requirements.txt requirements-dev.txt requirements/*.txt; do
        [ "$f" = requirements.txt ] && [ -f requirements.lock ] && continue
        [ -f "$f" ] && { run python -m pip install --quiet -r "$f"; found=1; }
      done
      if [ -z "$found" ] && [ -f pyproject.toml ]; then
        run python -m pip install --quiet -e ".[dev,test]" 2>/dev/null || run python -m pip install --quiet -e .
      fi ;;
  esac
fi
record_deps
ensure_pip
py_need pytest pytest==9.2.0
py_need pytest_cov pytest-cov==7.1.0
command -v ruff >/dev/null 2>&1 || py_install ruff
