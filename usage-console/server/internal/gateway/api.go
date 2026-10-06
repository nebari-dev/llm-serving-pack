package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The APIs a caller can speak to the gateway. OpenAI-style callers use
// /v1/chat/completions (and the rest of OpenAI's paths); Anthropic-style ones
// use Anthropic's Messages API under /anthropic, which is where Agent Router
// serves it: the Anthropic SDK with the gateway's /anthropic as its base URL
// posts to /anthropic/v1/messages.
const (
	APIOpenAI    = "openai"
	APIAnthropic = "anthropic"
)

// HeaderAPI marks an Anthropic-style request for the routes: the key check
// sets it (to APIAnthropic, and only then), the compiled AIGatewayRoutes
// match it (routing.APIHeader), and aigw/base.yaml strips any the caller
// sends.
const HeaderAPI = "X-Stargate-Api"

// APIOf is the API a request path (query and all) is in.
func APIOf(path string) string {
	if strings.HasPrefix(path, "/anthropic/") {
		return APIAnthropic
	}
	return APIOpenAI
}

// ErrorBody is a refusal in the caller's API's shape: OpenAI's
// {"error":{"code","message"}}, or Anthropic's
// {"type":"error","error":{"type","message"}} with our code alongside, so
// either SDK raises its usual error for the status.
func ErrorBody(api string, status int, code, msg string) []byte {
	var v any = map[string]any{"error": map[string]any{"code": code, "message": msg}}
	if api == APIAnthropic {
		v = map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrorType(status), "code": code, "message": msg}}
	}
	b, _ := json.Marshal(v)
	return b
}

// anthropicErrorType is Anthropic's error type for an HTTP status.
func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	return "api_error"
}

func writeAPIErr(w http.ResponseWriter, api string, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(ErrorBody(api, status, code, msg))
}
