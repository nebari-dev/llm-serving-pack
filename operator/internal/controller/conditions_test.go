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
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
)

func TestConditionFor(t *testing.T) {
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "aigateway.envoyproxy.io", Kind: "AIGatewayRoute"}}

	tests := []struct {
		name        string
		err         error
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage string
	}{
		{
			name:        "no error reports Applied with the ok message",
			err:         nil,
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonApplied,
			wantMessage: "route applied",
		},
		{
			name:        "missing CRD reports GatewayCRDUnavailable",
			err:         noMatch,
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonGatewayCRDUnavailable,
			wantMessage: noMatch.Error(),
		},
		{
			name:        "other errors report ApplyFailed and carry the message",
			err:         errors.New("admission webhook denied the request"),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonApplyFailed,
			wantMessage: "admission webhook denied the request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conditionFor(CondExternalEndpointReady, tt.err, "route applied")
			if got.Type != CondExternalEndpointReady {
				t.Errorf("Type = %q, want %q", got.Type, CondExternalEndpointReady)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
		})
	}
}

func TestGatewayRequeue(t *testing.T) {
	applied := metav1.Condition{Type: CondExternalEndpointReady, Status: metav1.ConditionTrue, Reason: reasonApplied}
	failed := metav1.Condition{Type: CondInternalEndpointReady, Status: metav1.ConditionFalse, Reason: reasonApplyFailed}
	crdMissing := metav1.Condition{Type: CondInferencePoolReady, Status: metav1.ConditionFalse, Reason: reasonGatewayCRDUnavailable}
	disabled := disabledCondition(CondInternalEndpointReady)
	pending := metav1.Condition{Type: CondExternalEndpointReady, Status: metav1.ConditionUnknown, Reason: reasonRoutePending}
	unreadable := metav1.Condition{Type: CondExternalEndpointReady, Status: metav1.ConditionUnknown, Reason: reasonRouteStatusUnreadable}
	notAccepted := metav1.Condition{Type: CondInternalEndpointReady, Status: metav1.ConditionFalse, Reason: reasonRouteNotAccepted}
	lookupFailed := metav1.Condition{Type: CondUpstreamCredentialResolved, Status: metav1.ConditionUnknown, Reason: "LookupFailed"}

	tests := []struct {
		name        string
		conds       []metav1.Condition
		wantFailed  bool
		wantRequeue time.Duration
	}{
		{name: "no conditions", conds: nil, wantFailed: false, wantRequeue: 0},
		{name: "all applied", conds: []metav1.Condition{applied}, wantFailed: false, wantRequeue: 0},
		{
			// A disabled endpoint is False, but deliberately so. Treating it as a
			// failure would degrade every model that turns an endpoint off.
			name:        "disabled endpoint is not a failure",
			conds:       []metav1.Condition{applied, disabled},
			wantFailed:  false,
			wantRequeue: 0,
		},
		{name: "apply failure retries after a minute", conds: []metav1.Condition{applied, failed}, wantFailed: true, wantRequeue: time.Minute},
		{name: "missing CRD retries on the CRD interval", conds: []metav1.Condition{crdMissing}, wantFailed: true, wantRequeue: gatewayCRDRequeue},
		{
			// A missing CRD is the faster case to recover from (install it and
			// the next pass succeeds), so it sets the interval when both occur.
			name:        "missing CRD wins over an apply failure",
			conds:       []metav1.Condition{failed, crdMissing},
			wantFailed:  true,
			wantRequeue: gatewayCRDRequeue,
		},
		{name: "route pending requeues fast and is not a failure", conds: []metav1.Condition{applied, pending}, wantFailed: false, wantRequeue: routePendingRequeue},
		{name: "unreadable route status retries and is not a failure", conds: []metav1.Condition{unreadable}, wantFailed: false, wantRequeue: routeStatusUnreadableRequeue},
		{name: "route not accepted is a failure retried after a minute", conds: []metav1.Condition{notAccepted}, wantFailed: true, wantRequeue: applyFailedRequeue},
		{name: "pending wins over not accepted", conds: []metav1.Condition{notAccepted, pending}, wantFailed: true, wantRequeue: routePendingRequeue},
		{name: "missing CRD wins over not accepted", conds: []metav1.Condition{notAccepted, crdMissing}, wantFailed: true, wantRequeue: gatewayCRDRequeue},
		{name: "unknown reasons contribute nothing", conds: []metav1.Condition{lookupFailed}, wantFailed: false, wantRequeue: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasGatewayFailure(tt.conds); got != tt.wantFailed {
				t.Errorf("hasGatewayFailure() = %v, want %v", got, tt.wantFailed)
			}
			if got := gatewayRequeue(tt.conds); got != tt.wantRequeue {
				t.Errorf("gatewayRequeue() = %v, want %v", got, tt.wantRequeue)
			}
		})
	}
}

