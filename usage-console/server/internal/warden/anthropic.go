package warden

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jbouder/stargate/server/internal/fakellm"
)

// messagesRequest is an Anthropic Messages API request (POST
// /anthropic/v1/messages) as Warden reads it: the JSON as sent, and every
// text in it the detectors read, in order. The engine works on OpenAI-style
// messages, so each text becomes one, with its message's role ("system" for
// the system prompt); a redaction is written back to where the text was.
//
// Texts are the system prompt (a string or text blocks), each message's
// content (a string, or its text blocks) and its tool results' content (the
// same). Everything else goes as sent: images and documents, the model's
// own tool calls (OpenAI-style requests' tool calls aren't inspected
// either), tool definitions, cache_control, numbers.
type messagesRequest struct {
	root  map[string]any
	texts []messagesText
}

type messagesText struct {
	role, text string
	set        func(string)
}

// parseMessages reads a Messages request, and the request the engine
// evaluates for it.
func parseMessages(body []byte) (*messagesRequest, fakellm.ChatRequest, error) {
	var cr fakellm.ChatRequest
	root, ok := decodeObject(body)
	if !ok {
		return nil, cr, errors.New("not a JSON object")
	}
	m := &messagesRequest{root: root}
	cr.Model, _ = root["model"].(string)
	cr.Stream, _ = root["stream"].(bool)
	if n, ok := root["max_tokens"].(json.Number); ok {
		v, _ := n.Int64()
		cr.MaxTokens = int(v)
	}
	m.content("system", root, "system")
	msgs, ok := root["messages"].([]any)
	if !ok {
		return nil, cr, errors.New("no messages")
	}
	for _, x := range msgs {
		msg, _ := x.(map[string]any)
		if msg == nil {
			return nil, cr, errors.New("a message isn't an object")
		}
		role, _ := msg["role"].(string)
		m.content(role, msg, "content")
	}
	for _, t := range m.texts {
		cr.Messages = append(cr.Messages, fakellm.Message{Role: t.role, Content: t.text})
	}
	return m, cr, nil
}

// content adds owner[key]'s texts: a string, or a list of blocks.
func (m *messagesRequest) content(role string, owner map[string]any, key string) {
	switch c := owner[key].(type) {
	case string:
		m.texts = append(m.texts, messagesText{role, c, func(s string) { owner[key] = s }})
	case []any:
		for _, x := range c {
			b, _ := x.(map[string]any)
			switch b["type"] {
			case "text":
				if s, ok := b["text"].(string); ok {
					m.texts = append(m.texts, messagesText{role, s, func(s string) { b["text"] = s }})
				}
			case "tool_result":
				m.content(role, b, "content")
			}
		}
	}
}

// rewrite applies the engine's changes: each text as the engine left it,
// and the model on a reroute.
func (m *messagesRequest) rewrite(req fakellm.ChatRequest, rerouted bool) ([]byte, error) {
	if len(req.Messages) != len(m.texts) {
		return nil, fmt.Errorf("%d texts in the body, %d evaluated", len(m.texts), len(req.Messages))
	}
	for i, t := range m.texts {
		t.set(req.Messages[i].Content)
	}
	if rerouted {
		m.root["model"] = req.Model
	}
	return encode(m.root), nil
}

// messagesDeltaTypes is the delta type that carries each streamed field of a
// Messages content block.
var messagesDeltaTypes = map[string]string{"text": "text_delta", "partial_json": "input_json_delta", "thinking": "thinking_delta"}

// messagesEvent rewrites one event of a Messages stream. A content block's
// text arrives as content_block_delta events (text, a tool call's
// partial_json, thinking); what may be the start of a placeholder is held
// until the next delta, and goes out in a delta of its own before the block
// stops.
func (b *bodyRehydrator) messagesEvent(ev []byte, lines []string, obj map[string]any) []byte {
	idx := indexOf(obj)
	switch obj["type"] {
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		changed := false
		for field := range messagesDeltaTypes {
			s, ok := delta[field].(string)
			if !ok {
				continue
			}
			if out := b.text(textKey{idx, "", field}).Feed(s); out != s {
				delta[field], changed = out, true
			}
		}
		if changed {
			return withData(lines, obj)
		}
	case "content_block_stop":
		return append(b.flushBlock(idx), ev...)
	case "message_stop":
		return append(b.flushBlocks(), ev...)
	}
	return ev
}

// flushBlock is a delta event for each text of a content block still held,
// or nothing.
func (b *bodyRehydrator) flushBlock(idx string) []byte {
	var out []byte
	for _, k := range b.order {
		if k.choice != idx {
			continue
		}
		if s := b.texts[k].Flush(); s != "" {
			ev := map[string]any{"type": "content_block_delta", "index": json.Number(idx), "delta": map[string]any{"type": messagesDeltaTypes[k.field], k.field: s}}
			out = append(out, []byte("event: content_block_delta\ndata: "+string(encode(ev))+"\n\n")...)
		}
	}
	return out
}

// flushBlocks flushes every block, for a stream that ends without stopping them.
func (b *bodyRehydrator) flushBlocks() []byte {
	var out []byte
	var seen []string
	for _, k := range b.order {
		if !slices.Contains(seen, k.choice) {
			seen = append(seen, k.choice)
			out = append(out, b.flushBlock(k.choice)...)
		}
	}
	return out
}

// withData is an event's lines with its data replaced by obj.
func withData(lines []string, obj map[string]any) []byte {
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
