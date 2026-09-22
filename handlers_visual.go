package main

import (
	"encoding/json"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
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
	token, err := a.generateEditToken(site.ID, site.TokenGeneration, currentUser.ID)
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

	a.render(w, r, "visual_dashboard.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "plugins",
		"PageTitle":   site.Name,
		"Workspace":   ws,
		"Site":        site,
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

// resolveVisualWrite validates a write-capable public call (write/clear
// cell, upload): the key must resolve to a real site AND the token must
// verify for that exact site. Both fail the same way (site=nil) so a
// caller can't distinguish "bad key" from "bad token" — no reason to help
// someone probing for valid keys/tokens tell those apart. Also returns the
// admin's user ID embedded in the token, so callers can attribute the
// resulting activity log entry to a real account instead of "unknown".
func (a *App) resolveVisualWrite(key, token string) (*VisualSite, int64) {
	site, err := a.store.GetVisualSiteByKeyToken(key)
	if err != nil || site == nil {
		return nil, 0
	}
	userID, ok, err := a.verifyEditToken(token, site.ID, site.TokenGeneration)
	if err != nil || !ok {
		return nil, 0
	}
	return site, userID
}

// resolveVisualTargetTable loads the workspace+table a write/clear call
// targets, and verifies the workspace actually matches the tracked
// site's own workspace — without this, a forged workspaceSlug in the
// request body could point a valid site's key/token at a completely
// different workspace's data.
func (a *App) resolveVisualTargetTable(site *VisualSite, workspaceSlug, tableSlug string) (*Workspace, *Table, bool) {
	ws, err := a.store.GetWorkspaceBySlug(workspaceSlug)
	if err != nil || ws == nil || ws.ID != site.WorkspaceID {
		return nil, nil, false
	}
	t, err := a.store.GetTableByWorkspaceSlug(ws.ID, tableSlug)
	if err != nil || t == nil {
		return nil, nil, false
	}
	return ws, t, true
}

type visualWriteCellRequest struct {
	Key           string `json:"key"`
	Token         string `json:"token"`
	WorkspaceSlug string `json:"workspaceSlug"`
	TableSlug     string `json:"tableSlug"`
	Column        string `json:"column"`
	Index         int    `json:"index"`
	Value         string `json:"value"`
	Type          string `json:"type"`
}

// handleVisualWriteCell is the direct-write counterpart to the grid's own
// handleSaveRecords (handlers_table.go) — same underlying mechanism
// (CoerceTyped + RecordStore.UpdateField under dataWriteMu), just reached
// through the site's public key + a short-lived edit token instead of a
// session cookie, since this is called cross-origin from the client site
// being edited rather than from EptaAdmin's own UI. The resulting
// activity entry reuses ActionValueUpdate — a visual edit and a grid edit
// are the same kind of action, and this way it's undoable through the
// existing Activité "Annuler" mechanism too.
func (a *App) handleVisualWriteCell(w http.ResponseWriter, r *http.Request) {
	var req visualWriteCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site, userID := a.resolveVisualWrite(req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, t, ok := a.resolveVisualTargetTable(site, req.WorkspaceSlug, req.TableSlug)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	columns, err := a.store.ListTableColumns(t.ID)
	if err != nil {
		log.Printf("list table columns error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	known := false
	for _, c := range columns {
		if c.Key == req.Column {
			known = true
			break
		}
	}
	if !known {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	value, err := CoerceTyped("en", req.Type, req.Value)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetTable(t.ID)
	if err != nil || fresh == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	records, err := LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	col := records.Column(req.Column)
	if req.Index < 0 || req.Index >= len(col) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	oldValue := col[req.Index]
	if err := records.UpdateField(req.Column, req.Index, value); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	newData, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := EnsureWorkspaceStorageWithinLimit(ws.ID, fresh.StoragePath, int64(len(newData))); err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if err := SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := a.store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump visual target table version error: %v", err)
	}

	a.logActivity(logActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: userID, Action: ActionValueUpdate, Details: map[string]any{"column": req.Column, "index": req.Index, "oldValue": oldValue, "newValue": value}})

	w.WriteHeader(http.StatusNoContent)
}

type visualClearCellRequest struct {
	Key           string `json:"key"`
	Token         string `json:"token"`
	WorkspaceSlug string `json:"workspaceSlug"`
	TableSlug     string `json:"tableSlug"`
	Column        string `json:"column"`
	Index         int    `json:"index"`
}

// handleVisualClearCell is the "✕" delete affordance's target — clears a
// cell's value (RecordStore.DeleteField, same as the grid's own delete),
// not the whole record, so every other field's alignment to it is
// undisturbed (see DeleteField's own doc comment in jsondata.go).
func (a *App) handleVisualClearCell(w http.ResponseWriter, r *http.Request) {
	var req visualClearCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site, userID := a.resolveVisualWrite(req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, t, ok := a.resolveVisualTargetTable(site, req.WorkspaceSlug, req.TableSlug)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetTable(t.ID)
	if err != nil || fresh == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	records, err := LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	oldValue, err := records.DeleteField(req.Column, req.Index)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := a.store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump visual target table version error: %v", err)
	}

	a.logActivity(logActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: userID, Action: ActionValueDelete, Details: map[string]any{"column": req.Column, "index": req.Index, "value": oldValue}})

	w.WriteHeader(http.StatusNoContent)
}

// handleVisualUpload backs the SDK's in-place image replace: multipart
// upload (key/token as form fields, since this is multipart not JSON),
// reusing the exact same storage path/quota check as the regular file
// upload endpoint (uploads.go) rather than a second storage mechanism.
func (a *App) handleVisualUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxGenericUploadSize+1<<20)
	if err := r.ParseMultipartForm(maxGenericUploadSize + 1<<20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key := r.FormValue("key")
	token := r.FormValue("token")
	site, _ := a.resolveVisualWrite(key, token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, err := a.store.GetWorkspaceByID(site.WorkspaceID)
	if err != nil || ws == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var file multipart.File
	var header *multipart.FileHeader
	file, header, err = r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer file.Close()

	if err := EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	filename, err := SaveUploadedFile(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		log.Printf("save visual upload error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	signedPath, err := a.signUploadPath(ws.Slug, filename)
	if err != nil {
		log.Printf("sign visual upload path error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": a.absoluteURL(r) + signedPath})
}

// absoluteURL resolves this instance's own externally-visible origin: the
// configured EPTAADMIN_PUBLIC_URL when set (see webhooks.go's identical
// preference for progressUrl), otherwise derived from the incoming
// request itself. The derived fallback matters here specifically: the
// visual SDK embeds this URL as an <img src> on a completely different
// domain, so a bare relative path (fine for a same-origin admin page)
// would resolve against the CLIENT site instead of EptaAdmin and break —
// unlike every other signed-URL use in this app, which stays same-origin
// and never needed this.
func (a *App) absoluteURL(r *http.Request) string {
	if a.publicURL != "" {
		return a.publicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleVisualVerifyToken is called once by the SDK when it sees
// ?epta_edit=<token> in the URL, before switching into edit mode.
func (a *App) handleVisualVerifyToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}
	site, _ := a.resolveVisualWrite(req.Key, req.Token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": site != nil})
}

// handleVisualLogout is called when an admin clicks "Quitter" — it bumps
// the site's token generation (visual_store.go), which invalidates the
// token used to make this very call, and every other outstanding token
// for the site, immediately rather than waiting for their natural
// expiry. Always 204: whether the token was already invalid or this is
// the first time it's used, the outcome from the caller's side is the
// same ("you are now logged out").
func (a *App) handleVisualLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	site, _ := a.resolveVisualWrite(req.Key, req.Token)
	if site != nil {
		if err := a.store.BumpVisualSiteTokenGeneration(site.ID); err != nil {
			log.Printf("bump visual site token generation error: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
