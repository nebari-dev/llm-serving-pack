package v1alpha1

import "fmt"

const (
	BackendOpenAI              = "OpenAI"
	BackendBedrock             = "Bedrock"
	CredentialAPIKey           = "APIKey"
	CredentialWorkloadIdentity = "WorkloadIdentity"
)

// HostingOrigin is the value the gateway's /v1/models endpoint reports as
// each model's owned_by: which kind of backend serves it. The values are part
// of the pack's public API — clients key hosting/compliance labeling off them
// — so they change only with a documented contract change. The pack states
// where a model is served; it makes no data-retention claims.
type HostingOrigin string

const (
	// OriginSelfHosted marks models served in-cluster by an LLMModel.
	OriginSelfHosted HostingOrigin = "self-hosted"
	// OriginOpenAI marks models proxied to an OpenAI-compatible provider.
	OriginOpenAI HostingOrigin = "openai"
	// OriginBedrock marks models proxied to AWS Bedrock.
	OriginBedrock HostingOrigin = "bedrock"
)

// Origin maps the admission-validated backend selection to the advertised
// hosting origin. An omitted backend is the legacy OpenAI-compatible path,
// mirroring provider.Resolve. A backend type without a mapping is an error so
// a new provider variant cannot ship with an unlabeled origin.
func (p ProviderSpec) Origin() (HostingOrigin, error) {
	backend := BackendOpenAI
	if p.Backend != nil {
		backend = p.Backend.Type
	}
	switch backend {
	case BackendOpenAI:
		return OriginOpenAI, nil
	case BackendBedrock:
		return OriginBedrock, nil
	default:
		return "", fmt.Errorf("backend type %q has no hosting origin mapping", backend)
	}
}
