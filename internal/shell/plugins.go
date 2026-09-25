package shell

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/tracking"
	"log"
	"net/http"
)

// pluginGroup pairs one workspace with its tracked sites.
type pluginGroup struct {
	Workspace *store.UserWorkspace
	Sites     []tracking.TrackedSiteView
}

// visualPluginGroup mirrors pluginGroup for the Visual integration type.
type visualPluginGroup struct {
	Workspace *store.UserWorkspace
	Sites     []*store.VisualSite
}

// handleGlobalPlugins lists every tracked site across every workspace the
// user can at least read data in — mirrors handleGlobalWebhooks
// (handlers_settings.go) exactly, but gated on roles.PermDataRead rather than
// roles.PermSettingsManage: viewing analytics is a read action, unlike creating
// or deleting a tracked site (still roles.PermSettingsManage, unchanged in
// handlers_tracking.go).
func HandleGlobalPlugins(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	groups := make([]pluginGroup, 0, len(workspaces))
	hasAnySites := false
	visualGroups := make([]visualPluginGroup, 0, len(workspaces))
	hasAnyVisualSites := false
	manageableWorkspaces := make([]*store.UserWorkspace, 0, len(workspaces))
	for _, ws := range workspaces {
		if roles.HasPermission(ws.Role, roles.PermSettingsManage) {
			manageableWorkspaces = append(manageableWorkspaces, ws)
		}
		if !roles.HasPermission(ws.Role, roles.PermDataRead) {
			continue
		}
		rawSites, err := a.Store.ListTrackedSites(ws.ID)
		if err != nil {
			log.Printf("list tracked sites error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		sites := make([]tracking.TrackedSiteView, 0, len(rawSites))
		for _, site := range rawSites {
			tableSlug := ""
			if t, err := a.Store.GetTable(site.TableID); err == nil && t != nil {
				tableSlug = t.Slug
			}
			sites = append(sites, tracking.TrackedSiteView{TrackedSite: site, TableSlug: tableSlug})
		}
		if len(sites) > 0 {
			hasAnySites = true
		}
		groups = append(groups, pluginGroup{Workspace: ws, Sites: sites})

		visualSites, err := a.Store.ListVisualSites(ws.ID)
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

	a.Render(w, r, "plugins.html", map[string]any{
		"CurrentUser":          currentUser,
		"ActiveNav":            "plugins",
		"PageTitle":            i18n.T(lang, "plugins.title"),
		"Groups":               groups,
		"HasAnySites":          hasAnySites,
		"VisualGroups":         visualGroups,
		"HasAnyVisualSites":    hasAnyVisualSites,
		"ManageableWorkspaces": manageableWorkspaces,
		"HeaderTitle":          i18n.T(lang, "plugins.title"),
		"HeaderIcon":           "puzzle",
	})
}
