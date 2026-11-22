VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: test-like-ci prepush all web build learn learn-check docs-diagrams-cache image chart-lint test test-postgres test-vault test-containers test-kubernetes test-argo test-verify test-local-model run demo clean originality vocabulary vocabulary-update test-s3 \
	tools release-local release-reproduce release-images vulncheck license-check lint-scripts lint-workflows \
	charts-sync test-install test-runner-chart bundle test-bundle test-git-compat test-upgrade test-upgrade-fast

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build
	$(MAKE) learn

# The documentation inside the binary (the Learn area): docs/ rendered into web/dist/learn-content,
# which Go's embed takes along with the web UI. Needs the docs tools (npm ci in docs/), no browser and
# no network; the diagrams are pre-drawn SVG in docs/.vitepress/diagrams (make docs-diagrams-cache).
LEARN_OUT := web/dist/learn-content/manifest.json.gz
LEARN_SOURCES := $(wildcard docs/*.md docs/*/*.md docs/*/*/*.md docs/learn/nav.json docs/.vitepress/diagrams/*.svg) \
	docs/.vitepress/config.mts scripts/docs/build-learn.mjs scripts/docs/learn-pages.mjs
learn: $(LEARN_OUT)
$(LEARN_OUT): docs/node_modules $(LEARN_SOURCES)
	node scripts/docs/build-learn.mjs

build: $(LEARN_OUT)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/launchway ./cmd/launchway

# Builds the container image (see Dockerfile). Docker works too: CONTAINER=docker.
CONTAINER ?= podman
IMAGE ?= launchway:$(VERSION)
# The image's OCI labels: the full commit, and the commit's time as its creation time (so two
# builds of one commit carry the same labels). The Dockerfile refuses a build without a commit.
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null)
CREATED ?= $(shell TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null)
SOURCE_REPOSITORY ?= https://github.com/JK-Magic-Creations/launchway
image:
	@test -n "$(REVISION)" || { echo "make image: no Git commit; build from a clone or set REVISION=<full commit sha> and CREATED" >&2; exit 1; }
	$(CONTAINER) build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) \
		--build-arg CREATED=$(CREATED) --build-arg SOURCE=$(SOURCE_REPOSITORY) -t $(IMAGE) .

# Checks the Helm charts in deploy/helm: lint, example renders, and that the server chart renders
# the same every time with no Secret material (scripts/chart-determinism.sh).
chart-lint: charts-sync
	helm lint deploy/helm/launchway --set externalURL=https://launchway.example.com --set strictValues=true
	helm template t deploy/helm/launchway -f deploy/helm/launchway/examples/ha.yaml --set strictValues=true >/dev/null
	scripts/chart-determinism.sh
	helm lint deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/remote.yaml
	helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/remote.yaml -f deploy/helm/launchway-runner/examples/networkpolicy.yaml >/dev/null
	helm template t deploy/helm/launchway-runner -f deploy/helm/launchway-runner/examples/deploy-runner.yaml >/dev/null

# The release gate for installing, upgrading and rolling back (deploy/test/upgrade/matrix.sh): kind,
# podman, helm, minutes. A release is not cut unless it is green (docs/development/releasing.md).
test-upgrade:
	deploy/test/upgrade/matrix.sh

# The hermetic subset that runs in every `make check`: renders only, no cluster (compatibility of the
# previous release's values and chart with this one, unknown values, the image and chart pairing).
test-upgrade-fast:
	scripts/upgrade-fast.sh

test:
	go vet ./...
	go test -race -timeout 30m ./...
	cd web && npx tsc --noEmit

# Runs the pipeline's own test step, in its image, as a numeric user with no account entry, on a copy
# of the committed tree (scripts/ci/test-like-ci.sh). Minutes; docs/development/quality-gates.md,
# "Before you push".
test-like-ci:
	sh scripts/ci/test-like-ci.sh

# Builds and tests the toggle SDKs (sdk/go/toggles, its OpenFeature provider, sdk/js): they are modules of their own,
# outside `go test ./...`. Not part of make check; docs/guide/toggles-sdks.md.
test-sdks:
	cd sdk/go/toggles && go vet ./... && go test -count=1 -race ./...
	cd sdk/go/toggles/openfeature && go vet ./... && go test -count=1 ./...
	cd sdk/js && npm ci --no-audit --no-fund --prefer-offline && npm test

# Everything to run before a push, in order: the fast gates, the docs build, the pipeline's test step.
prepush:
	$(MAKE) check
	$(MAKE) secrets-head
	$(MAKE) docs
	$(MAKE) test-like-ci

# Runs the Git hosting tests (internal/forge/storage, internal/forge/storagespike) on the oldest git
# Launchway supports, in a container (Podman or Docker; skips with a message when neither exists).
# Not part of make check: it takes minutes. docs/development/quality-gates.md, "The oldest supported git".
test-git-compat:
	sh scripts/git-compat/run.sh

# Runs the engine, source and API tests against PostgreSQL. Each test gets a
# schema of its own. A throwaway server:
#   podman run -d --name launchway-pg -p 127.0.0.1:55432:5432 -e POSTGRES_USER=launchway \
#     -e POSTGRES_PASSWORD=launchway -e POSTGRES_DB=launchway postgres:17-alpine
LAUNCHWAY_TEST_POSTGRES ?= postgres://launchway:launchway@127.0.0.1:55432/launchway?sslmode=disable
test-postgres:
	LAUNCHWAY_TEST_POSTGRES='$(LAUNCHWAY_TEST_POSTGRES)' go test -race -count=1 -timeout 60m ./internal/engine/ ./internal/source/ ./internal/api/

# Runs the vault secret store, and the transit root key holder of encryption at rest, against a real server. A throwaway OpenBao in
# dev mode (KV version 2 at secret/):
#   podman run --rm -d --name launchway-bao -p 127.0.0.1:58200:8200 \
#     -e BAO_DEV_ROOT_TOKEN_ID=dev-root quay.io/openbao/openbao:latest \
#     server -dev -dev-listen-address=0.0.0.0:8200
# HashiCorp Vault works the same with VAULT_DEV_ROOT_TOKEN_ID.
LAUNCHWAY_TEST_VAULT_ADDR ?= http://127.0.0.1:58200
LAUNCHWAY_TEST_VAULT_TOKEN ?= dev-root
test-vault:
	LAUNCHWAY_TEST_VAULT_ADDR='$(LAUNCHWAY_TEST_VAULT_ADDR)' LAUNCHWAY_TEST_VAULT_TOKEN='$(LAUNCHWAY_TEST_VAULT_TOKEN)' \
		go test -race -count=1 -run OpenBao -v ./internal/secrets/ ./internal/keyring/transit/ ./internal/sealing/

# Image steps' container tests start a rootless BuildKit (pulled once, its
# cache kept in Podman volumes launchway-test-buildkit*) and registries, and
# download the scanner's vulnerability database on first use.
test-containers:
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 ./internal/executor/
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 60m ./internal/imagebuild/...
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 60m -run 'ImageStep' ./internal/api/

# Verifications against real health sources in containers (Podman or Docker): Prometheus,
# Loki, ClickHouse, OpenSearch and PostgreSQL, pulled once by digest. The end-to-end test runs a
# healthy deploy that passes and a regression that fails and is rolled back, with a real server,
# build runner and deploy runner.
test-verify:
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 40m ./internal/verify/...
	LAUNCHWAY_TEST_CONTAINERS=1 go test -count=1 -timeout 40m -run 'Verify' ./internal/api/

# A real local model: starts Ollama in a container and pulls a small model (about 400 MB,
# kept in the Podman volume launchway-test-ollama-models). Opt-in; no paid API is called.
test-local-model:
	LAUNCHWAY_TEST_LOCAL_MODEL=1 go test -count=1 -timeout 30m -run LocalModel -v ./internal/ai/

# Needs a cluster: kind create cluster --name launchway-test
#                  kubectl --context kind-launchway-test create namespace launchway-test
# The test namespace is labelled to enforce Pod Security "restricted", which
# step pods must pass. The image step test deploys deploy/buildkit/buildkitd.yaml
# into a namespace of its own (launchway-build-test) and copies the seccomp
# profile it needs onto the kind node; it refuses any context not kind-*.
# Some tests pull python:3.12-slim and node:24-alpine and
# install a package from PyPI and from npm.
# KUBE_NAMESPACE and KUBE_BUILD_NAMESPACE name other namespaces than
# launchway-test and launchway-build-test, so that several checkouts can share
# one cluster (the build namespace is deleted and made again by the test).
KUBE_NAMESPACE ?= launchway-test
KUBE_BUILD_NAMESPACE ?= launchway-build-test
test-kubernetes:
	kubectl --context $${KUBE_CONTEXT:-kind-launchway-test} get namespace $(KUBE_NAMESPACE) >/dev/null 2>&1 || \
		kubectl --context $${KUBE_CONTEXT:-kind-launchway-test} create namespace $(KUBE_NAMESPACE)
	kubectl --context $${KUBE_CONTEXT:-kind-launchway-test} label --overwrite namespace $(KUBE_NAMESPACE) \
		pod-security.kubernetes.io/enforce=restricted
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} LAUNCHWAY_TEST_KUBE_NAMESPACE=$(KUBE_NAMESPACE) \
		go test -count=1 -run Kubernetes ./internal/executor/
	LAUNCHWAY_TEST_KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-launchway-test} LAUNCHWAY_TEST_KUBE_NAMESPACE=$(KUBE_NAMESPACE) \
		LAUNCHWAY_TEST_KUBE_BUILD_NAMESPACE=$(KUBE_BUILD_NAMESPACE) go test -count=1 -timeout 40m -run Kubernetes ./internal/api/

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

# Every third-party module compiled into the binary must carry one of these licenses. Launchway
# itself is AGPL-3.0-only (see LICENSE and LICENSING.md), not one of the dependencies checked here.
# The in-toto --ignore entries are modules whose license file the classifier cannot read and a
# person has checked: they carry the short Apache-2.0 notice (checked for attestation v1.2.0 and
# in-toto-golang v0.11.0; check again when they change).
ALLOWED_LICENSES := Apache-2.0,BSD-2-Clause,BSD-3-Clause,MIT,ISC,MPL-2.0,Unlicense,0BSD
license-check: tools
	$(TOOLS)/go-licenses check ./cmd/launchway --allowed_licenses=$(ALLOWED_LICENSES) \
		--ignore github.com/jk-magic-creations/launchway \
		--ignore github.com/in-toto/attestation --ignore github.com/in-toto/in-toto-golang

# shellcheck on the packaging, release and test scripts, always the pinned version in a container, so a
# hosted runner's own (newer) shellcheck cannot report findings that a laptop does not.
SHELLCHECK_IMAGE := docker.io/koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d
SHELL_SCRIPTS = $(shell git ls-files -co --exclude-standard 'packaging/*.sh' 'deploy/test/install/*.sh' \
	'deploy/test/runner-chart/*.sh' 'deploy/test/bundle/*.sh' 'scripts/charts-sync.sh' 'scripts/chart-determinism.sh' 'scripts/ci/race-group.sh')
lint-scripts:
	$(CONTAINER) run --rm --security-opt label=disable -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS)

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
	$(MAKE) learn-check
	cd docs && npx cspell --no-progress --config cspell.json "**/*.md"

# A live preview at http://127.0.0.1:5173/launchway/
docs-serve: docs/node_modules
	cd docs && DOCS_BASE='$(DOCS_BASE)' npx vitepress dev . --host 127.0.0.1 --port 5173

# Draws the Mermaid diagrams of the Learn pages as SVG, light and dark, into docs/.vitepress/diagrams
# (kept in Git, so building the product needs no browser). Needs a Chromium: LAUNCHWAY_DOCS_CHROME=/path/to/chrome.
docs-diagrams-cache: docs/node_modules
	node scripts/docs/render-diagrams.mjs

# Fails if a Learn page has a diagram without a drawing, a drawing is unused, or a page or link in the
# Learn contents is wrong. No browser, no network.
learn-check: docs/node_modules
	node scripts/docs/render-diagrams.mjs --check
	node scripts/docs/build-learn.mjs --check

.PHONY: docs docs-serve docs-matrix docs-roadmap docs-diagrams docs-reference

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
	for f in $$(grep -l '^<!-- try -->' docs/learn/*.md docs/learn/*/*.md); do scripts/docs/check-pipeline-examples.sh $$f bin/launchway || exit 1; done

# Regenerates docs/guide/outside-host-matrix.md from the host providers' declarations.
docs-matrix:
	go test ./internal/forge/hosts -run TestMatrixPageMatchesDeclarations -update

# Rebuilds docs/public/roadmap.html and docs/roadmap-list.md from docs/roadmap/.
docs-roadmap:
	node scripts/docs/build-roadmap.mjs .

# ---------------------------------------------------------------------------
# Quality gates (docs/development/quality-gates.md). Each fails the build.
# Tools are pinned by version and installed into the repository's own .tools
# directory (gitignored), never into the user's GOPATH.
# ---------------------------------------------------------------------------
TOOLS ?= $(CURDIR)/.tools
GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_LINT := $(TOOLS)/bin/golangci-lint
QUALITY_DIR := $(CURDIR)/quality

.PHONY: tracked-files image-inputs image-build-check test-sdks tools lint lint-baseline-update arch ratchet ratchet-update wall-update fmt-check vet \
	web-lint web-test web-size web-a11y coverage coverage-check coverage-update check check-all

tools: $(GOLANGCI_LINT)

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# golangci-lint exits non-zero when it finds anything; the baseline tool decides.
# Findings beyond quality/lint-baseline.json fail; the baseline may only shrink.
lint: $(GOLANGCI_LINT)
	@mkdir -p bin
	@$(GOLANGCI_LINT) run --output.json.path=bin/lint-report.json --output.text.path=stderr ./... >/dev/null 2>&1; rc=$$?; [ $$rc -le 1 ]
	go run ./scripts/quality/lintbaseline check bin/lint-report.json $(QUALITY_DIR)/lint-baseline.json

lint-baseline-update: $(GOLANGCI_LINT)
	@mkdir -p bin
	@$(GOLANGCI_LINT) run --output.json.path=bin/lint-report.json --output.text.path=stderr ./... >/dev/null 2>&1; rc=$$?; [ $$rc -le 1 ]
	go run ./scripts/quality/lintbaseline update bin/lint-report.json $(QUALITY_DIR)/lint-baseline.json

# Allowed imports between internal packages, and the size ratchet.
arch:
	go test -count=1 -run 'TestImportsAreInTheTable|TestTableHasNoStaleEdges|TestForbiddenEdges|TestKnownExceptionsAreStillNeeded|TestNoNewSQLOutsideTheStore|TestScopeDoorsAreCalledOnlyFromTheirFiles' ./internal/archtest/

originality:
	go test -count=1 -run TestNoOtherProductsAreNamed ./internal/archtest/

# The vocabulary guard: Launchway's own words are used the same way everywhere a person reads them
# (docs/reference/vocabulary.md). Fast, no network. `make vocabulary-update` lowers the baseline
# after a fix; `scripts/vocabulary-fix` applies the mechanical replacements.
vocabulary:
	node scripts/vocabulary/check.mjs

vocabulary-update:
	node scripts/vocabulary/check.mjs --update-baseline

tracked-files:
	sh scripts/check-tracked-files.sh

# The Dockerfile's web stage copies everything the UI's type check reads from outside web/ (fast, no container).
image-inputs:
	node scripts/check-image-inputs.mjs

# Runs the image's web stage itself: npm ci and `npm run build` (the type check included) on exactly what the
# Dockerfile copies. Needs a container tool (CONTAINER=podman or docker) and the network for its base image.
# Builds the web stage, then the shipped image itself and runs its server binary's self-check for reading a hosted
# repository from the server's own storage inside it. The image has no shell: a need for one, or a program the image
# lacks, fails here and not in production (it once did: every poll of a hosted repository failed).
image-build-check:
	$(CONTAINER) build --target web -t launchway-web-check -f Dockerfile .
	@test -n "$(REVISION)" || { echo "make image-build-check: no Git commit; set REVISION=<full commit sha> and CREATED" >&2; exit 1; }
	$(CONTAINER) build --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) --build-arg CREATED=$(CREATED) --build-arg SOURCE=$(SOURCE_REPOSITORY) -t launchway-image-check -f Dockerfile .
	$(CONTAINER) run --rm --network none launchway-image-check self-check hosted-read

