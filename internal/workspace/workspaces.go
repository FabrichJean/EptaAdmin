package workspace

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// loadDataSourceInWorkspace fetches a data source (a folder grouping
// tables — see store.Table) by slug, scoped to the given workspace, writing a
// 404 otherwise.
func loadDataSourceInWorkspace(a *app.App, w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.DataSource, bool) {
	slug := r.PathValue("dsSlug")
	ds, err := a.Store.GetDataSourceBySlug(ws.ID, slug)
	if err != nil {
		log.Printf("get data source error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if ds == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return ds, true
}

// dataSourceTreeItem pairs one data source with its tables for the
// secondary sidebar's tree (data source > table).
type dataSourceTreeItem struct {
	DataSource *store.DataSource
	Tables     []*store.Table
}

// buildDataSourceTree loads each data source's tables so the secondary
// sidebar can render the full data source > table tree without a round
// trip per node.
func buildDataSourceTree(a *app.App, dataSources []*store.DataSource) ([]dataSourceTreeItem, error) {
	tree := make([]dataSourceTreeItem, 0, len(dataSources))
	for _, ds := range dataSources {
		tables, err := a.Store.ListTablesByDataSource(ds.ID)
		if err != nil {
			return nil, err
		}
		tree = append(tree, dataSourceTreeItem{DataSource: ds, Tables: tables})
	}
	return tree, nil
}

// workspaceDetailData builds the common template data for
// workspace_detail.html, so the header context (breadcrumb, icon, badge...)
// stays consistent across the initial render and the two form-error
// redisplays.
func workspaceDetailData(lang string, currentUser *store.User, ws *store.Workspace, role string, members []*store.WorkspaceMember, dataSources []*store.DataSource, dataSourceTree []dataSourceTreeItem) map[string]any {
	return map[string]any{
		"CurrentUser":        currentUser,
		"ActiveNav":          "workspaces",
		"PageTitle":          ws.Name,
		"Workspace":          ws,
		"Members":            members,
		"DataSources":        dataSources,
		"DataSourceTree":     dataSourceTree,
		"ActiveTableID":      int64(0),
		"SidebarCollapsed":   true,
		"CanManageWSMembers": roles.HasPermission(role, roles.PermMembersManage),
		"CanManageSource":    roles.HasPermission(role, roles.PermSettingsManage),
		"CanImportData":      roles.HasPermission(role, roles.PermDataCreate),
		"CanEditData":        roles.HasPermission(role, roles.PermDataUpdate),
		"CanDeleteData":      roles.HasPermission(role, roles.PermDataDelete),
		"AssignableRoles":    roles.AssignableRolesWithLabels(lang, role),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name},
		},
		"HeaderTitle":       ws.Name,
		"HeaderIcon":        "workspace",
		"HeaderBadge":       i18n.T(lang, "workspace_detail.active_badge"),
		"HeaderDescription": i18n.T(lang, "workspace_detail.slug_prefix", ws.Slug),
		"MemberCount":       len(members),
	}
}

// This page has no breadcrumb (nothing sits "above" it), so it keeps the
// plain single-line header instead of the icon/title/description block —
// that richer header exists to pair with a breadcrumb that says something
// the big title doesn't already say (see workspaceDetailData).
func workspacesPageData(lang string, currentUser *store.User, workspaces []*store.UserWorkspace) map[string]any {
	return map[string]any{
		"CurrentUser": currentUser,
		"Workspaces":  workspaces,
		"ActiveNav":   "workspaces",
		"PageTitle":   i18n.T(lang, "nav.workspaces"),
		"HeaderIcon":  "workspace",
	}
}

func HandleWorkspacesPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.Render(w, r, "workspaces.html", workspacesPageData(a.ResolveLang(r), currentUser, workspaces))
}

