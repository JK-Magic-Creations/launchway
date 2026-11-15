VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build image chart-lint test test-postgres test-vault test-containers test-kubernetes run demo clean test-s3

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

test-containers:
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 ./internal/executor/

# Needs a cluster: kind create cluster --name launchway-test
#                  kubectl --context kind-launchway-test create namespace launchway-test
# The test namespace is labelled to enforce Pod Security "restricted", which
# step pods must pass. Some tests pull python:3.12-slim and node:24-alpine and
# install a package from PyPI and from npm.
test-kubernetes:
	kubectl --context $${KUBE_CONTEXT:-kind-launchway-test} label --overwrite namespace launchway-test \
		pod-security.kubernetes.io/enforce=restricted
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} go test -count=1 -run Kubernetes ./internal/executor/

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
