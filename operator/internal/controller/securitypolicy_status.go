package controller

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

const (
	condSecurityPoliciesReady = "SecurityPoliciesReady"
	reasonApplyFailed         = "ApplyFailed"
)

// Missing CRDs do not block serving-stack installation, but must be retried.
// Other failures return an error for controller-runtime's rate-limited retry.
func (r *LLMModelReconciler) reconcileSecurityPolicies(ctx context.Context, model *llmv1alpha1.LLMModel, auth *reconcilers.AuthResources) (pending bool, err error) {
	for _, policy := range []*unstructured.Unstructured{auth.ExternalSecurityPolicy, auth.InternalSecurityPolicy} {
		if policy == nil {
			continue
		}
		if applyErr := r.createOrUpdateUnstructured(ctx, policy); applyErr != nil {
			if meta.IsNoMatchError(applyErr) {
				pending = true
			} else {
				err = errors.Join(err, fmt.Errorf("reconciling SecurityPolicy %s: %w", policy.GetName(), applyErr))
			}
		}
	}
	condition := metav1.Condition{
		Type: condSecurityPoliciesReady, Status: metav1.ConditionTrue,
		Reason: "Applied", Message: "enabled endpoint security policies applied",
		ObservedGeneration: model.Generation,
	}
	if err != nil {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, reasonApplyFailed, err.Error()
	} else if pending {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, "GatewayCRDUnavailable", "SecurityPolicy CRD is not installed; waiting to apply endpoint authentication"
	}
	fresh := &llmv1alpha1.LLMModel{}
	if statusErr := r.Get(ctx, client.ObjectKeyFromObject(model), fresh); statusErr != nil {
		return pending, errors.Join(err, statusErr)
	}
	before := fresh.DeepCopy()
	if meta.SetStatusCondition(&fresh.Status.Conditions, condition) {
		err = errors.Join(err, r.Status().Patch(ctx, fresh, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})))
	}
	return pending, err
}
