package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// testGatewayKind is the Gateway API kind the API server defaults onto a
// parentRef.
const testGatewayKind = "Gateway"

// testAIGatewayRoute builds an applied AIGatewayRoute as the builders write
// it: one parentRef and one hostname, with the creationTimestamp the API
// server would have set.
func testAIGatewayRoute(ns, name, gwNS, gwName, section, host string, created time.Time) *unstructured.Unstructured {
	route := endpointObject("aigateway.envoyproxy.io", "v1beta1", "AIGatewayRoute", ns, name)
	route.SetCreationTimestamp(metav1.NewTime(created))
	route.Object["spec"] = map[string]interface{}{
		"hostnames": []interface{}{host},
		"parentRefs": []interface{}{
			map[string]interface{}{
				"name":        gwName,
				"namespace":   gwNS,
				"sectionName": section,
			},
		},
	}
	return route
}

// httpRouteFor builds the HTTPRoute AI Gateway generates for route, with the
// given status.parents entries.
func httpRouteFor(route *unstructured.Unstructured, created time.Time, generation int64, entries ...map[string]interface{}) *unstructured.Unstructured {
	hr := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", route.GetNamespace(), route.GetName())
	hr.SetGeneration(generation)
	hr.SetCreationTimestamp(metav1.NewTime(created))
	spec := route.Object["spec"].(map[string]interface{})
	hr.Object["spec"] = map[string]interface{}{
		"parentRefs": runtime.DeepCopyJSONValue(spec["parentRefs"]),
		"hostnames":  runtime.DeepCopyJSONValue(spec["hostnames"]),
	}
	parents := make([]interface{}, 0, len(entries))
	for _, e := range entries {
		parents = append(parents, e)
	}
	hr.Object["status"] = map[string]interface{}{"parents": parents}
	return hr
}

// parentEntry builds an Envoy Gateway status.parents entry for route's single
// parentRef.
func parentEntry(route *unstructured.Unstructured, status metav1.ConditionStatus, reason, message string, observedGeneration int64) map[string]interface{} {
	refs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	return map[string]interface{}{
		"parentRef":      runtime.DeepCopyJSONValue(refs[0]),
		"controllerName": "gateway.envoyproxy.io/gatewayclass-controller",
		"conditions": []interface{}{
			map[string]interface{}{
				"type":               "Accepted",
				"status":             string(status),
				"reason":             reason,
				"message":            message,
				"observedGeneration": observedGeneration,
				"lastTransitionTime": "2026-11-20T12:00:03Z",
			},
			map[string]interface{}{
				"type":               "ResolvedRefs",
				"status":             "True",
				"reason":             "ResolvedRefs",
				"message":            "Resolved all the Object references for the Route",
				"observedGeneration": observedGeneration,
				"lastTransitionTime": "2026-11-20T12:00:03Z",
			},
		},
	}
}

// acceptedHTTPRouteFor is a current HTTPRoute that the Gateway accepted.
func acceptedHTTPRouteFor(route *unstructured.Unstructured, created time.Time) *unstructured.Unstructured {
	return httpRouteFor(route, created, 1, parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1))
}

// httpRouteFaultReader fails every HTTPRoute Get with err.
type httpRouteFaultReader struct {
	client.Reader
	err error
}

