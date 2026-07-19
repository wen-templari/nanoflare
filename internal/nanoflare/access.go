package nanoflare

import "strings"

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

var (
	viewerScopes = []string{"apps:read", "kv:read", "db:read", "objects:read"}
	memberScopes = []string{"apps:read", "apps:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "db:read", "db:write", "objects:read", "objects:write"}
	adminScopes  = append(append([]string{}, memberScopes...), "orgs:read", "members:read", "members:write")
	ownerScopes  = append(append([]string{}, adminScopes...), "orgs:write", "members:owner")
)

func NormalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleOwner:
		return RoleOwner
	case RoleAdmin:
		return RoleAdmin
	case RoleMember:
		return RoleMember
	case RoleViewer:
		return RoleViewer
	default:
		return ""
	}
}

func RoleScopes(role string) []string {
	var scopes []string
	switch NormalizeRole(role) {
	case RoleOwner:
		scopes = ownerScopes
	case RoleAdmin:
		scopes = adminScopes
	case RoleMember:
		scopes = memberScopes
	case RoleViewer:
		scopes = viewerScopes
	default:
		return nil
	}
	return append([]string{}, scopes...)
}

func HasScope(scopes []string, scope string) bool {
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}
