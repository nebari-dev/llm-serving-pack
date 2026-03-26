package reconcilers

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

var (
	nonAlphanumericDash = regexp.MustCompile(`[^a-z0-9-]+`)
	multipleDashes      = regexp.MustCompile(`-{2,}`)
)

// Slugify converts a name into a valid Kubernetes label/name slug.
// It lowercases the input, replaces non-alphanumeric characters with dashes,
// collapses multiple dashes, trims leading/trailing dashes, and truncates
// to at most 63 characters. If truncation is needed, the result is 48 chars
// of the slug followed by a dash and an 8-character hash of the original input.
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlphanumericDash.ReplaceAllString(s, "-")
	s = multipleDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	if len(s) <= 48 {
		return s
	}

	// Truncate: 48 chars + "-" + 8-char hash of original name = 57 chars total (within 63-char K8s limit)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
	prefix := s[:48]
	prefix = strings.TrimRight(prefix, "-")
	return prefix + "-" + hash
}

// StandardLabels returns the standard Kubernetes labels for resources managed by this operator.
func StandardLabels(model *llmv1alpha1.LLMModel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "nebari-llm-operator",
		"app.kubernetes.io/instance":   model.Name,
		"app.kubernetes.io/name":       "llmmodel",
		"llm.nebari.dev/model":         model.Name,
	}
}

// SetOwnerReference sets the controller owner reference on obj to point to model.
func SetOwnerReference(model *llmv1alpha1.LLMModel, obj metav1.Object, scheme *runtime.Scheme) error {
	return controllerutil.SetControllerReference(model, obj, scheme)
}

// APIKeySecretName returns the name of the Secret holding API keys for the given model.
func APIKeySecretName(modelName string) string {
	return modelName + "-api-keys"
}

// APIKeyMetadataConfigMapName returns the name of the ConfigMap holding API key metadata for the given model.
func APIKeyMetadataConfigMapName(modelName string) string {
	return modelName + "-api-key-metadata"
}

// ExternalHostname returns the external hostname for a model's endpoint.
func ExternalHostname(subdomain, baseDomain string) string {
	return subdomain + ".llm." + baseDomain
}

// InternalHostname returns the internal hostname for a model's endpoint.
func InternalHostname(subdomain, baseDomain string) string {
	return subdomain + ".llm-internal." + baseDomain
}
