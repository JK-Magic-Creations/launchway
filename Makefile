VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build test test-containers test-kubernetes run demo clean

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/launchway ./cmd/launchway

test:
	go vet ./...
	go test -race ./...
	cd web && npx tsc --noEmit

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
