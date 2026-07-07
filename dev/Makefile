CLUSTER_NAME ?= llm-serving-test

# Pin every kubectl to the local kind cluster's context. Without this, the dev
# targets act on whatever your current kube-context happens to be - which could
# be a remote/production cluster - and would deploy the auth-bypassing dev-mode
# key-manager there. kind names its context "kind-<cluster>".
KUBECTL := kubectl --context kind-$(CLUSTER_NAME)

# Dependency versions. These move together: Envoy AI Gateway v0.5.x requires
# Envoy Gateway v1.6.x and Gateway API v1.4.0 (see
# https://aigateway.envoyproxy.io/docs/compatibility/). Bump them as a set
# when upgrading the AI Gateway.
CERT_MANAGER_VERSION ?= v1.17.2
GATEWAY_API_VERSION ?= v1.4.0
GIE_VERSION ?= v1.4.0
ENVOY_GATEWAY_VERSION ?= v1.6.7
AI_GATEWAY_VERSION ?= v0.5.0

# Helm 4 applies the eg chart's bundled Gateway API CRDs (crds/) server-side,
# which conflicts with the standalone install in `setup` (owned by a different
# field manager). Force ownership so the chart wins. Helm 3 has no server-side
# apply and skips already-present crds/, so it neither needs nor accepts this
# flag - keep it empty there. Empty when helm is missing (setup fails later).
HELM_MAJOR := $(shell helm version --template '{{.Version}}' 2>/dev/null | sed -E 's/^v?([0-9]+).*/\1/')
HELM_FORCE_CONFLICTS := $(if $(filter-out 1 2 3,$(HELM_MAJOR)),--force-conflicts,)

.PHONY: setup teardown build-images load-images deploy deploy-operator deploy-key-manager deploy-keycloak apply-test-model apply-passthrough-model create-openrouter-secret run-dev ui logs-operator logs-key-manager logs-keycloak pf-key-manager pf-keycloak clean help

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

run-dev: ## One-command UI dev environment: cluster + models + port-forward + hot-reload UI server (needs dev/.env with OPENROUTER_API_KEY)
	./run-dev.sh

setup: ## Create kind cluster and install dependencies
	# Idempotent: skip creation if the cluster already exists so a setup that
	# died midway can be re-run without `make teardown` first.
	@kind get clusters | grep -qx $(CLUSTER_NAME) || kind create cluster --name $(CLUSTER_NAME)
	# cert-manager
	$(KUBECTL) apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml
	$(KUBECTL) -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
	# Gateway API CRDs. Server-side apply so the field ownership matches the
	# eg chart's own SSA on Helm 4, letting it cleanly take co-ownership with
	# --force-conflicts (see HELM_FORCE_CONFLICTS above) instead of erroring.
	$(KUBECTL) apply --server-side --force-conflicts -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
	# GIE CRDs (includes the graduated inference.networking.k8s.io group)
	$(KUBECTL) apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/$(GIE_VERSION)/manifests.yaml
	# Envoy AI Gateway first: envoy-gateway's extensionManager below points at
	# the ai-gateway-controller XDS service, so bring it up before reconfiguring
	# envoy-gateway to avoid noisy connection-refused logs during translation.
	helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm --version $(AI_GATEWAY_VERSION) -n envoy-ai-gateway-system --create-namespace
	helm upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm --version $(AI_GATEWAY_VERSION) -n envoy-ai-gateway-system
	$(KUBECTL) -n envoy-ai-gateway-system wait --timeout=120s deployment/ai-gateway-controller --for=condition=Available
	# Envoy Gateway, wired with the AI Gateway ext_proc extension (enableBackend,
	# extensionManager, backendResources). Without this the per-model routing
	# layer 404s and passthrough upstreams never get a TLS transport socket.
	helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm --version $(ENVOY_GATEWAY_VERSION) -n envoy-gateway-system --create-namespace $(HELM_FORCE_CONFLICTS) -f eg-extension-values.yaml
	$(KUBECTL) -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=120s
	# CRDs (served LLMModels and external-provider PassthroughModels)
	$(KUBECTL) apply -f ../charts/nebari-llm-serving/crds/llmmodel-crd.yaml
	$(KUBECTL) apply -f ../charts/nebari-llm-serving/crds/passthroughmodel-crd.yaml
	# Test Gateways
	$(KUBECTL) apply -f gateways.yaml

