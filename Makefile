VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build test test-containers test-kubernetes run demo clean

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/slipway ./cmd/slipway

test:
	go vet ./...
	go test -race ./...
	cd web && npx tsc --noEmit

test-containers:
	SLIPWAY_TEST_CONTAINERS=1 go test -count=1 ./internal/executor/

# Needs a cluster: kind create cluster --name slipway-test
#                  kubectl --context kind-slipway-test create namespace slipway-test
test-kubernetes:
	SLIPWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-slipway-test} go test -count=1 -run Kubernetes ./internal/executor/

# Creates ../slipway-demo, a small repository to play with.
demo:
	./scripts/make-demo-repo.sh ../slipway-demo

# Runs the server with the demo repository and Slipway itself as projects.
run: all demo
	./bin/slipway server --config examples/local.yaml

clean:
	rm -rf bin web/dist/assets web/dist/index.html
