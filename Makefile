VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build test test-postgres test-containers test-kubernetes run demo clean

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/launchway ./cmd/launchway

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

test-containers:
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 ./internal/executor/

# Needs a cluster: kind create cluster --name launchway-test
#                  kubectl --context kind-launchway-test create namespace launchway-test
test-kubernetes:
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} go test -count=1 -run Kubernetes ./internal/executor/

# Creates ../launchway-demo, a small repository to play with.
demo:
	./scripts/make-demo-repo.sh ../launchway-demo

# Runs the server with the demo repository and Launchway itself as projects.
run: all demo
	./bin/launchway server --config examples/local.yaml

clean:
	rm -rf bin web/dist/assets web/dist/index.html