teardown: ## Delete kind cluster
	kind delete cluster --name $(CLUSTER_NAME)

build-images: ## Build all Docker images
	docker build -f ../operator/Dockerfile -t llm-operator:dev ../operator/
	docker build -f ../key-manager/Dockerfile -t llm-key-manager:dev ../
	docker build -t mock-vllm:dev mock-vllm/

load-images: ## Load images into kind
	kind load docker-image llm-operator:dev llm-key-manager:dev mock-vllm:dev --name $(CLUSTER_NAME)

deploy: deploy-operator deploy-key-manager ## Deploy everything

deploy-operator: ## Deploy operator
	$(KUBECTL) apply -f manifests/operator.yaml
	$(KUBECTL) apply -f manifests/cert-manager-config.yaml
	$(KUBECTL) apply -f manifests/webhook.yaml
	$(KUBECTL) -n llm-operator-system rollout status deployment/llm-operator --timeout=60s

deploy-key-manager: ## Deploy key manager (dev mode: auth bypassed, dev identity injected)
	$(KUBECTL) apply -f manifests/key-manager.yaml
	$(KUBECTL) -n llm-operator-system rollout status deployment/llm-key-manager --timeout=60s

deploy-keycloak: ## Deploy dev Keycloak (imports the nebari realm)
	$(KUBECTL) create namespace keycloak --dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) create configmap keycloak-realm-import -n keycloak \
		--from-file=realm-nebari.json=keycloak/realm-nebari.json \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) apply -f manifests/keycloak.yaml
	$(KUBECTL) -n keycloak rollout status deployment/keycloak --timeout=180s

apply-test-model: ## Apply served test model (mock vLLM)
	$(KUBECTL) apply -f manifests/test-model.yaml

create-openrouter-secret: ## Create the OpenRouter provider credential (OPENROUTER_API_KEY env var required)
	@test -n "$(OPENROUTER_API_KEY)" || { echo "set OPENROUTER_API_KEY"; exit 1; }
	$(KUBECTL) -n llm-operator-system create secret generic openrouter-api-key \
		--from-literal=apiKey="$(OPENROUTER_API_KEY)" \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -

apply-passthrough-model: ## Apply the OpenRouter PassthroughModel (run create-openrouter-secret first)
	$(KUBECTL) apply -f manifests/passthrough-test-model.yaml

ui: ## Port-forward the key-manager UI to http://localhost:8080 (dev mode, no login)
	@echo "key-manager UI at http://localhost:8080 (dev mode injects user 'dev')"
	$(KUBECTL) -n llm-operator-system port-forward svc/llm-key-manager 8080:8080

logs-operator: ## Tail operator logs
	$(KUBECTL) -n llm-operator-system logs -f deployment/llm-operator

logs-key-manager: ## Tail key manager logs
	$(KUBECTL) -n llm-operator-system logs -f deployment/llm-key-manager

logs-keycloak: ## Tail Keycloak logs
	$(KUBECTL) -n keycloak logs -f deployment/keycloak

pf-key-manager: ## Port-forward key-manager API to localhost:8080
	$(KUBECTL) -n llm-operator-system port-forward svc/llm-key-manager 8080:8080

pf-keycloak: ## Port-forward Keycloak to localhost:8180
	$(KUBECTL) -n keycloak port-forward svc/keycloak 8180:8080

clean: teardown ## Alias for teardown
