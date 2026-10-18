# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL builds the images. The targets are tested with Docker only.
CONTAINER_TOOL ?= docker

# Recipes use bash and stop on the first error.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# help prints the targets ("##" comments) by category ("##@" comments).

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: test-live
test-live: ## Run the provider tests against the live APIs that need no key.
	go test -tags=live -count=1 -run TestLive ./internal/signal/...

# The e2e tests use a dedicated Kind cluster and their own kubeconfig file.
KIND_CLUSTER ?= offpeak-e2e
KIND_NODE_IMAGE ?= kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
E2E_KUBECONFIG ?= $(LOCALBIN)/e2e-kubeconfig
# Set E2E_KEEP_CLUSTER=true to keep the cluster after the tests, for debugging.
E2E_KEEP_CLUSTER ?= false

.PHONY: setup-test-e2e
setup-test-e2e: kind ## Set up a Kind cluster for e2e tests if it does not exist
	@case "$$("$(KIND)" get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation."; \
			"$(KIND)" export kubeconfig --name "$(KIND_CLUSTER)" --kubeconfig "$(E2E_KUBECONFIG)" ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			"$(KIND)" create cluster --name "$(KIND_CLUSTER)" --image "$(KIND_NODE_IMAGE)" \
				--kubeconfig "$(E2E_KUBECONFIG)" ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e kubectl manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	@rc=0; PATH="$(LOCALBIN):$$PATH" KUBECONFIG="$(E2E_KUBECONFIG)" KIND="$(KIND)" KIND_CLUSTER=$(KIND_CLUSTER) \
		go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 30m || rc=$$?; \
	if [ "$(E2E_KEEP_CLUSTER)" != "true" ]; then $(MAKE) cleanup-test-e2e; fi; \
	exit $$rc

.PHONY: print-kind-node-image
print-kind-node-image:
	@echo "$(KIND_NODE_IMAGE)"

.PHONY: cleanup-test-e2e
cleanup-test-e2e: kind ## Tear down the Kind cluster used for e2e tests
	@"$(KIND)" delete cluster --name "$(KIND_CLUSTER)" --kubeconfig "$(E2E_KUBECONFIG)"

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: vulncheck
vulncheck: govulncheck ## Run govulncheck against code.
	"$(GOVULNCHECK)" ./...

