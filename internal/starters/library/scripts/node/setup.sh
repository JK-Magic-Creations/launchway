#!/bin/sh
# Installs dependencies from the lock file, with the package manager the
# repository uses: pnpm-lock.yaml -> pnpm, yarn.lock -> yarn, else npm.
set -e
. "$(dirname "$0")/../common.sh"
enter
[ -f package.json ] || { echo "no package.json in ${PARAM_WORKING_DIRECTORY:-.}" >&2; exit 1; }
mkdir -p .cache/npm .cache/pnpm .cache/yarn
if [ -f pnpm-lock.yaml ]; then pm=pnpm
elif [ -f yarn.lock ]; then pm=yarn
else pm=npm
fi
want=
for f in .nvmrc .node-version; do [ -f "$f" ] && want=$(tr -d 'v \n' < "$f") && break; done
have=$(node --version | tr -d v)
say "package manager: $pm; node $have${want:+ (the repository asks for $want)}"
if [ -n "$want" ] && [ "${want%%.*}" != "${have%%.*}" ]; then
  say "warning: the image has node ${have%%.*}, the repository asks for ${want%%.*}; set node_version: \"${want%%.*}\""
fi
if [ -n "${PARAM_INSTALL_COMMAND:-}" ]; then run "$PARAM_INSTALL_COMMAND"; exit 0; fi
case "$pm" in
  pnpm) corepack enable 2>/dev/null || npm install -g pnpm >/dev/null 2>&1
        run pnpm install --frozen-lockfile --store-dir .cache/pnpm ;;
  yarn) corepack enable 2>/dev/null || true
        if [ -f .yarnrc.yml ]; then run yarn install --immutable; else run yarn install --frozen-lockfile --cache-folder .cache/yarn; fi ;;
  npm)  if [ -f package-lock.json ] || [ -f npm-shrinkwrap.json ]; then run npm ci --cache .cache/npm --no-audit --no-fund
        else say "no lock file: using npm install (commit package-lock.json for repeatable builds)"; run npm install --cache .cache/npm --no-audit --no-fund; fi ;;
esac
