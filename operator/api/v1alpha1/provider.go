package v1alpha1

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// BackendOpenAI uses the OpenAI-compatible wire format.
	BackendOpenAI = "OpenAI"
	// BackendBedrock uses AWS Bedrock Converse.
	BackendBedrock = "Bedrock"
	// CredentialAPIKey injects a provider key from a Secret.
	CredentialAPIKey = "APIKey"
	// CredentialWorkloadIdentity uses the cloud SDK credential chain.
	CredentialWorkloadIdentity = "WorkloadIdentity"
)

var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

// ResolvedProvider is the validated input to the shared gateway builders.
// Backend-specific interpretation belongs in Resolve, not routing or access control.
// +kubebuilder:object:generate=false
type ResolvedProvider struct {
	Hostname       string
	Port           int32
	SchemaName     string
	SchemaVersion  string
	CredentialType string
	Region         string
	SecretName     string
}

// EndpointHostname returns the effective address, including a Bedrock default.
// Use Resolve before provisioning resources; this accessor also serves display clients.
func (p ProviderSpec) EndpointHostname() string {
	if p.Hostname != "" || p.Backend == nil || p.Backend.Type != BackendBedrock || p.Backend.Bedrock == nil {
		return p.Hostname
	}
	suffix := "amazonaws.com"
	if strings.HasPrefix(p.Backend.Bedrock.Region, "cn-") {
		suffix += ".cn"
	}
	return "bedrock-runtime." + p.Backend.Bedrock.Region + "." + suffix
}

// Resolve validates a provider and normalizes legacy defaults once for all consumers.
func (p ProviderSpec) Resolve() (*ResolvedProvider, error) {
	resolved := &ResolvedProvider{Hostname: p.EndpointHostname(), Port: p.Port, SecretName: p.CredentialSecretName}
	if resolved.Port == 0 {
		resolved.Port = 443
	}
	backend := BackendOpenAI
	if p.Backend != nil {
		backend = p.Backend.Type
	}
	switch backend {
	case BackendOpenAI:
		if p.Backend != nil && p.Backend.Bedrock != nil {
			return nil, fmt.Errorf("spec.provider.backend.bedrock is only valid with type=Bedrock")
		}
		resolved.SchemaName, resolved.SchemaVersion = BackendOpenAI, p.SchemaVersion
		if resolved.SchemaVersion == "" {
			resolved.SchemaVersion = "v1"
		}
		resolved.CredentialType = CredentialAPIKey
	case BackendBedrock:
		if p.Backend.Bedrock == nil || !awsRegionPattern.MatchString(p.Backend.Bedrock.Region) {
			return nil, fmt.Errorf("spec.provider.backend.bedrock.region must be an AWS region such as us-west-2")
		}
		// v1 may be present from legacy CRD defaulting; never send it to Converse.
		if p.SchemaVersion != "" && p.SchemaVersion != "v1" {
			return nil, fmt.Errorf("spec.provider.schemaVersion is only configurable for OpenAI")
		}
		resolved.SchemaName, resolved.Region = "AWSBedrock", p.Backend.Bedrock.Region
		resolved.CredentialType = CredentialWorkloadIdentity
	default:
		return nil, fmt.Errorf("spec.provider.backend.type must be OpenAI or Bedrock")
	}
	if p.Credential != nil && p.Credential.Type != resolved.CredentialType {
		return nil, fmt.Errorf("spec.provider.credential.type must be %s for backend %s", resolved.CredentialType, backend)
	}
	if resolved.CredentialType == CredentialAPIKey && strings.TrimSpace(p.CredentialSecretName) == "" {
		return nil, fmt.Errorf("spec.provider.credentialSecretName must not be empty")
	}
	if resolved.CredentialType == CredentialWorkloadIdentity && p.CredentialSecretName != "" {
		return nil, fmt.Errorf("spec.provider.credentialSecretName must be omitted for workload identity")
	}
	if resolved.Hostname == "" {
		return nil, fmt.Errorf("spec.provider.hostname must not be empty")
	}
	if problems := validation.IsDNS1123Subdomain(resolved.Hostname); len(problems) > 0 {
		return nil, fmt.Errorf("spec.provider.hostname must be a bare hostname: %s", strings.Join(problems, "; "))
	}
	if resolved.Port < 1 || resolved.Port > 65535 {
		return nil, fmt.Errorf("spec.provider.port must be between 1 and 65535")
	}
	return resolved, nil
}
