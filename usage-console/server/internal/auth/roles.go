package auth

import (
	"slices"
	"strings"
)

// Role is one of the spec's roles (§5.2). They come from Keycloak groups in
// the token's groups claim, not from anything assigned in Stargate.
type Role string

const (
	Owner    Role = "owner"
	Admin    Role = "admin"
	Security Role = "security"
	Finance  Role = "finance"
	Editor   Role = "editor"
	Viewer   Role = "viewer"
)

// DevUserEmail is who every caller is in dev mode (no IdP configured).
const DevUserEmail = "dev@localhost"

// Roles is every role, most powerful first: the order roles are listed in.
var Roles = []Role{Owner, Admin, Security, Finance, Editor, Viewer}

// ParseRole is s as a role, if it is one.
func ParseRole(s string) (Role, bool) {
	r := Role(strings.TrimSpace(strings.ToLower(s)))
	return r, slices.Contains(Roles, r)
}

// RolesFromGroups maps Keycloak groups to roles: a group whose last path
// segment is prefix+role ("/stargate-admin", or "stargate-admin" when the
// mapper doesn't send full paths) grants that role. Other groups (Nebari's
// own "admin", "developer") grant nothing. Roles come back in Roles order,
// each once.
func RolesFromGroups(groups []string, prefix string) []Role {
	var out []Role
	for _, r := range Roles {
		for _, g := range groups {
			name := g[strings.LastIndex(g, "/")+1:]
			if strings.HasPrefix(name, prefix) && Role(strings.TrimPrefix(name, prefix)) == r {
				out = append(out, r)
				break
			}
		}
	}
	return out
}
