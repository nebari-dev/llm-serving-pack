package warden

import (
	"bytes"
	"encoding/json"
	"mime"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/gateway"
)

// bodyRehydrator puts a request's redacted values back into its response
// (spec §4.5 step 5). Warden sees the response after Agent Router has
// translated it, so it's OpenAI chat completions either way: one JSON body,
// or server-sent events of chunks. Only strings inside the choices change.
//
// Envoy streams the body in chunks that follow neither events nor
// placeholders. A JSON body is held whole and rewritten at the end. Events
// go out as soon as they're complete, and a placeholder the model streams
// over several deltas ("[EM", "AIL_1]") is held back from the first until the
// next shows whether it is one.
type bodyRehydrator struct {
	v     *gateway.Vault
	sse   bool
	buf   []byte
	texts map[textKey]*gateway.Rehydrator
	order []textKey
	// last is the id, object and model of the last event, for an event Warden
	// adds to carry text still held when the stream ends.
	last map[string]any
}

// textKey is one text a streamed reply builds up across deltas: a field of a
// choice's delta ("content"), or of a tool call's function ("arguments").
// Choice and tool are their "index" values as sent; tool is "" for a delta
// field.
type textKey struct{ choice, tool, field string }

// newBodyRehydrator is nil for a body it can't read.
func newBodyRehydrator(v *gateway.Vault, contentType string) *bodyRehydrator {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch mt {
	case "application/json":
		return &bodyRehydrator{v: v}
	case "text/event-stream":
		return &bodyRehydrator{v: v, sse: true, texts: map[textKey]*gateway.Rehydrator{}}
	}
	return nil
}

// Feed takes the next chunk of the body and returns what goes out in its place.
func (b *bodyRehydrator) Feed(chunk []byte, end bool) []byte {
	b.buf = append(b.buf, chunk...)
	if !b.sse {
		if !end {
			return nil
		}
		out := rehydrateJSON(b.v, b.buf)
		b.buf = nil
		return out
	}
	var out []byte
	for {
		n := eventEnd(b.buf)
		if n < 0 {
			break
		}
		out = append(out, b.event(b.buf[:n])...)
		b.buf = b.buf[n:]
	}
	if end {
		if len(b.buf) > 0 {
			out = append(out, b.event(b.buf)...)
			b.buf = nil
		}
		out = append(out, b.flushAll()...)
	}
	return out
}

// eventEnd is the length of the first complete event in buf, or -1.
func eventEnd(buf []byte) int {
	n := -1
	for _, sep := range []string{"\n\n", "\r\n\r\n"} {
		if i := bytes.Index(buf, []byte(sep)); i >= 0 && (n < 0 || i+len(sep) < n) {
			n = i + len(sep)
		}
	}
	return n
}

// event rewrites one event, or returns it as sent when nothing in it changes.
func (b *bodyRehydrator) event(ev []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(ev), "\r\n"), "\n")
	var data []string
	for _, l := range lines {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(l, "\r"), "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	if len(data) == 0 {
		return ev
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" {
		return append(b.flushAll(), ev...)
	}
	obj, ok := decodeObject([]byte(payload))
	if !ok {
		return ev
	}
	b.last = map[string]any{"object": "chat.completion.chunk"}
	for _, k := range []string{"id", "object", "model", "created"} {
		if v, ok := obj[k]; ok {
			b.last[k] = v
		}
	}
	changed := false
	choices, _ := obj["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		if choice == nil {
			continue
		}
		idx := indexOf(choice)
		delta, _ := choice["delta"].(map[string]any)
		for field, v := range delta {
			switch v := v.(type) {
			case string:
				if s := b.text(textKey{idx, "", field}).Feed(v); s != v {
					delta[field], changed = s, true
				}
			case []any:
				if field != "tool_calls" {
					continue
				}
				for _, tc := range v {
					call, _ := tc.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					for k, a := range fn {
						if a, ok := a.(string); ok {
							if s := b.text(textKey{idx, indexOf(call), k}).Feed(a); s != a {
								fn[k], changed = s, true
							}
						}
					}
				}
			}
		}
		// The choice is done: what's still held never became a placeholder.
		if fr, _ := choice["finish_reason"].(string); fr != "" {
			if delta == nil {
				delta = map[string]any{}
			}
			if b.flushInto(delta, idx) {
				choice["delta"], changed = delta, true
			}
		}
	}
	if !changed {
		return ev
	}
	var out []string
	wrote := false
	for _, l := range lines {
		if strings.HasPrefix(l, "data:") {
			if !wrote {
				out = append(out, "data: "+string(encode(obj)))
				wrote = true
			}
			continue
		}
		out = append(out, l)
	}
	return []byte(strings.Join(out, "\n") + "\n\n")
}

