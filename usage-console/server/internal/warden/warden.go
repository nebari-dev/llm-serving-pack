// Package warden is Warden (spec §4.5 steps 1–7): an Envoy ext_proc service
// that Agent Router calls with each request's headers and buffered body. It
// runs the same engine as devgateway (budgets, rules, detectors, aliases), so
// both give the same verdict for the same request, and acts on it: a block
// becomes an immediate 403/429/503, a redaction rewrites the body, and a
// reroute rewrites the model and sets a backend hint the AIGatewayRoute
// matches on. When a redacting rule rehydrates on return, the response comes
// back through it too, and the redacted values go back in (rehydrate.go).
// Requests come in OpenAI's chat completions, or, on /anthropic/...,
// Anthropic's Messages API (anthropic.go); refusals and responses are in the
// caller's.
//
// It must run before Agent Router's own ext_proc, which reads the model and
// keeps a copy of the body it replays on retries; aigw/base.yaml orders the
// filters so it does. The decision goes back as dynamic metadata, which the
// access log records and receipt-ingest lays over the gateway's own fields.
//
// §9.3: config comes from a cached snapshot (no database call per request),
// evaluation has a deadline after which the rules' fail mode applies, a panic
// fails the same way, and a kill switch turns it into a pass-through.
package warden

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	procmodev3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/model"
	"github.com/jbouder/stargate/server/internal/store"
)

const (
	// MetadataNamespace is where the decision goes; the EnvoyExtensionPolicy
	// must list it as writable and the access log reads it from there.
	MetadataNamespace = "io.stargate.warden"
	MetadataKey       = "policy"
	// HeaderBackend is the routing hint for a reroute. The compiled
	// AIGatewayRoute has a rule per backend that matches it (routing.HintHeader),
	// and aigw/base.yaml strips any the caller sends.
	HeaderBackend = "x-stargate-backend"
)

// headerAfterAgentRouter is set by Agent Router's ext_proc once it has read
// the body. Warden seeing it means the filters are in the wrong order; the
// gateway strips any the caller sends.
const headerAfterAgentRouter = "x-ai-eg-original-path"

// grace is how long past the deadline the engine gets to report a per-rule
// fail mode before the watchdog decides for the whole request.
const grace = 5 * time.Millisecond

type Server struct {
	extprocv3.UnimplementedExternalProcessorServer

	Snap     *gateway.Current
	Deadline time.Duration // default 50ms
	// Refresh asks for an early snapshot reload, when a request carries a key
	// the snapshot doesn't have yet. It must not block.
	Refresh func()
	Now     func() time.Time

	passthrough atomic.Bool
	// throttle is each key's recent requests while a throttle budget is over
	// its cap. Warden runs as one process, so in-memory counts are the
	// gateway's; several replicas would each allow the rate.
	throttle gateway.Throttle
	// evaluate is the decision; tests swap it to simulate slow or broken rules.
	evaluate func(snap *gateway.Snapshot, k *store.KeyRecord, in gateway.Input) *gateway.Decision
}

// SetPassthrough flips the kill switch: while on, requests pass unpoliced.
func (s *Server) SetPassthrough(on bool) { s.passthrough.Store(on) }
func (s *Server) Passthrough() bool      { return s.passthrough.Load() }

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Process handles one request's stream. Agent Router is configured to send
// request headers and the buffered request body, then the response headers
// and the response body as it streams. Envoy opens one stream per request,
// so what the request redacted is still in this stream's vault when the
// response arrives; it goes when the stream does.
func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	var headers map[string]string
	var back responseState
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
			return nil
		}
		if err != nil {
			return err
		}
		var resp *extprocv3.ProcessingResponse
		switch v := req.Request.(type) {
		case *extprocv3.ProcessingRequest_RequestHeaders:
			headers = headerMap(v.RequestHeaders.GetHeaders())
			resp = &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: &extprocv3.HeadersResponse{}}}
		case *extprocv3.ProcessingRequest_RequestBody:
			d := s.decideWith(headers, v.RequestBody.GetBody())
			resp, back = d.resp, responseState{vault: d.vault, policy: d.policy, api: gateway.APIOf(headers[":path"])}
		case *extprocv3.ProcessingRequest_ResponseHeaders:
			resp = back.headers(headerMap(v.ResponseHeaders.GetHeaders()))
		case *extprocv3.ProcessingRequest_ResponseBody:
			resp = back.body(v.ResponseBody)
		default:
			// Phases the filter isn't configured to send; let them through.
			resp = passOther(req)
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// verdict is the answer to a request body, and what the response needs from
// it: the vault to rehydrate from and the decision to add the count to.
type verdict struct {
	resp   *extprocv3.ProcessingResponse
	vault  *gateway.Vault
	policy gateway.Policy
}

