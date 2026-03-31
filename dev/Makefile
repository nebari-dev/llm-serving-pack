CLUSTER_NAME ?= llm-serving-test

.PHONY: setup teardown build-images load-images deploy deploy-operator deploy-key-manager apply-test-model logs-operator logs-key-manager clean help

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

setup: ## Create kind cluster and install dependencies
	kind create cluster --name $(CLUSTER_NAME)
	# cert-manager
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
	kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
	# Gateway API CRDs
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml
	# GIE CRDs (v1.4.0, includes graduated k8s.io group)
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/v1.4.0/manifests.yaml
	# Envoy Gateway
	helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.3.0 -n envoy-gateway-system --create-namespace
	kubectl -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=120s
	# Envoy AI Gateway
	helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm --version v0.5.0 -n envoy-ai-gateway-system --create-namespace
	helm upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm --version v0.5.0 -n envoy-ai-gateway-system
	kubectl -n envoy-ai-gateway-system wait --timeout=120s deployment/ai-gateway-controller --for=condition=Available
	# LLMModel CRD
	kubectl apply -f ../charts/nebari-llm-serving/crds/llmmodel-crd.yaml
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

deploy-key-manager: ## Deploy key manager
	kubectl apply -f manifests/key-manager.yaml
	kubectl -n llm-operator-system rollout status deployment/llm-key-manager --timeout=60s

apply-test-model: ## Apply test model
	kubectl apply -f manifests/test-model.yaml

logs-operator: ## Tail operator logs
	kubectl -n llm-operator-system logs -f deployment/llm-operator

logs-key-manager: ## Tail key manager logs
	kubectl -n llm-operator-system logs -f deployment/llm-key-manager

clean: teardown ## Alias for teardown
