/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// OperatorConfig holds all pack-level configuration parsed from environment variables.
// It is loaded once at startup and passed to reconcilers.
type OperatorConfig struct {
	BaseDomain          string // LLM_BASE_DOMAIN (required)
	ExternalGatewayName string // LLM_EXTERNAL_GATEWAY_NAME (required)
	ExternalGatewayNS   string // LLM_EXTERNAL_GATEWAY_NAMESPACE (default: "envoy-gateway-system")
	InternalGatewayName string // LLM_INTERNAL_GATEWAY_NAME (required)
	InternalGatewayNS   string // LLM_INTERNAL_GATEWAY_NAMESPACE (default: "envoy-gateway-system")
	OIDCIssuerURL       string // LLM_OIDC_ISSUER_URL (required)
	OIDCGroupsClaim     string // LLM_OIDC_GROUPS_CLAIM (default: "groups")
	OIDCAudience        string // LLM_OIDC_AUDIENCE (optional, empty string means no audience check)
	DefaultServingImage string // LLM_DEFAULT_SERVING_IMAGE (default: "ghcr.io/llm-d/llm-d-cuda:v0.7.0")
	// DefaultEPPImage is the llm-d-inference-scheduler (Endpoint Picker)
	// image used for every model's EPP Deployment. Configurable so it can
	// be re-pinned (or pointed at a mirrored/air-gapped registry) without
	// rebuilding the operator. Default tracks the llm-d release the pack is
	// pinned to.
	DefaultEPPImage         string // LLM_DEFAULT_EPP_IMAGE (default: "ghcr.io/llm-d/llm-d-inference-scheduler:v0.8.0")
	DefaultStorageClassName string // LLM_DEFAULT_STORAGE_CLASS_NAME (optional, empty = cluster default)
	// APIKeysNamespace is no longer used to PLACE Secrets - per #59 the
	// API-key Secrets and metadata ConfigMaps now live in the LLMModel's
	// own namespace so SecurityPolicy.apiKeyAuth.credentialRefs can
	// reference them without a cross-namespace ref. This field is kept
	// only so the finalizer can clean up legacy resources from clusters
	// originally deployed against a separate api-keys namespace; new
	// deployments leave it empty.
	APIKeysNamespace string // LLM_API_KEYS_NAMESPACE (legacy cleanup hint, default empty)
	// OperatorNamespace is the namespace the operator runs in. Used as
	// the home namespace for the shared-TLS Certificate and Secret so
	// the cert lives in one well-known place independent of per-model
	// namespaces. Injected via the downward API on the Deployment.
	// Optional: empty is valid (webhook "test mode" per design.md
	// §Single-namespace deployment model; envtest doesn't run inside a
	// pod). The shared-TLS reconciler skips on empty rather than
	// failing the whole operator.
	OperatorNamespace string // POD_NAMESPACE (optional; empty = test mode)
	// ClusterIssuerName is the cert-manager ClusterIssuer used to issue
	// the shared-TLS certificate covering llm.<baseDomain> and
	// llm-internal.<baseDomain>. HTTP-01 is the assumed challenge type;
	// wildcards are not supported by this pack.
	ClusterIssuerName string // LLM_CLUSTER_ISSUER_NAME (default: "letsencrypt-production")
	// TLSSecretName, when non-empty, switches shared TLS to
	// bring-your-own-certificate mode: the operator does NOT create a
	// cert-manager Certificate (and deletes the one it previously
	// managed, if any). The shared Gateway listeners and ReferenceGrants
	// point at this pre-provisioned kubernetes.io/tls Secret in
	// OperatorNamespace instead. The certificate must cover
	// llm.<baseDomain> and llm-internal.<baseDomain> (two SANs or a
	// wildcard). Intended for air-gapped / private-CA environments where
	// ACME issuance is impossible. ClusterIssuerName is ignored while
	// this is set.
	TLSSecretName string // LLM_TLS_SECRET_NAME (optional; empty = cert-manager mode)
	// ManageSharedListeners controls whether the operator patches HTTPS
	// listeners onto the external and internal shared Gateways. When
	// false the operator still creates the shared Certificate but leaves
	// listener management to the cluster admin (runbook). Default true.
	ManageSharedListeners bool // LLM_MANAGE_SHARED_LISTENERS (default: "true")
	// RouteRequestTimeout is the HTTP request timeout stamped onto every
	// AIGatewayRoute the operator generates (spec.rules[].timeouts.request),
	// for both the external llm.<baseDomain> and internal
	// llm-internal.<baseDomain> endpoints. The Envoy AI Gateway propagates it
	// to the HTTPRoute it renders and Envoy enforces it as the upstream
	// response timeout. Empty leaves the field unset so the gateway's own 60s
	// default applies - which is too short for many LLM generations and,
	// because an explicit route timeout always wins, is not overridable by a
	// BackendTrafficPolicy. The Helm chart leaves this empty by default. Set
	// it to a Gateway API duration string such as "600s" or "10m" to raise
	// the timeout; invalid values are rejected at operator startup.
	RouteRequestTimeout string // LLM_ROUTE_REQUEST_TIMEOUT (optional; empty = gateway 60s default)
}

