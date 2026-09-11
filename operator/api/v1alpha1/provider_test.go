package v1alpha1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProviderResolve(t *testing.T) {
	bedrock := func() ProviderSpec {
		return ProviderSpec{Backend: &ProviderBackend{Type: BackendBedrock, Bedrock: &BedrockBackend{Region: "us-west-2"}}}
	}
	for _, tt := range []struct {
		name   string
		mutate func(*ProviderSpec)
		want   string
	}{
		{"missing region", func(p *ProviderSpec) { p.Backend.Bedrock = nil }, "region"},
		{"invalid region", func(p *ProviderSpec) { p.Backend.Bedrock.Region = "us-west-2/other" }, "region"},
		{"query in region", func(p *ProviderSpec) { p.Backend.Bedrock.Region = "us-west-2?other" }, "region"},
		{"invalid region with private endpoint", func(p *ProviderSpec) {
			p.Hostname = "bedrock.example.com"
			p.Backend.Bedrock.Region = "us-west-2#other"
		}, "region"},
		{"unknown backend", func(p *ProviderSpec) { p.Backend.Type = "Other" }, "backend.type"},
		{"wrong variant", func(p *ProviderSpec) { p.Backend.Type = BackendOpenAI }, "backend.bedrock"},
		{"wrong credential", func(p *ProviderSpec) { p.Credential = &ProviderCredential{Type: CredentialAPIKey} }, "credential.type"},
		{"unknown credential", func(p *ProviderSpec) { p.Credential = &ProviderCredential{Type: "Other"} }, "credential.type"},
		{"static key", func(p *ProviderSpec) { p.CredentialSecretName = "key" }, "credentialSecretName"},
		{"wrong path", func(p *ProviderSpec) { p.SchemaVersion = "api/v1" }, "schemaVersion"},
		{"bad override", func(p *ProviderSpec) { p.Hostname = "https://example.com" }, "bare hostname"},
		{"bad port", func(p *ProviderSpec) { p.Port = -1 }, "port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := bedrock()
			tt.mutate(&p)
			_, err := p.Resolve()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}

	t.Run("defaults survive JSON roundtrip without empty legacy fields", func(t *testing.T) {
		p := bedrock()
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "hostname") || strings.Contains(string(data), "credentialSecretName") {
			t.Fatalf("empty legacy fields would violate CRD minLength: %s", data)
		}
		var decoded ProviderSpec
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		// API server legacy defaults must not introduce an OpenAI version on Bedrock.
		decoded.Port, decoded.SchemaVersion = 443, "v1"
		resolved, err := decoded.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		if resolved.SchemaPrefix != "" || resolved.Hostname != "bedrock-runtime.us-west-2.amazonaws.com" {
			t.Fatalf("unexpected Bedrock defaults: %+v", resolved)
		}
	})
	t.Run("address override preserves schema and signing region", func(t *testing.T) {
		p := bedrock()
		p.Hostname = "bedrock.example.com"
		resolved, err := p.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		auth := resolved.SecurityPolicy["awsCredentials"].(map[string]interface{})
		if resolved.Hostname != p.Hostname || auth["region"] != "us-west-2" || resolved.SchemaName != "AWSBedrock" {
			t.Fatalf("unexpected resolved address: %+v", resolved)
		}
	})
	t.Run("explicit OpenAI and legacy input agree", func(t *testing.T) {
		p := ProviderSpec{Hostname: "openrouter.ai", SchemaVersion: "api/v1", CredentialSecretName: "key"}
		legacy, err := p.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		p.Backend = &ProviderBackend{Type: BackendOpenAI}
		p.Credential = &ProviderCredential{Type: CredentialAPIKey}
		explicit, err := p.Resolve()
		if err != nil || !reflect.DeepEqual(legacy, explicit) {
			t.Fatalf("explicit provider changed legacy behavior: %+v, %v", explicit, err)
		}
	})
}

func TestBedrockUsesAWSEndpointPartitions(t *testing.T) {
	for region, want := range map[string]string{
		"us-west-2":     "bedrock-runtime.us-west-2.amazonaws.com",
		"cn-north-1":    "bedrock-runtime.cn-north-1.amazonaws.com.cn",
		"us-gov-west-1": "bedrock-runtime.us-gov-west-1.amazonaws.com",
		"us-iso-east-1": "bedrock-runtime.us-iso-east-1.c2s.ic.gov",
	} {
		t.Run(region, func(t *testing.T) {
			p := ProviderSpec{Backend: &ProviderBackend{Type: BackendBedrock, Bedrock: &BedrockBackend{Region: region}}}
			resolved, err := p.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Hostname != want || p.EndpointHostname() != want {
				t.Fatalf("hostname = %q, want %q", resolved.Hostname, want)
			}
		})
	}
}

func TestOpenAIUsesExplicitPathPrefix(t *testing.T) {
	for _, tc := range []struct{ legacy, prefix string }{
		{"", "/v1"}, {"v1", "/v1"}, {"api/v1", "/api/v1"},
		{"/v1beta/openai/", "/v1beta/openai"}, {"/", "/"},
	} {
		t.Run(tc.legacy, func(t *testing.T) {
			p := ProviderSpec{Hostname: "provider.example.com", SchemaVersion: tc.legacy, CredentialSecretName: "key"}
			r, err := p.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if r.SchemaPrefix != tc.prefix || r.SchemaVersion != "" {
				t.Fatalf("prefix = %q, version = %q", r.SchemaPrefix, r.SchemaVersion)
			}
		})
	}
}
