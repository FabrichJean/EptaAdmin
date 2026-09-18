package main

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// roleRank orders roles from least to most privileged.
var roleRank = map[string]int{
	RoleViewer: 1,
	RoleEditor: 2,
	RoleAdmin:  3,
	RoleOwner:  4,
}

func isValidRole(role string) bool {
	_, ok := roleRank[role]
	return ok
}

// Atomic permissions, as laid out in docs/spec2.md §4. Coarser role checks
// (canManageMembers, etc.) are convenience wrappers built on top of these.
const (
	PermDataRead        = "data.read"
	PermDataCreate      = "data.create"
	PermDataUpdate      = "data.update"
	PermDataDelete      = "data.delete"
	PermWorkspaceManage = "workspace.manage" // rename/delete/transfer the workspace itself
	PermMembersRead     = "members.read"
	PermMembersManage   = "members.manage"
	PermSettingsManage  = "settings.manage" // workspace settings, incl. registering data sources
)

// rolePermissions maps each role to the atomic permissions it holds within
// a workspace. Owner implicitly has everything; the rest follow spec2 §4:
// Admin manages users/permissions/data/settings, Editor has full CRUD on
// data, Viewer is read-only.
var rolePermissions = map[string]map[string]bool{
	RoleOwner: {
		PermDataRead: true, PermDataCreate: true, PermDataUpdate: true, PermDataDelete: true,
		PermWorkspaceManage: true, PermMembersRead: true, PermMembersManage: true, PermSettingsManage: true,
	},
	RoleAdmin: {
		PermDataRead: true, PermDataCreate: true, PermDataUpdate: true, PermDataDelete: true,
		PermMembersRead: true, PermMembersManage: true, PermSettingsManage: true,
	},
	RoleEditor: {
		PermDataRead: true, PermDataCreate: true, PermDataUpdate: true, PermDataDelete: true,
	},
	RoleViewer: {
		PermDataRead: true,
	},
}

func hasPermission(role, permission string) bool {
	return rolePermissions[role][permission]
}

// canManageMembers reports whether a user with the given role may access
// the member creation panel (Owner and Admin only).
func canManageMembers(role string) bool {
	return roleRank[role] >= roleRank[RoleAdmin]
}

// assignableRoles returns the roles a given actor is allowed to grant to a
// new member. Owners can create Admins, Editors and Viewers; Admins can
// only create Editors and Viewers. Nobody can create another Owner through
// this flow — the Owner is set once, at the very first registration.
func assignableRoles(actorRole string) []string {
	switch actorRole {
	case RoleOwner:
		return []string{RoleAdmin, RoleEditor, RoleViewer}
	case RoleAdmin:
		return []string{RoleEditor, RoleViewer}
	default:
		return nil
	}
}

func canAssignRole(actorRole, targetRole string) bool {
	for _, r := range assignableRoles(actorRole) {
		if r == targetRole {
			return true
		}
	}
	return false
}

type RoleOption struct {
	Value string
	Label string
}

func assignableRolesWithLabels(actorRole string) []RoleOption {
	roles := assignableRoles(actorRole)
	opts := make([]RoleOption, 0, len(roles))
	for _, r := range roles {
		opts = append(opts, RoleOption{Value: r, Label: roleLabel(r)})
	}
	return opts
}

func roleLabel(role string) string {
	switch role {
	case RoleOwner:
		return "Propriétaire"
	case RoleAdmin:
		return "Administrateur"
	case RoleEditor:
		return "Éditeur"
	case RoleViewer:
		return "Lecteur"
	default:
		return role
	}
}
