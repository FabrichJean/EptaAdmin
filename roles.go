package main

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// Atomic permissions, as laid out in docs/spec2.md §4. These are always
// checked against a role *within a specific workspace* — there is no
// instance-wide administrator role; every account manages only the
// workspaces and members it actually owns.
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

// assignableRoles returns the roles a given actor may grant a new member of
// their workspace. A workspace Owner can invite Admins, Editors and
// Viewers; a workspace Admin can only invite Editors and Viewers. Nobody
// can invite another Owner — a workspace gets exactly one, whoever created
// it (or auto-created it, e.g. on self-registration).
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

func assignableRolesWithLabels(lang, actorRole string) []RoleOption {
	roles := assignableRoles(actorRole)
	opts := make([]RoleOption, 0, len(roles))
	for _, r := range roles {
		opts = append(opts, RoleOption{Value: r, Label: roleLabel(lang, r)})
	}
	return opts
}

func roleLabel(lang, role string) string {
	switch role {
	case RoleOwner:
		return T(lang, "role.owner")
	case RoleAdmin:
		return T(lang, "role.admin")
	case RoleEditor:
		return T(lang, "role.editor")
	case RoleViewer:
		return T(lang, "role.viewer")
	default:
		return role
	}
}
