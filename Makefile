VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build image chart-lint test test-postgres test-vault test-containers test-kubernetes test-argo run demo clean test-s3

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/launchway ./cmd/launchway

# Builds the container image (see Dockerfile). Docker works too: CONTAINER=docker.
CONTAINER ?= podman
IMAGE ?= launchway:$(VERSION)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
image:
	$(CONTAINER) build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) -t $(IMAGE) .

# Checks the Helm chart in deploy/helm/launchway.
chart-lint:
	helm lint deploy/helm/launchway --set postgresql.enabled=true
	helm template t deploy/helm/launchway -f deploy/helm/launchway/examples/ha.yaml >/dev/null

test:
	go vet ./...
	go test -race ./...
	cd web && npx tsc --noEmit

# Runs the engine, source and API tests against PostgreSQL. Each test gets a
# schema of its own. A throwaway server:
#   podman run -d --name launchway-pg -p 127.0.0.1:55432:5432 -e POSTGRES_USER=launchway \
#     -e POSTGRES_PASSWORD=launchway -e POSTGRES_DB=launchway postgres:17-alpine
LAUNCHWAY_TEST_POSTGRES ?= postgres://launchway:launchway@127.0.0.1:55432/launchway?sslmode=disable
test-postgres:
	LAUNCHWAY_TEST_POSTGRES='$(LAUNCHWAY_TEST_POSTGRES)' go test -race -count=1 ./internal/engine/ ./internal/source/ ./internal/api/

# Runs the vault secret store against a real server. A throwaway OpenBao in
# dev mode (KV version 2 at secret/):
#   podman run --rm -d --name launchway-bao -p 127.0.0.1:58200:8200 \
#     -e BAO_DEV_ROOT_TOKEN_ID=dev-root quay.io/openbao/openbao:latest \
#     server -dev -dev-listen-address=0.0.0.0:8200
# HashiCorp Vault works the same with VAULT_DEV_ROOT_TOKEN_ID.
LAUNCHWAY_TEST_VAULT_ADDR ?= http://127.0.0.1:58200
LAUNCHWAY_TEST_VAULT_TOKEN ?= dev-root
test-vault:
	LAUNCHWAY_TEST_VAULT_ADDR='$(LAUNCHWAY_TEST_VAULT_ADDR)' LAUNCHWAY_TEST_VAULT_TOKEN='$(LAUNCHWAY_TEST_VAULT_TOKEN)' \
		go test -race -count=1 -run OpenBao -v ./internal/secrets/

# Image steps' container tests start a rootless BuildKit (pulled once, its
# cache kept in Podman volumes launchway-test-buildkit*) and registries, and
# download the scanner's vulnerability database on first use.
test-containers:
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 ./internal/executor/
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 60m ./internal/imagebuild/...
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 60m -run 'ImageStep' ./internal/api/

# Needs a cluster: kind create cluster --name launchway-test
#                  kubectl --context kind-launchway-test create namespace launchway-test
# The test namespace is labelled to enforce Pod Security "restricted", which
# step pods must pass. The image step test deploys deploy/buildkit/buildkitd.yaml
# into a namespace of its own (launchway-build-test) and copies the seccomp
# profile it needs onto the kind node; it refuses any context not kind-*.
# Some tests pull python:3.12-slim and node:24-alpine and
# install a package from PyPI and from npm.
test-kubernetes:
	kubectl --context $${KUBE_CONTEXT:-kind-launchway-test} label --overwrite namespace launchway-test \
		pod-security.kubernetes.io/enforce=restricted
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} go test -count=1 -run Kubernetes ./internal/executor/
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} go test -count=1 -timeout 40m -run Kubernetes ./internal/api/

# Runs the deploy integration tests against Argo CD and Argo Rollouts in a kind
# cluster (see deploy/test/argo/README.md). Refuses any context not starting
# with kind-. Installs or updates the environment first.
test-argo:
	deploy/test/argo/test-env.sh --context $${KUBE_CONTEXT:-kind-launchway-test}
	deploy/test/argo/up.sh --context $${KUBE_CONTEXT:-kind-launchway-test}
	LAUNCHWAY_TEST_ARGO_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} go test -count=1 -timeout 60m -run Argo -v ./internal/deploy/integration/

# Creates ../launchway-demo, a small repository to play with, and a demo
# shared library with an app that uses it (see examples/local.yaml).
demo:
	./scripts/make-demo-repo.sh ../launchway-demo
	./scripts/make-demo-library.sh ../launchway-demo-library ../launchway-demo-lib-app ../launchway-demo-plain

# Runs the server with the demo repository and Launchway itself as projects.
run: all demo
	./bin/launchway server --config examples/local.yaml