ratchet:
	go test -count=1 -run TestSizeRatchet ./internal/archtest/

# Lowers quality/size-baseline.json after a file or function shrank; refuses growth.
ratchet-update:
	UPDATE_RATCHET=1 go test -count=1 -run TestSizeRatchet -v ./internal/archtest/

# Lowers quality/wall-baseline.json after SQL moved into internal/store; refuses growth.
wall-update:
	UPDATE_RATCHET=1 go test -count=1 -run TestNoNewSQLOutsideTheStore -v ./internal/archtest/

fmt-check:
	@out=$$(gofmt -l cmd internal scripts web/*.go); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

web-lint:
	cd web && npm ci --no-audit --no-fund --prefer-offline >/dev/null && npm run lint && npx tsc --noEmit

web-test:
	cd web && npm ci --no-audit --no-fund --prefer-offline >/dev/null && npm test

web-size:
	cd web && npm ci --no-audit --no-fund --prefer-offline >/dev/null && npm run size

# The accessibility scans in a real browser (docs/guide/accessibility.md). The fast part runs in
# make check (web-test: axe-core in jsdom over every kit part and routed page, and the token contrast test). This
# is the deeper part: axe-core in Chromium over the main pages at 1440 and 390 wide, light and dark, with color
# contrast and target size, reflow at 320 px and the focus ring; then the keyboard-only journeys. Each starts a
# throwaway server with demo data (scripts/a11y-server.sh). Needs a Chromium in ~/.cache/ms-playwright
# (npx playwright-core install chromium) or LAUNCHWAY_A11Y_CHROME, and make all first. Writes the results the
# conformance statement is built from to bin/a11y-report.json, and refreshes web/a11y-last-run.json and
# docs/reference/accessibility-conformance.md from them (commit both).
web-a11y: all
	@mkdir -p bin
	LAUNCHWAY_A11Y_REPORT=$(CURDIR)/bin/a11y-report.json scripts/a11y-server.sh node web/scripts/a11y-browser.mjs
	LAUNCHWAY_A11Y_KEYBOARD_REPORT=$(CURDIR)/bin/a11y-keyboard.json scripts/a11y-server.sh node web/scripts/a11y-keyboard.mjs
	node web/scripts/a11y-conformance.mjs --snapshot bin/a11y-report.json bin/a11y-keyboard.json

# Writes per-package coverage (no race detector) to bin/coverage.txt.
coverage:
	@mkdir -p bin
	go test -count=1 -cover ./... 2>&1 | grep -E '^(ok|FAIL|\?)[[:space:]]' | tee bin/coverage.txt

# Fails when a package is below quality/coverage-floor.json. Runs all tests.
coverage-check: coverage
	go run ./scripts/quality/covercheck check bin/coverage.txt $(QUALITY_DIR)/coverage-floor.json

# Raises the floors to today's values; never lowers one.
coverage-update: coverage
	go run ./scripts/quality/covercheck update bin/coverage.txt $(QUALITY_DIR)/coverage-floor.json

# The fast gates: a few minutes, no test suite of the server.
check: test-upgrade-fast fmt-check vet lint arch originality vocabulary tracked-files image-inputs ratchet web-lint web-test learn-check
	@echo "make check: all fast gates passed"

# Everything, including the Go tests with the coverage floors and the bundle budget.
check-all: check web-size coverage-check

# ---------------------------------------------------------------------------
# Security gates (docs/security/how-launchway-is-scanned.md). Not in `make check`: they need network
# (vulnerability databases, images) or take longer than seconds. The scanners run from pinned
# containers or with `go run` at a pinned version (scripts/security/lib.sh); nothing is installed on
# the machine and nothing is bundled into Launchway. `make security` runs them one at a time; the
# product's own pipeline and .github/workflows/security.yml run the same targets.
# ---------------------------------------------------------------------------
.PHONY: security govulncheck osv secrets secrets-head iac chart-pss fuzz image-scan scorecard dast

security: govulncheck osv secrets iac chart-pss fuzz
	@echo "make security: all security gates passed (image-scan and dast are separate: they need an image or a running server)"

# Go vulnerabilities: reachable ones fail, unreachable ones are listed.
govulncheck:
	scripts/security/govulncheck.sh

# Every locked dependency (Go, web UI, docs tools) against OSV.dev, plus npm audit.
osv:
	scripts/security/osv.sh

# The same scan, limited to the history of the checked-out commit (what a push publishes; the hosted
# checkout holds main alone). In make prepush. Skips with a notice when the container tool is missing.
secrets-head:
	@if command -v $${CONTAINER:-podman} >/dev/null 2>&1; then GITLEAKS_SCOPE=head scripts/security/gitleaks.sh; \
	else echo "secrets-head: SKIPPED: no $${CONTAINER:-podman}; the hosted Security workflow runs this scan"; fi

# Secrets in the whole Git history, against the shrink-only baseline quality/gitleaks-baseline.json.
secrets:
	scripts/security/gitleaks.sh

# Helm charts rendered and checked (kubeconform, kube-linter, Trivy config), Dockerfile lint (hadolint).
iac:
	scripts/security/iac.sh

# The rendered charts' pods against Pod Security "restricted", and no secret values in ConfigMaps.
chart-pss:
	go test -count=1 -run 'TestChartsRender|TestRestrictedChecker' ./internal/config/

# Short native Go fuzzing of every parser an outsider can feed (FUZZTIME=15s per target).
fuzz:
	scripts/security/fuzz.sh

# Trivy over the built image (make image first; IMAGE names it).
image-scan:
	scripts/security/image-scan.sh

# OpenSSF Scorecard of the public repository (informational).
scorecard:
	scripts/security/scorecard.sh

# OWASP ZAP baseline scan of a throwaway local server (optional; needs make all).
dast: all
	scripts/security/dast.sh

# The web UI's Content-Security-Policy in a real Chromium: every main page loads under it without a
# violation, and an inline script is blocked. Same needs as web-a11y (a Chromium, make all first).
.PHONY: web-csp
web-csp: all
	scripts/a11y-server.sh node web/scripts/csp-browser.mjs

# CycloneDX SBOMs of the source tree and of the built binary, with Syft (the same tool and format
# the release uses; releases attach them, signed). Written to bin/sbom-*.cdx.json.
.PHONY: sbom
sbom: tools build
	$(TOOLS)/syft dir:. --exclude ./web/node_modules --exclude ./docs/node_modules --exclude ./bin --exclude ./.claude -o cyclonedx-json=bin/sbom-source.cdx.json -q
	$(TOOLS)/syft file:bin/launchway -o cyclonedx-json=bin/sbom-binary.cdx.json -q
	@echo "sbom: bin/sbom-source.cdx.json and bin/sbom-binary.cdx.json written"
