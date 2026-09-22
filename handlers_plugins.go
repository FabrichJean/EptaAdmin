package main

import (
	"log"
	"net/http"
)

// pluginGroup pairs one workspace with its tracked sites — the "Plugins"
// hub page's only integration type today is Tracking, but the shape
// (a workspace, its list of configured instances) is generic enough to
// grow a second plugin type later without reshaping this struct.
type pluginGroup struct {
	Workspace *UserWorkspace
	Sites     []trackedSiteView
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
	for _, ws := range workspaces {
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
	}

	a.render(w, r, "plugins.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "plugins",
		"PageTitle":   T(lang, "plugins.title"),
		"Groups":      groups,
		"HasAnySites": hasAnySites,
		"HeaderTitle": T(lang, "plugins.title"),
		"HeaderIcon":  "puzzle",
	})
}