func (r httpRouteFaultReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "HTTPRoute" {
		return r.err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestReadRouteAcceptance(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Second)
	defaultParent := gatewayParent{name: "nebari-gateway", namespace: "envoy-gateway-system", sectionName: "llm-internal-https"}
	baseRoute := func() *unstructured.Unstructured {
		return testAIGatewayRoute("models", "m-internal", "envoy-gateway-system", "nebari-gateway", "llm-internal-https", "llm-internal.example.com", t0)
	}
	setRouteConditions := func(route *unstructured.Unstructured, conds ...map[string]interface{}) {
		list := make([]interface{}, 0, len(conds))
		for _, c := range conds {
			list = append(list, c)
		}
		route.Object["status"] = map[string]interface{}{"conditions": list}
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "m-internal", nil)
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, SearchedVersions: []string{"v1"}}

	type want struct {
		routeFound       bool
		stale            bool
		acceptedStatus   metav1.ConditionStatus // "" means accepted == nil
		acceptedReason   string                 // checked when non-empty
		anchor           time.Time              // checked when non-zero
		parent           *gatewayParent         // checked when non-nil
		routeName        string                 // checked when non-empty
		aiGatewayMessage string
	}
	tests := []struct {
		name string
		// route returns the applied AIGatewayRoute; nil uses baseRoute.
		route func() *unstructured.Unstructured
		// seed returns the HTTPRoute to store, or nil for none.
		seed func(route *unstructured.Unstructured) *unstructured.Unstructured
		// fault, when set, is returned for every HTTPRoute Get.
		fault     error
		want      want
		wantErr   string
		wantErrIs func(error) bool
	}{
		{
			name: "accepted current entry",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				return acceptedHTTPRouteFor(route, t1)
			},
			want: want{routeFound: true, acceptedStatus: metav1.ConditionTrue, anchor: t1, parent: &defaultParent, routeName: "models/m-internal"},
		},
		{
			name: "not found falls back to the AIGatewayRoute anchor (F3)",
			want: want{routeFound: false, anchor: t0, parent: &defaultParent, routeName: "models/m-internal"},
		},
		{
			name: "found with no entry",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured { return httpRouteFor(route, t1, 1) },
			want: want{routeFound: true, anchor: t1},
		},
		{
			name: "entry for another listener is ignored",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				e := parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1)
				e["parentRef"].(map[string]interface{})["sectionName"] = "llm-https"
				return httpRouteFor(route, t1, 1, e)
			},
			want: want{routeFound: true, anchor: t1},
		},
		{
			name: "Accepted=False current entry",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				return httpRouteFor(route, t1, 1, parentEntry(route, metav1.ConditionFalse, "NoMatchingParent", "No listeners included by this parent ref allowed this attachment.", 1))
			},
			want: want{routeFound: true, acceptedStatus: metav1.ConditionFalse, acceptedReason: "NoMatchingParent", anchor: t1},
		},
		{
			name: "old observedGeneration is stale (F1)",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				return httpRouteFor(route, t1, 2, parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1))
			},
			want: want{routeFound: true, stale: true, anchor: t1},
		},
		{
			name: "current entry at a newer generation is accepted",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				return httpRouteFor(route, t1, 2, parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 2))
			},
			want: want{routeFound: true, acceptedStatus: metav1.ConditionTrue, anchor: t1},
		},
		{
			name: "hostnames differ is stale (F1)",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				hr := acceptedHTTPRouteFor(route, t1)
				hr.Object["spec"].(map[string]interface{})["hostnames"] = []interface{}{"old.example.com"}
				return hr
			},
			want: want{routeFound: true, stale: true, anchor: t1},
		},
		{
			name: "parentRef differs is stale (F1)",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				hr := acceptedHTTPRouteFor(route, t1)
				hr.Object["spec"].(map[string]interface{})["parentRefs"].([]interface{})[0].(map[string]interface{})["name"] = "old-gateway"
				return hr
			},
			want: want{routeFound: true, stale: true, anchor: t1},
		},
		{
			name: "defaulted group/kind on HTTPRoute parentRef still current",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				hr := acceptedHTTPRouteFor(route, t1)
				ref := hr.Object["spec"].(map[string]interface{})["parentRefs"].([]interface{})[0].(map[string]interface{})
				ref["group"] = "gateway.networking.k8s.io"
				ref["kind"] = testGatewayKind
				return hr
			},
			want: want{routeFound: true, acceptedStatus: metav1.ConditionTrue, anchor: t1},
		},
		{
			name: "entry with empty namespace matches a Gateway in the route's namespace",
			route: func() *unstructured.Unstructured {
				return testAIGatewayRoute("models", "m-internal", "models", "nebari-gateway", "llm-internal-https", "llm-internal.example.com", t0)
			},
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				e := parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1)
				delete(e["parentRef"].(map[string]interface{}), "namespace")
				return httpRouteFor(route, t1, 1, e)
			},
			want: want{routeFound: true, acceptedStatus: metav1.ConditionTrue, anchor: t1, parent: &gatewayParent{name: "nebari-gateway", namespace: "models", sectionName: "llm-internal-https"}},
		},
		{
			name: "entry with empty namespace does not match a Gateway elsewhere",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				e := parentEntry(route, metav1.ConditionTrue, "Accepted", "Route is accepted", 1)
				delete(e["parentRef"].(map[string]interface{}), "namespace")
				return httpRouteFor(route, t1, 1, e)
			},
			want: want{routeFound: true, anchor: t1},
		},
		{
			name: "AI Gateway NotAccepted message (F2)",
			route: func() *unstructured.Unstructured {
				r := baseRoute()
				setRouteConditions(r, map[string]interface{}{"type": "NotAccepted", "status": "False", "reason": "NotAccepted", "message": "gateway missing"})
				return r
			},
			want: want{routeFound: false, anchor: t0, aiGatewayMessage: "gateway missing"},
		},
		{
			name: "AI Gateway Accepted=False message",
			route: func() *unstructured.Unstructured {
				r := baseRoute()
				setRouteConditions(r, map[string]interface{}{"type": "Accepted", "status": "False", "message": "x"})
				return r
			},
			want: want{routeFound: false, anchor: t0, aiGatewayMessage: "x"},
		},
		{
			name: "AI Gateway Accepted=True gives no message",
			route: func() *unstructured.Unstructured {
				r := baseRoute()
				setRouteConditions(r, map[string]interface{}{"type": "Accepted", "status": "True", "message": "ok"})
				return r
			},
			want: want{routeFound: false, anchor: t0, aiGatewayMessage: ""},
		},
		{
			name: "two parentRefs is an error",
			route: func() *unstructured.Unstructured {
				r := baseRoute()
				refs := r.Object["spec"].(map[string]interface{})["parentRefs"].([]interface{})
				r.Object["spec"].(map[string]interface{})["parentRefs"] = append(refs, map[string]interface{}{"name": "other", "namespace": "x", "sectionName": "y"})
				return r
			},
			wantErr: "has 2 parentRefs, want 1",
		},
		{
			name:      "HTTPRoute read error is returned",
			fault:     forbidden,
			wantErrIs: apierrors.IsForbidden,
		},
		{
			name:      "HTTPRoute NoMatch error stays detectable",
			fault:     noMatch,
			wantErrIs: meta.IsNoMatchError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := baseRoute()
			if tt.route != nil {
				route = tt.route()
			}
			builder := fake.NewClientBuilder()
			if tt.seed != nil {
				builder = builder.WithObjects(tt.seed(route))
			}
			var reader client.Reader = builder.Build()
			if tt.fault != nil {
				reader = httpRouteFaultReader{Reader: reader, err: tt.fault}
			}

			obs, err := readRouteAcceptance(context.Background(), reader, route)

			if tt.wantErr != "" || tt.wantErrIs != nil {
				if err == nil {
					t.Fatalf("err = nil, want error")
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %q, want it to contain %q", err, tt.wantErr)
				}
				if tt.wantErrIs != nil && !tt.wantErrIs(err) {
					t.Errorf("err = %v, wrong kind", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			w := tt.want
			if obs.routeFound != w.routeFound {
				t.Errorf("routeFound = %v, want %v", obs.routeFound, w.routeFound)
			}
			if obs.stale != w.stale {
				t.Errorf("stale = %v, want %v", obs.stale, w.stale)
			}
			if w.acceptedStatus == "" {
				if obs.accepted != nil {
					t.Errorf("accepted = %+v, want nil", *obs.accepted)
				}
			} else {
				if obs.accepted == nil {
					t.Fatalf("accepted = nil, want status %s", w.acceptedStatus)
				}
				if obs.accepted.Status != w.acceptedStatus {
					t.Errorf("accepted.Status = %s, want %s", obs.accepted.Status, w.acceptedStatus)
				}
				if w.acceptedReason != "" && obs.accepted.Reason != w.acceptedReason {
					t.Errorf("accepted.Reason = %s, want %s", obs.accepted.Reason, w.acceptedReason)
				}
			}
			if !w.anchor.IsZero() && !obs.anchor.Equal(w.anchor) {
				t.Errorf("anchor = %v, want %v", obs.anchor, w.anchor)
			}
			if w.parent != nil && obs.parent != *w.parent {
				t.Errorf("parent = %+v, want %+v", obs.parent, *w.parent)
			}
			if w.routeName != "" && obs.routeName != w.routeName {
				t.Errorf("routeName = %q, want %q", obs.routeName, w.routeName)
			}
			if obs.aiGatewayMessage != w.aiGatewayMessage {
				t.Errorf("aiGatewayMessage = %q, want %q", obs.aiGatewayMessage, w.aiGatewayMessage)
			}
		})
	}
}

