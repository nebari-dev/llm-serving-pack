package openmeterapi

import (
	"regexp"
	"strings"
)

// NormalizeModelID is OpenMeter's canonical form of a provider and model ID:
// the form its price table is keyed by, and the one its cost queries look an
// event's model up under. A manual price is stored as sent, so one stored
// under any other form never applies.
//
// Ported from OpenMeter's llmcost.NormalizeModelID and NormalizeProvider
// (openmeter/llmcost/normalize.go at f32c3d66411c234e2160113e110d7c72b5012afe,
// v1.0.0-beta.234, Apache-2.0). Keep it in step when upgrading OpenMeter.
func NormalizeModelID(provider, modelID string) (string, string) {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	// Bedrock version suffixes first (-v1:0), so a date before one is terminal.
	modelID = bedrockVersionSuffix.ReplaceAllString(modelID, "")
	modelID = dateSuffix.ReplaceAllString(modelID, "")
	modelID = bedrockRegionPrefix.ReplaceAllString(modelID, "")
	modelID = dotVersion.ReplaceAllString(modelID, "${1}-${2}")
	if canonical, ok := modelAliases[modelID]; ok {
		modelID = canonical
	}
	return normalizeProvider(provider), modelID
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if strings.HasPrefix(provider, "vertex_ai-") || strings.HasPrefix(provider, "vertex_ai_") {
		return "vertex_ai"
	}
	if canonical, ok := providerAliases[provider]; ok {
		return canonical
	}
	return provider
}

var (
	dateSuffix           = regexp.MustCompile(`(-\d{8}|-\d{4}-\d{2}-\d{2})$`)
	bedrockVersionSuffix = regexp.MustCompile(`-v\d+:\d+$`)
	bedrockRegionPrefix  = regexp.MustCompile(`^(us|eu|ap)\.`)
	dotVersion           = regexp.MustCompile(`(\d)\.(\d)`)
	modelAliases         = map[string]string{"deepseek-chat": "deepseek-v3", "deepseek-reasoner": "deepseek-r1"}
	providerAliases      = map[string]string{
		"azure_ai": "azure", "bedrock_converse": "bedrock", "amazon-bedrock": "bedrock",
		"gemini": "google", "aws": "amazon", "facebook": "meta", "mistralai": "mistral",
		"x-ai": "xai", "nano-gpt": "nanogpt", "nano_gpt": "nanogpt",
	}
)
