// Package traffic generates demo requests with the mix the mockup showed:
// weighted keys, the occasional PII, secret or EU-region request, streaming
// about half the time. cmd/trafficgen sends them over HTTP; the backfill feeds
// them straight into the gateway engine.
package traffic

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/demo"
	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/model"
)

type Request struct {
	Secret    string
	Region    string
	SessionID string
	Actor     string
	Body      fakellm.ChatRequest
}

var actors = []string{"u_4412", "u_0981", "u_2231", "svc-digest", "u_7713", "", ""}

var filler = strings.Fields(`please review the attached ticket history and summarize the customer issue
with the relevant order details list the next steps for the support engineer and flag anything
that needs escalation keep the answer short and include the product area affected`)

func text(r *rand.Rand, tokens int) string {
	var b strings.Builder
	for b.Len() < tokens*4 {
		b.WriteString(filler[r.IntN(len(filler))])
		b.WriteByte(' ')
	}
	return b.String()
}

type Generator struct {
	keys    []model.APIKey
	weights []int
	total   int
}

func New() *Generator {
	g := &Generator{}
	for _, k := range demo.Keys {
		if w := demo.KeyWeights[k.ID]; w > 0 {
			g.keys = append(g.keys, k)
			g.weights = append(g.weights, w)
			g.total += w
		}
	}
	return g
}

func (g *Generator) key(r *rand.Rand) model.APIKey {
	x := r.IntN(g.total)
	for i, w := range g.weights {
		if x -= w; x < 0 {
			return g.keys[i]
		}
	}
	return g.keys[0]
}

// Next builds one request.
func (g *Generator) Next(r *rand.Rand) Request {
	k := g.key(r)
	m := k.AllowedModels[r.IntN(len(k.AllowedModels))]
	if k.Team == "batch" && r.Float64() < 0.5 {
		m = "summarize-digest"
	}
	if r.Float64() < 0.005 {
		// A model the key isn't allowed: blocked at admission.
		others := slices.DeleteFunc([]string{"gpt-5.5", "claude-opus-4-1", "claude-sonnet-5"}, func(x string) bool { return slices.Contains(k.AllowedModels, x) })
		if len(others) > 0 {
			m = others[r.IntN(len(others))]
		}
	}

	promptTokens := 200 + int(r.Float64()*r.Float64()*9000)
	if k.Team == "batch" {
		promptTokens = 200 + int(r.Float64()*r.Float64()*40000)
	}
	maxTokens := 900
	if k.Team == "agents" {
		maxTokens = 2400
	}
	if m == "smollm2" || m == "gpt-4o-mini" {
		// Real upstreams: a small model on this machine, OpenRouter's billing.
		promptTokens, maxTokens = 40+r.IntN(200), 64
	}
	user := text(r, promptTokens)

	req := Request{
		Secret: demo.DevSecret(k.Prefix),
		Actor:  actors[r.IntN(len(actors))],
	}
	switch x := r.Float64(); {
	case x < 0.035:
		user += "\nHere is the config that fails: OPENAI_KEY=sk-" + fmt.Sprintf("%024x", r.Uint64()) + "\n"
	case x < 0.075:
		user += fmt.Sprintf("\nCustomer contact: jordan.lee%d@example.com\n", r.IntN(900))
	case x < 0.11:
		user += fmt.Sprintf("\nCustomer SSN on file: %03d-%02d-%04d, email m.chen@example.org\n", 100+r.IntN(800), 10+r.IntN(89), 1000+r.IntN(8999))
	case x < 0.15:
		req.Region = "eu"
	case x < 0.155:
		user += "\nCard on file 4111 1111 1111 1111\n"
	}
	if k.Team == "agents" {
		req.SessionID = fmt.Sprintf("sess_%06x", r.IntN(1<<24))
	}
	req.Body = fakellm.ChatRequest{
		Model:     m,
		Stream:    r.Float64() < 0.55,
		MaxTokens: maxTokens,
		Messages: []fakellm.Message{
			{Role: "system", Content: "You are the " + k.Project + " assistant for the " + k.Team + " team."},
			{Role: "user", Content: user},
		},
	}
	return req
}