func TestRouteAcceptanceCondition(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Minute)
	young := now.Add(-time.Minute)
	old := now.Add(-3 * time.Minute)
	const condType = CondInternalEndpointReady
	const okMessage = "internal route and JWT policy applied"
	const gwListener = "Gateway envoy-gateway-system/nebari-gateway listener llm-internal-https"

	prevUnknown := func(ts time.Time) *metav1.Condition {
		return &metav1.Condition{Type: condType, Status: metav1.ConditionUnknown, Reason: reasonRoutePending, LastTransitionTime: metav1.NewTime(ts)}
	}
	prevTrue := func() *metav1.Condition {
		return &metav1.Condition{Type: condType, Status: metav1.ConditionTrue, Reason: reasonApplied, LastTransitionTime: metav1.NewTime(old)}
	}
	prevFalse := func(reason string) *metav1.Condition {
		return &metav1.Condition{Type: condType, Status: metav1.ConditionFalse, Reason: reason, LastTransitionTime: metav1.NewTime(old)}
	}
	// obs builds an observation for the default route and parent.
	obs := func(found bool, anchor time.Time) routeObservation {
		return routeObservation{
			parent:     gatewayParent{name: "nebari-gateway", namespace: "envoy-gateway-system", sectionName: "llm-internal-https"},
			routeName:  "models/m-internal",
			routeFound: found,
			anchor:     anchor,
		}
	}
	withAccepted := func(o routeObservation, status metav1.ConditionStatus, reason, message string) routeObservation {
		o.accepted = &metav1.Condition{Type: "Accepted", Status: status, Reason: reason, Message: message}
		return o
	}
	stale := func(anchor time.Time) routeObservation {
		o := obs(true, anchor)
		o.stale = true
		return o
	}
	notFoundWithAI := func() routeObservation {
		o := obs(false, old)
		o.aiGatewayMessage = "gateway missing"
		return o
	}
	const egMessage = "No listeners included by this parent ref allowed this attachment."

	tests := []struct {
		name         string
		obs          routeObservation
		prev         *metav1.Condition
		wantStatus   metav1.ConditionStatus
		wantReason   string
		wantMessage  string // exact when non-empty
		wantSuffix   string // checked when non-empty
		wantContains string // checked when non-empty
	}{
		{
			name:        "accepted is True and ignores prev",
			obs:         withAccepted(obs(true, old), metav1.ConditionTrue, "Accepted", "Route is accepted"),
			prev:        prevFalse(reasonRouteNotAccepted),
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonApplied,
			wantMessage: okMessage + "; accepted by " + gwListener,
			wantSuffix:  "accepted by Gateway envoy-gateway-system/nebari-gateway listener llm-internal-https",
		},
		{
			name:        "not found inside creation window",
			obs:         obs(false, young),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  reasonRoutePending,
			wantMessage: "waiting for " + gwListener + " to accept the route",
		},
		{
			name:        "not found, both windows closed, AI message appended (F3, Q11)",
			obs:         notFoundWithAI(),
			prev:        prevUnknown(old),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonRouteNotAccepted,
			wantMessage: "AI Gateway has not generated HTTPRoute models/m-internal; AI Gateway: gateway missing",
		},
		{
			name:        "no entry, both closed",
			obs:         obs(true, old),
			prev:        prevUnknown(old),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonRouteNotAccepted,
			wantMessage: gwListener + " reported no status for the route",
		},
		{
			name:       "Accepted=False inside window is pending (Q15)",
			obs:        withAccepted(obs(true, young), metav1.ConditionFalse, "NoMatchingParent", egMessage),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:         "Accepted=False after windows carries EG reason (Journey 3)",
			obs:          withAccepted(obs(true, old), metav1.ConditionFalse, "NoMatchingParent", egMessage),
			prev:         prevUnknown(old),
			wantStatus:   metav1.ConditionFalse,
			wantReason:   reasonRouteNotAccepted,
			wantMessage:  gwListener + " did not accept the route: NoMatchingParent: " + egMessage,
			wantContains: "NoMatchingParent: " + egMessage,
		},
		{
			name:       "creation window boundary: exactly 2m is open",
			obs:        obs(true, now.Add(-2*time.Minute)),
			prev:       prevUnknown(old),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "creation window boundary: 2m1s is closed",
			obs:        obs(true, now.Add(-2*time.Minute-time.Second)),
			prev:       prevUnknown(old),
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonRouteNotAccepted,
		},
		{
			name:       "either window: prev True, old route, no entry (R2-F1)",
			obs:        obs(true, old),
			prev:       prevTrue(),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "either window: prev True, Accepted=False",
			obs:        withAccepted(obs(true, old), metav1.ConditionFalse, "NoMatchingParent", egMessage),
			prev:       prevTrue(),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "either window: prev True, not found",
			obs:        obs(false, old),
			prev:       prevTrue(),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "fresh route with prev RouteNotAccepted is pending",
			obs:        obs(true, young),
			prev:       prevFalse(reasonRouteNotAccepted),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:        "stale, prev nil",
			obs:         stale(old),
			wantStatus:  metav1.ConditionUnknown,
			wantReason:  reasonRoutePending,
			wantMessage: "waiting for " + gwListener + " to evaluate the route change",
		},
		{
			name:       "stale, prev True",
			obs:        stale(old),
			prev:       prevTrue(),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "stale, prev ApplyFailed",
			obs:        stale(old),
			prev:       prevFalse(reasonApplyFailed),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:       "stale, prev young Unknown",
			obs:        stale(old),
			prev:       prevUnknown(young),
			wantStatus: metav1.ConditionUnknown,
			wantReason: reasonRoutePending,
		},
		{
			name:        "stale, prev old Unknown",
			obs:         stale(old),
			prev:        prevUnknown(old),
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonRouteNotAccepted,
			wantMessage: "generated HTTPRoute models/m-internal does not reflect the applied route or was not re-evaluated by " + gwListener,
		},
		{
			name:       "stale, prev Unknown with zero time",
			obs:        stale(old),
			prev:       prevUnknown(time.Time{}),
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonRouteNotAccepted,
		},
		{
			name:       "stale, prev RouteNotAccepted stays",
			obs:        stale(young),
			prev:       prevFalse(reasonRouteNotAccepted),
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonRouteNotAccepted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeAcceptanceCondition(condType, tt.prev, tt.obs, now, okMessage)

			if got.Type != condType {
				t.Errorf("Type = %q, want %q", got.Type, condType)
			}
			if got.Status != tt.wantStatus || got.Reason != tt.wantReason {
				t.Errorf("Status/Reason = %s/%s, want %s/%s (message %q)", got.Status, got.Reason, tt.wantStatus, tt.wantReason, got.Message)
			}
			if !got.LastTransitionTime.IsZero() {
				t.Errorf("LastTransitionTime = %v, want zero", got.LastTransitionTime)
			}
			if tt.wantMessage != "" && got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantSuffix != "" && !strings.HasSuffix(got.Message, tt.wantSuffix) {
				t.Errorf("Message = %q, want suffix %q", got.Message, tt.wantSuffix)
			}
			if tt.wantContains != "" && !strings.Contains(got.Message, tt.wantContains) {
				t.Errorf("Message = %q, want it to contain %q", got.Message, tt.wantContains)
			}
		})
	}
}

