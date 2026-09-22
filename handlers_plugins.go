package main

import (
	"log"
	"net/http"
)

// pluginGroup pairs one workspace with its tracked sites.
type pluginGroup struct {
	Workspace *UserWorkspace
	Sites     []trackedSiteView
}

// visualPluginGroup mirrors pluginGroup for the Visual integration type.
type visualPluginGroup struct {
	Workspace *UserWorkspace
	Sites     []*VisualSite
}

// handleGlobalPlugins lists every tracked site across every workspace the
// user can at least read data in — mirrors handleGlobalWebhooks
// (handlers_settings.go) exactly, but gated on PermDataRead rather than
// PermSettingsManage: viewing analytics is a read action, unlike creating
// or deleting a tracked site (still PermSettingsManage, unchanged in
// handlers_tracking.go).
func (a *App) handleGlobalPlugins(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	groups := make([]pluginGroup, 0, len(workspaces))
	hasAnySites := false
	visualGroups := make([]visualPluginGroup, 0, len(workspaces))
	hasAnyVisualSites := false
	manageableWorkspaces := make([]*UserWorkspace, 0, len(workspaces))
	for _, ws := range workspaces {
		if hasPermission(ws.Role, PermSettingsManage) {
			manageableWorkspaces = append(manageableWorkspaces, ws)
		}
		if !hasPermission(ws.Role, PermDataRead) {
			continue
		}
		rawSites, err := a.store.ListTrackedSites(ws.ID)
		if err != nil {
			log.Printf("list tracked sites error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		sites := make([]trackedSiteView, 0, len(rawSites))
		for _, site := range rawSites {
			tableSlug := ""
			if t, err := a.store.GetTable(site.TableID); err == nil && t != nil {
				tableSlug = t.Slug
			}
			sites = append(sites, trackedSiteView{TrackedSite: site, TableSlug: tableSlug})
		}
		if len(sites) > 0 {
			hasAnySites = true
		}
		groups = append(groups, pluginGroup{Workspace: ws, Sites: sites})

		visualSites, err := a.store.ListVisualSites(ws.ID)
		if err != nil {
			log.Printf("list visual sites error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		if len(visualSites) > 0 {
			hasAnyVisualSites = true
		}
		visualGroups = append(visualGroups, visualPluginGroup{Workspace: ws, Sites: visualSites})
	}

	a.render(w, r, "plugins.html", map[string]any{
		"CurrentUser":          currentUser,
		"ActiveNav":            "plugins",
		"PageTitle":            T(lang, "plugins.title"),
		"Groups":               groups,
		"HasAnySites":          hasAnySites,
		"VisualGroups":         visualGroups,
		"HasAnyVisualSites":    hasAnyVisualSites,
		"ManageableWorkspaces": manageableWorkspaces,
		"HeaderTitle":          T(lang, "plugins.title"),
		"HeaderIcon":           "puzzle",
	})
}
