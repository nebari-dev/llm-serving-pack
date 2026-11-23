package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	llmv1alpha1 "github.com/nebari-dev/nebari-llm-serving-pack/operator/api/v1alpha1"
	"github.com/nebari-dev/nebari-llm-serving-pack/operator/internal/controller/reconcilers"
)

// seedAcceptedPassthroughRoutes returns, for each endpoint pm enables, the
// HTTPRoute AI Gateway would generate for it, already accepted by its
// Gateway. A model whose resources do not build has no routes to seed, so it
// gets none; the tests that use such a model expect the build failure.
func seedAcceptedPassthroughRoutes(t *testing.T, pm *llmv1alpha1.PassthroughModel, created time.Time) []client.Object {
	t.Helper()
	resources, err := reconcilers.BuildPassthroughResources(pm, testConfig(), nil, nil)
	if err != nil {
		return nil
	}
	objs := make([]client.Object, 0, 2)
	for _, route := range []*unstructured.Unstructured{resources.ExternalRoute, resources.InternalRoute} {
		if route == nil {
			continue
		}
		hr := acceptedHTTPRouteFor(route, created)
		hr.SetNamespace(pm.Namespace)
		objs = append(objs, hr)
	}
	return objs
}

// httpRouteFaultClient fails Gets of the HTTPRoute named failName with err
// while err is set.
type httpRouteFaultClient struct {
	client.Client
	failName string
	err      error
}

