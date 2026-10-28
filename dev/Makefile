CLUSTER_NAME ?= llm-serving-test

# Pin every kubectl to the local kind cluster's context. Without this, the dev
# targets act on whatever your current kube-context happens to be - which could
# be a remote/production cluster - and would deploy the auth-bypassing dev-mode
# key-manager there. kind names its context "kind-<cluster>".
KUBECTL := kubectl --context kind-$(CLUSTER_NAME)

# Dependency versions. These move together: Envoy AI Gateway v1.1.x uses
# Envoy Gateway v1.8.1 and Gateway API v1.5.1 (see
# https://aigateway.envoyproxy.io/docs/compatibility/). Bump them as a set
# when upgrading the AI Gateway.
CERT_MANAGER_VERSION ?= v1.17.2
GATEWAY_API_VERSION ?= v1.5.1
GIE_VERSION ?= v1.4.0
ENVOY_GATEWAY_VERSION ?= v1.8.1
AI_GATEWAY_VERSION ?= v1.1.0
# OpenMeter collector image the usage collector runs; keep in step with
# usage.collector.image in the chart values.
USAGE_COLLECTOR_IMAGE ?= ghcr.io/openmeterio/benthos-collector:v1.0.0-beta.234@sha256:cdc35416af4ededfbdb531463a3135127274f595c4b4b44573e685e82aac4fc4
# CloudNativePG operator, which Nebari installs on every cluster; the bundled
# OpenMeter's Postgres is a CloudNativePG Cluster. 0.29.1 ships operator
# 1.30.1, which fixes CVE-2026-84304 (gRPC) in 1.30.0.
CNPG_CHART_VERSION ?= 0.29.1
CNPG_IMAGE_TAG ?= 1.30.1@sha256:923c267ec29636db3bee20f993d0ec4973fa22998e1adad37da79e4d32b5bc07

# Helm 4 applies the eg chart's bundled Gateway API CRDs (crds/) server-side.
# Those are the experimental channel, and the safe-upgrades admission policy
# that Gateway API v1.5 installs with the standard channel in `setup` refuses
# experimental CRDs on top of standard ones, so the chart install fails. On
# Helm 4, `setup` therefore applies only Envoy Gateway's own CRDs from the
# chart and installs the chart without its crds/ (and without a second copy of
# the admission policy). Helm 3 skips already-present crds/, so it needs none
# of this - keep it empty there. Empty when helm is missing (setup fails later).
HELM_MAJOR := $(shell helm version --template '{{.Version}}' 2>/dev/null | sed -E 's/^v?([0-9]+).*/\1/')
EG_CHART := oci://docker.io/envoyproxy/gateway-helm
EG_SKIP_BUNDLED_CRDS := $(if $(filter-out 1 2 3,$(HELM_MAJOR)),--skip-crds --set crds.gatewayAPI.safeUpgradePolicy.enabled=false,)

.PHONY: setup teardown build-images load-images deploy deploy-operator deploy-key-manager deploy-keycloak apply-test-model apply-passthrough-model create-openrouter-secret run-dev ui test-auth test-model-scope deploy-cnpg usage-e2e usage-tracer usage-forgery-check test-usage-collector logs-operator logs-key-manager logs-keycloak pf-key-manager pf-keycloak clean help

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
	# Gateway API CRDs (standard channel).
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
	@if [ -n "$(EG_SKIP_BUNDLED_CRDS)" ]; then \
		tmp=$$(mktemp -d) && \
		helm pull $(EG_CHART) --version $(ENVOY_GATEWAY_VERSION) --untar --untardir $$tmp && \
		$(KUBECTL) apply --server-side -f $$tmp/gateway-helm/charts/crds/crds/generated/ && \
		rm -rf $$tmp; \
	fi
	helm upgrade -i eg $(EG_CHART) --version $(ENVOY_GATEWAY_VERSION) -n envoy-gateway-system --create-namespace $(EG_SKIP_BUNDLED_CRDS) -f eg-extension-values.yaml
	$(KUBECTL) -n envoy-gateway-system rollout status deployment/envoy-gateway --timeout=120s
	# CRDs (served LLMModels and external-provider PassthroughModels)
	$(KUBECTL) apply -f ../charts/nebari-llm-serving/crds/llmmodel-crd.yaml
	$(KUBECTL) apply -f ../charts/nebari-llm-serving/crds/passthroughmodel-crd.yaml
	# Test Gateways
	$(KUBECTL) apply -f gateways.yaml
	$(MAKE) deploy-cnpg

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

deploy-cnpg: ## Install the CloudNativePG operator (as Nebari does), for the bundled OpenMeter's Postgres
	helm upgrade -i cnpg cloudnative-pg --repo https://cloudnative-pg.github.io/charts --version $(CNPG_CHART_VERSION) \
		--kube-context kind-$(CLUSTER_NAME) -n cnpg-system --create-namespace --set image.tag=$(CNPG_IMAGE_TAG) --wait

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

ui: ## Run the React UI dev server against the local cluster (alias for run-dev)
	@echo "The UI is now the React app in ../frontend, served by Vite at http://localhost:5173."
	@echo "Starting the one-command dev environment (cluster + models + API port-forward + Vite)..."
	./run-dev.sh

test-auth: ## Verify the key-manager's real bearer-token JWKS validation (deploys Keycloak, flips off dev mode, mints a token, asserts 200/401). KEEP_AUTH=1 leaves real-auth mode on.
	./test-auth.sh

test-model-scope: ## Verify per-model API-key scoping and x-llm-client-id anti-spoofing through the gateway (deploys two mock models, asserts the 200/403/401 matrix). KEEP=1 leaves the models.
	./test-model-scope.sh

usage-e2e: ## Usage metering end to end with the bundled OpenMeter and usage console: alice and bob call the mock provider, per-person and per-team totals read back from OpenMeter's meters and the console's API (run `make setup` first)
	./usage-e2e.sh

usage-forgery-check: ## Check callers cannot choose the identity or request id the usage access log records (after usage-e2e)
	./usage-forgery-check.sh

usage-tracer: ## Web page that sends one request and follows it through the gateway log, collector, Kafka, ClickHouse, OpenMeter and the console, at http://127.0.0.1:18894 (run `make usage-e2e` first)
	python3 usage-tracer/server.py

test-usage-collector: ## Run the usage collector pipeline tests in the pinned collector image
	docker run --rm -e OPENMETER_URL=http://unused -e PACK_NAMESPACE=llm-system -v "$(CURDIR)/../charts/nebari-llm-serving/files:/t:ro" \
		$(USAGE_COLLECTOR_IMAGE) test /t/usage-collector.yaml

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
