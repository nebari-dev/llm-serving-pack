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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PassthroughModelSpec defines routing and access control for an external
// OpenAI-compatible provider (OpenRouter, api.openai.com, a self-hosted
// vLLM elsewhere, ...). The operator provisions gateway routing and the
// same two auth layers it builds for served models, but no storage,
// serving, or scheduling resources.
type PassthroughModelSpec struct {
	// provider is the upstream OpenAI-compatible endpoint
	// +required
	Provider ProviderSpec `json:"provider"`
	// models selects which model ids route to this provider
	// +optional
	Models PassthroughModels `json:"models,omitempty"`
	// access controls who may use this provider. Same semantics as
	// LLMModel: either public must be true or groups must be non-empty.
	// +required
	Access AccessSpec `json:"access"`
	// endpoints toggles the shared external (API key) and internal (JWT)
	// endpoints. Both default to enabled.
	// +optional
	Endpoints PassthroughEndpoints `json:"endpoints,omitempty"`
}

// ProviderSpec identifies the upstream provider and its credential.
type ProviderSpec struct {
	// hostname of the provider, e.g. openrouter.ai
	// +kubebuilder:validation:MinLength=1
	// +required
	Hostname string `json:"hostname"`
	// port of the provider endpoint (TLS is always used)
	// +kubebuilder:default=443
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// schemaVersion is the upstream API path prefix passed to the
	// AIServiceBackend OpenAI schema, e.g. "api/v1" for OpenRouter or
	// "v1" for api.openai.com.
	// +kubebuilder:default=v1
	// +optional
	SchemaVersion string `json:"schemaVersion,omitempty"`
	// credentialSecretName names a Secret in the PassthroughModel's own
	// namespace whose "apiKey" key holds the provider API key. The
	// gateway injects it upstream; end users never see it.
	// +kubebuilder:validation:MinLength=1
	// +required
	CredentialSecretName string `json:"credentialSecretName"`
}

// PassthroughModels selects which model ids reach the provider.
type PassthroughModels struct {
	// catchAll routes any model id not claimed by a more specific route
	// on the shared listener. Served LLMModels always win because their
	// routes match both the Host and x-ai-eg-model headers.
	// +optional
	CatchAll bool `json:"catchAll,omitempty"`
	// declared model ids get explicit routes and are advertised by the
	// gateway's /v1/models endpoint so OpenAI-compatible UIs can build
	// a model picker.
	// +optional
	Declared []string `json:"declared,omitempty"`
}

// PassthroughEndpoints toggles the shared endpoints.
type PassthroughEndpoints struct {
	// external configures the external (API key) endpoint
	// +optional
	External EndpointToggle `json:"external,omitempty"`
	// internal configures the internal (JWT) endpoint
	// +optional
	Internal EndpointToggle `json:"internal,omitempty"`
}

// EndpointToggle enables or disables one shared endpoint.
type EndpointToggle struct {
	// enabled controls whether the endpoint route is created
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// PassthroughModelPhase represents the current lifecycle phase
// +kubebuilder:validation:Enum=Pending;Ready;Error
type PassthroughModelPhase string

const (
	PassthroughPhasePending PassthroughModelPhase = "Pending"
	PassthroughPhaseReady   PassthroughModelPhase = "Ready"
	PassthroughPhaseError   PassthroughModelPhase = "Error"
)

// PassthroughModelStatus defines the observed state of PassthroughModel
type PassthroughModelStatus struct {
	// phase is the current lifecycle phase
	// +optional
	Phase PassthroughModelPhase `json:"phase,omitempty"`
	// conditions represent the current state of the resource
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// observedGeneration is the last generation reconciled
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// endpoints contains the shared endpoint URLs
	// +optional
	Endpoints EndpointStatus `json:"endpoints,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.hostname`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PassthroughModel is the Schema for the passthroughmodels API
type PassthroughModel struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of PassthroughModel
	// +required
	Spec PassthroughModelSpec `json:"spec"`

	// status defines the observed state of PassthroughModel
	// +optional
	Status PassthroughModelStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// PassthroughModelList contains a list of PassthroughModel
type PassthroughModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PassthroughModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PassthroughModel{}, &PassthroughModelList{})
}