.PHONY: lint-actions
lint-actions: actionlint ## Lint the GitHub Actions workflows.
	"$(ACTIONLINT)" .github/workflows/*.yml

.PHONY: scan-image
scan-image: trivy ## Scan the manager image for HIGH and CRITICAL vulnerabilities.
	"$(TRIVY)" image --severity HIGH,CRITICAL --exit-code 1 --no-progress ${IMG}

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# Set BASE_IMAGE to use another registry. Keep it pinned by digest.
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS are the target platforms of docker-buildx. It needs Docker Buildx and a registry.
PLATFORMS ?= linux/amd64,linux/arm64
.PHONY: docker-buildx
docker-buildx: ## Build and push the manager image for PLATFORMS.
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) --tag ${IMG} .

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize kubectl ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply --server-side -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize kubectl ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize kubectl ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply --server-side -f -

.PHONY: undeploy
undeploy: kustomize kubectl ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= $(LOCALBIN)/kubectl
KIND ?= $(LOCALBIN)/kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
ACTIONLINT ?= $(LOCALBIN)/actionlint
TRIVY ?= $(LOCALBIN)/trivy

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.3
CONTROLLER_TOOLS_VERSION ?= v0.23.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0
ACTIONLINT_VERSION ?= v1.7.12
KIND_VERSION ?= v0.33.0
TRIVY_VERSION ?= 0.76.0
# SHA-256 of trivy_$(TRIVY_VERSION)_Linux-64bit.tar.gz from the release checksums file.
TRIVY_SHA256 ?= e24b1c51293c1b15e4ba8d45bbc7891ba053f90278ec2698f3562c993e145fe7
KUBECTL_VERSION ?= v1.37.1
# SHA-256 of the linux/amd64 kubectl binary. Set it for another platform.
KUBECTL_SHA256 ?= 65691ff77eb6fa44c908b77a1082c9f092c3b9733b5cefabec0d1104890e21a8
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

.PHONY: kind
kind: $(KIND) ## Download kind locally if necessary.
$(KIND): $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,$(KIND_VERSION))

.PHONY: kubectl
kubectl: $(KUBECTL) ## Download kubectl locally if necessary and check its SHA-256.
$(KUBECTL): $(LOCALBIN)
	@[ -f "$(KUBECTL)-$(KUBECTL_VERSION)" ] || { \
		set -e; \
		curl -fsSL -o "$(KUBECTL)-$(KUBECTL_VERSION).tmp" \
			"https://dl.k8s.io/release/$(KUBECTL_VERSION)/bin/$$(go env GOOS)/$$(go env GOARCH)/kubectl"; \
		echo "$(KUBECTL_SHA256)  $(KUBECTL)-$(KUBECTL_VERSION).tmp" | sha256sum -c -; \
		chmod +x "$(KUBECTL)-$(KUBECTL_VERSION).tmp"; \
		mv "$(KUBECTL)-$(KUBECTL_VERSION).tmp" "$(KUBECTL)-$(KUBECTL_VERSION)"; \
	}
	ln -sf "$(KUBECTL)-$(KUBECTL_VERSION)" "$(KUBECTL)"

.PHONY: trivy
trivy: $(TRIVY) ## Download trivy (linux/amd64) locally if necessary and check its SHA-256.
$(TRIVY): $(LOCALBIN)
	@[ -f "$(TRIVY)-$(TRIVY_VERSION)" ] || { \
		set -e; \
		tgz="$(LOCALBIN)/trivy_$(TRIVY_VERSION).tar.gz"; \
		curl -fsSL -o "$$tgz" \
			"https://github.com/aquasecurity/trivy/releases/download/v$(TRIVY_VERSION)/trivy_$(TRIVY_VERSION)_Linux-64bit.tar.gz"; \
		echo "$(TRIVY_SHA256)  $$tgz" | sha256sum -c -; \
		tar -xzf "$$tgz" -C "$(LOCALBIN)" trivy; \
		mv "$(LOCALBIN)/trivy" "$(TRIVY)-$(TRIVY_VERSION)"; \
		rm -f "$$tgz"; \
	}
	ln -sf "$(TRIVY)-$(TRIVY_VERSION)" "$(TRIVY)"

.PHONY: actionlint
actionlint: $(ACTIONLINT) ## Download actionlint locally if necessary.
$(ACTIONLINT): $(LOCALBIN)
	$(call go-install-tool,$(ACTIONLINT),github.com/rhysd/actionlint/cmd/actionlint,$(ACTIONLINT_VERSION))

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Download govulncheck locally if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

# go-install-tool installs package $(2) at version $(3) as $(1), if it is not there.
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef

##@ Helm Deployment

## Helm binary to use for deploying the chart (pinned, see HELM_VERSION)
HELM ?= $(LOCALBIN)/helm
HELM_VERSION ?= v4.3.1
## SHA-256 of helm-$(HELM_VERSION)-linux-amd64.tar.gz. Set it for another platform.
HELM_SHA256 ?= 0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b
## Namespace to deploy the Helm release
HELM_NAMESPACE ?= offpeak-system
## Name of the Helm release
HELM_RELEASE ?= offpeak
## Path to the Helm chart directory
HELM_CHART_DIR ?= dist/chart
## Additional arguments to pass to helm commands
HELM_EXTRA_ARGS ?=

.PHONY: install-helm
install-helm: $(HELM) ## Download Helm locally if necessary and check its SHA-256.
$(HELM): $(LOCALBIN)
	@[ -f "$(HELM)-$(HELM_VERSION)" ] || { \
		set -e; \
		tgz="$(LOCALBIN)/helm-$(HELM_VERSION).tar.gz"; \
		curl -fsSL -o "$$tgz" "https://get.helm.sh/helm-$(HELM_VERSION)-$$(go env GOOS)-$$(go env GOARCH).tar.gz"; \
		echo "$(HELM_SHA256)  $$tgz" | sha256sum -c -; \
		tar -xzf "$$tgz" -C "$(LOCALBIN)" --strip-components=1 "$$(go env GOOS)-$$(go env GOARCH)/helm"; \
		mv "$(LOCALBIN)/helm" "$(HELM)-$(HELM_VERSION)"; \
		rm -f "$$tgz"; \
	}
	ln -sf "$(HELM)-$(HELM_VERSION)" "$(HELM)"

.PHONY: helm-deploy
helm-deploy: install-helm ## Deploy manager to the K8s cluster via Helm. Specify an image with IMG.
	IMG="$(IMG)"; $(HELM) upgrade --install $(HELM_RELEASE) $(HELM_CHART_DIR) \
		--namespace $(HELM_NAMESPACE) \
		--create-namespace \
		--set manager.image.repository=$${IMG%:*} \
		--set manager.image.tag=$${IMG##*:} \
		--wait \
		--timeout 5m \
		$(HELM_EXTRA_ARGS)

.PHONY: check-chart
check-chart: manifests install-helm ## Check that the chart CRDs match config/crd/bases, and lint the chart.
	hack/check-chart-crds.sh
	"$(HELM)" lint dist/chart

.PHONY: test-chart
test-chart: kind kubectl install-helm ## Install the Helm chart on a throwaway Kind cluster and check the controller.
	hack/test-chart.sh

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the Helm release from the K8s cluster.
	$(HELM) uninstall $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-status
helm-status: ## Show Helm release status.
	$(HELM) status $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-history
helm-history: ## Show Helm release history.
	$(HELM) history $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)

.PHONY: helm-rollback
helm-rollback: ## Rollback to previous Helm release.
	$(HELM) rollback $(HELM_RELEASE) --namespace $(HELM_NAMESPACE)