// Any authenticated user may create a workspace; they become its Owner.
func HandleCreateWorkspace(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	name := strings.TrimSpace(r.FormValue("name"))

	renderError := func(msg string) {
		workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
		if err != nil {
			log.Printf("list workspaces error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := workspacesPageData(lang, currentUser, workspaces)
		data["Error"] = msg
		data["Name"] = name
		a.Render(w, r, "workspaces.html", data)
	}

	if name == "" {
		renderError(i18n.T(lang, "workspaces.name_required"))
		return
	}

	ws, err := a.Store.CreateWorkspace(name, currentUser.ID)
	if err != nil {
		if err == store.ErrWorkspaceExists {
			renderError(i18n.T(lang, "workspaces.name_exists"))
		} else {
			log.Printf("create workspace error: %v", err)
			renderError(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWorkspaceCreate, Details: map[string]any{"name": ws.Name}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}

// loadWorkspaceMembership fetches the workspace and the caller's role in
// it, writing an HTTP error and returning ok=false if either is missing.
func HandleWorkspaceDetail(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}

	members, err := a.Store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSources, err := a.Store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("list data sources error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSourceTree, err := buildDataSourceTree(a, dataSources)
	if err != nil {
		log.Printf("build data source tree error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	data := workspaceDetailData(a.ResolveLang(r), currentUser, ws, role, members, dataSources, dataSourceTree)
	data["AssignableMembers"] = assignableMembersForWorkspace(a, currentUser, members)
	a.Render(w, r, "workspace_detail.html", data)
}

func assignableMembersForWorkspace(a *app.App, currentUser *store.User, members []*store.WorkspaceMember) []*store.User {
	roster, err := a.Store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		log.Printf("list assignable members error: %v", err)
		return nil
	}
	alreadyIn := map[int64]bool{}
	for _, member := range members {
		alreadyIn[member.UserID] = true
	}
	assignable := make([]*store.User, 0, len(roster))
	for _, user := range roster {
		if !alreadyIn[user.ID] {
			assignable = append(assignable, user)
		}
	}
	return assignable
}

// handleAddWorkspaceMember assigns one of the caller's own members (created
// via the /members roster page) to this workspace. It never creates a new
// account — that only happens on the roster page — so the only failure
// modes here are "not your member" and "already in this workspace".
func HandleAddWorkspaceMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	targetRole := strings.TrimSpace(r.FormValue("role"))

	renderDetail := func(msg string) {
		data, ok := workspaceMembersPageData(a, w, lang, currentUser, ws, role)
		if !ok {
			return
		}
		data["MemberError"] = msg
		a.Render(w, r, "workspace_members.html", data)
	}

	if userID == 0 || !roles.CanAssignRole(role, targetRole) {
		renderDetail(i18n.T(lang, "workspace_detail.invalid_username_or_role"))
		return
	}

	target, err := a.Store.GetUserByID(userID)
	if err != nil {
		log.Printf("lookup user error: %v", err)
		renderDetail(i18n.T(lang, "common.error_generic_retry"))
		return
	}
	if target == nil || target.CreatedBy != currentUser.ID {
		renderDetail(i18n.T(lang, "workspace_detail.not_your_member"))
		return
	}

	if err := a.Store.AddWorkspaceMember(ws.ID, target.ID, targetRole); err != nil {
		if err == store.ErrAlreadyMember {
			renderDetail(i18n.T(lang, "workspace_detail.already_member"))
		} else {
			log.Printf("add workspace member error: %v", err)
			renderDetail(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionMemberAdd, Details: map[string]any{"username": target.Username, "role": roles.RoleLabel(lang, targetRole)}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

// workspaceMembersPageData builds the common template data for
// workspace_members.html, fetching the current member list plus the
// caller's own roster (minus whoever's already in this workspace) so the
// "assign" form only ever offers members that person actually created.
func workspaceMembersPageData(a *app.App, w http.ResponseWriter, lang string, currentUser *store.User, ws *store.Workspace, role string) (map[string]any, bool) {
	members, err := a.Store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	roster, err := a.Store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		log.Printf("list roster error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	alreadyIn := map[int64]bool{}
	for _, m := range members {
		alreadyIn[m.UserID] = true
	}
	var assignable []*store.User
	for _, u := range roster {
		if !alreadyIn[u.ID] {
			assignable = append(assignable, u)
		}
	}
	return map[string]any{
		"CurrentUser":        currentUser,
		"ActiveNav":          "workspaces",
		"PageTitle":          i18n.T(lang, "workspace_detail.members_modal_title"),
		"Workspace":          ws,
		"Members":            members,
		"AssignableMembers":  assignable,
		"CanManageWSMembers": roles.HasPermission(role, roles.PermMembersManage),
		"AssignableRoles":    roles.AssignableRolesWithLabels(lang, role),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: i18n.T(lang, "workspace_detail.members_modal_title")},
		},
		"HeaderTitle": i18n.T(lang, "workspace_detail.members_modal_title"),
		"HeaderIcon":  "person",
	}, true
}

func HandleWorkspaceMembersPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	data, ok := workspaceMembersPageData(a, w, lang, currentUser, ws, role)
	if !ok {
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data["MemberError"] = errMsg
	}
	a.Render(w, r, "workspace_members.html", data)
}

// handleRemoveWorkspaceMember revokes a member's access. Removing the
// workspace's only Owner is refused (see store.Store.RemoveWorkspaceMember).
func HandleRemoveWorkspaceMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	target, err := a.Store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	if err := a.Store.RemoveWorkspaceMember(ws.ID, targetID); err != nil {
		if err == store.ErrLastOwner {
			http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
			return
		}
		log.Printf("remove workspace member error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	username := ""
	if target != nil {
		username = target.Username
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionMemberRemove, Details: map[string]any{"username": username}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

// handleUpdateWorkspaceMemberRole changes a member's role. Demoting the
// workspace's only Owner is refused (see store.Store.UpdateWorkspaceMemberRole).
func HandleUpdateWorkspaceMemberRole(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newRole := strings.TrimSpace(r.FormValue("role"))
	if !roles.CanAssignRole(role, newRole) {
		http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_detail.invalid_username_or_role")), http.StatusSeeOther)
		return
	}

	target, err := a.Store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	if err := a.Store.UpdateWorkspaceMemberRole(ws.ID, targetID, newRole); err != nil {
		if err == store.ErrLastOwner {
			http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
			return
		}
		log.Printf("update workspace member role error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	username := ""
	if target != nil {
		username = target.Username
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionMemberRoleChange, Details: map[string]any{"username": username, "role": roles.RoleLabel(lang, newRole)}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

func HandleCreateDataSource(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))

	renderDetail := func(msg string) {
		members, err := a.Store.ListWorkspaceMembers(ws.ID)
		if err != nil {
			log.Printf("list workspace members error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		dataSources, err := a.Store.ListDataSources(ws.ID)
		if err != nil {
			log.Printf("list data sources error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		dataSourceTree, err := buildDataSourceTree(a, dataSources)
		if err != nil {
			log.Printf("build data source tree error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := workspaceDetailData(lang, currentUser, ws, role, members, dataSources, dataSourceTree)
		data["AssignableMembers"] = assignableMembersForWorkspace(a, currentUser, data["Members"].([]*store.WorkspaceMember))
		data["SourceError"] = msg
		a.Render(w, r, "workspace_detail.html", data)
	}

	if name == "" {
		renderDetail(i18n.T(lang, "workspace_detail.datasource_name_required"))
		return
	}

	ds, err := a.Store.CreateDataSource(ws.ID, name, "json")
	if err != nil {
		if err == store.ErrDataSourceExists {
			renderDetail(i18n.T(lang, "workspace_detail.datasource_exists"))
		} else {
			log.Printf("create data source error: %v", err)
			renderDetail(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: store.ActionDataSourceCreate, Details: map[string]any{"name": ds.Name}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}