// getEnvOrDefault returns the value of the environment variable named by key,
// or defaultVal if the variable is not set or is empty.
func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// getEnvBool parses an environment variable as a boolean. Accepted true values
// are "true", "1", "yes", "on" (case-insensitive). Anything else returns false.
// An unset or empty variable returns defaultVal.
func getEnvBool(key string, defaultVal bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "" {
		return defaultVal
	}
	switch v {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// gatewayAPIDurationRE matches the Gateway API Duration format (GEP-2257)
// required by AIGatewayRoute spec.rules[].timeouts.request. time.ParseDuration
// is not a substitute: it accepts units the CRD rejects (ns, us), allows
// decimals, and treats bare numbers differently, so it would disagree with the
// API server's own validation of the value.
var gatewayAPIDurationRE = regexp.MustCompile(`^([0-9]{1,5}(h|m|s|ms)){1,4}$`)

// LoadFromEnv reads configuration from environment variables and returns an
// OperatorConfig. It returns a descriptive error listing all missing required
// variables if any are absent.
func LoadFromEnv() (*OperatorConfig, error) {
	var missing []string

	require := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg := &OperatorConfig{
		BaseDomain:              require("LLM_BASE_DOMAIN"),
		ExternalGatewayName:     require("LLM_EXTERNAL_GATEWAY_NAME"),
		ExternalGatewayNS:       getEnvOrDefault("LLM_EXTERNAL_GATEWAY_NAMESPACE", "envoy-gateway-system"),
		InternalGatewayName:     require("LLM_INTERNAL_GATEWAY_NAME"),
		InternalGatewayNS:       getEnvOrDefault("LLM_INTERNAL_GATEWAY_NAMESPACE", "envoy-gateway-system"),
		OIDCIssuerURL:           require("LLM_OIDC_ISSUER_URL"),
		OIDCGroupsClaim:         getEnvOrDefault("LLM_OIDC_GROUPS_CLAIM", "groups"),
		OIDCAudience:            os.Getenv("LLM_OIDC_AUDIENCE"),
		DefaultServingImage:     getEnvOrDefault("LLM_DEFAULT_SERVING_IMAGE", "ghcr.io/llm-d/llm-d-cuda:v0.7.0"),
		DefaultEPPImage:         getEnvOrDefault("LLM_DEFAULT_EPP_IMAGE", "ghcr.io/llm-d/llm-d-inference-scheduler:v0.8.0"),
		DefaultStorageClassName: os.Getenv("LLM_DEFAULT_STORAGE_CLASS_NAME"),
		APIKeysNamespace:        os.Getenv("LLM_API_KEYS_NAMESPACE"),
		OperatorNamespace:       os.Getenv("POD_NAMESPACE"),
		ClusterIssuerName:       getEnvOrDefault("LLM_CLUSTER_ISSUER_NAME", "letsencrypt-production"),
		TLSSecretName:           os.Getenv("LLM_TLS_SECRET_NAME"),
		ManageSharedListeners:   getEnvBool("LLM_MANAGE_SHARED_LISTENERS", true),
		RouteRequestTimeout:     os.Getenv("LLM_ROUTE_REQUEST_TIMEOUT"),
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	if cfg.RouteRequestTimeout != "" && !gatewayAPIDurationRE.MatchString(cfg.RouteRequestTimeout) {
		return nil, fmt.Errorf("LLM_ROUTE_REQUEST_TIMEOUT %q is not a valid Gateway API duration string (e.g. 600s, 10m)", cfg.RouteRequestTimeout)
	}

	return cfg, nil
}
