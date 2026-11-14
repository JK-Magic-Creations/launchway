#!/bin/sh
# Creates a small Git repository with a pipeline that shows what Launchway does.
set -e
dir=${1:-../launchway-demo}
if [ -d "$dir/.git" ]; then
  echo "$dir already exists; leaving it alone."
  exit 0
fi
mkdir -p "$dir/.launchway"
cd "$dir"
git init -q -b main
cat > .launchway/pipeline.yaml <<'EOF'
pipeline: demo-app
on:
  push: { branches: [main, "feature/*"] }

# Named containers: a toolbox, and a cache that runs beside the steps.
containers:
  tools: { image: docker.io/library/alpine:3.22 }
  cache: { image: docker.io/library/redis:7-alpine, service: true }

steps:
  lint:
    description: Check the code style
    image: docker.io/library/alpine:3.22
    run: |
      echo "Checking style in $(ls | wc -l) files"
      sleep 1
      printf '\033[32m✓ no problems found\033[0m\n'

  unit-tests:
    description: Run the fast tests
    image: docker.io/library/alpine:3.22
    run: |
      for t in parser engine api runner ui; do
        sleep 0.6
        printf '\033[32mPASS\033[0m  %s\n' "$t"
      done
      echo "coverage=84%" >> "$LAUNCHWAY_OUTPUTS"

  integration-tests:
    description: Test against the cache service
    container: tools
    needs: unit-tests
    run: |
      echo "Talking to the cache service at localhost:6379"
      for i in $(seq 1 30); do
        reply=$(printf 'PING\r\n' | nc -w 1 localhost 6379 2>/dev/null | tr -d '\r') || true
        if [ "$reply" = "+PONG" ]; then printf '\033[32mPASS\033[0m  cache answered %s\n' "$reply"; exit 0; fi
        sleep 1
      done
      echo "the cache never answered" >&2
      exit 1

  security-scan:
    description: Look for known problems in dependencies
    image: docker.io/library/alpine:3.22
    run: |
      echo "Scanning dependencies…"
      sleep 2
      printf '\033[33m2 low-severity findings\033[0m\n'

  build:
    description: Build and push the image
    image: docker.io/library/alpine:3.22
    needs: [lint, integration-tests, security-scan]
    run: |
      echo "Building $LAUNCHWAY_PROJECT at $LAUNCHWAY_COMMIT (coverage $LAUNCHWAY_NEEDS_UNIT_TESTS_COVERAGE)"
      sleep 2
      digest=$(echo "$LAUNCHWAY_COMMIT" | sha256sum | cut -c1-16)
      echo "image=registry.example.com/demo-app@sha256:$digest" >> "$LAUNCHWAY_OUTPUTS"
      echo "Pushed registry.example.com/demo-app@sha256:$digest"

  smoke-test:
    description: Check that the built image answers
    image: docker.io/library/alpine:3.22
    needs: build
    run: |
      echo "Testing $LAUNCHWAY_NEEDS_BUILD_IMAGE"
      sleep 1
      # Fails on the first attempt while a file called FLAKY exists, so you
      # can try "Resume". Delete FLAKY and push to make it pass first time.
      if [ "$LAUNCHWAY_ATTEMPT" = "1" ] && [ -f FLAKY ]; then
        echo "GET /healthz → 503 Service Unavailable" >&2
        exit 1
      fi
      echo "GET /healthz → 200 OK"

  # Pauses the run until someone approves it, on main only.
  approve-dev:
    description: A person decides whether this build goes to dev
    kind: approval
    needs: smoke-test
    when: { branch: main }
    message: Deploy this build to dev?
    timeout: 24h

  deploy-dev:
    description: Deploy the image to the dev environment
    image: docker.io/library/alpine:3.22
    needs: approve-dev
    when: { branch: main }
    run: |
      echo "Would deploy $LAUNCHWAY_NEEDS_BUILD_IMAGE to dev"
EOF
echo "A demo app for trying Launchway." > README.md
echo "Delete this file to make the smoke test pass first time." > FLAKY
git add -A
git -c user.name=Launchway -c user.email=demo@localhost commit -q -m "Initial demo app"
git checkout -q -b feature/new-button
echo "button" > button.txt
git add -A
git -c user.name=Launchway -c user.email=demo@localhost commit -q -m "Add a new button"
git checkout -q main
echo "Created $dir"
