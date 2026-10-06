package gateway

import (
	"regexp"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
)

// Mask is a request and its response as content capture stores them (spec
// §9.2: detected values never appear in what's kept): every value any
// detector matches, whether or not a rule acted on it, becomes its
// placeholder. Placeholders are shared, so one value is one name in both.
// The input isn't changed.
func (d *Detectors) Mask(msgs []fakellm.Message, response string) ([]fakellm.Message, string) {
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Content)
		all.WriteByte('\n')
	}
	all.WriteString(response)
	names := newPlaceholders(all.String())
	out := slices.Clone(msgs)
	for _, e := range d.Entities() {
		for i := range out {
			out[i].Content = d.redact(e, out[i].Content, names.For)
		}
		response = d.redact(e, response, names.For)
	}
	return out, response
}

var placeholder = regexp.MustCompile(`\[([A-Za-z][A-Za-z0-9_]*)_\d+\]`)

// Unmask puts a sample value back where a placeholder stands, one its
// detector matches, so a rule's "contains entity" decides on masked content
// as it would have on the original: the same entities, as many times.
// A placeholder no detector writes (an entity deleted since) stays.
func (d *Detectors) Unmask(text string) string {
	byLabel := map[string]string{}
	for _, det := range d.all() {
		if det.sample != "" {
			byLabel[det.label] = det.sample
		}
	}
	return placeholder.ReplaceAllStringFunc(text, func(ph string) string {
		if s, ok := byLabel[placeholder.FindStringSubmatch(ph)[1]]; ok {
			return s
		}
		return ph
	})
}

// RouteFor is the route the gateway would pick for a request as the caller
// sent it: of the routes whose model and header matches all hold, the one
// with the most header matches, then the first in rule order (the compiled
// AIGatewayRoute's precedence). nil when none matches.
func RouteFor(routes []model.Route, requested string, headers map[string]string) *model.Route {
	var best *model.Route
	for i := range routes {
		r := &routes[i]
		ok := slices.ContainsFunc(r.Match.Models, func(m string) bool {
			return m == requested || m == "*" || strings.HasSuffix(m, "*") && strings.HasPrefix(requested, strings.TrimSuffix(m, "*"))
		})
		for _, h := range r.Match.Headers {
			if v, k := headers[strings.ToLower(h.Name)]; !k || v != h.Value {
				ok = false
			}
		}
		if ok && (best == nil || len(r.Match.Headers) > len(best.Match.Headers)) {
			best = r
		}
	}
	return best
}
