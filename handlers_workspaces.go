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
func workspaceDetailData(currentUser *User, ws *Workspace, role string, members []*WorkspaceMember, dataSources []*DataSource) map[string]any {
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
		"AssignableRoles":    assignableRolesWithLabels(role),
		"Breadcrumb": []Breadcrumb{
			{Label: "Workspaces", URL: "/workspaces"},
			{Label: ws.Name},
		},
		"HeaderTitle":       ws.Name,
		"HeaderIcon":        "workspace",
		"HeaderBadge":       "Workspace actif",
		"HeaderDescription": "Slug : " + ws.Slug,
		"MemberCount":       len(members),
	}
}

func workspacesPageData(currentUser *User, workspaces []*UserWorkspace) map[string]any {
	return map[string]any{
		"CurrentUser":       currentUser,
		"Workspaces":        workspaces,
		"ActiveNav":         "workspaces",
		"PageTitle":         "Workspaces",
		"CanManageMembers":  canManageMembers(currentUser.Role),
		"HeaderTitle":       "Workspaces",
		"HeaderIcon":        "workspace",
		"HeaderDescription": "Chaque workspace regroupe ses propres membres, rôles et sources de données JSON.",
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
	a.render(w, "workspaces.html", workspacesPageData(currentUser, workspaces))
}

// Any authenticated user may create a workspace; they become its Owner.
func (a *App) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	name := strings.TrimSpace(r.FormValue("name"))

	renderError := func(msg string) {
		workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
		if err != nil {
			log.Printf("list workspaces error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := workspacesPageData(currentUser, workspaces)
		data["Error"] = msg
		data["Name"] = name
		a.render(w, "workspaces.html", data)
	}

	if name == "" {
		renderError("Merci de renseigner un nom de workspace.")
		return
	}

	ws, err := a.store.CreateWorkspace(name, currentUser.ID)
	if err != nil {
		if err == ErrWorkspaceExists {
			renderError(err.Error())
		} else {
			log.Printf("create workspace error: %v", err)
			renderError("Une erreur est survenue, réessayez.")
		}
		return
	}

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
		http.Error(w, "Accès refusé.", http.StatusForbidden)
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

	a.render(w, "workspace_detail.html", workspaceDetailData(currentUser, ws, role, members, dataSources))
}

func (a *App) handleAddWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermMembersManage) {
		http.Error(w, "Accès refusé.", http.StatusForbidden)
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
		data := workspaceDetailData(currentUser, ws, role, members, dataSources)
		data["MemberError"] = msg
		a.render(w, "workspace_detail.html", data)
	}

	if username == "" || !canAssignRole(role, targetRole) {
		renderDetail("Identifiant invalide ou rôle non autorisé.")
		return
	}

	target, err := a.store.GetUserByUsername(username)
	if err != nil {
		log.Printf("lookup user error: %v", err)
		renderDetail("Une erreur est survenue, réessayez.")
		return
	}
	if target == nil {
		renderDetail("Aucun utilisateur avec cet identifiant.")
		return
	}

	if err := a.store.AddWorkspaceMember(ws.ID, target.ID, targetRole); err != nil {
		if err == ErrAlreadyMember {
			renderDetail(err.Error())
		} else {
			log.Printf("add workspace member error: %v", err)
			renderDetail("Une erreur est survenue, réessayez.")
		}
		return
	}

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}

func (a *App) handleCreateDataSource(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		http.Error(w, "Accès refusé.", http.StatusForbidden)
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
		data := workspaceDetailData(currentUser, ws, role, members, dataSources)
		data["SourceError"] = msg
		a.render(w, "workspace_detail.html", data)
	}

	if name == "" {
		renderDetail("Merci de renseigner un nom.")
		return
	}

	if _, err := a.store.CreateDataSource(ws.ID, name, "json"); err != nil {
		if err == ErrDataSourceExists {
			renderDetail(err.Error())
		} else {
			log.Printf("create data source error: %v", err)
			renderDetail("Une erreur est survenue, réessayez.")
		}
		return
	}

	http.Redirect(w, r, "/workspaces/"+ws.Slug, http.StatusSeeOther)
}
