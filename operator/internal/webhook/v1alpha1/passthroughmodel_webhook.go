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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// nolint:unused
// log is for logging in this package.
var passthroughmodellog = logf.Log.WithName("passthroughmodel-resource")

// SetupPassthroughModelWebhookWithManager registers the webhook for
// PassthroughModel in the manager. operatorNamespace carries the same
// invariant as the LLMModel webhook: the api-keys Secret must be co-located
// with the SecurityPolicy that references it, so PassthroughModels must
// live in the operator's namespace. Empty disables the check (tests).
func SetupPassthroughModelWebhookWithManager(mgr ctrl.Manager, operatorNamespace string) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&llmv1alpha1.PassthroughModel{}).
		WithValidator(&PassthroughModelCustomValidator{
			Reader:            mgr.GetAPIReader(),
			OperatorNamespace: operatorNamespace,
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-llm-nebari-dev-v1alpha1-passthroughmodel,mutating=false,failurePolicy=fail,sideEffects=None,groups=llm.nebari.dev,resources=passthroughmodels,verbs=create;update,versions=v1alpha1,name=vpassthroughmodel-v1alpha1.kb.io,admissionReviewVersions=v1

// PassthroughModelCustomValidator validates PassthroughModel resources on
// create and update.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PassthroughModelCustomValidator struct {
	// Reader is an uncached, direct API reader. The validation reads below
	// (namespace label and LLMModel name-collision) must observe authoritative
	// state: a cache-backed client can miss an object created moments earlier
	// and would then admit a colliding resource. See validateNoNameCollision.
	Reader            client.Reader
	OperatorNamespace string // pod namespace; PassthroughModels must live here. Empty disables the check.
}

var _ webhook.CustomValidator = &PassthroughModelCustomValidator{}

// ValidateCreate implements webhook.CustomValidator for PassthroughModel.
func (v *PassthroughModelCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	pm, ok := obj.(*llmv1alpha1.PassthroughModel)
	if !ok {
		return nil, fmt.Errorf("expected a PassthroughModel object but got %T", obj)
	}
	passthroughmodellog.Info("Validation for PassthroughModel upon creation", "name", pm.GetName())

	return v.validate(ctx, pm)
}

// ValidateUpdate implements webhook.CustomValidator for PassthroughModel.
func (v *PassthroughModelCustomValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	pm, ok := newObj.(*llmv1alpha1.PassthroughModel)
	if !ok {
		return nil, fmt.Errorf("expected a PassthroughModel object for the newObj but got %T", newObj)
	}
	passthroughmodellog.Info("Validation for PassthroughModel upon update", "name", pm.GetName())

	return v.validate(ctx, pm)
}

// ValidateDelete implements webhook.CustomValidator for PassthroughModel.
func (v *PassthroughModelCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	pm, ok := obj.(*llmv1alpha1.PassthroughModel)
	if !ok {
		return nil, fmt.Errorf("expected a PassthroughModel object but got %T", obj)
	}
	passthroughmodellog.Info("Validation for PassthroughModel upon deletion", "name", pm.GetName())

	return nil, nil
}

func (v *PassthroughModelCustomValidator) validate(ctx context.Context, pm *llmv1alpha1.PassthroughModel) (admission.Warnings, error) {
	if err := v.validateOperatorNamespace(pm.Namespace); err != nil {
		return nil, err
	}

	if err := v.validateNamespaceLabel(ctx, pm.Namespace); err != nil {
		return nil, err
	}

	if err := validatePassthroughAccess(pm); err != nil {
		return nil, err
	}

	if err := validateProvider(pm); err != nil {
		return nil, err
	}

	if err := validateModels(pm); err != nil {
		return nil, err
	}

	if err := validateEndpoints(pm); err != nil {
		return nil, err
	}

	if err := v.validateNoNameCollision(ctx, pm); err != nil {
		return nil, err
	}

	return nil, nil
}

// validateOperatorNamespace mirrors the LLMModel webhook: the api-keys
// Secret must be co-located with the SecurityPolicy that references it
// (Envoy Gateway's APIKeyAuth rejects cross-namespace credentialRefs) and
// the key-manager only reads Secrets in its own namespace.
func (v *PassthroughModelCustomValidator) validateOperatorNamespace(namespace string) error {
	if v.OperatorNamespace == "" {
		return nil
	}
	if namespace != v.OperatorNamespace {
		return fmt.Errorf(
			"PassthroughModel must be created in the operator's namespace %q (got %q); "+
				"the API-key Secret needs to be co-located with its SecurityPolicy "+
				"because Envoy Gateway's APIKeyAuth rejects cross-namespace credentialRefs",
			v.OperatorNamespace, namespace,
		)
	}
	return nil
}

// validateNamespaceLabel checks that the namespace carries nebari.dev/managed=true.
func (v *PassthroughModelCustomValidator) validateNamespaceLabel(ctx context.Context, namespaceName string) error {
	var ns corev1.Namespace
	if err := v.Reader.Get(ctx, types.NamespacedName{Name: namespaceName}, &ns); err != nil {
		return fmt.Errorf("failed to get namespace %q: %w", namespaceName, err)
	}

	if ns.Labels["nebari.dev/managed"] != "true" {
		return fmt.Errorf(
			"namespace %q does not have the required label nebari.dev/managed=true; "+
				"PassthroughModel resources can only be created in namespaces managed by Nebari",
			namespaceName,
		)
	}
	return nil
}

// validatePassthroughAccess applies the same rule as LLMModel access
// validation: either public=true or a non-empty groups list, so the
// internal JWT endpoint never renders an unreachable default-deny policy.
func validatePassthroughAccess(pm *llmv1alpha1.PassthroughModel) error {
	public := pm.Spec.Access.Public != nil && *pm.Spec.Access.Public
	if public {
		return nil
	}
	if len(pm.Spec.Access.Groups) == 0 {
		return fmt.Errorf(
			"spec.access must declare either public=true or at least one group in groups; " +
				"a passthrough with neither cannot be accessed by any user",
		)
	}
	return nil
}

// validateProvider rejects obviously broken provider configs the CRD schema
// alone cannot express.
func validateProvider(pm *llmv1alpha1.PassthroughModel) error {
	if strings.TrimSpace(pm.Spec.Provider.Hostname) == "" {
		return fmt.Errorf("spec.provider.hostname must not be empty")
	}
	if strings.Contains(pm.Spec.Provider.Hostname, "://") {
		return fmt.Errorf(
			"spec.provider.hostname must be a bare hostname (got %q); drop the URL scheme",
			pm.Spec.Provider.Hostname,
		)
	}
	// The Backend fqdn.hostname must be a bare DNS name: no port (set
	// spec.provider.port instead), no path, and no embedded whitespace.
	if strings.ContainsAny(pm.Spec.Provider.Hostname, "/:") ||
		strings.ContainsAny(pm.Spec.Provider.Hostname, " \t\n\r") {
		return fmt.Errorf(
			"spec.provider.hostname must be a bare hostname (got %q); "+
				"drop any port, path, or whitespace (use spec.provider.port for the port)",
			pm.Spec.Provider.Hostname,
		)
	}
	if strings.TrimSpace(pm.Spec.Provider.CredentialSecretName) == "" {
		return fmt.Errorf("spec.provider.credentialSecretName must not be empty")
	}
	return nil
}

// validateEndpoints rejects a PassthroughModel with both shared endpoints
// disabled: it would provision provider plumbing but no route, leaving the
// provider unreachable. Both endpoints default to enabled (nil == enabled),
// so this only fires when a user explicitly sets both to false.
func validateEndpoints(pm *llmv1alpha1.PassthroughModel) error {
	externalEnabled := pm.Spec.Endpoints.External.Enabled == nil || *pm.Spec.Endpoints.External.Enabled
	internalEnabled := pm.Spec.Endpoints.Internal.Enabled == nil || *pm.Spec.Endpoints.Internal.Enabled
	if !externalEnabled && !internalEnabled {
		return fmt.Errorf(
			"spec.endpoints cannot disable both external and internal; " +
				"a passthrough with neither endpoint enabled is never reachable",
		)
	}
	return nil
}

// validateNoNameCollision rejects a PassthroughModel whose name is already
// taken by an LLMModel in the same namespace. Both kinds derive their
// api-keys Secret name as "<name>-api-keys", so sharing a name makes the two
// controllers fight over one Secret and lets the key-manager surface one
// model's keys under the other. Names must be unique across both kinds.
func (v *PassthroughModelCustomValidator) validateNoNameCollision(ctx context.Context, pm *llmv1alpha1.PassthroughModel) error {
	var existing llmv1alpha1.LLMModel
	err := v.Reader.Get(ctx, types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, &existing)
	if err == nil {
		return fmt.Errorf(
			"name %q is already used by an LLMModel in namespace %q; PassthroughModel and "+
				"LLMModel names must be unique across both kinds because they share the "+
				"api-keys Secret %q",
			pm.Name, pm.Namespace, reconcilers.APIKeySecretName(pm.Name),
		)
	}
	// NotFound: no collision. NoMatch: the LLMModel CRD is not installed, so
	// no LLMModel can exist to collide with. Either way, allow.
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	return fmt.Errorf("checking for name collision with LLMModel %q: %w", pm.Name, err)
}

// validateModels requires the CR to route something: a catch-all, declared
// model ids, or both. Declared ids must be non-empty strings.
func validateModels(pm *llmv1alpha1.PassthroughModel) error {
	if !pm.Spec.Models.CatchAll && len(pm.Spec.Models.Declared) == 0 {
		return fmt.Errorf(
			"spec.models must enable catchAll or declare at least one model id; " +
				"a passthrough that routes nothing is never reachable",
		)
	}
	for i, id := range pm.Spec.Models.Declared {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("spec.models.declared[%d] must not be empty", i)
		}
	}
	return nil
}
