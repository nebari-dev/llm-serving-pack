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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var llmmodellog = logf.Log.WithName("llmmodel-resource")

// SetupLLMModelWebhookWithManager registers the webhook for LLMModel in the manager.
func SetupLLMModelWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&llmv1alpha1.LLMModel{}).
		WithValidator(&LLMModelCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

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
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &LLMModelCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := obj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object but got %T", obj)
	}
	llmmodellog.Info("Validation for LLMModel upon creation", "name", llmmodel.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := newObj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object for the newObj but got %T", newObj)
	}
	llmmodellog.Info("Validation for LLMModel upon update", "name", llmmodel.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type LLMModel.
func (v *LLMModelCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	llmmodel, ok := obj.(*llmv1alpha1.LLMModel)
	if !ok {
		return nil, fmt.Errorf("expected a LLMModel object but got %T", obj)
	}
	llmmodellog.Info("Validation for LLMModel upon deletion", "name", llmmodel.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
