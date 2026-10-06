package gateway

import (
	"strings"
	"testing"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

// §9.2: detected values never appear in what's stored. A captured prompt
// keeps a placeholder where any detector matched, policy or not.
func TestMaskReplacesEveryDetectedValue(t *testing.T) {
	d, _ := NewDetectors(nil)
	msgs := []fakellm.Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "mail ana@example.com, SSN 123-45-6789, again ana@example.com"}}
	got, resp := d.Mask(msgs, "Sure, I wrote to ana@example.com and bob@example.org")
	if strings.Contains(got[1].Content+resp, "@example") || strings.Contains(got[1].Content, "123-45-6789") {
		t.Fatalf("a detected value survived: %q / %q", got[1].Content, resp)
	}
	if got[1].Content != "mail [EMAIL_1], SSN [SSN_1], again [EMAIL_1]" {
		t.Errorf("prompt = %q", got[1].Content)
	}
	// Placeholders are shared between prompt and response: the same value is the same name.
	if resp != "Sure, I wrote to [EMAIL_1] and [EMAIL_2]" {
		t.Errorf("response = %q", resp)
	}
	if got[0].Content != "be brief" || msgs[1].Content == got[1].Content {
		t.Error("Mask must leave clean text alone and not modify its input")
	}
}

// Replay turns placeholders back into a value the same detector matches, so
// "contains entity" is decided exactly on masked content, counts included.
func TestUnmaskedSamplesMatchOnlyTheirOwnDetector(t *testing.T) {
	custom := []store.CustomEntity{{Name: "ticket", Pattern: `\bTCK-\d{5}\b`, Label: "TICKET", MustMatch: []string{"TCK-00042"}}}
	d, err := NewDetectors(custom)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range d.Entities() {
		det := d.all()[e]
		text := d.Unmask("x [" + det.label + "_1] y [" + det.label + "_2] z")
		for _, other := range d.Entities() {
			want := 0
			if other == e {
				want = 2
			}
			if n := d.find(other, text); n != want {
				t.Errorf("%s's sample in %q: %s found %d times, want %d", e, text, other, n, want)
			}
		}
	}
	if got := d.Unmask("[NOT_A_LABEL_1] stays"); got != "[NOT_A_LABEL_1] stays" {
		t.Errorf("unknown label changed: %q", got)
	}
}

func TestRouteForMatchesHeadersAsTheGatewayDoes(t *testing.T) {
	routes := []model.Route{
		{Name: "gpt", Match: model.RouteMatch{Models: []string{"gpt-*"}}},
		{Name: "gpt-eu", Match: model.RouteMatch{Models: []string{"gpt-5.5"}, Headers: []model.HeaderMatch{{Name: "X-Data-Region", Value: "eu"}}}},
		{Name: "all", Match: model.RouteMatch{Models: []string{"*"}}}, // a catch-all is kept last
	}
	for _, c := range []struct {
		model, region, want string
	}{{"gpt-5.5", "eu", "gpt-eu"}, {"gpt-5.5", "", "gpt"}, {"claude-sonnet-5", "eu", "all"}} {
		got := RouteFor(routes, c.model, map[string]string{"x-data-region": c.region})
		if got == nil || got.Name != c.want {
			t.Errorf("%s/%s: got %v, want %s", c.model, c.region, got, c.want)
		}
	}
	if RouteFor(routes[1:2], "gpt-5-mini", nil) != nil {
		t.Error("no route matches, so none captures")
	}
}
