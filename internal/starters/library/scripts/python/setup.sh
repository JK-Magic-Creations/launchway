#!/bin/sh
# A virtual environment (.venv) with the project's dependencies and the test
# tools. The package manager is read from the repository's own files:
#   uv.lock          -> uv sync --frozen
#   poetry.lock      -> poetry install
#   anything else   -> pip (requirements*.txt, or the project in pyproject.toml)
set -e
. "$(dirname "$0")/../common.sh"
enter
mkdir -p .cache/pip .cache/uv
export PIP_CACHE_DIR="$PWD/.cache/pip" UV_CACHE_DIR="$PWD/.cache/uv" PIP_DISABLE_PIP_VERSION_CHECK=1
if [ -f uv.lock ]; then tool=uv
elif [ -f poetry.lock ]; then tool=poetry
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
      run uv sync --frozen
      . "$(venv_dir)/bin/activate" ;;
    poetry)
      command -v poetry >/dev/null 2>&1 || python3 -m pip install --quiet poetry
      POETRY_VIRTUALENVS_IN_PROJECT=true run poetry install --no-interaction
      . "$(venv_dir)/bin/activate" ;;
    pip)
      python3 -m venv "$(venv_dir)"
      . "$(venv_dir)/bin/activate"
      found=
      for f in requirements.txt requirements-dev.txt requirements/*.txt; do
        [ -f "$f" ] && { run pip install --quiet -r "$f"; found=1; }
      done
      if [ -z "$found" ] && [ -f pyproject.toml ]; then
        run pip install --quiet -e ".[dev,test]" 2>/dev/null || run pip install --quiet -e .
      fi ;;
  esac
fi
for pkg in pytest pytest-cov ruff; do
  python -c "import $(echo $pkg | tr - _)" 2>/dev/null || run pip install --quiet "$pkg"
done
