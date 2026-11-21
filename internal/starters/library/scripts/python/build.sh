#!/bin/sh
# A wheel and a source distribution in dist/, for a library.
set -e
. "$(dirname "$0")/../common.sh"
enter
. "$(venv_dir)/bin/activate"
python -c "import build" 2>/dev/null || run pip install --quiet build
run python -m build --outdir dist
