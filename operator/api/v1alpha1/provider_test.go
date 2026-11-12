package v1alpha1

import "testing"

// crdBackendTypes mirrors the +kubebuilder:validation:Enum on
// ProviderBackend.Type. Adding a value there without extending Origin's
// mapping fails this test, so a new provider variant cannot ship with an
// unlabeled hosting origin.
var crdBackendTypes = []string{BackendOpenAI, BackendBedrock}

func TestOriginCoversEveryBackendType(t *testing.T) {
	for _, bt := range crdBackendTypes {
		spec := ProviderSpec{Backend: &ProviderBackend{Type: bt}}
		origin, err := spec.Origin()
		if err != nil {
			t.Errorf("Origin() for backend %q: %v", bt, err)
			continue
		}
		if origin == "" {
			t.Errorf("Origin() for backend %q is empty", bt)
		}
	}
}

func TestOriginValues(t *testing.T) {
	cases := []struct {
		name    string
		backend *ProviderBackend
		want    HostingOrigin
	}{
		{"omitted backend is the legacy OpenAI-compatible path", nil, OriginOpenAI},
		{"openai", &ProviderBackend{Type: BackendOpenAI}, OriginOpenAI},
		{"bedrock", &ProviderBackend{Type: BackendBedrock}, OriginBedrock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProviderSpec{Backend: tc.backend}.Origin()
			if err != nil {
				t.Fatalf("Origin() error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Origin() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOriginRejectsUnknownBackend(t *testing.T) {
	_, err := ProviderSpec{Backend: &ProviderBackend{Type: "Frontier"}}.Origin()
	if err == nil {
		t.Fatal("Origin() must error for a backend type without a mapping")
	}
}
