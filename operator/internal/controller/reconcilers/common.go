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

// APIKeySecretSuffix is the naming suffix shared by every api-keys Secret.
// APIKeySecretName appends it, and the controllers' Secret watches filter on
// it to recognize api-keys Secrets among operator-managed Secrets.
const APIKeySecretSuffix = "-api-keys"

// APIKeySecretName returns the name of the Secret holding API keys for the given model.
func APIKeySecretName(modelName string) string {
	return modelName + APIKeySecretSuffix
}

// APIKeyMetadataConfigMapName returns the name of the ConfigMap holding API key metadata for the given model.
func APIKeyMetadataConfigMapName(modelName string) string {
	return modelName + "-api-key-metadata"
}

// EffectiveSubdomain returns the subdomain that would be used for this LLMModel
// if per-model hostname routing were in effect. As of v0.1.0-alpha.3 the routing
// layer no longer uses subdomains - all models share a single external/internal
// hostname pair and are disambiguated by the `x-ai-eg-model` header (see
// BuildRoutingResources). This helper is retained because the LLMModel CRD still
// exposes `endpoints.external.subdomain` and the validating webhook still checks
// the >63-char limit; it may be re-wired into routing in the future when wildcard
// certificates are available.
//
// It uses spec.endpoints.external.subdomain if set, otherwise it slugifies the
// model name. It also validates that the result does not exceed 63 characters
// (the maximum length of a single DNS label).
func EffectiveSubdomain(model *llmv1alpha1.LLMModel) (string, error) {
	subdomain := model.Spec.Endpoints.External.Subdomain
	if subdomain == "" {
		subdomain = Slugify(model.Name)
	}

	if len(subdomain) > 63 {
		return "", fmt.Errorf(
			"effective subdomain %q is %d characters long; subdomains must be 63 characters or fewer",
			subdomain, len(subdomain),
		)
	}

	return subdomain, nil
}

// ExternalHostname returns the per-model external hostname. Currently unused at
// the routing layer (see BuildRoutingResources) but retained for tests and any
// future per-model FQDN routing.
func ExternalHostname(subdomain, baseDomain string) string {
	return subdomain + ".llm." + baseDomain
}

// InternalHostname returns the per-model internal hostname. Currently unused at
// the routing layer (see BuildRoutingResources) but retained for tests and any
// future per-model FQDN routing.
func InternalHostname(subdomain, baseDomain string) string {
	return subdomain + ".llm-internal." + baseDomain
}

// SharedExternalHostname returns the shared external hostname used by every
// model on the cluster. All models share this hostname so a single TLS
// certificate (issued via HTTP-01) can serve all of them; per-model routing
// happens via the `x-ai-eg-model` header instead of per-model FQDNs.
func SharedExternalHostname(baseDomain string) string {
	return "llm." + baseDomain
}

// SharedInternalHostname returns the shared internal hostname used by every
// model on the cluster. See SharedExternalHostname for the rationale.
func SharedInternalHostname(baseDomain string) string {
	return "llm-internal." + baseDomain
}
