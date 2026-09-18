package provider

import (
	"context"
	"fmt"
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
	// AWS owns endpoint and partition rules. This resolver is local: admission
	// does not load credentials or call AWS. It also works for isolated regions.
	endpoint, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(
		context.Background(), bedrockruntime.EndpointParameters{Region: &region},
	)
	if err != nil {
		return nil, fmt.Errorf("spec.provider.backend.bedrock.region: %w", err)
	}
	if err := validateBedrockOverride(p.Hostname, region); err != nil {
		return nil, err
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

// Recognize public and VPC Bedrock runtime DNS using the SDK's partition rules.
// A region in an unrelated label (e.g. a VPC endpoint prefix) is not evidence
// of the signing region. Custom DNS remains an operator-managed escape hatch.
func validateBedrockOverride(hostname, region string) error {
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	labels := strings.Split(hostname, ".")
	for i, label := range labels {
		if (label != "bedrock-runtime" && label != "bedrock-runtime-fips") || i+2 >= len(labels) {
			continue
		}
		hostRegion := labels[i+1]
		fips := label == "bedrock-runtime-fips"
		dualStack := strings.HasSuffix(hostname, ".api.aws")
		params := bedrockruntime.EndpointParameters{Region: &hostRegion, UseFIPS: &fips, UseDualStack: &dualStack}
		endpoint, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(), params)
		if err != nil {
			continue
		}
		suffix := strings.Join(labels[i:], ".")
		// VPC endpoint DNS inserts "vpce" immediately after the region.
		if labels[i+2] == "vpce" {
			suffix = strings.Join(append(append([]string{}, labels[i:i+2]...), labels[i+3:]...), ".")
		}
		if suffix != endpoint.URI.Hostname() {
			continue
		}
		params.Region = &region
		expected, err := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(), params)
		if err != nil || suffix != expected.URI.Hostname() {
			return fmt.Errorf("spec.provider.hostname region does not match spec.provider.backend.bedrock.region %s; requests would be signed for the wrong region", region)
		}
	}
	return nil
}
