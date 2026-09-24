package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/jbouder/stargate/server/internal/fakellm"
)

// Upstream calls one backend. onDelta receives streamed content (nil for
// non-streaming callers); returning false stops the stream.
type Upstream interface {
	Call(ctx context.Context, backend string, req fakellm.ChatRequest, onDelta func(string) bool) Result
}

// Execute tries candidates in order, moving on after retryable failures, and
// inspects the response. It returns the candidate that answered and the names
// of those that failed first.
func Execute(ctx context.Context, d *Decision, up Upstream, onDelta func(string) bool) (*Candidate, Result, []string) {
	var failed []string
	var res Result
	for i := range d.Candidates {
		c := &d.Candidates[i]
		req := d.Req
		req.Model = c.Model
		insp := &Inspector{}
		res = up.Call(ctx, c.Backend.Name, req, func(delta string) bool {
			keep, ok := insp.Feed(delta)
			if keep != "" && onDelta != nil && !onDelta(keep) {
				return false
			}
			return ok
		})
		if res.Status == 200 {
			if !req.Stream {
				// Non-streaming bodies are inspected whole and cut the same way.
				insp.Feed(res.Content)
			}
			res.Content, res.Truncated = insp.Content(), insp.Cut
			return c, res, failed
		}
		if !Retryable(res.Status) || i == len(d.Candidates)-1 {
			return c, res, failed
		}
		failed = append(failed, fmt.Sprintf("%s: %d", c.Backend.Name, res.Status))
	}
	return &d.Candidates[len(d.Candidates)-1], res, failed
}

// HTTPUpstream calls cmd/fake-openai (or anything OpenAI-compatible) at
// BaseURL/{backend}/v1/chat/completions.
type HTTPUpstream struct {
	BaseURL string
	Client  *http.Client
}

func (h *HTTPUpstream) Call(ctx context.Context, backend string, req fakellm.ChatRequest, onDelta func(string) bool) Result {
	start := time.Now()
	if req.Stream {
		req.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{true}
	}
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/"+backend+"/v1/chat/completions", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(hr)
	if err != nil {
		return Result{Status: 502, ErrMsg: err.Error(), Duration: time.Since(start)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return Result{Status: resp.StatusCode, ErrMsg: e.Error.Message, Duration: time.Since(start)}
	}

	if !req.Stream {
		var out struct {
			Choices []struct {
				Message fakellm.Message `json:"message"`
			} `json:"choices"`
			Usage fakellm.Usage `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Result{Status: 502, ErrMsg: "bad upstream body: " + err.Error(), Duration: time.Since(start)}
		}
		res := Result{Status: 200, Usage: &out.Usage, Duration: time.Since(start)}
		if len(out.Choices) > 0 {
			res.Content = out.Choices[0].Message.Content
		}
		return res
	}

	res := Result{Status: 200}
	var content strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if line == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *fakellm.Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			res.Usage = chunk.Usage
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			if res.TTFT == nil {
				t := time.Since(start)
				res.TTFT = &t
			}
			content.WriteString(c.Delta.Content)
			if !onDelta(c.Delta.Content) {
				cancel()
				res.Content, res.Duration = content.String(), time.Since(start)
				return res
			}
		}
	}
	res.Content, res.Duration = content.String(), time.Since(start)
	return res
}

// SimUpstream runs fakellm in-process without sleeping; the backfill uses it
// to synthesize history quickly.
type SimUpstream struct{ Rand *rand.Rand }

func (s *SimUpstream) Call(_ context.Context, backend string, req fakellm.ChatRequest, onDelta func(string) bool) Result {
	p := fakellm.Simulate(backend, req, s.Rand)
	if p.Status != 200 {
		return Result{Status: p.Status, ErrMsg: p.Error, Duration: p.Duration}
	}
	res := Result{Status: 200, Usage: &p.Usage, Duration: p.Duration}
	if req.Stream {
		t := p.TTFT
		res.TTFT = &t
	}
	var b strings.Builder
	for _, c := range p.Chunks {
		b.WriteString(c)
		if req.Stream && !onDelta(c) {
			break
		}
	}
	res.Content = b.String()
	return res
}
