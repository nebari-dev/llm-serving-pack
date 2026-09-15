package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestProviderResolve(t *testing.T) {
	bedrock := func() llmv1alpha1.ProviderSpec {
		return llmv1alpha1.ProviderSpec{Backend: &llmv1alpha1.ProviderBackend{Type: llmv1alpha1.BackendBedrock, Bedrock: &llmv1alpha1.BedrockBackend{Region: "us-west-2"}}}
	}
	for _, tt := range []struct {
		name   string
		mutate func(*llmv1alpha1.ProviderSpec)
		want   string
	}{
		{"missing region", func(p *llmv1alpha1.ProviderSpec) { p.Backend.Bedrock = nil }, "region"},
		{"invalid region", func(p *llmv1alpha1.ProviderSpec) { p.Backend.Bedrock.Region = "us-west-2/other" }, "region"},
		{"query in region", func(p *llmv1alpha1.ProviderSpec) { p.Backend.Bedrock.Region = "us-west-2?other" }, "region"},
		{"invalid region with private endpoint", func(p *llmv1alpha1.ProviderSpec) {
			p.Hostname = "bedrock.example.com"
			p.Backend.Bedrock.Region = "us-west-2#other"
		}, "region"},
		{"cross-region AWS endpoint", func(p *llmv1alpha1.ProviderSpec) {
			p.Hostname = "bedrock-runtime.us-east-1.amazonaws.com"
		}, "signed for the wrong region"},
		{"unknown backend", func(p *llmv1alpha1.ProviderSpec) { p.Backend.Type = "Other" }, "backend.type"},
		{"wrong variant", func(p *llmv1alpha1.ProviderSpec) { p.Backend.Type = llmv1alpha1.BackendOpenAI }, "backend.bedrock"},
		{"wrong credential", func(p *llmv1alpha1.ProviderSpec) {
			p.Credential = &llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialAPIKey}
		}, "credential.type"},
		{"unknown credential", func(p *llmv1alpha1.ProviderSpec) {
			p.Credential = &llmv1alpha1.ProviderCredential{Type: "Other"}
		}, "credential.type"},
		{"static key", func(p *llmv1alpha1.ProviderSpec) { p.CredentialSecretName = "key" }, "credentialSecretName"},
		{"wrong path", func(p *llmv1alpha1.ProviderSpec) { p.SchemaVersion = "api/v1" }, "schemaVersion"},
		{"bad override", func(p *llmv1alpha1.ProviderSpec) { p.Hostname = "https://example.com" }, "bare hostname"},
		{"bad port", func(p *llmv1alpha1.ProviderSpec) { p.Port = -1 }, "port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := bedrock()
			tt.mutate(&p)
			_, err := Resolve(p)
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
		var decoded llmv1alpha1.ProviderSpec
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		// API server legacy defaults must not introduce an OpenAI version on Bedrock.
		decoded.Port, decoded.SchemaVersion = 443, "v1"
		resolved, err := Resolve(decoded)
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
		resolved, err := Resolve(p)
		if err != nil {
			t.Fatal(err)
		}
		auth := resolved.SecurityPolicy["awsCredentials"].(map[string]interface{})
		if resolved.Hostname != p.Hostname || auth["region"] != "us-west-2" || resolved.SchemaName != "AWSBedrock" {
			t.Fatalf("unexpected resolved address: %+v", resolved)
		}
	})
	t.Run("AWS-owned override must carry the signing region", func(t *testing.T) {
		p := bedrock()
		p.Hostname = "vpce-0abc123.bedrock-runtime.us-west-2.vpce.amazonaws.com"
		if _, err := Resolve(p); err != nil {
			t.Fatalf("same-region VPC endpoint rejected: %v", err)
		}
		p.Hostname = "vpce-0abc123.bedrock-runtime.us-east-1.vpce.amazonaws.com"
		if _, err := Resolve(p); err == nil {
			t.Fatal("cross-region VPC endpoint accepted")
		}
	})
	t.Run("explicit OpenAI and legacy input agree", func(t *testing.T) {
		p := llmv1alpha1.ProviderSpec{Hostname: "openrouter.ai", SchemaVersion: "api/v1", CredentialSecretName: "key"}
		legacy, err := Resolve(p)
		if err != nil {
			t.Fatal(err)
		}
		p.Backend = &llmv1alpha1.ProviderBackend{Type: llmv1alpha1.BackendOpenAI}
		p.Credential = &llmv1alpha1.ProviderCredential{Type: llmv1alpha1.CredentialAPIKey}
		explicit, err := Resolve(p)
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
			p := llmv1alpha1.ProviderSpec{Backend: &llmv1alpha1.ProviderBackend{Type: llmv1alpha1.BackendBedrock, Bedrock: &llmv1alpha1.BedrockBackend{Region: region}}}
			resolved, err := Resolve(p)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Hostname != want {
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
			p := llmv1alpha1.ProviderSpec{Hostname: "provider.example.com", SchemaVersion: tc.legacy, CredentialSecretName: "key"}
			r, err := Resolve(p)
			if err != nil {
				t.Fatal(err)
			}
			if r.SchemaPrefix != tc.prefix || r.SchemaVersion != "" {
				t.Fatalf("prefix = %q, version = %q", r.SchemaPrefix, r.SchemaVersion)
			}
		})
	}
}

func TestProviderHostnameCompatibility(t *testing.T) {
	for _, backend := range []string{llmv1alpha1.BackendOpenAI, llmv1alpha1.BackendBedrock} {
		for _, hostname := range []string{"api.example.com", "Api.Example.COM", "api.example.com.", "Api.Example.COM."} {
			t.Run(backend+"/"+hostname, func(t *testing.T) {
				p := llmv1alpha1.ProviderSpec{Hostname: hostname, CredentialSecretName: "key"}
				if backend == llmv1alpha1.BackendBedrock {
					p.CredentialSecretName = ""
					p.Backend = &llmv1alpha1.ProviderBackend{Type: backend, Bedrock: &llmv1alpha1.BedrockBackend{Region: "us-west-2"}}
				}
				r, err := Resolve(p)
				if err != nil || r.Hostname != "api.example.com" {
					t.Fatalf("resolved hostname: %+v, %v", r, err)
				}
				if p.Hostname != hostname {
					t.Fatal("normalization changed stored input")
				}
			})
		}
	}
	for _, hostname := range []string{"api_example.com", "api.example.com..", "api..example.com", " api.example.com", "https://api.example.com", "api.example.com:443", "api.example.com/path", "."} {
		t.Run(hostname, func(t *testing.T) {
			p := llmv1alpha1.ProviderSpec{Hostname: hostname, CredentialSecretName: "key"}
			if _, err := Resolve(p); err == nil {
				t.Fatalf("invalid hostname accepted: %q", hostname)
			}
		})
	}
}
