VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build image chart-lint test test-postgres test-vault test-containers test-kubernetes test-argo run demo clean test-s3 \
	tools release-local release-reproduce release-images vulncheck license-check lint-scripts lint-workflows \
	charts-sync test-install test-runner-chart bundle test-bundle

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

# Checks the Helm charts in deploy/helm.
chart-lint: charts-sync
	helm lint deploy/helm/launchway --set postgresql.enabled=true
	helm template t deploy/helm/launchway -f deploy/helm/launchway/examples/ha.yaml >/dev/null
	helm lint deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/remote.yaml
	helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/remote.yaml -f deploy/helm/launchway-runner/examples/networkpolicy.yaml >/dev/null
	helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/deploy-runner.yaml >/dev/null

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

# ---- Releases (see packaging/README.md and SUPPORT.md) ----

# The release tools, at pinned versions, in bin/tools. Built from source with the Go toolchain
# go.mod names, so nothing is downloaded but Go modules (checked against the checksum database).
TOOLS := $(CURDIR)/bin/tools
COSIGN_VERSION := v3.1.4
SYFT_VERSION := v1.57.2
NFPM_VERSION := v2.49.0
GOVULNCHECK_VERSION := v1.10.0
GO_LICENSES_VERSION := v2.0.1
ACTIONLINT_VERSION := v1.7.12
TOOL_BINS := $(TOOLS)/cosign $(TOOLS)/syft $(TOOLS)/nfpm $(TOOLS)/govulncheck $(TOOLS)/go-licenses $(TOOLS)/actionlint
tools: $(TOOL_BINS)
$(TOOLS)/cosign:      ; GOBIN=$(TOOLS) go install github.com/sigstore/cosign/v3/cmd/cosign@$(COSIGN_VERSION)
$(TOOLS)/syft:        ; GOBIN=$(TOOLS) go install -ldflags "-X main.version=$(SYFT_VERSION)" github.com/anchore/syft/cmd/syft@$(SYFT_VERSION)
$(TOOLS)/nfpm:        ; GOBIN=$(TOOLS) go install github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
$(TOOLS)/govulncheck: ; GOBIN=$(TOOLS) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
$(TOOLS)/go-licenses: ; GOBIN=$(TOOLS) go install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
$(TOOLS)/actionlint:  ; GOBIN=$(TOOLS) go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# The version a release build stamps: the tag at HEAD, else a development version naming the
# commit. Either way it depends only on the commit, so rebuilding gives the same bytes.
RELEASE_VERSION ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || echo v0.0.0-dev.$$(git rev-parse --short=12 HEAD))
RELEASE_DIR ?= dist/$(RELEASE_VERSION)

# Everything a release publishes, built locally into dist/VERSION and published nowhere:
# binaries, deb and rpm, install.sh, charts, SBOMs, SHA256SUMS signed with a key (made once in
# .release-key/ unless COSIGN_KEY names one), signed provenance, and apt and yum repositories.
# The release workflow runs the same scripts with SIGN=keyless.
release-local: tools charts-sync
	PATH="$(TOOLS):$$PATH" packaging/release/release.sh $(RELEASE_VERSION) $(RELEASE_DIR)
	PATH="$(TOOLS):$$PATH" packaging/release/repo.sh $(RELEASE_DIR)

# Builds the release twice, from clean directories, and compares every artifact byte for byte.
release-reproduce: tools charts-sync
	PATH="$(TOOLS):$$PATH" packaging/release/reproduce.sh $(RELEASE_VERSION)

# The server and runner images for linux/amd64 and linux/arm64, from the binaries in
# $(RELEASE_DIR), pushed to IMAGE_REPO (default: a throwaway registry on 127.0.0.1:5555), signed,
# with the SBOM and provenance attached. See packaging/release/images.sh.
release-images: tools
	PATH="$(TOOLS):$$PATH" packaging/release/images.sh $(RELEASE_VERSION) $(RELEASE_DIR)

vulncheck: tools
	$(TOOLS)/govulncheck ./...

# Every module compiled into the binary must carry one of these licenses. The --ignore entries are
# modules whose license file the classifier cannot read and a person has checked: the in-toto
# modules carry the short Apache-2.0 notice (checked for attestation v1.2.0 and in-toto-golang
# v0.11.0; check again when they change).
ALLOWED_LICENSES := Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC,MPL-2.0,Unlicense,0BSD
license-check: tools
	$(TOOLS)/go-licenses check ./cmd/launchway --allowed_licenses=$(ALLOWED_LICENSES) \
		--ignore github.com/in-toto/attestation --ignore github.com/in-toto/in-toto-golang

# shellcheck on the packaging, release and test scripts (installed, or in a container).
SHELLCHECK_IMAGE := docker.io/koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
SHELL_SCRIPTS = $(shell git ls-files -co --exclude-standard 'packaging/*.sh' 'deploy/test/install/*.sh' \
	'deploy/test/runner-chart/*.sh' 'deploy/test/bundle/*.sh' 'scripts/charts-sync.sh')
lint-scripts:
	if command -v shellcheck >/dev/null 2>&1; then shellcheck $(SHELL_SCRIPTS); \
	else $(CONTAINER) run --rm --security-opt label=disable -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS); fi

lint-workflows: tools
	$(TOOLS)/actionlint

# The templates both charts use for the Kubernetes executor (step namespace, RBAC) are written
# once, in deploy/helm/shared, and copied into each; internal/config/chart_test.go fails if a copy
# differs or the two charts render different RBAC.
charts-sync:
	scripts/charts-sync.sh

# Installs the runner with install.sh and with the deb and rpm packages (from the generated
# repositories) in systemd containers of several distributions, connects each to a real server,
# runs a pipeline on it, upgrades and uninstalls. Needs podman. See deploy/test/install.
test-install: tools charts-sync
	PATH="$(TOOLS):$$PATH" deploy/test/install/test.sh

# Installs the main chart (server) and the runner chart in separate namespaces of the kind
# cluster and runs a pipeline through the remote runner. See deploy/test/runner-chart.
test-runner-chart: charts-sync
	deploy/test/runner-chart/test.sh --context $${KUBE_CONTEXT:-kind-launchway-test}

# One archive for an installation without internet access: binaries, packages, images (OCI
# layout, with their signatures), charts, checksums, signatures and an offline guide. Needs
# release-local and release-images first.
bundle: tools
	PATH="$(TOOLS):$$PATH" packaging/release/bundle.sh $(RELEASE_VERSION) $(RELEASE_DIR)

# Loads the bundle's images into the kind cluster with no registry and installs both charts.
test-bundle:
	deploy/test/bundle/test.sh --context $${KUBE_CONTEXT:-kind-launchway-test} dist/launchway-bundle-$(RELEASE_VERSION).tar