clean:
	rm -rf bin web/dist/assets web/dist/index.html

# Runs the S3 artifact store against a real server. A throwaway MinIO:
#   podman run --rm -d --name launchway-minio -p 127.0.0.1:59000:9000 \
#     -e MINIO_ROOT_USER=launchway -e MINIO_ROOT_PASSWORD=launchway-secret \
#     quay.io/minio/minio:latest server /data
# (MinIO stopped publishing free images in late 2025; if that pull is refused,
# use a MinIO build you trust or any other S3-compatible server.) With LAUNCHWAY_TEST_S3_CREATE_BUCKET=1 the tests create the bucket
# themselves (a signed PUT), so no mc client is needed. Stop it with
#   podman stop launchway-minio
LAUNCHWAY_TEST_S3_ENDPOINT ?= http://127.0.0.1:59000
LAUNCHWAY_TEST_S3_BUCKET ?= launchway-test
LAUNCHWAY_TEST_S3_ACCESS_KEY ?= launchway
LAUNCHWAY_TEST_S3_SECRET_KEY ?= launchway-secret
LAUNCHWAY_TEST_S3_CREATE_BUCKET ?= 1
test-s3:
	LAUNCHWAY_TEST_S3_ENDPOINT='$(LAUNCHWAY_TEST_S3_ENDPOINT)' LAUNCHWAY_TEST_S3_BUCKET='$(LAUNCHWAY_TEST_S3_BUCKET)' \
		LAUNCHWAY_TEST_S3_ACCESS_KEY='$(LAUNCHWAY_TEST_S3_ACCESS_KEY)' LAUNCHWAY_TEST_S3_SECRET_KEY='$(LAUNCHWAY_TEST_S3_SECRET_KEY)' \
		LAUNCHWAY_TEST_S3_CREATE_BUCKET='$(LAUNCHWAY_TEST_S3_CREATE_BUCKET)' \
		go test -race -count=1 -run S3 -v ./internal/blobstore/

# The documentation site (docs/, built with VitePress; see docs/contributing/docs.md).
# Needs Node 24+. `make docs` installs the pinned tools once (npm ci), then
# builds without network access and checks links, anchors, spelling and the
# roadmap. DOCS_BASE is the path the site is served under.
DOCS_BASE ?= /launchway/
docs/node_modules: docs/package-lock.json
	cd docs && npm ci --no-audit --no-fund
	touch docs/node_modules

docs: docs/node_modules
	cd docs && DOCS_BASE='$(DOCS_BASE)' npx vitepress build .
	node scripts/docs/check-links.mjs docs/.vitepress/dist '$(DOCS_BASE)'
	node scripts/docs/build-roadmap.mjs --check .
	cd docs && npx cspell --no-progress --config cspell.json "**/*.md"

# A live preview at http://127.0.0.1:5173/launchway/
docs-serve: docs/node_modules
	cd docs && DOCS_BASE='$(DOCS_BASE)' npx vitepress dev . --host 127.0.0.1 --port 5173

.PHONY: docs docs-serve docs-roadmap docs-roadmap-sync docs-diagrams docs-reference

# Renders every Mermaid diagram of the built site in a headless browser, light
# and dark (run make docs first). Needs a Chromium: `cd docs && npx
# playwright-core install chromium`, or LAUNCHWAY_DOCS_CHROME=/path/to/chrome.
docs-diagrams: docs/node_modules
	node scripts/docs/check-diagrams.mjs docs/.vitepress/dist '$(DOCS_BASE)'

# Checks the reference pages against the code: the keys the parsers accept,
# routes, events, audit actions, policy rules, and that every pipeline example
# in docs/reference/pipeline.md is valid.
docs-reference: build
	go test -count=1 -run 'Reference|DocsEvents' ./internal/pipeline/ ./internal/config/ ./internal/api/ ./internal/engine/ ./internal/policy/
	scripts/docs/check-pipeline-examples.sh docs/reference/pipeline.md bin/launchway

# Rebuilds docs/public/roadmap.html and docs/roadmap-list.md from docs/roadmap/.
docs-roadmap:
	node scripts/docs/build-roadmap.mjs .

# Writes docs/roadmap/roadmap.json from a maintainer's roadmap file, keeping only the public
# fields and dropping items that are not about the product (see scripts/docs/build-roadmap.mjs),
# then rebuilds. Usage: make docs-roadmap-sync ROADMAP_JSON=path/to/roadmap.json
docs-roadmap-sync:
	@test -n '$(ROADMAP_JSON)' || { echo 'docs-roadmap-sync: set ROADMAP_JSON=path/to/roadmap.json' >&2; exit 2; }
	node scripts/docs/build-roadmap.mjs --sync='$(ROADMAP_JSON)' .
