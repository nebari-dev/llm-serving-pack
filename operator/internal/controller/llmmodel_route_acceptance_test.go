package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// seedAcceptedLLMModelRoutes returns, for each endpoint model enables, the
// HTTPRoute AI Gateway would generate for it, already accepted by its
// Gateway.
func seedAcceptedLLMModelRoutes(t *testing.T, model *llmv1alpha1.LLMModel, created time.Time) []client.Object {
	t.Helper()
	routing, err := reconcilers.BuildRoutingResources(model, testConfig())
	if err != nil {
		t.Fatalf("building routes to seed: %v", err)
	}
	objs := make([]client.Object, 0, 2)
	for _, route := range []*unstructured.Unstructured{routing.ExternalRoute, routing.InternalRoute} {
		if route == nil {
			continue
		}
		route = route.DeepCopy()
		route.SetNamespace(model.Namespace)
		hr := acceptedHTTPRouteFor(route, created)
		objs = append(objs, hr)
	}
	return objs
}

// llmModelAcceptanceEnv is the state one TestLLMModelRouteAcceptance
// scenario runs against.
type llmModelAcceptanceEnv struct {
	t      *testing.T
	base   client.Client           // the client without HTTPRoute faults
	faults []*httpRouteFaultClient // one per endpoint HTTPRoute
	routes []*unstructured.Unstructured
	// external is the external AIGatewayRoute as built.
	external *unstructured.Unstructured
}

// setHTTPRoutes creates or replaces the HTTPRoute of every enabled endpoint
// with build(route).
func (e *llmModelAcceptanceEnv) setHTTPRoutes(build func(route *unstructured.Unstructured) *unstructured.Unstructured) {
	e.t.Helper()
	for _, route := range e.routes {
		e.setHTTPRoute(build(route))
	}
}

// setHTTPRoute creates or replaces one HTTPRoute with a plain Update (Q21:
// the fake client has no status subresource for it).
func (e *llmModelAcceptanceEnv) setHTTPRoute(hr *unstructured.Unstructured) {
	e.t.Helper()
	ctx := context.Background()
	existing := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", hr.GetNamespace(), hr.GetName())
	err := e.base.Get(ctx, client.ObjectKeyFromObject(hr), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := e.base.Create(ctx, hr); err != nil {
			e.t.Fatal(err)
		}
	case err != nil:
		e.t.Fatal(err)
	default:
		hr.SetResourceVersion(existing.GetResourceVersion())
		if err := e.base.Update(ctx, hr); err != nil {
			e.t.Fatal(err)
		}
	}
}

// llmModelAcceptanceStep is one reconcile and what it must produce for both
// endpoint conditions.
type llmModelAcceptanceStep struct {
	// mutate runs before the clock advance and the reconcile.
	mutate  func(e *llmModelAcceptanceEnv)
	advance time.Duration

	wantStatus   metav1.ConditionStatus
	wantReason   string
	wantContains string // checked when non-empty
	wantPhase    llmv1alpha1.LLMModelPhase
	wantRequeue  time.Duration
	// wantLTTAtNow requires the stored LastTransitionTime to equal the fake
	// clock time at this reconcile.
	wantLTTAtNow bool
	// wantLTT, when non-zero, is the stored LastTransitionTime.
	wantLTT time.Time
	// wantExternal, when non-nil, is the external endpoint condition's status
	// and reason; the other want fields then apply to the internal one only.
	wantExternal *metav1.Condition
	// sameMessage requires each endpoint's stored message to equal its
	// message after the previous step.
	sameMessage bool
}

// checkEndpoint asserts one endpoint condition after step i. now is the fake
// clock time at that reconcile; prevMessage is the endpoint's message after
// the previous step.
func (step llmModelAcceptanceStep) checkEndpoint(t *testing.T, i int, condType string, cond *metav1.Condition, now time.Time, prevMessage string) {
	t.Helper()
	if condType == CondExternalEndpointReady && step.wantExternal != nil {
		if cond.Status != step.wantExternal.Status || cond.Reason != step.wantExternal.Reason {
			t.Errorf("step %d: %s = %s/%s (%q), want %s/%s", i, condType, cond.Status, cond.Reason, cond.Message, step.wantExternal.Status, step.wantExternal.Reason)
		}
		return
	}
	if cond.Status != step.wantStatus || cond.Reason != step.wantReason {
		t.Errorf("step %d: %s = %s/%s (%q), want %s/%s", i, condType, cond.Status, cond.Reason, cond.Message, step.wantStatus, step.wantReason)
	}
	if step.wantContains != "" && !strings.Contains(cond.Message, step.wantContains) {
		t.Errorf("step %d: %s message = %q, want it to contain %q", i, condType, cond.Message, step.wantContains)
	}
	if step.wantLTTAtNow && !cond.LastTransitionTime.Time.Equal(now) {
		t.Errorf("step %d: %s LastTransitionTime = %v, want %v", i, condType, cond.LastTransitionTime.Time, now)
	}
	if !step.wantLTT.IsZero() && !cond.LastTransitionTime.Time.Equal(step.wantLTT) {
		t.Errorf("step %d: %s LastTransitionTime = %v, want %v", i, condType, cond.LastTransitionTime.Time, step.wantLTT)
	}
	if step.sameMessage && cond.Message != prevMessage {
		t.Errorf("step %d: %s message = %q, want unchanged %q", i, condType, cond.Message, prevMessage)
	}
}