func (b *bodyRehydrator) text(k textKey) *gateway.Rehydrator {
	r, ok := b.texts[k]
	if !ok {
		r = b.v.Rehydrator()
		b.texts[k] = r
		b.order = append(b.order, k)
	}
	return r
}

// flushInto adds a choice's held text to its delta, and reports whether
// there was any.
func (b *bodyRehydrator) flushInto(delta map[string]any, choice string) bool {
	held := false
	for _, k := range b.order {
		if k.choice != choice {
			continue
		}
		s := b.texts[k].Flush()
		if s == "" {
			continue
		}
		held = true
		if k.tool == "" {
			prev, _ := delta[k.field].(string)
			delta[k.field] = prev + s
			continue
		}
		calls, _ := delta["tool_calls"].([]any)
		delta["tool_calls"] = append(calls, map[string]any{"index": json.Number(k.tool), "function": map[string]any{k.field: s}})
	}
	return held
}

// flushAll is an event carrying whatever is still held when the stream ends
// without the choices finishing, or nothing.
func (b *bodyRehydrator) flushAll() []byte {
	var choices []any
	var seen []string
	for _, k := range b.order {
		if slices.Contains(seen, k.choice) {
			continue
		}
		seen = append(seen, k.choice)
		delta := map[string]any{}
		if b.flushInto(delta, k.choice) {
			choices = append(choices, map[string]any{"index": json.Number(k.choice), "delta": delta})
		}
	}
	if len(choices) == 0 {
		return nil
	}
	ev := map[string]any{"object": "chat.completion.chunk"}
	for k, v := range b.last {
		ev[k] = v
	}
	ev["choices"] = choices
	return []byte("data: " + string(encode(ev)) + "\n\n")
}

// rehydrateJSON restores placeholders in a chat completion's choices, or
// returns body as sent when it has none (or isn't JSON).
func rehydrateJSON(v *gateway.Vault, body []byte) []byte {
	obj, ok := decodeObject(body)
	if !ok {
		return body
	}
	choices, changed := swapStrings(obj["choices"], v.Swap)
	if !changed {
		return body
	}
	obj["choices"] = choices
	return encode(obj)
}

// swapStrings applies f to every string in x.
func swapStrings(x any, f func(string) string) (any, bool) {
	changed := false
	switch x := x.(type) {
	case string:
		s := f(x)
		return s, s != x
	case []any:
		for i := range x {
			var c bool
			x[i], c = swapStrings(x[i], f)
			changed = changed || c
		}
	case map[string]any:
		for k := range x {
			var c bool
			x[k], c = swapStrings(x[k], f)
			changed = changed || c
		}
	}
	return x, changed
}

// decodeObject keeps numbers as written, so re-encoding changes only strings.
func decodeObject(b []byte) (map[string]any, bool) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var obj map[string]any
	if err := d.Decode(&obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

func encode(v any) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	e.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func indexOf(m map[string]any) string {
	if n, ok := m["index"].(json.Number); ok {
		return n.String()
	}
	return "0"
}