// failOnGetReader fails the test on any Get: it proves nothing was read.
type failOnGetReader struct {
	client.Reader
	t *testing.T
}

func (r failOnGetReader) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	r.t.Errorf("unexpected Get of %s", key)
	return nil
}

func TestEndpointCondition(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Second)
	now := t0.Add(10 * time.Minute)
	const condType = CondInternalEndpointReady
	const okMessage = "internal route and JWT policy applied"
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "m-internal", nil)
	noMatch := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, SearchedVersions: []string{"v1"}}
	const prevMessage = "Gateway envoy-gateway-system/nebari-gateway listener llm-internal-https reported no status for the route"
	prevNotAccepted := &metav1.Condition{
		Type: condType, Status: metav1.ConditionFalse, Reason: reasonRouteNotAccepted,
		Message: prevMessage, LastTransitionTime: metav1.NewTime(t0),
	}

	tests := []struct {
		name     string
		applyErr error
		// failOnGet makes any Get fail the test.
		failOnGet bool
		// seed returns the HTTPRoute to store, or nil for none.
		seed  func(route *unstructured.Unstructured) *unstructured.Unstructured
		fault error
		prev  *metav1.Condition
		// repeat feeds the first result back as prev and checks the second
		// result's message is identical.
		repeat       bool
		wantStatus   metav1.ConditionStatus
		wantReason   string
		wantMessage  string // exact when non-empty
		wantPrefix   string // checked when non-empty
		wantContains string // checked when non-empty
	}{
		{
			name:       "apply error short-circuits with no read",
			applyErr:   forbidden,
			failOnGet:  true,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonApplyFailed,
		},
		{
			name:       "NoMatch read error is GatewayCRDUnavailable",
			fault:      noMatch,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonGatewayCRDUnavailable,
		},
		{
			name:         "other read error is RouteStatusUnreadable",
			fault:        forbidden,
			wantStatus:   metav1.ConditionUnknown,
			wantReason:   reasonRouteStatusUnreadable,
			wantPrefix:   "reading HTTPRoute status: ",
			wantContains: forbidden.Error(),
		},
		{
			name:         "read error keeps RouteNotAccepted (R2-F4)",
			fault:        forbidden,
			prev:         prevNotAccepted,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   reasonRouteNotAccepted,
			wantPrefix:   prevMessage + readErrorMarker,
			wantContains: forbidden.Error(),
		},
		{
			name:       "repeated read error does not grow the message",
			fault:      forbidden,
			prev:       prevNotAccepted,
			repeat:     true,
			wantStatus: metav1.ConditionFalse,
			wantReason: reasonRouteNotAccepted,
			wantPrefix: prevMessage + readErrorMarker,
		},
		{
			name: "accepted route",
			seed: func(route *unstructured.Unstructured) *unstructured.Unstructured {
				return acceptedHTTPRouteFor(route, t1)
			},
			wantStatus:  metav1.ConditionTrue,
			wantReason:  reasonApplied,
			wantMessage: okMessage + "; accepted by Gateway envoy-gateway-system/nebari-gateway listener llm-internal-https",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := testAIGatewayRoute("models", "m-internal", "envoy-gateway-system", "nebari-gateway", "llm-internal-https", "llm-internal.example.com", t0)
			builder := fake.NewClientBuilder()
			if tt.seed != nil {
				builder = builder.WithObjects(tt.seed(route))
			}
			var reader client.Reader = builder.Build()
			if tt.fault != nil {
				reader = httpRouteFaultReader{Reader: reader, err: tt.fault}
			}
			if tt.failOnGet {
				reader = failOnGetReader{t: t}
			}
			clk := clocktesting.NewFakePassiveClock(now)

			got := endpointCondition(context.Background(), reader, clk, condType, tt.prev, tt.applyErr, route, okMessage)
			if tt.repeat {
				first := got
				got = endpointCondition(context.Background(), reader, clk, condType, &first, tt.applyErr, route, okMessage)
				if got.Message != first.Message {
					t.Errorf("repeated Message = %q, want unchanged %q", got.Message, first.Message)
				}
			}

			if got.Type != condType {
				t.Errorf("Type = %q, want %q", got.Type, condType)
			}
			if got.Status != tt.wantStatus || got.Reason != tt.wantReason {
				t.Errorf("Status/Reason = %s/%s, want %s/%s (message %q)", got.Status, got.Reason, tt.wantStatus, tt.wantReason, got.Message)
			}
			if !got.LastTransitionTime.Time.Equal(now) {
				t.Errorf("LastTransitionTime = %v, want the injected clock's %v (R2-F2)", got.LastTransitionTime.Time, now)
			}
			if tt.wantMessage != "" && got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantPrefix != "" && !strings.HasPrefix(got.Message, tt.wantPrefix) {
				t.Errorf("Message = %q, want prefix %q", got.Message, tt.wantPrefix)
			}
			if tt.wantContains != "" && !strings.Contains(got.Message, tt.wantContains) {
				t.Errorf("Message = %q, want it to contain %q", got.Message, tt.wantContains)
			}
			if strings.Count(got.Message, readErrorMarker) > 1 {
				t.Errorf("Message = %q carries more than one read error", got.Message)
			}
		})
	}
}

func TestRealClockIfNil(t *testing.T) {
	fakeClock := clocktesting.NewFakePassiveClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		in   clock.PassiveClock
		want clock.PassiveClock
	}{
		{name: "nil gives the real clock", in: nil, want: clock.RealClock{}},
		{name: "a fake is returned unchanged", in: fakeClock, want: fakeClock},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := realClockIfNil(tt.in); got != tt.want {
				t.Errorf("realClockIfNil(%v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
