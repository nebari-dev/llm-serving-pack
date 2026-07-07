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
var llmmodellog = logf.Log.WithName("llmmodel-resource")

// SetupLLMModelWebhookWithManager registers the webhook for LLMModel in the manager.
// operatorNamespace is the namespace the operator pod runs in; the webhook
// rejects LLMModel CRs created in any other namespace because the API-key
// Secret must live in the same namespace as the SecurityPolicy that
// references it (Envoy Gateway's APIKeyAuth.credentialRefs does not honor
// ReferenceGrant for cross-namespace refs - see #59) and the key-manager
// can only read Secrets in its own namespace. An empty operatorNamespace
// disables the check (used in tests that don't run inside a pod).
func SetupLLMModelWebhookWithManager(mgr ctrl.Manager, operatorNamespace string) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&llmv1alpha1.LLMModel{}).
		WithValidator(&LLMModelCustomValidator{
			Reader:            mgr.GetAPIReader(),
			OperatorNamespace: operatorNamespace,
		}).
		Complete()
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-llm-nebari-dev-v1alpha1-llmmodel,mutating=false,failurePolicy=fail,sideEffects=None,groups=llm.nebari.dev,resources=llmmodels,verbs=create;update,versions=v1alpha1,name=vllmmodel-v1alpha1.kb.io,admissionReviewVersions=v1

// LLMModelCustomValidator struct is responsible for validating the LLMModel resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type LLMModelCustomValidator struct {
	// Reader is an uncached, direct API reader. The validation reads below
	// (namespace label and PassthroughModel name-collision) must observe
	// authoritative state: a cache-backed client can miss an object created
	// moments earlier and would then admit a colliding resource. See
	// validateNoNameCollision.
	Reader            client.Reader
	OperatorNamespace string // pod namespace; LLMModels must live here. Empty disables the check.
}

var _ webhook.CustomValidator = &LLMModelCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := obj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object but got %T", obj)
	}
	llmmodellog.Info("Validation for LLMModel upon creation", "name", llmmodel.GetName())

	if err := v.validateOperatorNamespace(llmmodel.Namespace); err != nil {
		return nil, err
	}

	if err := v.validateNamespaceLabel(ctx, llmmodel.Namespace); err != nil {
		return nil, err
	}

	if err := validateAccess(llmmodel); err != nil {
		return nil, err
	}

	if err := v.validateNoNameCollision(ctx, llmmodel); err != nil {
		return nil, err
	}

	// Validate the effective subdomain length even though routing no longer
	// uses it. The CRD still exposes endpoints.external.subdomain; rejecting
	// >63-char values at admission time keeps the field a valid DNS label
	// for any future per-model FQDN routing. No cross-model collision check
	// is performed - models share a hostname pair and disambiguate via the
	// `x-ai-eg-model` header (see reconcilers.BuildRoutingResources).
	if _, err := reconcilers.EffectiveSubdomain(llmmodel); err != nil {
		return nil, err
	}

	warnings := emptyDirPreloadWarnings(llmmodel)

	return warnings, nil
}

// validateNoNameCollision rejects an LLMModel whose name is already taken by a
// PassthroughModel in the same namespace. Both kinds derive their api-keys
// Secret name as "<name>-api-keys", so sharing a name makes the two
// controllers fight over one Secret and lets the key-manager surface one
// model's keys under the other. Names must be unique across both kinds.
func (v *LLMModelCustomValidator) validateNoNameCollision(ctx context.Context, llmmodel *llmv1alpha1.LLMModel) error {
	var existing llmv1alpha1.PassthroughModel
	err := v.Reader.Get(ctx, types.NamespacedName{Name: llmmodel.Name, Namespace: llmmodel.Namespace}, &existing)
	if err == nil {
		return fmt.Errorf(
			"name %q is already used by a PassthroughModel in namespace %q; LLMModel and "+
				"PassthroughModel names must be unique across both kinds because they share the "+
				"api-keys Secret %q",
			llmmodel.Name, llmmodel.Namespace, reconcilers.APIKeySecretName(llmmodel.Name),
		)
	}
	// NotFound: no collision. NoMatch: the PassthroughModel CRD is not
	// installed, so none can exist to collide with. Either way, allow.
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	return fmt.Errorf("checking for name collision with PassthroughModel %q: %w", llmmodel.Name, err)
}

