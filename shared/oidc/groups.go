package oidc

import (
	"fmt"
	"strings"
)

// GroupForm says how the values in the token's groups claim are written, and
// so how a configured group is compared with them.
type GroupForm string

const (
	// GroupFormPath compares Keycloak group paths: NIC's realm emits the
	// groups claim with full.path=true ("/llm", "/org/team-a"), so a
	// configured "llm" or "/llm" both mean the top-level group llm.
	GroupFormPath GroupForm = "path"
	// GroupFormName compares values exactly as written, for a claim that is
	// not a Keycloak group path, such as realm_access.roles.
	GroupFormName GroupForm = "name"
)

// ParseGroupForm reads a LLM_OIDC_GROUPS_CLAIM_FORM value. Empty means path.
func ParseGroupForm(s string) (GroupForm, error) {
	switch f := GroupForm(strings.TrimSpace(s)); f {
	case "", GroupFormPath:
		return GroupFormPath, nil
	case GroupFormName:
		return GroupFormName, nil
	default:
		return "", fmt.Errorf("groups claim form %q: want %q or %q", s, GroupFormPath, GroupFormName)
	}
}

// Normalize returns g in the form the groups claim carries. In path form a
// leading "/" is added and empty segments dropped; a value with no segments
// normalizes to "" and matches nothing. In name form only surrounding space
// is trimmed.
func (f GroupForm) Normalize(g string) string {
	if f == GroupFormName {
		return strings.TrimSpace(g)
	}
	var segs []string
	for _, s := range strings.Split(strings.TrimSpace(g), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return ""
	}
	return "/" + strings.Join(segs, "/")
}