func (s *Server) decide(headers map[string]string, body []byte) *extprocv3.ProcessingResponse {
	return s.decideWith(headers, body).resp
}

// decideWith runs the engine under the deadline and a panic guard.
func (s *Server) decideWith(headers map[string]string, body []byte) verdict {
	snap := s.Snap.Load()
	if s.Passthrough() {
		return verdict{resp: s.unpoliced(snap, "passthrough", "", "Warden in pass-through (kill switch) · not evaluated")}
	}
	limit := s.Deadline
	if limit <= 0 {
		limit = 50 * time.Millisecond
	}
	keyID := headers[strings.ToLower(gateway.HeaderKeyID)]
	api := gateway.APIOf(headers[":path"])
	// The model, once the body is parsed, so a request refused for want of a
	// decision still says what it asked for.
	var requested atomic.Pointer[string]
	requestedModel := func() string {
		if m := requested.Load(); m != nil {
			return *m
		}
		return ""
	}
	start := s.now()
	done := make(chan verdict, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("warden: panic evaluating %s: %v", headers["x-request-id"], r)
				done <- verdict{resp: s.failMode(snap, api, keyID, requestedModel(), "evaluation panicked")}
			}
		}()
		done <- s.evaluateRequest(snap, headers, body, start, start.Add(limit), &requested)
	}()
	t := time.NewTimer(limit + grace)
	defer t.Stop()
	select {
	case r := <-done:
		return r
	case <-t.C:
		log.Printf("warden: %s not decided within %v", headers["x-request-id"], limit)
		return verdict{resp: s.failMode(snap, api, keyID, requestedModel(), fmt.Sprintf("not decided within %v", limit))}
	}
}

