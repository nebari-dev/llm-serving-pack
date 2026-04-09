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
	DefaultServingImage    string // LLM_DEFAULT_SERVING_IMAGE (default: "ghcr.io/llm-d/llm-d-cuda:v0.5.1")
	DefaultStorageClassName string // LLM_DEFAULT_STORAGE_CLASS_NAME (optional, empty = cluster default)
	APIKeysNamespace       string // LLM_API_KEYS_NAMESPACE (default: "llm-api-keys")
}

// getEnvOrDefault returns the value of the environment variable named by key,
// or defaultVal if the variable is not set or is empty.
func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

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
		BaseDomain:          require("LLM_BASE_DOMAIN"),
		ExternalGatewayName: require("LLM_EXTERNAL_GATEWAY_NAME"),
		ExternalGatewayNS:   getEnvOrDefault("LLM_EXTERNAL_GATEWAY_NAMESPACE", "envoy-gateway-system"),
		InternalGatewayName: require("LLM_INTERNAL_GATEWAY_NAME"),
		InternalGatewayNS:   getEnvOrDefault("LLM_INTERNAL_GATEWAY_NAMESPACE", "envoy-gateway-system"),
		OIDCIssuerURL:       require("LLM_OIDC_ISSUER_URL"),
		OIDCGroupsClaim:     getEnvOrDefault("LLM_OIDC_GROUPS_CLAIM", "groups"),
		OIDCAudience:        os.Getenv("LLM_OIDC_AUDIENCE"),
		DefaultServingImage:    getEnvOrDefault("LLM_DEFAULT_SERVING_IMAGE", "ghcr.io/llm-d/llm-d-cuda:v0.5.1"),
		DefaultStorageClassName: os.Getenv("LLM_DEFAULT_STORAGE_CLASS_NAME"),
		APIKeysNamespace:       getEnvOrDefault("LLM_API_KEYS_NAMESPACE", "llm-api-keys"),
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}
