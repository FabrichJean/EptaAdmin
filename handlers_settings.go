package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// webhookGroup pairs one workspace with its webhooks, for the global
// "Webhook" nav page (see handleGlobalWebhooks) — mirrors the
// global/per-workspace split the Activité page already has.
type webhookGroup struct {
	Workspace *UserWorkspace
	Webhooks  []*Webhook
}

// handleGlobalWebhooks lists every webhook across every workspace the
// user can manage settings for — the sidebar's global "Webhook" entry,
// so members with several workspaces don't have to open each one's
// settings page separately to see what's configured.
func (a *App) handleGlobalWebhooks(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	groups := make([]webhookGroup, 0, len(workspaces))
	for _, ws := range workspaces {
		if !hasPermission(ws.Role, PermSettingsManage) {
			continue
		}
		hooks, err := a.store.ListWebhooks(ws.ID)
		if err != nil {
			log.Printf("list webhooks error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		groups = append(groups, webhookGroup{Workspace: ws, Webhooks: hooks})
	}

	a.render(w, r, "webhooks.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "webhooks",
		"PageTitle":   T(lang, "settings.webhooks_title"),
		"Groups":      groups,
		"HeaderTitle": T(lang, "settings.webhooks_title"),
		"HeaderIcon":  "webhook",
	})
}

// handleSettingsPage renders a workspace's settings — for now, just its
// webhooks (see webhooks.go). Gated on PermSettingsManage, the same
// permission that already covers registering data sources.
func (a *App) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	webhooks, err := a.store.ListWebhooks(ws.ID)
	if err != nil {
		log.Printf("list webhooks error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.render(w, r, "settings.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "workspaces",
		"PageTitle":   T(lang, "settings.title"),
		"Workspace":   ws,
		"Webhooks":    webhooks,
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: T(lang, "settings.title")},
		},
		"HeaderTitle": T(lang, "settings.title"),
		"HeaderIcon":  "code",
	})
}

func (a *App) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "settings.webhook_url_invalid")})
		return
	}

	hook, err := a.store.CreateWebhook(ws.ID, url)
	if err != nil {
		log.Printf("create webhook error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionWebhookCreate, Details: map[string]any{"url": hook.URL}})

	writeJSON(w, http.StatusOK, map[string]any{
		"id":  hook.ID,
		"url": hook.URL,
	})
}

func (a *App) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}
	hook, ok := a.loadWebhookInWorkspace(w, r, ws)
	if !ok {
		return
	}

	if err := a.store.DeleteWebhook(hook.ID); err != nil {
		log.Printf("delete webhook error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionWebhookDelete, Details: map[string]any{"url": hook.URL}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleToggleWebhook(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}
	hook, ok := a.loadWebhookInWorkspace(w, r, ws)
	if !ok {
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	if err := a.store.SetWebhookEnabled(hook.ID, req.Enabled); err != nil {
		log.Printf("set webhook enabled error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	toggleAction := ActionWebhookDisable
	if req.Enabled {
		toggleAction = ActionWebhookEnable
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: toggleAction, Details: map[string]any{"url": hook.URL}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTriggerWebhook delivers a manual "Envoyer" click synchronously
// (unlike an automatic trigger from logActivity, which always runs in its
// own goroutine) so the click gets an immediate, honest success/failure
// instead of the app just claiming it worked.
func (a *App) handleTriggerWebhook(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermSettingsManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}
	hook, ok := a.loadWebhookInWorkspace(w, r, ws)
	if !ok {
		return
	}

	deliverErr := a.deliverWebhook(hook, ActionWebhookManualTrigger, true, map[string]any{"triggeredBy": currentUser.Username})
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionWebhookManualTrigger, Details: map[string]any{"url": hook.URL}})

	// Reload the row so the caller can update its "last triggered" text in
	// place instead of reloading the whole page to see the outcome.
	resp := map[string]any{"ok": deliverErr == nil}
	if updated, err := a.store.GetWebhook(hook.ID); err != nil {
		log.Printf("get webhook after trigger error: %v", err)
	} else if updated != nil && updated.LastTriggeredAt.Valid {
		resp["lastTriggeredAt"] = updated.LastTriggeredAt.Time.Local().Format("02/01/2006 15:04")
		resp["lastStatus"] = updated.LastStatus
	}

	if deliverErr != nil {
		resp["error"] = T(lang, "settings.webhook_trigger_failed") + " (" + deliverErr.Error() + ")"
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadWebhookInWorkspace resolves {id} to a webhook that really belongs
// to this workspace, writing a 404 otherwise — closes off one workspace's
// members from acting on another workspace's webhook by guessing its id.
func (a *App) loadWebhookInWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) (*Webhook, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	hook, err := a.store.GetWebhook(id)
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
