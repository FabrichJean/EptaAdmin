package webhook

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/tracking"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// webhookGroup pairs one workspace with its webhooks, for the global
// "Webhook" nav page (see handleGlobalWebhooks) — mirrors the
// global/per-workspace split the Activité page already has.
type webhookGroup struct {
	Workspace *store.UserWorkspace
	Webhooks  []*store.Webhook
}

// handleGlobalWebhooks lists every webhook across every workspace the
// user can manage settings for — the sidebar's global "Webhook" entry,
// so members with several workspaces don't have to open each one's
// settings page separately to see what's configured.
func HandleGlobalWebhooks(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	groups := make([]webhookGroup, 0, len(workspaces))
	for _, ws := range workspaces {
		if !roles.HasPermission(ws.Role, roles.PermSettingsManage) {
			continue
		}
		hooks, err := a.Store.ListWebhooks(ws.ID)
		if err != nil {
			log.Printf("list webhooks error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		groups = append(groups, webhookGroup{Workspace: ws, Webhooks: hooks})
	}

	a.Render(w, r, "webhooks.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "webhooks",
		"PageTitle":   i18n.T(lang, "settings.webhooks_title"),
		"Groups":      groups,
		"HeaderTitle": i18n.T(lang, "settings.webhooks_title"),
		"HeaderIcon":  "webhook",
	})
}

// handleSettingsPage renders a workspace's settings — for now, just its
// webhooks (see webhooks.go). Gated on roles.PermSettingsManage, the same
// permission that already covers registering data sources.
func HandleSettingsPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	webhooks, err := a.Store.ListWebhooks(ws.ID)
	if err != nil {
		log.Printf("list webhooks error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
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

	a.Render(w, r, "settings.html", map[string]any{
		"CurrentUser":  currentUser,
		"ActiveNav":    "workspaces",
		"PageTitle":    i18n.T(lang, "settings.title"),
		"Workspace":    ws,
		"Webhooks":     webhooks,
		"TrackedSites": sites,
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: i18n.T(lang, "settings.title")},
		},
		"HeaderTitle": i18n.T(lang, "settings.title"),
		"HeaderIcon":  "code",
	})
}

func HandleCreateWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "settings.webhook_url_invalid")})
		return
	}

	hook, err := a.Store.CreateWebhook(ws.ID, url)
	if err != nil {
		log.Printf("create webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWebhookCreate, Details: map[string]any{"url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":  hook.ID,
		"url": hook.URL,
	})
}

func HandleDeleteWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadWebhookInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	if err := a.Store.DeleteWebhook(hook.ID); err != nil {
		log.Printf("delete webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWebhookDelete, Details: map[string]any{"url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateWebhook is a partial update (PATCH semantics): either field
// may be sent alone or together — a plain enable/disable toggle sends only
// "enabled", the URL-edit pencil sends only "url". Each field that's
// actually present gets its own activity entry, so the log reads as
// distinct actions ("enabled", "changed the URL") rather than one vague
// "updated" blob.
func HandleUpdateWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadWebhookInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	var req struct {
		Enabled *bool   `json:"enabled"`
		URL     *string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}

	if req.Enabled != nil {
		if err := a.Store.SetWebhookEnabled(hook.ID, *req.Enabled); err != nil {
			log.Printf("set webhook enabled error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		toggleAction := store.ActionWebhookDisable
		if *req.Enabled {
			toggleAction = store.ActionWebhookEnable
		}
		a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: toggleAction, Details: map[string]any{"url": hook.URL}})
	}

	if req.URL != nil {
		newURL := strings.TrimSpace(*req.URL)
		if !strings.HasPrefix(newURL, "https://") && !strings.HasPrefix(newURL, "http://") {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "settings.webhook_url_invalid")})
			return
		}
		oldURL := hook.URL
		if err := a.Store.UpdateWebhookURL(hook.ID, newURL); err != nil {
			log.Printf("update webhook url error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWebhookURLUpdate, Details: map[string]any{"oldUrl": oldURL, "newUrl": newURL}})
	}

	updated, err := a.Store.GetWebhook(hook.ID)
	if err != nil {
		log.Printf("get webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"url":       updated.URL,
		"enabled":   updated.Enabled,
		"updatedAt": updated.UpdatedAt.Local().Format("02/01/2006 15:04"),
	})
}

// handleRegenerateWebhookSecret replaces a webhook's signing secret and
// returns the new one — the "..." menu action for when a secret may have
// leaked. Like at creation, this is the only response that will ever carry
// the plaintext secret again.
func HandleRegenerateWebhookSecret(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadWebhookInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	secret, err := a.Store.RegenerateWebhookSecret(hook.ID)
	if err != nil {
		log.Printf("regenerate webhook secret error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWebhookSecretRegenerate, Details: map[string]any{"url": hook.URL}})

	updated, err := a.Store.GetWebhook(hook.ID)
	if err != nil {
		log.Printf("get webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{
		"secret":    secret,
		"updatedAt": updated.UpdatedAt.Local().Format("02/01/2006 15:04"),
	})
}

// handleTriggerWebhook delivers a manual "Envoyer" click synchronously
// (unlike an automatic trigger from logActivity, which always runs in its
// own goroutine) so the click gets an immediate, honest success/failure
// instead of the app just claiming it worked.
func HandleTriggerWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermSettingsManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadWebhookInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	deliverErr := a.DeliverWebhook(hook, store.ActionWebhookManualTrigger, true, map[string]any{"triggeredBy": currentUser.Username})
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionWebhookManualTrigger, Details: map[string]any{"url": hook.URL}})

	// Reload the row so the caller can update its "last triggered" text in
	// place instead of reloading the whole page to see the outcome.
	resp := map[string]any{"ok": deliverErr == nil}
	if updated, err := a.Store.GetWebhook(hook.ID); err != nil {
		log.Printf("get webhook after trigger error: %v", err)
	} else if updated != nil {
		resp["updatedAt"] = updated.UpdatedAt.Local().Format("02/01/2006 15:04")
		if updated.LastTriggeredAt.Valid {
			resp["lastTriggeredAt"] = updated.LastTriggeredAt.Time.Local().Format("02/01/2006 15:04")
			resp["lastStatus"] = updated.LastStatus
		}
	}

	if deliverErr != nil {
		resp["error"] = i18n.T(lang, "settings.webhook_trigger_failed") + " (" + deliverErr.Error() + ")"
		webutil.WriteJSON(w, http.StatusBadGateway, resp)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, resp)
}

// loadWebhookInWorkspace resolves {id} to a webhook that really belongs
// to this workspace, writing a 404 otherwise — closes off one workspace's
// members from acting on another workspace's webhook by guessing its id.
func loadWebhookInWorkspace(a *app.App, w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.Webhook, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	hook, err := a.Store.GetWebhook(id)
	if err != nil {
		log.Printf("get webhook error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if hook == nil || hook.WorkspaceID != ws.ID {
		http.NotFound(w, r)
		return nil, false
	}
	return hook, true
}
