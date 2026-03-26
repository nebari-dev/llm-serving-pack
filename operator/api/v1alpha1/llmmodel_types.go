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
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LLMModelSpec defines the desired state of LLMModel
type LLMModelSpec struct {
	// model specifies the LLM to serve
	Model ModelSpec `json:"model"`
	// resources specifies compute requirements
	Resources ResourceSpec `json:"resources"`
	// serving configures the vLLM serving layer
	// +optional
	Serving ServingSpec `json:"serving,omitempty"`
	// access controls who can use this model
	Access AccessSpec `json:"access"`
	// endpoints configures external and internal access
	// +optional
	Endpoints EndpointSpec `json:"endpoints,omitempty"`
	// advanced provides escape hatches for power users
	// +optional
	Advanced AdvancedSpec `json:"advanced,omitempty"`
}

// ModelSource defines where the model comes from
// +kubebuilder:validation:Enum=huggingface;oci
type ModelSource string

const (
	ModelSourceHuggingFace ModelSource = "huggingface"
	ModelSourceOCI         ModelSource = "oci"
)

type ModelSpec struct {
	// name is the model identifier (e.g., "mistralai/Devstral-Small-2505")
	Name string `json:"name"`
	// source is where to load the model from
	Source ModelSource `json:"source"`
	// revision pins a specific HF commit hash or tag for reproducible deployments
	// +optional
	Revision string `json:"revision,omitempty"`
	// authSecretName references a K8s Secret containing HF_TOKEN (huggingface only)
	// +optional
	AuthSecretName string `json:"authSecretName,omitempty"`
	// image is the OCI image containing the model (oci source only)
	// +optional
	Image string `json:"image,omitempty"`
	// storage configures where model files are stored
	// +optional
	Storage StorageSpec `json:"storage,omitempty"`
	// preload uses an init container to download the model before vLLM starts
	// +optional
	Preload *bool `json:"preload,omitempty"`
}

// StorageType defines the volume type for model storage
// +kubebuilder:validation:Enum=pvc;emptyDir
type StorageType string

const (
	StorageTypePVC      StorageType = "pvc"
	StorageTypeEmptyDir StorageType = "emptyDir"
)

type StorageSpec struct {
	// type is the volume type (pvc or emptyDir)
	// +optional
	// +kubebuilder:default=pvc
	Type StorageType `json:"type,omitempty"`
	// size is the storage size (e.g., "200Gi")
	// +optional
	Size string `json:"size,omitempty"`
	// storageClassName overrides the default storage class
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}

type ResourceSpec struct {
	// gpu specifies GPU requirements
	GPU GPUSpec `json:"gpu"`
	// requests specifies CPU/memory resource requests
	// +optional
	Requests corev1.ResourceList `json:"requests,omitempty"`
	// limits specifies CPU/memory resource limits
	// +optional
	Limits corev1.ResourceList `json:"limits,omitempty"`
}

type GPUSpec struct {
	// count is the number of GPUs required
	Count int32 `json:"count"`
	// type is the GPU type (e.g., "nvidia")
	// +kubebuilder:default=nvidia
	Type string `json:"type"`
}

type ServingSpec struct {
	// image overrides the default vLLM container image
	// +optional
	Image string `json:"image,omitempty"`
	// replicas is the number of serving replicas
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`
	// tensorParallelism controls tensor parallelism (defaults to gpu.count)
	// +optional
	TensorParallelism *int32 `json:"tensorParallelism,omitempty"`
	// dataParallelism controls data parallelism
	// +optional
	// +kubebuilder:default=1
	DataParallelism *int32 `json:"dataParallelism,omitempty"`
	// vllmArgs are additional arguments passed to vLLM
	// +optional
	VLLMArgs []string `json:"vllmArgs,omitempty"`
	// monitoring configures Prometheus monitoring
	// +optional
	Monitoring MonitoringSpec `json:"monitoring,omitempty"`
}

type MonitoringSpec struct {
	// enabled controls whether a PodMonitor is created
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

type AccessSpec struct {
	// groups lists OIDC groups that can access this model
	Groups []string `json:"groups"`
}

type EndpointSpec struct {
	// external configures the external (API key) endpoint
	// +optional
	External ExternalEndpointSpec `json:"external,omitempty"`
	// internal configures the internal (JWT) endpoint
	// +optional
	Internal InternalEndpointSpec `json:"internal,omitempty"`
}

type ExternalEndpointSpec struct {
	// enabled controls whether the external endpoint is created
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
	// subdomain overrides the auto-generated subdomain
	// +optional
	Subdomain string `json:"subdomain,omitempty"`
}

type InternalEndpointSpec struct {
	// enabled controls whether the internal endpoint is created
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
}

type AdvancedSpec struct {
	// vllmOverrides are raw overrides merged into the vLLM Deployment spec
	// +optional
	VLLMOverrides *apiextensionsv1.JSON `json:"vllmOverrides,omitempty"`
	// inferencePoolOverrides are raw overrides merged into the InferencePool spec
	// +optional
	InferencePoolOverrides *apiextensionsv1.JSON `json:"inferencePoolOverrides,omitempty"`
}

// LLMModelPhase represents the current lifecycle phase
// +kubebuilder:validation:Enum=Pending;Downloading;Starting;Ready;Degraded;Error
type LLMModelPhase string

const (
	PhasePending     LLMModelPhase = "Pending"
	PhaseDownloading LLMModelPhase = "Downloading"
	PhaseStarting    LLMModelPhase = "Starting"
	PhaseReady       LLMModelPhase = "Ready"
	PhaseDegraded    LLMModelPhase = "Degraded"
	PhaseError       LLMModelPhase = "Error"
)

// LLMModelStatus defines the observed state of LLMModel
type LLMModelStatus struct {
	// phase is the current lifecycle phase
	// +optional
	Phase LLMModelPhase `json:"phase,omitempty"`
	// conditions represent the current state of the resource
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// observedGeneration is the last generation reconciled
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// modelSize is the actual model size after download
	// +optional
	ModelSize string `json:"modelSize,omitempty"`
	// replicas tracks ready vs desired replica counts
	// +optional
	Replicas ReplicaStatus `json:"replicas,omitempty"`
	// endpoints contains the actual endpoint URLs
	// +optional
	Endpoints EndpointStatus `json:"endpoints,omitempty"`
}

type ReplicaStatus struct {
	Ready   int32 `json:"ready"`
	Desired int32 `json:"desired"`
}

type EndpointStatus struct {
	// +optional
	External string `json:"external,omitempty"`
	// +optional
	Internal string `json:"internal,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model.name`
// +kubebuilder:printcolumn:name="Replicas",type=string,JSONPath=`.status.replicas.ready`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LLMModel is the Schema for the llmmodels API
type LLMModel struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of LLMModel
	// +required
	Spec LLMModelSpec `json:"spec"`

	// status defines the observed state of LLMModel
	// +optional
	Status LLMModelStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// LLMModelList contains a list of LLMModel
type LLMModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LLMModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMModel{}, &LLMModelList{})
}
