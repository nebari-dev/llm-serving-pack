package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// routePendingWindow is the length of the creation window and of the
// previous-condition window.
const routePendingWindow = 2 * time.Minute

// readErrorMarker separates a kept RouteNotAccepted message from the HTTPRoute
// read error appended to it, so a later read error can replace the earlier one.
const readErrorMarker = "; last status read failed: "

// gatewayParent identifies the Gateway listener a route attaches to.
type gatewayParent struct {
	name        string
	namespace   string
	sectionName string
}

// routeObservation is what one read found out about an applied route.
type routeObservation struct {
	parent gatewayParent
	// routeName is the AIGatewayRoute's "<namespace>/<name>", for messages.
	routeName string
	// routeFound is false when the generated HTTPRoute does not exist.
	routeFound bool
	// stale is true when the HTTPRoute exists but does not yet describe the
	// applied AIGatewayRoute: its parentRefs or hostnames differ, or the
	// matching parent entry's Accepted condition was written for an older
	// HTTPRoute generation. A stale observation always has accepted == nil.
	// It is bounded by the previous-condition window only, never by anchor.
	stale bool
	// anchor starts the creation window: the generated HTTPRoute's
	// creationTimestamp, or the AIGatewayRoute's when routeFound is false.
	anchor time.Time
	// accepted is the Accepted condition of the current status.parents entry
	// that matches parent. nil means there is no such entry.
	accepted *metav1.Condition
	// aiGatewayMessage is the message of the AIGatewayRoute's NotAccepted
	// condition (or of Accepted=False), otherwise empty. It is used only to
	// enrich a RouteNotAccepted message.
	aiGatewayMessage string
}

// readRouteAcceptance takes the parent ref, hostnames, creationTimestamp and
// NotAccepted message from the applied AIGatewayRoute, then gets the
// HTTPRoute of the same name and namespace. NotFound is not an error.
func readRouteAcceptance(ctx context.Context, c client.Reader, route *unstructured.Unstructured) (routeObservation, error) {
	ns, name := route.GetNamespace(), route.GetName()
	refs, _, err := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if err != nil {
		return routeObservation{}, fmt.Errorf("AIGatewayRoute %s/%s parentRefs: %w", ns, name, err)
	}
	if len(refs) != 1 {
		return routeObservation{}, fmt.Errorf("AIGatewayRoute %s/%s has %d parentRefs, want 1", ns, name, len(refs))
	}
	parent, ok := parentFromRef(refs[0], ns)
	if !ok {
		return routeObservation{}, fmt.Errorf("AIGatewayRoute %s/%s parentRef is not an object", ns, name)
	}
	hostnames, _, err := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if err != nil {
		return routeObservation{}, fmt.Errorf("AIGatewayRoute %s/%s hostnames: %w", ns, name, err)
	}

	obs := routeObservation{
		parent:           parent,
		routeName:        ns + "/" + name,
		aiGatewayMessage: aiGatewayNotAcceptedMessage(route),
	}

	httpRoute := endpointObject("gateway.networking.k8s.io", "v1", "HTTPRoute", ns, name)
	if err := c.Get(ctx, client.ObjectKeyFromObject(httpRoute), httpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			obs.anchor = route.GetCreationTimestamp().Time
			return obs, nil
		}
		return routeObservation{}, fmt.Errorf("get HTTPRoute %s/%s: %w", ns, name, err)
	}
	obs.routeFound = true
	obs.anchor = httpRoute.GetCreationTimestamp().Time

	// The HTTPRoute must describe the applied AIGatewayRoute before its
	// status says anything about it.
	routeHostnames, _, err := unstructured.NestedStringSlice(httpRoute.Object, "spec", "hostnames")
	if err != nil {
		return routeObservation{}, fmt.Errorf("HTTPRoute %s/%s hostnames: %w", ns, name, err)
	}
	if !slices.Equal(routeHostnames, hostnames) {
		obs.stale = true
		return obs, nil
	}
	routeRefs, _, err := unstructured.NestedSlice(httpRoute.Object, "spec", "parentRefs")
	if err != nil {
		return routeObservation{}, fmt.Errorf("HTTPRoute %s/%s parentRefs: %w", ns, name, err)
	}
	if len(routeRefs) != 1 {
		obs.stale = true
		return obs, nil
	}
	if p, ok := parentFromRef(routeRefs[0], ns); !ok || p != parent {
		obs.stale = true
		return obs, nil
	}

	entries, _, err := unstructured.NestedSlice(httpRoute.Object, "status", "parents")
	if err != nil {
		return routeObservation{}, fmt.Errorf("HTTPRoute %s/%s status.parents: %w", ns, name, err)
	}
	for _, e := range entries {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if p, ok := parentFromRef(entry["parentRef"], ns); !ok || p != parent {
			continue
		}
		accepted, err := acceptedCondition(entry)
		if err != nil {
			return routeObservation{}, fmt.Errorf("HTTPRoute %s/%s Accepted condition: %w", ns, name, err)
		}
		if accepted != nil && accepted.ObservedGeneration != httpRoute.GetGeneration() {
			obs.stale = true
			return obs, nil
		}
		obs.accepted = accepted
		return obs, nil
	}
	return obs, nil
}

