package main

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// loadDataSourceInWorkspace fetches a data source (a folder grouping
// tables — see Table) by slug, scoped to the given workspace, writing a
// 404 otherwise.
func (a *App) loadDataSourceInWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) (*DataSource, bool) {
	slug := r.PathValue("dsSlug")
	ds, err := a.store.GetDataSourceBySlug(ws.ID, slug)
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
	DataSource *DataSource
	Tables     []*Table
}

// buildDataSourceTree loads each data source's tables so the secondary
// sidebar can render the full data source > table tree without a round
// trip per node.
func (a *App) buildDataSourceTree(dataSources []*DataSource) ([]dataSourceTreeItem, error) {
	tree := make([]dataSourceTreeItem, 0, len(dataSources))
	for _, ds := range dataSources {
		tables, err := a.store.ListTablesByDataSource(ds.ID)
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
func workspaceDetailData(lang string, currentUser *User, ws *Workspace, role string, members []*WorkspaceMember, dataSources []*DataSource, dataSourceTree []dataSourceTreeItem) map[string]any {
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
		"CanManageWSMembers": hasPermission(role, PermMembersManage),
		"CanManageSource":    hasPermission(role, PermSettingsManage),
		"CanImportData":      hasPermission(role, PermDataCreate),
		"CanEditData":        hasPermission(role, PermDataUpdate),
		"CanDeleteData":      hasPermission(role, PermDataDelete),
		"AssignableRoles":    assignableRolesWithLabels(lang, role),
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name},
		},
		"HeaderTitle":       ws.Name,
		"HeaderIcon":        "workspace",
		"HeaderBadge":       T(lang, "workspace_detail.active_badge"),
		"HeaderDescription": T(lang, "workspace_detail.slug_prefix", ws.Slug),
		"MemberCount":       len(members),
	}
}

// This page has no breadcrumb (nothing sits "above" it), so it keeps the
// plain single-line header instead of the icon/title/description block —
// that richer header exists to pair with a breadcrumb that says something
// the big title doesn't already say (see workspaceDetailData).
func workspacesPageData(lang string, currentUser *User, workspaces []*UserWorkspace) map[string]any {
	return map[string]any{
		"CurrentUser": currentUser,
		"Workspaces":  workspaces,
		"ActiveNav":   "workspaces",
		"PageTitle":   T(lang, "nav.workspaces"),
		"HeaderIcon":  "workspace",
	}
}

func (a *App) handleWorkspacesPage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "workspaces.html", workspacesPageData(a.resolveLang(r), currentUser, workspaces))
}

