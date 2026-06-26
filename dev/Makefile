CLUSTER_NAME ?= llm-serving-test

# Dependency versions. These move together: Envoy AI Gateway v0.5.x requires
# Envoy Gateway v1.6.x and Gateway API v1.4.0 (see
# https://aigateway.envoyproxy.io/docs/compatibility/). Bump them as a set
# when upgrading the AI Gateway.
CERT_MANAGER_VERSION ?= v1.17.2
GATEWAY_API_VERSION ?= v1.4.0
GIE_VERSION ?= v1.4.0
ENVOY_GATEWAY_VERSION ?= v1.6.7
AI_GATEWAY_VERSION ?= v0.5.0

.PHONY: setup teardown build-images load-images deploy deploy-operator deploy-key-manager apply-test-model apply-passthrough-model create-openrouter-secret ui logs-operator logs-key-manager clean help

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

setup: ## Create kind cluster and install dependencies
	kind create cluster --name $(CLUSTER_NAME)
	# cert-manager
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml
	kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
	# Gateway API CRDs
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
	# GIE CRDs (includes the graduated inference.networking.k8s.io group)
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/$(GIE_VERSION)/manifests.yaml
	# Envoy AI Gateway first: envoy-gateway's extensionManager below points at
	# the ai-gateway-controller XDS service, so bring it up before reconfiguring
	# envoy-gateway to avoid noisy connection-refused logs during translation.
	helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm --version $(AI_GATEWAY_VERSION) -n envoy-ai-gateway-system --create-namespace
	helm upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm --version $(AI_GATEWAY_VERSION) -n envoy-ai-gateway-system
	kubectl -n envoy-ai-gateway-system wait --timeout=120s deployment/ai-gateway-controller --for=condition=Available
	# Envoy Gateway, wired with the AI Gateway ext_proc extension (enableBackend,
	# extensionManager, backendResources). Without this the per-model routing
	# layer 404s and passthrough upstreams never get a TLS transport socket.
	helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm --version $(ENVOY_GATEWAY_VERSION) -n envoy-gateway-system --create-namespace -f eg-extension-values.yaml
	kubectl -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=120s
	# CRDs (served LLMModels and external-provider PassthroughModels)
	kubectl apply -f ../charts/nebari-llm-serving/crds/llmmodel-crd.yaml
	kubectl apply -f ../charts/nebari-llm-serving/crds/passthroughmodel-crd.yaml
	# Test Gateways
	kubectl apply -f gateways.yaml

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
	kubectl apply -f manifests/operator.yaml
	kubectl apply -f manifests/cert-manager-config.yaml
	kubectl apply -f manifests/webhook.yaml
	kubectl -n llm-operator-system rollout status deployment/llm-operator --timeout=60s

deploy-key-manager: ## Deploy key manager (dev mode: auth bypassed, dev identity injected)
	kubectl apply -f manifests/key-manager.yaml
	kubectl -n llm-operator-system rollout status deployment/llm-key-manager --timeout=60s

apply-test-model: ## Apply served test model (mock vLLM)
	kubectl apply -f manifests/test-model.yaml

create-openrouter-secret: ## Create the OpenRouter provider credential (OPENROUTER_API_KEY env var required)
	@test -n "$(OPENROUTER_API_KEY)" || { echo "set OPENROUTER_API_KEY"; exit 1; }
	kubectl -n llm-operator-system create secret generic openrouter-api-key \
		--from-literal=apiKey="$(OPENROUTER_API_KEY)" \
		--dry-run=client -o yaml | kubectl apply -f -

apply-passthrough-model: ## Apply the OpenRouter PassthroughModel (run create-openrouter-secret first)
	kubectl apply -f manifests/passthrough-test-model.yaml

ui: ## Port-forward the key-manager UI to http://localhost:8080 (dev mode, no login)
	@echo "key-manager UI at http://localhost:8080 (dev mode injects user 'dev')"
	kubectl -n llm-operator-system port-forward svc/llm-key-manager 8080:8080

logs-operator: ## Tail operator logs
	kubectl -n llm-operator-system logs -f deployment/llm-operator

logs-key-manager: ## Tail key manager logs
	kubectl -n llm-operator-system logs -f deployment/llm-key-manager

clean: teardown ## Alias for teardown
