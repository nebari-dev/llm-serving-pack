package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// GatewayTest is one real request through the gateway, for onboarding.
type GatewayTest struct {
	Status int `json:"status"`
	// SessionID tags the request, so its receipt is the one with this session.
	SessionID string `json:"sessionId"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
	MS        int64  `json:"ms"`
}

// gatewayTest takes {"secret", "model"}: the caller's own key, which this
// server doesn't keep (it stores only hashes), and a model it may use.
func (s *Server) gatewayTest(_ http.ResponseWriter, r *http.Request, _ string) (any, error) {
	var in struct{ Secret, Model string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, badRequest("invalid JSON body")
	}
	if in.Secret == "" || in.Model == "" {
		return nil, badRequest("secret and model are required")
	}
	return s.sendGatewayTest(r.Context(), in.Secret, in.Model)
}

// sendGatewayTest sends a short chat request in a session of its own. Envoy
// replaces a caller's x-request-id at the edge (receipt ids derive from it),
// so the session is how the console finds the receipt.
func (s *Server) sendGatewayTest(ctx context.Context, secret, model string) (GatewayTest, error) {
	if s.GatewayURL == "" {
		return GatewayTest{}, errors.New("no gateway URL configured (stargate-api serve -gateway)")
	}
	var id [16]byte
	rand.Read(id[:])
	session := "onboarding-" + hex.EncodeToString(id[:8])
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "Say hello to a new Stargate user in under ten words."}},
	})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.GatewayURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return GatewayTest{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Session-Id", session)
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return GatewayTest{}, fmt.Errorf("gateway unreachable: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	out := GatewayTest{Status: res.StatusCode, SessionID: session, MS: time.Since(start).Milliseconds()}
	var parsed struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	json.Unmarshal(raw, &parsed)
	switch {
	case res.StatusCode == http.StatusOK && len(parsed.Choices) > 0:
		out.Reply = parsed.Choices[0].Message.Content
	case parsed.Error != nil:
		out.Error = parsed.Error.Message
	default:
		out.Error = strings.TrimSpace(string(raw))
	}
	return out, nil
}

// overheadWindow and overheadGoal: the last hour, against spec G6's 10ms p50.
const (
	overheadWindow = time.Hour
	overheadGoalMS = 10
)

// gatewayOverhead is the gateway's own time on requests, from receipts.
func (s *Server) gatewayOverhead(_ http.ResponseWriter, r *http.Request, t string) (any, error) {
	o, err := s.Store.GatewayOverhead(r.Context(), t, time.Now().Add(-overheadWindow))
	if err != nil {
		return nil, err
	}
	round := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		x := math.Round(*v*10) / 10
		return &x
	}
	return map[string]any{"p50Ms": round(o.P50MS), "p95Ms": round(o.P95MS), "samples": o.Samples, "windowMinutes": int(overheadWindow.Minutes()), "goalMs": overheadGoalMS}, nil
}