// Any authenticated user may create a workspace; they become its Owner.
func (a *App) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	name := strings.TrimSpace(r.FormValue("name"))

	renderError := func(msg string) {
		workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
		if err != nil {
			log.Printf("list workspaces error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := workspacesPageData(lang, currentUser, workspaces)
		data["Error"] = msg
		data["Name"] = name
		a.render(w, r, "workspaces.html", data)
	}

	if name == "" {
		renderError(T(lang, "workspaces.name_required"))
		return
	}

	ws, err := a.store.CreateWorkspace(name, currentUser.ID)
	if err != nil {
		if err == ErrWorkspaceExists {
			renderError(T(lang, "workspaces.name_exists"))
		} else {
			log.Printf("create workspace error: %v", err)
			renderError(T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionWorkspaceCreate, Details: map[string]any{"name": ws.Name}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}

// loadWorkspaceMembership fetches the workspace and the caller's role in
// it, writing an HTTP error and returning ok=false if either is missing.
func (a *App) loadWorkspaceMembership(w http.ResponseWriter, r *http.Request, currentUser *User) (ws *Workspace, role string, ok bool) {
	slug := r.PathValue("slug")
	ws, err := a.store.GetWorkspaceBySlug(slug)
	if err != nil {
		log.Printf("get workspace error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if ws == nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	role, err = a.store.GetWorkspaceMemberRole(ws.ID, currentUser.ID)
	if err != nil {
		log.Printf("get workspace role error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if role == "" {
		http.Error(w, T(a.resolveLang(r), "common.access_denied"), http.StatusForbidden)
		return nil, "", false
	}
	return ws, role, true
}

func (a *App) handleWorkspaceDetail(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}

	members, err := a.store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSources, err := a.store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("list data sources error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSourceTree, err := a.buildDataSourceTree(dataSources)
	if err != nil {
		log.Printf("build data source tree error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.render(w, r, "workspace_detail.html", workspaceDetailData(a.resolveLang(r), currentUser, ws, role, members, dataSources, dataSourceTree))
}

// handleAddWorkspaceMember assigns one of the caller's own members (created
// via the /members roster page) to this workspace. It never creates a new
// account — that only happens on the roster page — so the only failure
// modes here are "not your member" and "already in this workspace".
func (a *App) handleAddWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.resolveLang(r)
	if !hasPermission(role, PermMembersManage) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	targetRole := strings.TrimSpace(r.FormValue("role"))

	renderDetail := func(msg string) {
		data, ok := a.workspaceMembersPageData(w, lang, currentUser, ws, role)
		if !ok {
			return
		}
		data["MemberError"] = msg
		a.render(w, r, "workspace_members.html", data)
	}

	if userID == 0 || !canAssignRole(role, targetRole) {
		renderDetail(T(lang, "workspace_detail.invalid_username_or_role"))
		return
	}

	target, err := a.store.GetUserByID(userID)
	if err != nil {
		log.Printf("lookup user error: %v", err)
		renderDetail(T(lang, "common.error_generic_retry"))
		return
	}
	if target == nil || target.CreatedBy != currentUser.ID {
		renderDetail(T(lang, "workspace_detail.not_your_member"))
		return
	}

	if err := a.store.AddWorkspaceMember(ws.ID, target.ID, targetRole); err != nil {
		if err == ErrAlreadyMember {
			renderDetail(T(lang, "workspace_detail.already_member"))
		} else {
			log.Printf("add workspace member error: %v", err)
			renderDetail(T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionMemberAdd, Details: map[string]any{"username": target.Username, "role": roleLabel(lang, targetRole)}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

// workspaceMembersPageData builds the common template data for
// workspace_members.html, fetching the current member list plus the
// caller's own roster (minus whoever's already in this workspace) so the
// "assign" form only ever offers members that person actually created.
func (a *App) workspaceMembersPageData(w http.ResponseWriter, lang string, currentUser *User, ws *Workspace, role string) (map[string]any, bool) {
	members, err := a.store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	roster, err := a.store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		log.Printf("list roster error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	alreadyIn := map[int64]bool{}
	for _, m := range members {
		alreadyIn[m.UserID] = true
	}
	var assignable []*User
	for _, u := range roster {
		if !alreadyIn[u.ID] {
			assignable = append(assignable, u)
		}
	}
	return map[string]any{
		"CurrentUser":        currentUser,
		"ActiveNav":          "workspaces",
		"PageTitle":          T(lang, "workspace_detail.members_modal_title"),
		"Workspace":          ws,
		"Members":            members,
		"AssignableMembers":  assignable,
		"CanManageWSMembers": hasPermission(role, PermMembersManage),
		"AssignableRoles":    assignableRolesWithLabels(lang, role),
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: T(lang, "workspace_detail.members_modal_title")},
		},
		"HeaderTitle": T(lang, "workspace_detail.members_modal_title"),
		"HeaderIcon":  "person",
	}, true
}

func (a *App) handleWorkspaceMembersPage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.resolveLang(r)
	data, ok := a.workspaceMembersPageData(w, lang, currentUser, ws, role)
	if !ok {
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data["MemberError"] = errMsg
	}
	a.render(w, r, "workspace_members.html", data)
}

// handleRemoveWorkspaceMember revokes a member's access. Removing the
// workspace's only Owner is refused (see Store.RemoveWorkspaceMember).
func (a *App) handleRemoveWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.resolveLang(r)
	if !hasPermission(role, PermMembersManage) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	target, err := a.store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	if err := a.store.RemoveWorkspaceMember(ws.ID, targetID); err != nil {
		if err == ErrLastOwner {
			http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
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
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionMemberRemove, Details: map[string]any{"username": username}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

// handleUpdateWorkspaceMemberRole changes a member's role. Demoting the
// workspace's only Owner is refused (see Store.UpdateWorkspaceMemberRole).
func (a *App) handleUpdateWorkspaceMemberRole(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.resolveLang(r)
	if !hasPermission(role, PermMembersManage) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newRole := strings.TrimSpace(r.FormValue("role"))
	if !canAssignRole(role, newRole) {
		http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(T(lang, "workspace_detail.invalid_username_or_role")), http.StatusSeeOther)
		return
	}

	target, err := a.store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	if err := a.store.UpdateWorkspaceMemberRole(ws.ID, targetID, newRole); err != nil {
		if err == ErrLastOwner {
			http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members?error="+url.QueryEscape(T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
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
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionMemberRoleChange, Details: map[string]any{"username": username, "role": roleLabel(lang, newRole)}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug+"/members", http.StatusSeeOther)
}

func (a *App) handleCreateDataSource(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	lang := a.resolveLang(r)
	if !hasPermission(role, PermSettingsManage) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))

	renderDetail := func(msg string) {
		members, err := a.store.ListWorkspaceMembers(ws.ID)
		if err != nil {
			log.Printf("list workspace members error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		dataSources, err := a.store.ListDataSources(ws.ID)
		if err != nil {
			log.Printf("list data sources error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		dataSourceTree, err := a.buildDataSourceTree(dataSources)
		if err != nil {
			log.Printf("build data source tree error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := workspaceDetailData(lang, currentUser, ws, role, members, dataSources, dataSourceTree)
		data["SourceError"] = msg
		a.render(w, r, "workspace_detail.html", data)
	}

	if name == "" {
		renderDetail(T(lang, "workspace_detail.datasource_name_required"))
		return
	}

	ds, err := a.store.CreateDataSource(ws.ID, name, "json")
	if err != nil {
		if err == ErrDataSourceExists {
			renderDetail(T(lang, "workspace_detail.datasource_exists"))
		} else {
			log.Printf("create data source error: %v", err)
			renderDetail(T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: ActionDataSourceCreate, Details: map[string]any{"name": ds.Name}})

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}
