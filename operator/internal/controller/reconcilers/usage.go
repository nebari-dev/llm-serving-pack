package reconcilers

// tokenUsageCosts returns the llmRequestCosts every generated AIGatewayRoute
// carries. They make the Envoy AI Gateway store each request's token counts in
// Envoy dynamic metadata (namespace io.envoy.ai_gateway) under the keys below,
// where the gateway access log can read them, e.g.
// %DYNAMIC_METADATA(io.envoy.ai_gateway:llm_input_token)%.
//
// The keys are a contract with the access-log format the usage collector
// parses (examples/usage-access-log.yaml); renaming one silently drops that
// count from every usage record.
//
// Declaring any cost also makes the gateway add
// stream_options.include_usage=true to streamed chat completions, so a stream
// ends with one extra usage chunk the client did not ask for. Without it a
// streamed response reports no token counts at all.
func tokenUsageCosts() []interface{} {
	costs := []struct{ key, costType string }{
		{"llm_input_token", "InputToken"},
		{"llm_cached_input_token", "CachedInputToken"},
		{"llm_cache_creation_input_token", "CacheCreationInputToken"},
		{"llm_output_token", "OutputToken"},
		{"llm_reasoning_token", "ReasoningToken"},
		{"llm_total_token", "TotalToken"},
	}
	out := make([]interface{}, 0, len(costs))
	for _, c := range costs {
		out = append(out, map[string]interface{}{
			"metadataKey": c.key,
			"type":        c.costType,
		})
	}
	return out
}
