package warden

import (
	"bufio"
	"bytes"
	"encoding/json"
	"mime"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
	"github.com/jbouder/stargate/server/internal/gateway"
	"github.com/jbouder/stargate/server/internal/ingest"
	"github.com/jbouder/stargate/server/internal/store"
)

// MaxCapture bounds the response a capture keeps; past it the rest passes
// through unkept and the content says it was cut.
const MaxCapture = 1 << 20

// capture is one request's content, kept on its ext_proc stream until the
// response ends (spec §9.2: per-route opt-in). It holds the prompt as the
// caller sent it, in memory only, like the vault; what's stored is masked
// (Detectors.Mask), so no detected value leaves the stream.
type capture struct {
	sink      func(store.CapturedContent)
	det       *gateway.Detectors
	tenant    string
	receiptID string
	route     string
	ts        time.Time
	msgs      []fakellm.Message
	api       string
	sse       bool
	buf       []byte
	truncated bool
	note      string // why the response isn't kept, or is partial
	ended     bool   // the response ended (or there was none: refused)
	done      bool
}

// newCapture is the capture for a request on a capturing route, or nil.
func (s *Server) newCapture(snap *gateway.Snapshot, h map[string]string, cr fakellm.ChatRequest, api string, now time.Time) *capture {
	if s.Capture == nil {
		return nil
	}
	route := snap.CaptureRoute(cr.Model, h)
	if route == "" {
		return nil
	}
	id := h["x-request-id"]
	return &capture{sink: s.Capture, det: snap.Detectors, tenant: snap.Tenant, receiptID: ingest.ReceiptID(id), route: route, ts: now, msgs: cr.Messages, api: api}
}

// headers notes how to read the response, or why it can't be kept.
func (c *capture) headers(h map[string]string) {
	if enc := h["content-encoding"]; enc != "" && enc != "identity" {
		c.note = "the response is " + enc + "-encoded, so it isn't kept"
		return
	}
	mt, _, _ := mime.ParseMediaType(h["content-type"])
	c.sse = mt == "text/event-stream"
}

// add keeps the next part of the response as the caller gets it.
func (c *capture) add(b []byte) {
	if c.note != "" {
		return
	}
	if room := MaxCapture - len(c.buf); len(b) > room {
		b, c.truncated = b[:room], true
	}
	c.buf = append(c.buf, b...)
}

// finish keeps the content: the response ended, or there won't be one.
func (c *capture) finish() {
	if c != nil {
		c.ended = true
		c.flush()
	}
}

// flush hands the masked content to the sink, once. Before finish, what's
// kept says the response was cut short rather than pass for all of it.
func (c *capture) flush() {
	if c == nil || c.done {
		return
	}
	c.done = true
	if !c.ended && c.note == "" {
		c.note = "the stream ended before the response did: this is what arrived"
	}
	msgs, resp := c.det.Mask(c.msgs, responseText(c.api, c.sse, c.buf))
	b, _ := json.Marshal(map[string]any{"messages": msgs, "response": resp, "route": c.route, "truncated": c.truncated, "note": c.note})
	c.sink(store.CapturedContent{Tenant: c.tenant, ReceiptID: c.receiptID, Route: c.route, TS: c.ts, Content: b})
}

// responseText is the reply's text: the assistant's message (OpenAI) or text
// blocks (Anthropic), from one JSON body or the deltas of an event stream.
// A body it can't read is kept as sent.
func responseText(api string, sse bool, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var out strings.Builder
	add := func(v map[string]any) {
		if api == gateway.APIAnthropic {
			if d, ok := v["delta"].(map[string]any); ok {
				t, _ := d["text"].(string)
				out.WriteString(t)
			}
			if cs, ok := v["content"].([]any); ok {
				for _, c := range cs {
					if m, ok := c.(map[string]any); ok {
						t, _ := m["text"].(string)
						out.WriteString(t)
					}
				}
			}
			return
		}
		cs, _ := v["choices"].([]any)
		for _, c := range cs {
			m, _ := c.(map[string]any)
			for _, k := range []string{"message", "delta"} {
				if msg, ok := m[k].(map[string]any); ok {
					t, _ := msg["content"].(string)
					out.WriteString(t)
				}
			}
		}
	}
	if !sse {
		var v map[string]any
		if json.Unmarshal(body, &v) != nil {
			return string(body)
		}
		add(v)
		return out.String()
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), MaxCapture)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var v map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &v) == nil {
			add(v)
		}
	}
	return out.String()
}
