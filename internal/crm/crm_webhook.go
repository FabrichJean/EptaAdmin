package crm

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// This file is the CRM+ equivalent of internal/webhook/webhook.go: a team's
// own webhooks, scoped by crm_team_id instead of workspace_id (see
// store.CRMTeamWebhook and app.FireCRMTeamWebhooks/DeliverCRMTeamWebhook).
// Management is gated on roles.PermWorkspaceManage — the same permission
// HandleRenameCRMTeam already uses for "administer this team's settings".
// Unlike workspace webhooks there is no URL-edit-in-place endpoint: delete
// and recreate covers changing the destination, which keeps this smaller.

func HandleCreateCRMTeamWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermWorkspaceManage) {
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

	hook, err := a.Store.CreateCRMTeamWebhook(team.ID, url)
	if err != nil {
		log.Printf("create crm team webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMWebhookCreate, Details: map[string]any{"teamName": team.Name, "url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":  hook.ID,
		"url": hook.URL,
	})
}

func HandleDeleteCRMTeamWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermWorkspaceManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadCRMTeamWebhook(a, w, r, team)
	if !ok {
		return
	}

	if err := a.Store.DeleteCRMTeamWebhook(hook.ID); err != nil {
		log.Printf("delete crm team webhook error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMWebhookDelete, Details: map[string]any{"teamName": team.Name, "url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleUpdateCRMTeamWebhook only toggles enabled — see this file's own
// comment on why there's no URL-edit endpoint.
func HandleUpdateCRMTeamWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermWorkspaceManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadCRMTeamWebhook(a, w, r, team)
	if !ok {
		return
	}

	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	if req.Enabled == nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	if err := a.Store.SetCRMTeamWebhookEnabled(hook.ID, *req.Enabled); err != nil {
		log.Printf("set crm team webhook enabled error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	toggleAction := store.ActionCRMWebhookDisable
	if *req.Enabled {
		toggleAction = store.ActionCRMWebhookEnable
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: toggleAction, Details: map[string]any{"teamName": team.Name, "url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true, "enabled": *req.Enabled})
}

// HandleRegenerateCRMTeamWebhookSecret replaces a webhook's signing secret
// and returns the new one — the only response that will ever carry it
// again, same as at creation.
func HandleRegenerateCRMTeamWebhookSecret(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermWorkspaceManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadCRMTeamWebhook(a, w, r, team)
	if !ok {
		return
	}

	secret, err := a.Store.RegenerateCRMTeamWebhookSecret(hook.ID)
	if err != nil {
		log.Printf("regenerate crm team webhook secret error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMWebhookSecretRegenerate, Details: map[string]any{"teamName": team.Name, "url": hook.URL}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

// HandleTriggerCRMTeamWebhook delivers a manual "Envoyer" click
// synchronously (unlike an automatic trigger, which runs in its own
// goroutine) so the click gets an immediate, honest success/failure.
func HandleTriggerCRMTeamWebhook(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermWorkspaceManage) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	hook, ok := loadCRMTeamWebhook(a, w, r, team)
	if !ok {
		return
	}

	deliverErr := a.DeliverCRMTeamWebhook(hook, store.ActionCRMWebhookManualTrigger, true, map[string]any{"triggeredBy": currentUser.Username})
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMWebhookManualTrigger, Details: map[string]any{"teamName": team.Name, "url": hook.URL}})

	resp := map[string]any{"ok": deliverErr == nil}
	if updated, err := a.Store.GetCRMTeamWebhook(hook.ID); err != nil {
		log.Printf("get crm team webhook after trigger error: %v", err)
	} else if updated != nil {
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

// loadCRMTeamWebhook resolves {id} to a webhook that really belongs to this
// team, writing a 404 otherwise — closes off one team's members from
// acting on another team's webhook by guessing its id.
func loadCRMTeamWebhook(a *app.App, w http.ResponseWriter, r *http.Request, team *store.CRMTeam) (*store.CRMTeamWebhook, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	hook, err := a.Store.GetCRMTeamWebhook(id)
	if err != nil {
		log.Printf("get crm team webhook error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if hook == nil || hook.CRMTeamID != team.ID {
		http.NotFound(w, r)
		return nil, false
	}
	return hook, true
}