// parentFromRef reads a Gateway API parentRef. An empty namespace means the
// referencing route's own namespace. group and kind are ignored, so API-server
// defaulting cannot cause a mismatch.
func parentFromRef(ref interface{}, routeNamespace string) (gatewayParent, bool) {
	m, ok := ref.(map[string]interface{})
	if !ok {
		return gatewayParent{}, false
	}
	name, _, _ := unstructured.NestedString(m, "name")
	namespace, _, _ := unstructured.NestedString(m, "namespace")
	section, _, _ := unstructured.NestedString(m, "sectionName")
	if namespace == "" {
		namespace = routeNamespace
	}
	return gatewayParent{name: name, namespace: namespace, sectionName: section}, true
}

// acceptedCondition returns the Accepted condition of a status.parents entry,
// or nil when it has none.
func acceptedCondition(entry map[string]interface{}) (*metav1.Condition, error) {
	conds, _, err := unstructured.NestedSlice(entry, "conditions")
	if err != nil {
		return nil, err
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != "Accepted" {
			continue
		}
		var cond metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &cond); err != nil {
			return nil, err
		}
		return &cond, nil
	}
	return nil, nil
}

// aiGatewayNotAcceptedMessage returns the message of the AIGatewayRoute's
// first NotAccepted=False or Accepted=False condition, otherwise "".
func aiGatewayNotAcceptedMessage(route *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(route.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["status"] != string(metav1.ConditionFalse) {
			continue
		}
		if m["type"] == "NotAccepted" || m["type"] == "Accepted" {
			msg, _, _ := unstructured.NestedString(m, "message")
			return msg
		}
	}
	return ""
}

// realClockIfNil returns clk, or clock.RealClock{} when clk is nil.
func realClockIfNil(clk clock.PassiveClock) clock.PassiveClock {
	if clk == nil {
		return clock.RealClock{}
	}
	return clk
}

