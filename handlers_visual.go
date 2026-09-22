package main

import (
	"encoding/json"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// loadVisualSiteInWorkspace mirrors loadTrackedSiteInWorkspace: fetch a
// visual site by id, scoped to the given workspace, 404 otherwise.
func (a *App) loadVisualSiteInWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) (*VisualSite, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	site, err := a.store.GetVisualSite(id)
	if err != nil {
		log.Printf("get visual site error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if site == nil || site.WorkspaceID != ws.ID {
		http.NotFound(w, r)
		return nil, false
	}
	return site, true
}

// handleCreateVisualSite provisions a new visual site: just a name,
// domain and a public key — unlike a tracked site there's no table to
// provision (see store.go's schema comment: visual_fields is a plain SQL
// table, not a datasource/table entry).
func (a *App) handleCreateVisualSite(w http.ResponseWriter, r *http.Request) {
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
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "sites.name_required")})
		return
	}

	token, site, err := a.store.CreateVisualSite(ws.ID, name, strings.TrimSpace(req.Domain))
	if err != nil {
		log.Printf("create visual site error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionVisualSiteCreate, Details: map[string]any{"name": name}})

	writeJSON(w, http.StatusOK, map[string]any{
		"id":        site.ID,
		"name":      site.Name,
		"domain":    site.Domain,
		"key":       token,
		"keyPrefix": site.KeyPrefix,
	})
}

func (a *App) handleDeleteVisualSite(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	if err := a.store.DeleteVisualSite(site.ID, ws.ID); err != nil {
		log.Printf("delete visual site error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionVisualSiteDelete, Details: map[string]any{"name": site.Name}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleRegenerateVisualSiteKey(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	token, err := a.store.RegenerateVisualSiteKey(site.ID)
	if err != nil {
		log.Printf("regenerate visual site key error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionVisualSiteKeyRegenerate, Details: map[string]any{"name": site.Name}})

	writeJSON(w, http.StatusOK, map[string]string{"key": token})
}

// handleUpdateVisualSiteDomain lets the admin set/change a site's domain
// after creation — nothing required entering one up front, but generating
// an edit link needs one (see handleGenerateVisualEditLink below).
func (a *App) handleUpdateVisualSiteDomain(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if err := a.store.UpdateVisualSiteDomain(site.ID, ws.ID, domain); err != nil {
		log.Printf("update visual site domain error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"domain": domain})
}

// handleGenerateVisualEditLink builds the "Ouvrir en mode édition" URL:
// the site's own domain plus a short-lived signed token (see
// visual_signing.go) — opening it lets static/visual.js recognize this
// visitor as an authenticated admin, without any shared session cookie
// (impossible cross-domain) or OAuth popup.
func (a *App) handleGenerateVisualEditLink(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	if site.Domain == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "visual.domain_required")})
		return
	}
	token, err := a.generateEditToken(site.ID, site.TokenGeneration)
	if err != nil {
		log.Printf("generate visual edit token error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	base := site.Domain
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")

	writeJSON(w, http.StatusOK, map[string]string{"url": base + "/?epta_edit=" + token})
}

// handleVisualSiteDashboard shows a site's snippet, edit-link generator
// and every currently-mapped element — the read/manage surface that
// stays inside EptaAdmin, deliberately separate from the generic
// datasource grid.
func (a *App) handleVisualSiteDashboard(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataRead) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	fields, err := a.store.ListVisualFields(site.ID)
	if err != nil {
		log.Printf("list visual fields error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	syncedTableSlug := ""
	if site.SyncedTableID.Valid {
		if t, err := a.store.GetTable(site.SyncedTableID.Int64); err == nil && t != nil {
			syncedTableSlug = t.Slug
		}
	}

	a.render(w, r, "visual_dashboard.html", map[string]any{
		"CurrentUser":     currentUser,
		"ActiveNav":       "plugins",
		"PageTitle":       site.Name,
		"Workspace":       ws,
		"Site":            site,
		"Fields":          fields,
		"SyncedTableSlug": syncedTableSlug,
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: T(lang, "plugins.title"), URL: "/plugins"},
			{Label: site.Name},
		},
		"HeaderTitle": site.Name,
		"HeaderIcon":  "activity",
	})
}

// handleDeleteVisualFieldFromDashboard lets an admin unlink a mapped
// element from inside EptaAdmin, not only from the live site's delete
// affordance.
func (a *App) handleDeleteVisualFieldFromDashboard(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}
	fieldID, err := strconv.ParseInt(r.PathValue("fieldID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.DeleteVisualFieldByID(fieldID, site.ID); err != nil {
		log.Printf("delete visual field error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// visualSyncColumnKeys are the fixed columns of a visual site's synced
// snapshot table — one row per currently-mapped field, rebuilt from
// scratch on every "Appliquer" click (see handleApplyVisualSiteToDataSource).
var visualSyncColumnKeys = []string{"page_url", "selector", "type", "value", "updated_at"}

// handleApplyVisualSiteToDataSource materializes the visual site's
// current edited-elements state into a real, browsable/exportable table —
// visual_fields (store.go) stays the live source of truth the SDK reads
// from, this is a manual, on-demand snapshot for people who want to see
// or export the same data through the standard grid/API surface. Each
// click fully overwrites the table's contents with a fresh snapshot
// (not an incremental append, unlike tracking's event log) — the synced
// table is created once (first click) and reused on every later one, via
// VisualSite.SyncedTableID.
func (a *App) handleApplyVisualSiteToDataSource(w http.ResponseWriter, r *http.Request) {
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
	site, ok := a.loadVisualSiteInWorkspace(w, r, ws)
	if !ok {
		return
	}

	var t *Table
	if site.SyncedTableID.Valid {
		existing, err := a.store.GetTable(site.SyncedTableID.Int64)
