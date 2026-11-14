#!/bin/sh
# Creates a small Git repository with a pipeline that shows what Slipway does.
set -e
dir=${1:-../slipway-demo}
if [ -d "$dir/.git" ]; then
  echo "$dir already exists; leaving it alone."
  exit 0
fi
mkdir -p "$dir/.slipway"
cd "$dir"
git init -q -b main
cat > .slipway/pipeline.yaml <<'EOF'
pipeline: demo-app
on:
  push: { branches: [main, "feature/*"] }

steps:
  lint:
    image: docker.io/library/alpine:3.22
    run: |
      echo "Checking style in $(ls | wc -l) files"
      sleep 1
      printf '\033[32m✓ no problems found\033[0m\n'

  unit-tests:
    image: docker.io/library/alpine:3.22
    run: |
      for t in parser engine api runner ui; do
        sleep 0.6
        printf '\033[32mPASS\033[0m  %s\n' "$t"
      done
      echo "coverage=84%" >> "$SLIPWAY_OUTPUTS"

  security-scan:
    image: docker.io/library/alpine:3.22
    run: |
      echo "Scanning dependencies…"
      sleep 2
      printf '\033[33m2 low-severity findings\033[0m\n'

  build:
    image: docker.io/library/alpine:3.22
    needs: [lint, unit-tests, security-scan]
    run: |
      echo "Building $SLIPWAY_PROJECT at $SLIPWAY_COMMIT (coverage $SLIPWAY_NEEDS_UNIT_TESTS_COVERAGE)"
      sleep 2
      digest=$(echo "$SLIPWAY_COMMIT" | sha256sum | cut -c1-16)
      echo "image=registry.example.com/demo-app@sha256:$digest" >> "$SLIPWAY_OUTPUTS"
      echo "Pushed registry.example.com/demo-app@sha256:$digest"

  smoke-test:
    image: docker.io/library/alpine:3.22
    needs: build
    run: |
      echo "Testing $SLIPWAY_NEEDS_BUILD_IMAGE"
      sleep 1
      # Fails on the first attempt while a file called FLAKY exists, so you
      # can try "Resume". Delete FLAKY and push to make it pass first time.
      if [ "$SLIPWAY_ATTEMPT" = "1" ] && [ -f FLAKY ]; then
        echo "GET /healthz → 503 Service Unavailable" >&2
        exit 1
      fi
      echo "GET /healthz → 200 OK"

  deploy-dev:
    image: docker.io/library/alpine:3.22
    needs: smoke-test
    when: { branch: main }
    run: |
      echo "Would deploy $SLIPWAY_NEEDS_BUILD_IMAGE to dev"
EOF
echo "A demo app for trying Slipway." > README.md
echo "Delete this file to make the smoke test pass first time." > FLAKY
git add -A
git -c user.name=Slipway -c user.email=demo@localhost commit -q -m "Initial demo app"
git checkout -q -b feature/new-button
echo "button" > button.txt
git add -A
git -c user.name=Slipway -c user.email=demo@localhost commit -q -m "Add a new button"
git checkout -q main
echo "Created $dir"