// endpointCondition is the one call both reconcilers make per enabled
// endpoint. applyErr != nil returns conditionFor(condType, applyErr, "")
// and reads nothing. Otherwise it reads acceptance and derives the condition.
// A NoMatch read error returns conditionFor(condType, err, ""), which is
// GatewayCRDUnavailable. Any other read error returns prev's status and
// reason with the error added to the message when prev is
// False/RouteNotAccepted, otherwise Unknown/RouteStatusUnreadable carrying
// the error text. Every returned condition has LastTransitionTime set to
// clk.Now().
func endpointCondition(
	ctx context.Context,
	c client.Reader,
	clk clock.PassiveClock,
	condType string,
	prev *metav1.Condition,
	applyErr error,
	route *unstructured.Unstructured,
	okMessage string,
) metav1.Condition {
	now := clk.Now()
	cond := func() metav1.Condition {
		if applyErr != nil {
			return conditionFor(condType, applyErr, "")
		}
		obs, err := readRouteAcceptance(ctx, c, route)
		switch {
		case err == nil:
			return routeAcceptanceCondition(condType, prev, obs, now, okMessage)
		case meta.IsNoMatchError(err):
			return conditionFor(condType, err, "")
		case prev != nil && prev.Status == metav1.ConditionFalse && prev.Reason == reasonRouteNotAccepted:
			// Keep the latch (R2-F4); replace any earlier read error.
			msg, _, _ := strings.Cut(prev.Message, readErrorMarker)
			return metav1.Condition{
				Type:    condType,
				Status:  metav1.ConditionFalse,
				Reason:  reasonRouteNotAccepted,
				Message: msg + readErrorMarker + err.Error(),
			}
		default:
			return metav1.Condition{
				Type:    condType,
				Status:  metav1.ConditionUnknown,
				Reason:  reasonRouteStatusUnreadable,
				Message: "reading HTTPRoute status: " + err.Error(),
			}
		}
	}()
	cond.LastTransitionTime = metav1.NewTime(now)
	return cond
}

// routeAcceptanceCondition is pure: no client, no clock. prev decides the
// previous-condition window; it is not read when the route is accepted. The
// returned condition has a zero LastTransitionTime; endpointCondition stamps it.
func routeAcceptanceCondition(condType string, prev *metav1.Condition, obs routeObservation, now time.Time, okMessage string) metav1.Condition {
	gw := obs.parent.namespace + "/" + obs.parent.name
	ls := obs.parent.sectionName
	hr := obs.routeName
	ai := ""
	if obs.aiGatewayMessage != "" {
		ai = "; AI Gateway: " + obs.aiGatewayMessage
	}

	// A zero t reads as a closed window: now.Sub saturates far past the window.
	open := func(t time.Time) bool { return now.Sub(t) <= routePendingWindow }
	prevOpen := prev == nil ||
		prev.Status == metav1.ConditionTrue ||
		(prev.Status == metav1.ConditionFalse && prev.Reason != reasonRouteNotAccepted) ||
		(prev.Status == metav1.ConditionUnknown && !prev.LastTransitionTime.IsZero() && open(prev.LastTransitionTime.Time))

	pending := func(what string) metav1.Condition {
		return metav1.Condition{
			Type:    condType,
			Status:  metav1.ConditionUnknown,
			Reason:  reasonRoutePending,
			Message: "waiting for Gateway " + gw + " listener " + ls + " to " + what,
		}
	}
	notAccepted := func(msg string) metav1.Condition {
		return metav1.Condition{
			Type:    condType,
			Status:  metav1.ConditionFalse,
			Reason:  reasonRouteNotAccepted,
			Message: msg,
		}
	}

	switch {
	case obs.accepted != nil && obs.accepted.Status == metav1.ConditionTrue:
		return metav1.Condition{
			Type:    condType,
			Status:  metav1.ConditionTrue,
			Reason:  reasonApplied,
			Message: okMessage + "; accepted by Gateway " + gw + " listener " + ls,
		}
	case obs.stale:
		// A stale route's creationTimestamp is old by definition, so only the
		// previous-condition window bounds it.
		if prevOpen {
			return pending("evaluate the route change")
		}
		return notAccepted("generated HTTPRoute " + hr + " does not reflect the applied route or was not re-evaluated by Gateway " + gw + " listener " + ls + ai)
	}

	// Not found, no entry, or a current Accepted that is not True: pending
	// while either window is open (Q15, R2-F1).
	if open(obs.anchor) || prevOpen {
		return pending("accept the route")
	}
	switch {
	case !obs.routeFound:
		return notAccepted("AI Gateway has not generated HTTPRoute " + hr + ai)
	case obs.accepted == nil:
		return notAccepted("Gateway " + gw + " listener " + ls + " reported no status for the route" + ai)
	default:
		return notAccepted("Gateway " + gw + " listener " + ls + " did not accept the route: " + obs.accepted.Reason + ": " + obs.accepted.Message)
	}
}
