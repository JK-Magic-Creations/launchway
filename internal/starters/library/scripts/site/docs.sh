#!/bin/sh
# Checks that the documentation folder exists and says what is in it.
set -e
. "$(dirname "$0")/../common.sh"
enter
dir=${PARAM_DOCS_DIR:-docs}
[ -d "$dir" ] || { echo "$dir does not exist: set docs_dir: to the folder of documents" >&2; exit 1; }
say "$dir: $(find "$dir" -type f | wc -l) files"
