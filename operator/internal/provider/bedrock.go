package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func resolveBedrock(p llmv1alpha1.ProviderSpec) (*Resolved, error) {
	if p.Backend.Bedrock == nil || strings.TrimSpace(p.Backend.Bedrock.Region) == "" {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region must not be empty")
	}
	if err := requireCredential(p, llmv1alpha1.CredentialWorkloadIdentity); err != nil {
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
	// The hostname override exists for private endpoints. SigV4 signs for
	// spec region, so an AWS-owned hostname (public or VPC endpoint DNS)
	// must carry that region; custom private DNS names are accepted as-is.
	if override := strings.TrimSuffix(strings.ToLower(p.Hostname), "."); strings.Contains(override, ".amazonaws.") {
		if !slices.Contains(strings.Split(override, "."), region) {
			return nil, fmt.Errorf("spec.provider.hostname region does not match spec.provider.backend.bedrock.region %s; requests would be signed for the wrong region", region)
		}
	}
	// AWS owns endpoint and partition rules. This resolver is local: admission
	// does not load credentials or call AWS. It also works for isolated regions.
	endpoint, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(
		context.Background(), bedrockruntime.EndpointParameters{Region: &region},
	)
	if err != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region: %w", err)
	}
	return &Resolved{
		Hostname:   endpoint.URI.Hostname(),
		SchemaName: "AWSBedrock",
		SecurityPolicy: map[string]interface{}{
			"type":           "AWSCredentials",
			"awsCredentials": map[string]interface{}{"region": region},
		},
	}, nil
}
