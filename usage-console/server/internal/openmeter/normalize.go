package openmeter

import (
	"regexp"
	"strings"
)

// canonicalModel is OpenMeter's canonical form of a model ID, the one its
// price table is keyed by and its cost queries look up. Ported from
// OpenMeter's llmcost.NormalizeModelID (openmeter/llmcost/normalize.go at
// e00163c4b8a0726198c95a9ccb158d38ea8c05b8, Apache-2.0); keep it in step
// when upgrading OpenMeter.
func canonicalModel(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	// Bedrock version suffixes first (-v1:0), so a date before one is terminal.
	id = bedrockVersionSuffix.ReplaceAllString(id, "")
	id = dateSuffix.ReplaceAllString(id, "")
	id = bedrockRegionPrefix.ReplaceAllString(id, "")
	id = dotVersion.ReplaceAllString(id, "${1}-${2}")
	if canonical, ok := modelAliases[id]; ok {
		id = canonical
	}
	return id
}

var (
	dateSuffix           = regexp.MustCompile(`(-\d{8}|-\d{4}-\d{2}-\d{2})$`)
	bedrockVersionSuffix = regexp.MustCompile(`-v\d+:\d+$`)
	bedrockRegionPrefix  = regexp.MustCompile(`^(us|eu|ap)\.`)
	dotVersion           = regexp.MustCompile(`(\d)\.(\d)`)
	modelAliases         = map[string]string{"deepseek-chat": "deepseek-v3", "deepseek-reasoner": "deepseek-r1"}
)
