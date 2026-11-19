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

package controller

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

// Status condition types shared by the LLMModel and PassthroughModel
// reconcilers.
//
// Both controllers apply gateway resources whose CRDs are installed
// separately from this pack (section 4 of the install runbook). A missing CRD
// does not fail a reconcile: everything the pack owns still converges, and the
// applies succeed on a later pass once the CRDs exist. It must be visible,
// though, because a route that was never created shows up only as an HTTP 404
// or 500 at request time.
const (
	// CondBackendConfigured covers a PassthroughModel's provider plumbing:
	// Backend, BackendTLSPolicy, AIServiceBackend, BackendSecurityPolicy.
	CondBackendConfigured = "BackendConfigured"
	// CondExternalEndpointReady covers the external endpoint. On a
	// PassthroughModel that is the route and its apiKeyAuth policy; on an
	// LLMModel it is the route only, because the policy is applied earlier and
	// reported by SecurityPoliciesReady.
	CondExternalEndpointReady = "ExternalEndpointReady"
	// CondInternalEndpointReady covers the internal endpoint: the route and its
	// JWT policy on a PassthroughModel, the route only on an LLMModel.
	CondInternalEndpointReady = "InternalEndpointReady"
	// CondInferencePoolReady covers the InferencePool an LLMModel needs. It is
	// separate from the endpoint conditions because it comes from a different
	// CRD bundle: a cluster can have the AI Gateway CRDs and not the Inference
	// Extension's, and the llm-d End-Point Picker crashloops without
	// InferencePool while routing looks fine.
	CondInferencePoolReady = "InferencePoolReady"
)

const (
	reasonApplied  = "Applied"
	reasonDisabled = "EndpointDisabled"
	// applyFailedRequeue is how long to wait before retrying a gateway apply
	// that failed for a reason other than a missing CRD.
	applyFailedRequeue = time.Minute
)

// conditionFor turns the outcome of an apply into a status condition. A nil
// err reports Applied with okMessage. A missing CRD reports
// GatewayCRDUnavailable; any other error reports ApplyFailed. Both carry the
// error text, which is where a reader learns which resource failed.
func conditionFor(condType string, err error, okMessage string) metav1.Condition {
	if err != nil {
		reason := reasonApplyFailed
		if meta.IsNoMatchError(err) {
			reason = reasonGatewayCRDUnavailable
		}
		return metav1.Condition{
			Type:    condType,
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: err.Error(),
		}
	}
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionTrue,
		Reason:  reasonApplied,
		Message: okMessage,
	}
}

// disabledCondition reports an endpoint the spec switched off. It is False
// like a failure, but for a deliberate reason, and hasGatewayFailure does not
// count it.
func disabledCondition(condType string) metav1.Condition {
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionFalse,
		Reason:  reasonDisabled,
		Message: "endpoint disabled in spec",
	}
}

// hasGatewayFailure reports whether any condition records a gateway apply
// that failed or is waiting on a missing CRD.
func hasGatewayFailure(conds []metav1.Condition) bool {
	return gatewayRequeue(conds) > 0
}

// gatewayRequeue returns how soon to retry after the gateway applies recorded
// in conds, or 0 when none failed. A missing CRD retries on the shorter CRD
// interval, since installing the CRD is enough for the next pass to succeed.
func gatewayRequeue(conds []metav1.Condition) time.Duration {
	var requeue time.Duration
	for _, c := range conds {
		if c.Status != metav1.ConditionFalse {
			continue
		}
		switch c.Reason {
		case reasonGatewayCRDUnavailable:
			return gatewayCRDRequeue
		case reasonApplyFailed:
			requeue = applyFailedRequeue
		}
	}
	return requeue
}

// phaseWithGatewayFailure downgrades a Ready model to Degraded when one of
// its gateway resources could not be applied. The workload is serving but
// nothing can route to it, which is what Degraded describes; Error would
// overstate it. Other phases already say something more specific and are
// left alone.
func phaseWithGatewayFailure(phase llmv1alpha1.LLMModelPhase, conds []metav1.Condition) llmv1alpha1.LLMModelPhase {
	if phase == llmv1alpha1.PhaseReady && hasGatewayFailure(conds) {
		return llmv1alpha1.PhaseDegraded
	}
	return phase
}