func (s *Server) evaluateRequest(snap *gateway.Snapshot, h map[string]string, body []byte, now, deadline time.Time, requested *atomic.Pointer[string]) verdict {
	keyID := h[strings.ToLower(gateway.HeaderKeyID)]
	api := gateway.APIOf(h[":path"])
	fail := func(requested, reason string) verdict {
		return verdict{resp: s.failMode(snap, api, keyID, requested, reason)}
	}
	if _, ok := h[headerAfterAgentRouter]; ok {
		// Agent Router would route on the old model and replay the body it
		// kept, undoing redactions, so nothing Warden decides would hold.
		log.Printf("warden: running after Agent Router's ext_proc; check the filter order in aigw/base.yaml")
		return fail("", "Warden runs after Agent Router's ext_proc")
	}
	// The caller's API: OpenAI chat completions, or Anthropic's Messages.
	var cr fakellm.ChatRequest
	var mr *messagesRequest
	var err error
	if api == gateway.APIAnthropic {
		mr, cr, err = parseMessages(body)
	} else {
		err = json.Unmarshal(body, &cr)
	}
	if err != nil || cr.Model == "" {
		// Not a request the detectors can read (another endpoint, or OpenAI
		// content parts). Nothing was inspected, so the fail mode decides.
		return fail(cr.Model, "request body not inspectable")
	}
	requested.Store(&cr.Model)
	if keyID == "" {
		// The key check always sets it; without it there's no identity.
		return fail(cr.Model, "no identity from the key check")
	}
	k := snap.KeyByID(keyID)
	if k == nil {
		// Created since the last reload. The key check vouched for it and its
		// model, so evaluate rules with what the headers say and catch up.
		if s.Refresh != nil {
			s.Refresh()
		}
		rec := store.KeyRecord{APIKey: model.APIKey{ID: keyID, Name: keyID, Team: h[strings.ToLower(gateway.HeaderTeam)],
			Project: gateway.ProjectName(h[strings.ToLower(gateway.HeaderProject)]), ProjectID: h[strings.ToLower(gateway.HeaderProjectID)],
			AllowedModels: []string{snap.Resolve(cr.Model)}}}
		k = &rec
	}
	in := gateway.Input{Region: h["x-data-region"], SessionID: h["x-session-id"], Actor: h["x-actor"],
		Req: cr, Body: body, Now: now, Deadline: deadline, Throttle: &s.throttle}
	eval := s.evaluate
	if eval == nil {
		eval = func(snap *gateway.Snapshot, k *store.KeyRecord, in gateway.Input) *gateway.Decision {
			return gateway.AdmitKey(snap, k, in, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
		}
	}
	d := eval(snap, k, in)
	p := d.Policy(snap, s.now())
	md := metadata(p)
	if d.Reject != nil {
		return verdict{resp: reject(api, d.Reject.Status, d.Reject.Code, d.Reject.Message, keyID, md, d.Reject.RetryAfter)}
	}

	common := &extprocv3.CommonResponse{}
	if len(p.Redactions) > 0 || d.Rerouted() {
		var nb []byte
		if mr != nil {
			nb, err = mr.rewrite(d.Req, d.Rerouted())
		} else {
			nb, err = rewrite(body, d.Req, d.Rerouted())
		}
		if err != nil {
			return fail(cr.Model, "couldn't rewrite the request body: "+err.Error())
		}
		common.BodyMutation = &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_Body{Body: nb}}
		common.HeaderMutation = &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{setHeader("content-length", strconv.Itoa(len(nb)))}}
		if d.Rerouted() {
			common.HeaderMutation.SetHeaders = append(common.HeaderMutation.SetHeaders, setHeader(HeaderBackend, d.Candidates[0].Backend.Name))
		}
	}
	return verdict{resp: &extprocv3.ProcessingResponse{
		Response:        &extprocv3.ProcessingResponse_RequestBody{RequestBody: &extprocv3.BodyResponse{Response: common}},
		DynamicMetadata: md,
	}, vault: d.Vault, policy: p}
}

// responseState is what a request leaves for its response.
type responseState struct {
	vault  *gateway.Vault
	policy gateway.Policy
	api    string          // the caller's, which the response is in
	rh     *bodyRehydrator // nil: the body passes as sent
}

// headers decides whether the body needs Warden. With nothing to restore it
// asks Envoy not to send it (the policy's allowModeOverride), and passes it
// through if it comes anyway. Restoring changes the body's length, so
// content-length goes and Envoy sends it chunked.
func (st *responseState) headers(h map[string]string) *extprocv3.ProcessingResponse {
	if st.vault.Len() == 0 {
		return skipBody(nil)
	}
	if enc := h["content-encoding"]; enc != "" && enc != "identity" {
		return skipBody(metadata(st.final("not restored: the response is " + enc + "-encoded")))
	}
	if st.rh = newBodyRehydrator(st.vault, h["content-type"], st.api); st.rh == nil {
		return skipBody(metadata(st.final(fmt.Sprintf("not restored: a %q response isn't JSON or an event stream", h["content-type"]))))
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extprocv3.HeadersResponse{
		Response: &extprocv3.CommonResponse{HeaderMutation: &extprocv3.HeaderMutation{RemoveHeaders: []string{"content-length"}}},
	}}}
}