func TestPhaseWithGatewayFailure(t *testing.T) {
	failed := []metav1.Condition{{Type: CondExternalEndpointReady, Status: metav1.ConditionFalse, Reason: reasonApplyFailed}}
	crdMissing := []metav1.Condition{{Type: CondInferencePoolReady, Status: metav1.ConditionFalse, Reason: reasonGatewayCRDUnavailable}}
	ok := []metav1.Condition{{Type: CondExternalEndpointReady, Status: metav1.ConditionTrue, Reason: reasonApplied}}
	notAccepted := []metav1.Condition{{Type: CondExternalEndpointReady, Status: metav1.ConditionFalse, Reason: reasonRouteNotAccepted}}
	pending := []metav1.Condition{{Type: CondExternalEndpointReady, Status: metav1.ConditionUnknown, Reason: reasonRoutePending}}
	unreadable := []metav1.Condition{{Type: CondExternalEndpointReady, Status: metav1.ConditionUnknown, Reason: reasonRouteStatusUnreadable}}

	tests := []struct {
		name  string
		phase llmv1alpha1.LLMModelPhase
		conds []metav1.Condition
		want  llmv1alpha1.LLMModelPhase
	}{
		{
			// The pod is up but nothing can route to it. That is what Degraded
			// means; Error would overstate it.
			name:  "Ready degrades when an apply failed",
			phase: llmv1alpha1.PhaseReady,
			conds: failed,
			want:  llmv1alpha1.PhaseDegraded,
		},
		{
			name:  "Ready degrades when a gateway CRD is missing",
			phase: llmv1alpha1.PhaseReady,
			conds: crdMissing,
			want:  llmv1alpha1.PhaseDegraded,
		},
		{
			name:  "Ready stays Ready when every apply succeeded",
			phase: llmv1alpha1.PhaseReady,
			conds: ok,
			want:  llmv1alpha1.PhaseReady,
		},
		{
			// Degraded from determinePhase (some replicas down) already says
			// the model is not fully healthy.
			name:  "Degraded stays Degraded",
			phase: llmv1alpha1.PhaseDegraded,
			conds: failed,
			want:  llmv1alpha1.PhaseDegraded,
		},
		{
			name:  "Error is not softened to Degraded",
			phase: llmv1alpha1.PhaseError,
			conds: failed,
			want:  llmv1alpha1.PhaseError,
		},
		{
			name:  "Ready degrades when a route was not accepted",
			phase: llmv1alpha1.PhaseReady,
			conds: notAccepted,
			want:  llmv1alpha1.PhaseDegraded,
		},
		{
			name:  "Ready stays Ready while a route is pending",
			phase: llmv1alpha1.PhaseReady,
			conds: pending,
			want:  llmv1alpha1.PhaseReady,
		},
		{
			name:  "Ready stays Ready when route status is unreadable",
			phase: llmv1alpha1.PhaseReady,
			conds: unreadable,
			want:  llmv1alpha1.PhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phaseWithGatewayFailure(tt.phase, tt.conds); got != tt.want {
				t.Errorf("phaseWithGatewayFailure(%q) = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

func TestShortestRequeue(t *testing.T) {
	tests := []struct {
		name string
		ds   []time.Duration
		want time.Duration
	}{
		{name: "no durations", ds: nil, want: 0},
		{name: "all zero", ds: []time.Duration{0, 0}, want: 0},
		{name: "zero is ignored", ds: []time.Duration{time.Minute, 0, 15 * time.Second}, want: 15 * time.Second},
		{name: "shortest positive wins", ds: []time.Duration{5 * time.Second, time.Minute}, want: 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortestRequeue(tt.ds...); got != tt.want {
				t.Errorf("shortestRequeue(%v) = %v, want %v", tt.ds, got, tt.want)
			}
		})
	}
}