// validateOperatorNamespace rejects LLMModels created outside the operator's
// own namespace. The pack's auth model needs the API-key Secret to live in
// the same namespace as the SecurityPolicy that references it (Envoy Gateway's
// APIKeyAuth.credentialRefs does not honor ReferenceGrant); the simplest way
// to keep that invariant is to require all LLMModels to live alongside the
// operator and key-manager. Empty OperatorNamespace skips the check (used in
// envtest setups that don't run the operator inside a pod).
func (v *LLMModelCustomValidator) validateOperatorNamespace(namespace string) error {
	if v.OperatorNamespace == "" {
		return nil
	}
	if namespace != v.OperatorNamespace {
		return fmt.Errorf(
			"LLMModel must be created in the operator's namespace %q (got %q); "+
				"the API-key Secret needs to be co-located with its SecurityPolicy "+
				"because Envoy Gateway's APIKeyAuth rejects cross-namespace credentialRefs",
			v.OperatorNamespace, namespace,
		)
	}
	return nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := newObj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object for the newObj but got %T", newObj)
	}
	llmmodellog.Info("Validation for LLMModel upon update", "name", llmmodel.GetName())

	if err := v.validateOperatorNamespace(llmmodel.Namespace); err != nil {
		return nil, err
	}

	if err := v.validateNamespaceLabel(ctx, llmmodel.Namespace); err != nil {
		return nil, err
	}

	if err := validateAccess(llmmodel); err != nil {
		return nil, err
	}

	if err := v.validateNoNameCollision(ctx, llmmodel); err != nil {
		return nil, err
	}

	// Validate the effective subdomain length even though routing no longer
	// uses it. The CRD still exposes endpoints.external.subdomain; rejecting
	// >63-char values at admission time keeps the field a valid DNS label
	// for any future per-model FQDN routing. No cross-model collision check
	// is performed - models share a hostname pair and disambiguate via the
	// `x-ai-eg-model` header (see reconcilers.BuildRoutingResources).
	if _, err := reconcilers.EffectiveSubdomain(llmmodel); err != nil {
		return nil, err
	}

	warnings := emptyDirPreloadWarnings(llmmodel)

	return warnings, nil
}

// validateAccess ensures the model explicitly declares who may call it: either
// Access.Public=true or a non-empty Access.Groups list. A model with neither
// would have no one able to call its internal JWT endpoint (the SecurityPolicy
// would default-deny) and no groups to gate external API-key minting against,
// so the ambiguous state is rejected at admission time.
func validateAccess(llmmodel *llmv1alpha1.LLMModel) error {
	public := llmmodel.Spec.Access.Public != nil && *llmmodel.Spec.Access.Public
	if public {
		return nil
	}
	if len(llmmodel.Spec.Access.Groups) == 0 {
		return fmt.Errorf(
			"spec.access must declare either public=true or at least one group in groups; " +
				"a model with neither cannot be accessed by any user",
		)
	}
	return nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := obj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object but got %T", obj)
	}
	llmmodellog.Info("Validation for LLMModel upon deletion", "name", llmmodel.GetName())

	return nil, nil
}

// validateNamespaceLabel checks that the given namespace has the nebari.dev/managed=true label.
func (v *LLMModelCustomValidator) validateNamespaceLabel(ctx context.Context, namespaceName string) error {
	var ns corev1.Namespace
	if err := v.Reader.Get(ctx, types.NamespacedName{Name: namespaceName}, &ns); err != nil {
		return fmt.Errorf("failed to get namespace %q: %w", namespaceName, err)
	}

	if ns.Labels["nebari.dev/managed"] != "true" {
		return fmt.Errorf(
			"namespace %q does not have the required label nebari.dev/managed=true; "+
				"LLMModel resources can only be created in namespaces managed by Nebari",
			namespaceName,
		)
	}

	return nil
}

// emptyDirPreloadWarnings returns a warning if the model uses emptyDir storage with preload enabled.
func emptyDirPreloadWarnings(llmmodel *llmv1alpha1.LLMModel) admission.Warnings {
	if llmmodel.Spec.Model.Storage.Type == llmv1alpha1.StorageTypeEmptyDir &&
		llmmodel.Spec.Model.Preload != nil &&
		*llmmodel.Spec.Model.Preload {
		return admission.Warnings{
			"storage type is emptyDir with preload=true: every pod restart triggers a re-download of the model, " +
				"which may cause significant delays and bandwidth usage; consider using a PVC for persistent storage",
		}
	}

	return nil
}
