package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func (p ProviderSpec) resolveBedrock() (*ResolvedProvider, error) {
	if p.Backend.Bedrock == nil || strings.TrimSpace(p.Backend.Bedrock.Region) == "" {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region must not be empty")
	}
	if err := p.requireCredential(CredentialWorkloadIdentity); err != nil {
		return nil, err
	}
	// v1 may be present from legacy CRD defaulting; never send it to Converse.
	if p.SchemaVersion != "" && p.SchemaVersion != "v1" {
		return nil, fmt.Errorf("spec.provider.schemaVersion is only configurable for OpenAI")
	}
	region := p.Backend.Bedrock.Region
	if !smithyhttp.ValidHostLabel(region) {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region must be a valid DNS label")
	}
	// AWS owns endpoint and partition rules. This resolver is local: admission
	// does not load credentials or call AWS. It also works for isolated regions.
	endpoint, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(
		context.Background(), bedrockruntime.EndpointParameters{Region: &region},
	)
	if err != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region: %w", err)
	}
	return &ResolvedProvider{
		Hostname:   endpoint.URI.Hostname(),
		SchemaName: "AWSBedrock",
		SecurityPolicy: map[string]interface{}{
			"type":           "AWSCredentials",
			"awsCredentials": map[string]interface{}{"region": region},
		},
	}, nil
}