// body answers one chunk of the response body with what goes out in its
// place. At the end the decision gains the count restored, for the receipt.
func (st *responseState) body(b *extprocv3.HttpBody) *extprocv3.ProcessingResponse {
	br := &extprocv3.BodyResponse{}
	resp := &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{ResponseBody: br}}
	if st.rh == nil {
		return resp
	}
	m := &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_ClearBody{ClearBody: true}}
	if out := st.rh.Feed(b.GetBody(), b.GetEndOfStream()); len(out) > 0 {
		m.Mutation = &extprocv3.BodyMutation_Body{Body: out}
	}
	br.Response = &extprocv3.CommonResponse{BodyMutation: m}
	if b.GetEndOfStream() {
		resp.DynamicMetadata = metadata(st.final(""))
	}
	return resp
}

// final is the request's decision with the rehydration on it: a count per
// redaction, and a trace step saying what came back, or why nothing did.
func (st *responseState) final(skipped string) gateway.Policy {
	p := st.policy
	p.Redactions = slices.Clone(p.Redactions)
	restored := st.vault.Restored()
	var parts []string
	for i, r := range p.Redactions {
		if n := restored[r.Type]; n > 0 {
			p.Redactions[i].Rehydrated = n
			parts = append(parts, fmt.Sprintf("%d %s", n, r.Type))
		}
	}
	step := model.TraceStep{Step: gateway.StepRehydrated, Input: fmt.Sprintf("%d placeholders held", st.vault.Len()),
		Outcome: "restored " + strings.Join(parts, ", "), MS: 0.1, State: "ok"}
	switch {
	case skipped != "":
		step.Outcome, step.State = skipped, "warn"
	case len(parts) == 0:
		step.Outcome, step.State = "none in the response", "skip"
	}
	p.Trace = append(slices.Clone(p.Trace), step)
	return p
}

// skipBody answers response headers without asking for the body.
func skipBody(md *structpb.Struct) *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extprocv3.HeadersResponse{}},
		ModeOverride: &procmodev3.ProcessingMode{ResponseHeaderMode: procmodev3.ProcessingMode_SKIP,
			ResponseBodyMode: procmodev3.ProcessingMode_NONE, ResponseTrailerMode: procmodev3.ProcessingMode_SKIP},
		DynamicMetadata: md,
	}
}

// failMode decides a request Warden couldn't evaluate. It can't tell which
// policies would have matched, so it fails closed if any enforcing policy
// does (§4.5: fail mode is per policy).
func (s *Server) failMode(snap *gateway.Snapshot, api, keyID, requested, reason string) *extprocv3.ProcessingResponse {
	var closed *model.Policy
	for i, p := range snap.Policies {
		if p.Mode == "enforce" && p.FailMode == "closed" {
			closed = &snap.Policies[i]
			break
		}
	}
	if closed == nil {
		return s.unpoliced(snap, "fail-open", requested, reason+" · every enforced policy fails open")
	}
	outcome := fmt.Sprintf("%s · %s v%d fails closed", reason, closed.Name, closed.Version)
	p := gateway.Policy{Mode: "fail-closed", Verdict: "blocked", RequestedModel: requested, Rules: []model.RuleEval{}, Redactions: []model.Redaction{},
		Trace: []model.TraceStep{{Step: "Rules evaluated", Input: fmt.Sprintf("%d rules", snap.RuleCount()), Outcome: outcome, State: "fail"}},
		Blocked: &gateway.PolicyBlock{Status: 503, ErrorCode: "policy_unavailable", ErrorDetail: "Policy couldn't be evaluated (" + reason + ") and fails closed.",
			ResolvedModel: snap.Resolve(requested), Backend: "—", Provider: "—", Region: "—"}}
	return reject(api, 503, p.Blocked.ErrorCode, p.Blocked.ErrorDetail, keyID, metadata(p), 0)
}

