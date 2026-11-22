#!/bin/sh
# A wheel and a source distribution in dist/, for a library.
set -e
. "$(dirname "$0")/../common.sh"
. "$(dirname "$0")/lib.sh"
enter
. "$(venv_dir)/bin/activate"
py_need build build
run python -m build --outdir dist