func TestLLMModelRouteAcceptance(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "auth-retry", errors.New("denied"))

	accepted := func(e *llmModelAcceptanceEnv) {
		e.setHTTPRoutes(func(route *unstructured.Unstructured) *unstructured.Unstructured {
			return acceptedHTTPRouteFor(route, t0)
		})
	}
	noEntry := func(e *llmModelAcceptanceEnv) {
		e.setHTTPRoutes(func(route *unstructured.Unstructured) *unstructured.Unstructured {
			return httpRouteFor(route, t0, 1)
		})
	}
	staleGeneration := func(e *llmModelAcceptanceEnv) {
		e.setHTTPRoutes(func(route *unstructured.Unstructured) *unstructured.Unstructured {
			return httpRouteFor(route, t0, 2, parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1))
		})
	}
	failReads := func(e *llmModelAcceptanceEnv) {
		for _, f := range e.faults {
			f.err = forbidden
		}
	}

	pending := func(requeue time.Duration) llmModelAcceptanceStep {
		return llmModelAcceptanceStep{
			wantStatus: metav1.ConditionUnknown, wantReason: reasonRoutePending,
			wantPhase: llmv1alpha1.PhaseReady, wantRequeue: requeue,
		}
	}
	notAccepted := func(contains string) llmModelAcceptanceStep {
		return llmModelAcceptanceStep{
			wantStatus: metav1.ConditionFalse, wantReason: reasonRouteNotAccepted, wantContains: contains,
			wantPhase: llmv1alpha1.PhaseDegraded, wantRequeue: time.Minute,
		}
	}
	ready := llmModelAcceptanceStep{
		wantStatus: metav1.ConditionTrue, wantReason: reasonApplied,
		wantPhase: llmv1alpha1.PhaseReady, wantRequeue: 0,
	}
	with := func(s llmModelAcceptanceStep, f func(*llmModelAcceptanceStep)) llmModelAcceptanceStep {
		f(&s)
		return s
	}
	missingGateway := []llmModelAcceptanceStep{
		pending(5 * time.Second),
		with(notAccepted("AI Gateway has not generated HTTPRoute"), func(s *llmModelAcceptanceStep) { s.advance = 3 * time.Minute }),
	}

	tests := []struct {
		name  string
		steps []llmModelAcceptanceStep
	}{
		{
			name:  "missing Gateway (J2)",
			steps: missingGateway,
		},
		{
			name: "Gateway reports no status for the route (J2)",
			steps: []llmModelAcceptanceStep{
				with(pending(5*time.Second), func(s *llmModelAcceptanceStep) { s.mutate = noEntry }),
				with(notAccepted("reported no status for the route"), func(s *llmModelAcceptanceStep) { s.advance = 3 * time.Minute }),
			},
		},
		{
			name: "read error on RouteNotAccepted (R2-F4)",
			steps: append(append([]llmModelAcceptanceStep{}, missingGateway...),
				with(notAccepted(readErrorMarker), func(s *llmModelAcceptanceStep) { s.mutate = failReads }),
				with(notAccepted(readErrorMarker), func(s *llmModelAcceptanceStep) { s.sameMessage = true })),
		},
		{
			name: "heals with no CR edit (J4)",
			steps: append(append([]llmModelAcceptanceStep{}, missingGateway...),
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted })),
		},
		{
			name: "fresh healthy model (J5)",
			steps: []llmModelAcceptanceStep{
				pending(5 * time.Second),
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted }),
			},
		},
		{
			name: "unreadable status leaves Ready (F4)",
			steps: []llmModelAcceptanceStep{
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted }),
				{
					mutate:     failReads,
					wantStatus: metav1.ConditionUnknown, wantReason: reasonRouteStatusUnreadable, wantContains: forbidden.Error(),
					wantPhase: llmv1alpha1.PhaseReady, wantRequeue: 15 * time.Second,
				},
			},
		},
		{
			name: "prev read from the fetched model",
			steps: []llmModelAcceptanceStep{
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted }),
				with(pending(5*time.Second), func(s *llmModelAcceptanceStep) {
					s.mutate = staleGeneration
					s.advance = 30 * time.Second
					s.wantLTTAtNow = true
				}),
				// An unchanged status keeps the stored LastTransitionTime.
				with(pending(5*time.Second), func(s *llmModelAcceptanceStep) {
					s.advance = time.Minute
					s.wantLTT = t0.Add(30 * time.Second)
				}),
				// Only a stored Unknown condition closes the previous-condition
				// window; with no prev the stale route would stay pending.
				with(notAccepted("does not reflect the applied route"), func(s *llmModelAcceptanceStep) { s.advance = 3 * time.Minute }),
				// RouteNotAccepted sticks, and so does its LastTransitionTime.
				with(notAccepted("does not reflect the applied route"), func(s *llmModelAcceptanceStep) {
					s.advance = time.Minute
					s.wantLTT = t0.Add(30*time.Second + time.Minute + 3*time.Minute)
				}),
			},
		},
		{
			name: "both endpoints in different states",
			steps: []llmModelAcceptanceStep{
				with(pending(5*time.Second), func(s *llmModelAcceptanceStep) {
					s.mutate = func(e *llmModelAcceptanceEnv) { e.setHTTPRoute(acceptedHTTPRouteFor(e.external, t0)) }
					s.wantExternal = &metav1.Condition{Status: metav1.ConditionTrue, Reason: reasonApplied}
				}),
				with(notAccepted("AI Gateway has not generated HTTPRoute default/auth-retry-internal"), func(s *llmModelAcceptanceStep) {
					s.advance = 3 * time.Minute
					s.wantExternal = &metav1.Condition{Status: metav1.ConditionTrue, Reason: reasonApplied}
				}),
			},
		},
		{
			name: "parent-ref change on an accepted route (R2-F1)",
			steps: []llmModelAcceptanceStep{
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted }),
				with(pending(5*time.Second), func(s *llmModelAcceptanceStep) { s.mutate = noEntry; s.advance = 3 * time.Minute }),
				with(ready, func(s *llmModelAcceptanceStep) { s.mutate = accepted }),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			r, policyClient, model := newPolicyTestReconciler(t)
			clk := clocktesting.NewFakePassiveClock(t0)
			r.Clock = clk

			routing, err := reconcilers.BuildRoutingResources(model, testConfig())
			if err != nil {
				t.Fatal(err)
			}
			env := &llmModelAcceptanceEnv{t: t, base: policyClient, external: routing.ExternalRoute}
			var c client.Client = policyClient
			for _, route := range []*unstructured.Unstructured{routing.ExternalRoute, routing.InternalRoute} {
				route.SetNamespace(model.Namespace)
				env.routes = append(env.routes, route)
				fault := &httpRouteFaultClient{Client: c, failName: route.GetName()}
				env.faults = append(env.faults, fault)
				c = fault
			}
			r.Client = c

			// Every scenario starts with no HTTPRoutes, whatever the shared
			// fixture seeds.
			for _, route := range env.routes {
				hr := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", route.GetNamespace(), route.GetName())
				if err := policyClient.Delete(ctx, hr); client.IgnoreNotFound(err) != nil {
					t.Fatal(err)
				}
			}

			// The first reconcile creates the Deployment; mark it ready so the
			// steps run with the gateway resources applied.
			req := reconcileRequest(model.Name)
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			dep := &appsv1.Deployment{}
			if err := policyClient.Get(ctx, client.ObjectKeyFromObject(model), dep); err != nil {
				t.Fatal(err)
			}
			dep.Status.Replicas = 1
			dep.Status.ReadyReplicas = 1
			if err := policyClient.Status().Update(ctx, dep); err != nil {
				t.Fatal(err)
			}

			prevMessages := map[string]string{}
			for i, step := range tt.steps {
				if step.mutate != nil {
					step.mutate(env)
				}
				clk.SetTime(clk.Now().Add(step.advance))
				result, err := r.Reconcile(ctx, req)
				if err != nil {
					t.Fatalf("step %d: reconcile error: %v", i, err)
				}
				stored := &llmv1alpha1.LLMModel{}
				if err := policyClient.Get(ctx, req.NamespacedName, stored); err != nil {
					t.Fatal(err)
				}
				for _, condType := range []string{CondExternalEndpointReady, CondInternalEndpointReady} {
					cond := meta.FindStatusCondition(stored.Status.Conditions, condType)
					if cond == nil {
						t.Fatalf("step %d: no %s condition in %+v", i, condType, stored.Status.Conditions)
					}
					step.checkEndpoint(t, i, condType, cond, clk.Now(), prevMessages[condType])
					prevMessages[condType] = cond.Message
				}
				if stored.Status.Phase != step.wantPhase {
					t.Errorf("step %d: phase = %s, want %s", i, stored.Status.Phase, step.wantPhase)
				}
				if result.RequeueAfter != step.wantRequeue {
					t.Errorf("step %d: RequeueAfter = %s, want %s", i, result.RequeueAfter, step.wantRequeue)
				}
			}
		})
	}
}
