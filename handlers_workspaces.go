package main

import (
	"log"
	"net/http"
	"strings"
)

// workspaceDetailData builds the common template data for
// workspace_detail.html, so the header context (breadcrumb, icon, badge...)
// stays consistent across the initial render and the two form-error
// redisplays.
func workspaceDetailData(lang string, currentUser *User, ws *Workspace, role string, members []*WorkspaceMember, dataSources []*DataSource) map[string]any {
	return map[string]any{
		"CurrentUser":        currentUser,
		"ActiveNav":          "workspaces",
		"PageTitle":          ws.Name,
		"Workspace":          ws,
		"Members":            members,
		"DataSources":        dataSources,
		"CanManageMembers":   canManageMembers(currentUser.Role),
		"CanManageWSMembers": hasPermission(role, PermMembersManage),
		"CanManageSource":    hasPermission(role, PermSettingsManage),
		"CanImportData":      hasPermission(role, PermDataCreate),
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
		"CurrentUser":      currentUser,
		"Workspaces":       workspaces,
		"ActiveNav":        "workspaces",
		"PageTitle":        T(lang, "nav.workspaces"),
		"CanManageMembers": canManageMembers(currentUser.Role),
		"HeaderIcon":       "workspace",
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

	a.render(w, r, "workspace_detail.html", workspaceDetailData(a.resolveLang(r), currentUser, ws, role, members, dataSources))
}

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

	username := strings.TrimSpace(r.FormValue("username"))
	targetRole := strings.TrimSpace(r.FormValue("role"))

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
		data := workspaceDetailData(lang, currentUser, ws, role, members, dataSources)
		data["MemberError"] = msg
		a.render(w, r, "workspace_detail.html", data)
	}

	if username == "" || !canAssignRole(role, targetRole) {
		renderDetail(T(lang, "workspace_detail.invalid_username_or_role"))
		return
	}

	target, err := a.store.GetUserByUsername(username)
	if err != nil {
		log.Printf("lookup user error: %v", err)
		renderDetail(T(lang, "common.error_generic_retry"))
		return
	}
	if target == nil {
		renderDetail(T(lang, "workspace_detail.user_not_found"))
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

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
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
		data := workspaceDetailData(lang, currentUser, ws, role, members, dataSources)
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