func (c *httpRouteFaultClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.err != nil && key.Name == c.failName && obj.GetObjectKind().GroupVersionKind().Kind == "HTTPRoute" {
		return c.err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// passthroughAcceptanceEnv is the state one TestPassthroughRouteAcceptance
// scenario runs against.
type passthroughAcceptanceEnv struct {
	t        *testing.T
	c        *httpRouteFaultClient
	clk      *clocktesting.FakePassiveClock
	internal *unstructured.Unstructured // the internal AIGatewayRoute as built
	external *unstructured.Unstructured // the external AIGatewayRoute as built, nil when disabled
}

// setHTTPRoute creates or replaces the internal HTTPRoute with a plain Update
// (Q21: the fake client has no status subresource for it).
func (e *passthroughAcceptanceEnv) setHTTPRoute(hr *unstructured.Unstructured) {
	e.t.Helper()
	ctx := context.Background()
	existing := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", hr.GetNamespace(), hr.GetName())
	err := e.c.Client.Get(ctx, client.ObjectKeyFromObject(hr), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := e.c.Create(ctx, hr); err != nil {
			e.t.Fatal(err)
		}
	case err != nil:
		e.t.Fatal(err)
	default:
		hr.SetResourceVersion(existing.GetResourceVersion())
		if err := e.c.Update(ctx, hr); err != nil {
			e.t.Fatal(err)
		}
	}
}

// acceptanceStep is one reconcile and what it must produce.
type acceptanceStep struct {
	// mutate runs before the clock advance and the reconcile.
	mutate  func(e *passthroughAcceptanceEnv)
	advance time.Duration

	wantStatus   metav1.ConditionStatus
	wantReason   string
	wantContains string // checked when non-empty
	wantPhase    llmv1alpha1.PassthroughModelPhase
	wantRequeue  time.Duration
	// wantLTT, when non-zero, is the stored LastTransitionTime of the
	// internal endpoint condition.
	wantLTT time.Time
	// sameMessage requires the stored message to equal the previous step's.
	sameMessage bool
	// wantExternal, when non-nil, is the stored external endpoint condition's
	// status and reason (only for scenarios with the external endpoint enabled).
	wantExternal *metav1.Condition
}

func TestPassthroughRouteAcceptance(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "provider-internal", errors.New("denied"))

	noEntry := func(e *passthroughAcceptanceEnv) { e.setHTTPRoute(httpRouteFor(e.internal, t0, 1)) }
	noListener := func(e *passthroughAcceptanceEnv) {
		e.setHTTPRoute(httpRouteFor(e.internal, t0, 1, parentEntry(e.internal, metav1.ConditionFalse, "NoMatchingParent", "No listeners match", 1)))
	}
	accepted := func(e *passthroughAcceptanceEnv) { e.setHTTPRoute(acceptedHTTPRouteFor(e.internal, t0)) }
	staleGeneration := func(e *passthroughAcceptanceEnv) {
		e.setHTTPRoute(httpRouteFor(e.internal, t0, 2, parentEntry(e.internal, metav1.ConditionTrue, "Accepted", "Route is accepted", 1)))
	}
	failReads := func(e *passthroughAcceptanceEnv) { e.c.err = forbidden }

	pending := func(requeue time.Duration) acceptanceStep {
		return acceptanceStep{
			wantStatus: metav1.ConditionUnknown, wantReason: reasonRoutePending,
			wantPhase: llmv1alpha1.PassthroughPhaseReady, wantRequeue: requeue,
		}
	}
	notAccepted := func(contains string, requeue time.Duration) acceptanceStep {
		return acceptanceStep{
			wantStatus: metav1.ConditionFalse, wantReason: reasonRouteNotAccepted, wantContains: contains,
			wantPhase: llmv1alpha1.PassthroughPhaseError, wantRequeue: requeue,
		}
	}
	ready := acceptanceStep{
		wantStatus: metav1.ConditionTrue, wantReason: reasonApplied,
		wantPhase: llmv1alpha1.PassthroughPhaseReady, wantRequeue: 0,
	}
	with := func(s acceptanceStep, f func(*acceptanceStep)) acceptanceStep {
		f(&s)
		return s
	}

	tests := []struct {
		name string
		// externalLeftover keeps the disabled external endpoint's
		// AIGatewayRoute and HTTPRoute in place, so its cleanup is pending.
		externalLeftover bool
		// externalEnabled leaves the external endpoint on; every other
		// scenario disables it.
		externalEnabled bool
		steps           []acceptanceStep
	}{
		{
			name: "missing Gateway (J1)",
			steps: []acceptanceStep{
				pending(5 * time.Second),
				with(notAccepted("AI Gateway has not generated HTTPRoute", time.Minute), func(s *acceptanceStep) { s.advance = 3 * time.Minute }),
				notAccepted("AI Gateway has not generated HTTPRoute", time.Minute),
			},
		},
		{
			name: "HTTPRoute with no entry (J1)",
			steps: []acceptanceStep{
				with(pending(5*time.Second), func(s *acceptanceStep) { s.mutate = noEntry }),
				with(notAccepted("reported no status", time.Minute), func(s *acceptanceStep) { s.advance = 3 * time.Minute }),
			},
		},
		{
			name: "no matching listener (J3), then heals with no CR edit (J4)",
			steps: []acceptanceStep{
				with(pending(5*time.Second), func(s *acceptanceStep) { s.mutate = noListener }),
				with(notAccepted("NoMatchingParent: No listeners match", time.Minute), func(s *acceptanceStep) { s.advance = 3 * time.Minute }),
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
			},
		},
		{
			name: "fresh healthy model (J5)",
			steps: []acceptanceStep{
				pending(5 * time.Second),
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
			},
		},
		{
			name: "stale latch (OPEN-2)",
			steps: []acceptanceStep{
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
				with(pending(5*time.Second), func(s *acceptanceStep) { s.mutate = staleGeneration; s.wantLTT = t0 }),
				with(pending(5*time.Second), func(s *acceptanceStep) { s.advance = time.Minute; s.wantLTT = t0 }),
				with(notAccepted("does not reflect the applied route", time.Minute), func(s *acceptanceStep) { s.advance = 2 * time.Minute }),
				with(notAccepted("does not reflect the applied route", time.Minute), func(s *acceptanceStep) { s.sameMessage = true }),
			},
		},
		{
			name: "parent-ref change on an accepted route (R2-F1)",
			steps: []acceptanceStep{
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
				with(pending(5*time.Second), func(s *acceptanceStep) { s.mutate = noEntry; s.advance = 3 * time.Minute }),
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
			},
		},
		{
			name: "read error on RouteNotAccepted (R2-F4)",
			steps: []acceptanceStep{
				pending(5 * time.Second),
				with(notAccepted("AI Gateway has not generated HTTPRoute", time.Minute), func(s *acceptanceStep) { s.advance = 3 * time.Minute }),
				with(notAccepted(readErrorMarker, time.Minute), func(s *acceptanceStep) { s.mutate = failReads }),
				with(notAccepted(readErrorMarker, time.Minute), func(s *acceptanceStep) { s.sameMessage = true }),
			},
		},
		{
			name: "read error on a healthy endpoint (F4)",
			steps: []acceptanceStep{
				with(ready, func(s *acceptanceStep) { s.mutate = accepted }),
				{
					mutate:     failReads,
					wantStatus: metav1.ConditionUnknown, wantReason: reasonRouteStatusUnreadable, wantContains: forbidden.Error(),
					wantPhase: llmv1alpha1.PassthroughPhaseReady, wantRequeue: 15 * time.Second,
				},
			},
		},
		{
			name:             "cleanup not slowed (R2-F3)",
			externalLeftover: true,
			steps: []acceptanceStep{
				pending(5 * time.Second),
				with(notAccepted("AI Gateway has not generated HTTPRoute", 15*time.Second), func(s *acceptanceStep) { s.advance = 3 * time.Minute }),
			},
		},
		{
			name:            "both endpoints in different states",
			externalEnabled: true,
			steps: []acceptanceStep{
				with(pending(5*time.Second), func(s *acceptanceStep) {
					s.mutate = func(e *passthroughAcceptanceEnv) { e.setHTTPRoute(acceptedHTTPRouteFor(e.external, t0)) }
					s.wantExternal = &metav1.Condition{Status: metav1.ConditionTrue, Reason: reasonApplied}
				}),
				with(notAccepted("AI Gateway has not generated HTTPRoute default/provider-internal", time.Minute), func(s *acceptanceStep) {
					s.advance = 3 * time.Minute
					s.wantExternal = &metav1.Condition{Status: metav1.ConditionTrue, Reason: reasonApplied}
				}),
			},
		},
		{
			name: "stored LTT from fake clock (R2-F2)",
			steps: []acceptanceStep{
				with(pending(5*time.Second), func(s *acceptanceStep) { s.wantLTT = t0 }),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pm := newPassthroughModel("provider", "default")
			pm.UID = "route-acceptance-test"
			pm.Generation = 1
			pm.Finalizers = []string{finalizerName}
			if !tt.externalEnabled {
				disabled := false
				pm.Spec.Endpoints.External.Enabled = &disabled
			}

			scheme := apikeysTestScheme(t)
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			secret := namedSecret(pm.Spec.Provider.CredentialSecretName, pm.Namespace)
			secret.Data = map[string][]byte{"apiKey": []byte("test-key")}
			objects := []client.Object{pm, secret}
			if tt.externalLeftover {
				name := reconcilers.ModelRouteName(pm.Name, "external")
				route := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", pm.Namespace, name)
				route.SetFinalizers([]string{"aigateway.envoyproxy.io/finalizer"})
				if err := controllerutil.SetControllerReference(pm, route, scheme); err != nil {
					t.Fatal(err)
				}
				objects = append(objects, route, endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", pm.Namespace, name))
			}

			resources, err := reconcilers.BuildPassthroughResources(pm, testConfig(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			clk := clocktesting.NewFakePassiveClock(t0)
			env := &passthroughAcceptanceEnv{
				t: t,
				c: &httpRouteFaultClient{
					Client:   fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(pm).WithObjects(objects...).Build(),
					failName: resources.InternalRoute.GetName(),
				},
				clk:      clk,
				internal: resources.InternalRoute,
				external: resources.ExternalRoute,
			}
			r := &PassthroughModelReconciler{Client: env.c, Scheme: scheme, Config: testConfig(), Clock: clk}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pm)}

			var prevMessage string
			for i, step := range tt.steps {
				if step.mutate != nil {
					step.mutate(env)
				}
				clk.SetTime(clk.Now().Add(step.advance))
				result, err := r.Reconcile(ctx, req)
				if err != nil {
					t.Fatalf("step %d: reconcile error: %v", i, err)
				}
				stored := &llmv1alpha1.PassthroughModel{}
				if err := env.c.Client.Get(ctx, req.NamespacedName, stored); err != nil {
					t.Fatal(err)
				}
				cond := meta.FindStatusCondition(stored.Status.Conditions, CondInternalEndpointReady)
				if cond == nil {
					t.Fatalf("step %d: no %s condition in %+v", i, CondInternalEndpointReady, stored.Status.Conditions)
				}
				if cond.Status != step.wantStatus || cond.Reason != step.wantReason {
					t.Errorf("step %d: condition = %s/%s (%q), want %s/%s", i, cond.Status, cond.Reason, cond.Message, step.wantStatus, step.wantReason)
				}
				if step.wantContains != "" && !strings.Contains(cond.Message, step.wantContains) {
					t.Errorf("step %d: message = %q, want it to contain %q", i, cond.Message, step.wantContains)
				}
				if stored.Status.Phase != step.wantPhase {
					t.Errorf("step %d: phase = %s, want %s", i, stored.Status.Phase, step.wantPhase)
				}
				if result.RequeueAfter != step.wantRequeue {
					t.Errorf("step %d: RequeueAfter = %s, want %s", i, result.RequeueAfter, step.wantRequeue)
				}
				if !step.wantLTT.IsZero() && !cond.LastTransitionTime.Time.Equal(step.wantLTT) {
					t.Errorf("step %d: LastTransitionTime = %v, want %v", i, cond.LastTransitionTime.Time, step.wantLTT)
				}
				if step.sameMessage && cond.Message != prevMessage {
					t.Errorf("step %d: message = %q, want unchanged %q", i, cond.Message, prevMessage)
				}
				if step.wantExternal != nil {
					ext := meta.FindStatusCondition(stored.Status.Conditions, CondExternalEndpointReady)
					if ext == nil || ext.Status != step.wantExternal.Status || ext.Reason != step.wantExternal.Reason {
						t.Errorf("step %d: external condition = %+v, want %s/%s", i, ext, step.wantExternal.Status, step.wantExternal.Reason)
					}
				}
				prevMessage = cond.Message
			}
		})
	}
}
