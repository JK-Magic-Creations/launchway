#!/bin/sh
# Creates a demo shared library from examples/library, released twice (tags
# v1 and v2) and with unreleased work on main; an app whose pipeline uses
# it; and a repository with no pipeline file, which the server gives one
# from the library (see examples/local.yaml).
set -e
here=$(cd "$(dirname "$0")/.." && pwd)
lib=${1:-../launchway-demo-library}
app=${2:-../launchway-demo-lib-app}
plain=${3:-../launchway-demo-plain}
commit() { git -c user.name=Launchway -c user.email=demo@localhost commit -q -m "$1"; }

if [ -d "$lib/.git" ]; then
  echo "$lib already exists; leaving it alone."
else
  mkdir -p "$lib"
  cp -R "$here/examples/library/." "$lib/"
  (
    cd "$lib"
    git init -q -b main
    git add -A
    commit "Templates for checks and releases"
    git tag v1
    # Release 2: the build says which release of the library made it.
    sed -i.bak 's/^echo "Built sha256:\$digest"$/echo "Built sha256:$digest (release script v2)"/' scripts/build.sh && rm -f scripts/build.sh.bak
    git add -A
    commit "build.sh: say which release built the image"
    git tag v2
    # Work in progress on main, not released: try it with a library override.
    sed -i.bak 's/^echo "\$count note(s) left in the code"$/echo "$count note(s) left in the code (unreleased count-notes.sh)"/' scripts/count-notes.sh && rm -f scripts/count-notes.sh.bak
    git add -A
    commit "count-notes.sh: work in progress"
  )
  echo "Created $lib (tags v1 and v2, unreleased work on main)"
fi

if [ -d "$app/.git" ]; then
  echo "$app already exists; leaving it alone."
else
  mkdir -p "$app/.launchway"
  cat > "$app/.launchway/pipeline.yaml" <<'YAML'
pipeline: demo-lib-app
on:
  push: { branches: [main] }

# The templates come from the library at a release. Change v1 to v2, or
# start a run with a library override, to try another version.
libraries:
  platform: { repo: demo-library, version: v1 }

steps:
  release:
    uses: platform/release
    with: { environment: dev, approve: false }

  smoke-test:
    description: Check the deployed service answers
    image: docker.io/library/alpine:3.22
    needs: release
    run: |
      echo "Checking $LAUNCHWAY_NEEDS_RELEASE_URL (image $LAUNCHWAY_NEEDS_RELEASE_DIGEST)"
      echo "The checks found $LAUNCHWAY_NEEDS_RELEASE_NOTES note(s)"
YAML
  cat > "$app/start.sh" <<'SH'
#!/bin/sh
# TODO: read the port from the environment
echo "listening on 8080"
SH
  chmod +x "$app/start.sh"
  (cd "$app" && git init -q -b main && git add -A && commit "Demo app that uses the demo library")
  echo "Created $app"
fi

if [ -d "$plain/.git" ]; then
  echo "$plain already exists; leaving it alone."
else
  mkdir -p "$plain"
  printf '#!/bin/sh\necho "no pipeline file here: the server gives this repository one"\n' > "$plain/run.sh"
  chmod +x "$plain/run.sh"
  (cd "$plain" && git init -q -b main && git add -A && commit "A repository with no pipeline file")
  echo "Created $plain"
fi