// unpoliced lets the request through untouched, saying why in the receipt.
func (s *Server) unpoliced(snap *gateway.Snapshot, mode, requested, why string) *extprocv3.ProcessingResponse {
	p := gateway.Policy{Mode: mode, Verdict: "allowed", RequestedModel: requested, Rules: []model.RuleEval{}, Redactions: []model.Redaction{},
		Trace: []model.TraceStep{{Step: "Rules evaluated", Input: fmt.Sprintf("%d rules", snap.RuleCount()), Outcome: why, State: "warn"}}}
	return &extprocv3.ProcessingResponse{
		Response:        &extprocv3.ProcessingResponse_RequestBody{RequestBody: &extprocv3.BodyResponse{}},
		DynamicMetadata: metadata(p),
	}
}

// rewrite applies the engine's changes to the caller's JSON, leaving every
// field it doesn't own as sent: message contents, and the model on a reroute.
// Without a reroute the model stays as asked, so Agent Router's alias routes
// still see it.
func rewrite(body []byte, req fakellm.ChatRequest, rerouted bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(top["messages"], &msgs); err != nil {
		return nil, err
	}
	if len(msgs) != len(req.Messages) {
		return nil, fmt.Errorf("%d messages in the body, %d evaluated", len(msgs), len(req.Messages))
	}
	for i := range msgs {
		c, _ := json.Marshal(req.Messages[i].Content)
		msgs[i]["content"] = c
	}
	var err error
	if top["messages"], err = json.Marshal(msgs); err != nil {
		return nil, err
	}
	if rerouted {
		top["model"], _ = json.Marshal(req.Model)
	}
	return json.Marshal(top)
}

// reject answers the caller directly, in its API's error shape. keyID goes
// on the response the way the key check's 403 does, so the access log
// records the refusal. retryAfter, in seconds, is set for a refusal worth
// retrying (a throttle); 0 omits it.
func reject(api string, code int, errCode, msg, keyID string, md *structpb.Struct, retryAfter int) *extprocv3.ProcessingResponse {
	body := gateway.ErrorBody(api, code, errCode, msg)
	hm := &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{setHeader("content-type", "application/json")}}
	if keyID != "" {
		hm.SetHeaders = append(hm.SetHeaders, setHeader(gateway.HeaderKeyID, keyID))
	}
	if retryAfter > 0 {
		hm.SetHeaders = append(hm.SetHeaders, setHeader("retry-after", strconv.Itoa(retryAfter)))
	}
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{ImmediateResponse: &extprocv3.ImmediateResponse{
			Status: &typev3.HttpStatus{Code: typev3.StatusCode(code)}, Headers: hm, Body: body, Details: "warden_" + errCode,
		}},
		DynamicMetadata: md,
	}
}

// metadata carries the decision as one JSON string, which the access log
// copies verbatim.
func metadata(p gateway.Policy) *structpb.Struct {
	b, _ := json.Marshal(p)
	return &structpb.Struct{Fields: map[string]*structpb.Value{
		MetadataNamespace: structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
			MetadataKey: structpb.NewStringValue(string(b)),
		}}),
	}}
}

func setHeader(k, v string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		Header:       &corev3.HeaderValue{Key: k, RawValue: []byte(v)},
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
	}
}

func headerMap(hm *corev3.HeaderMap) map[string]string {
	m := make(map[string]string, len(hm.GetHeaders()))
	for _, h := range hm.GetHeaders() {
		v := h.GetValue()
		if len(h.GetRawValue()) > 0 {
			v = string(h.GetRawValue())
		}
		m[strings.ToLower(h.GetKey())] = v
	}
	return m
}

func passOther(req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestTrailers:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestTrailers{RequestTrailers: &extprocv3.TrailersResponse{}}}
	case *extprocv3.ProcessingRequest_ResponseHeaders:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extprocv3.HeadersResponse{}}}
	case *extprocv3.ProcessingRequest_ResponseBody:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{ResponseBody: &extprocv3.BodyResponse{}}}
	default:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseTrailers{ResponseTrailers: &extprocv3.TrailersResponse{}}}
	}
}

var _ extprocv3.ExternalProcessorServer = (*Server)(nil)
