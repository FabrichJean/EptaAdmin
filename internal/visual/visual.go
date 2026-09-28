package visual

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
	"eptaadmin/internal/webutil"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// loadVisualSiteInWorkspace mirrors loadTrackedSiteInWorkspace: fetch a
// visual site by id, scoped to the given workspace, 404 otherwise.
func loadVisualSiteInWorkspace(a *app.App, w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.VisualSite, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	site, err := a.Store.GetVisualSite(id)
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
func HandleCreateVisualSite(a *app.App, w http.ResponseWriter, r *http.Request) {
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
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "sites.name_required")})
		return
	}

	token, site, err := a.Store.CreateVisualSite(ws.ID, name, strings.TrimSpace(req.Domain))
	if err != nil {
		log.Printf("create visual site error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionVisualSiteCreate, Details: map[string]any{"name": name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":        site.ID,
		"name":      site.Name,
		"domain":    site.Domain,
		"key":       token,
		"keyPrefix": site.KeyPrefix,
	})
}

func HandleDeleteVisualSite(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadVisualSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	if err := a.Store.DeleteVisualSite(site.ID, ws.ID); err != nil {
		log.Printf("delete visual site error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionVisualSiteDelete, Details: map[string]any{"name": site.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func HandleRegenerateVisualSiteKey(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadVisualSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	token, err := a.Store.RegenerateVisualSiteKey(site.ID)
	if err != nil {
		log.Printf("regenerate visual site key error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionVisualSiteKeyRegenerate, Details: map[string]any{"name": site.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"key": token})
}

// handleUpdateVisualSiteDomain lets the admin set/change a site's domain
// after creation — nothing required entering one up front, but generating
// an edit link needs one (see handleGenerateVisualEditLink below).
func HandleUpdateVisualSiteDomain(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadVisualSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if err := a.Store.UpdateVisualSiteDomain(site.ID, ws.ID, domain); err != nil {
		log.Printf("update visual site domain error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"domain": domain})
}

// handleGenerateVisualEditLink builds the "Ouvrir en mode édition" URL:
// the site's own domain plus a short-lived signed token (see
// visual_signing.go) — opening it lets static/visual.js recognize this
// visitor as an authenticated admin, without any shared session cookie
// (impossible cross-domain) or OAuth popup.
func HandleGenerateVisualEditLink(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadVisualSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	if site.Domain == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "visual.domain_required")})
		return
	}
	token, err := generateEditToken(a, site.ID, site.TokenGeneration, currentUser.ID)
	if err != nil {
		log.Printf("generate visual edit token error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	base := site.Domain
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"url": base + "/?epta_edit=" + token})
}

// handleVisualSiteDashboard shows a site's snippet, edit-link generator
// and every currently-mapped element — the read/manage surface that
// stays inside EptaAdmin, deliberately separate from the generic
// datasource grid.
func HandleVisualSiteDashboard(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataRead) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	site, ok := loadVisualSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	a.Render(w, r, "visual_dashboard.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "plugins",
		"PageTitle":   site.Name,
		"Workspace":   ws,
		"Site":        site,
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: i18n.T(lang, "plugins.title"), URL: "/plugins"},
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
func resolveVisualWrite(a *app.App, key, token string) (*store.VisualSite, int64) {
	site, err := a.Store.GetVisualSiteByKeyToken(key)
	if err != nil || site == nil {
		return nil, 0
	}
	userID, ok, err := verifyEditToken(a, token, site.ID, site.TokenGeneration)
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
func resolveVisualTargetTable(a *app.App, site *store.VisualSite, workspaceSlug, tableSlug string) (*store.Workspace, *store.Table, bool) {
	ws, err := a.Store.GetWorkspaceBySlug(workspaceSlug)
	if err != nil || ws == nil || ws.ID != site.WorkspaceID {
		return nil, nil, false
	}
	t, err := a.Store.GetTableByWorkspaceSlug(ws.ID, tableSlug)
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
// (store.CoerceTyped + store.RecordStore.UpdateField under webutil.DataWriteMu), just reached
// through the site's public key + a short-lived edit token instead of a
// session cookie, since this is called cross-origin from the client site
// being edited rather than from EptaAdmin's own UI. The resulting
// activity entry reuses store.ActionValueUpdate — a visual edit and a grid edit
// are the same kind of action, and this way it's undoable through the
// existing Activité "Annuler" mechanism too.
func HandleVisualWriteCell(a *app.App, w http.ResponseWriter, r *http.Request) {
	var req visualWriteCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site, userID := resolveVisualWrite(a, req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, t, ok := resolveVisualTargetTable(a, site, req.WorkspaceSlug, req.TableSlug)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	columns, err := a.Store.ListTableColumns(t.ID)
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
	value, err := store.CoerceTyped("en", req.Type, req.Value)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	fresh, err := a.Store.GetTable(t.ID)
	if err != nil || fresh == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	records, err := store.LoadRecordStore(fresh.StoragePath)
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
	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, fresh.StoragePath, int64(len(newData))); err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := a.Store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump visual target table version error: %v", err)
	}

	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: userID, Action: store.ActionValueUpdate, Details: map[string]any{"column": req.Column, "index": req.Index, "oldValue": oldValue, "newValue": value}})

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
// cell's value (store.RecordStore.DeleteField, same as the grid's own delete),
// not the whole record, so every other field's alignment to it is
// undisturbed (see DeleteField's own doc comment in jsondata.go).
func HandleVisualClearCell(a *app.App, w http.ResponseWriter, r *http.Request) {
	var req visualClearCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site, userID := resolveVisualWrite(a, req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, t, ok := resolveVisualTargetTable(a, site, req.WorkspaceSlug, req.TableSlug)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	fresh, err := a.Store.GetTable(t.ID)
	if err != nil || fresh == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	records, err := store.LoadRecordStore(fresh.StoragePath)
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
	if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save visual target record store error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := a.Store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump visual target table version error: %v", err)
	}

	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: userID, Action: store.ActionValueDelete, Details: map[string]any{"column": req.Column, "index": req.Index, "value": oldValue}})

	w.WriteHeader(http.StatusNoContent)
}

// handleVisualUpload backs the SDK's in-place image replace: multipart
// upload (key/token as form fields, since this is multipart not JSON),
// reusing the exact same storage path/quota check as the regular file
// upload endpoint (uploads.go) rather than a second storage mechanism.
func HandleVisualUpload(a *app.App, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxGenericUploadSize+1<<20)
	if err := r.ParseMultipartForm(uploads.MaxGenericUploadSize + 1<<20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key := r.FormValue("key")
	token := r.FormValue("token")
	site, _ := resolveVisualWrite(a, key, token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	ws, err := a.Store.GetWorkspaceByID(site.WorkspaceID)
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

	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	filename, err := uploads.SaveUploadedFile(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		log.Printf("save visual upload error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	signedPath, err := uploads.SignUploadPath(a.Store, ws.Slug, filename)
	if err != nil {
		log.Printf("sign visual upload path error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"url": a.AbsoluteURL(r) + signedPath})
}

// handleVisualVerifyToken is called once by the SDK when it sees
// ?epta_edit=<token> in the URL, before switching into edit mode.
func HandleVisualVerifyToken(a *app.App, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}
	site, _ := resolveVisualWrite(a, req.Key, req.Token)
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": site != nil})
}

// handleVisualLogout is called when an admin clicks "Quitter" — it bumps
// the site's token generation (visual_store.go), which invalidates the
// token used to make this very call, and every other outstanding token
// for the site, immediately rather than waiting for their natural
// expiry. Always 204: whether the token was already invalid or this is
// the first time it's used, the outcome from the caller's side is the
// same ("you are now logged out").
func HandleVisualLogout(a *app.App, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string `json:"key"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	site, _ := resolveVisualWrite(a, req.Key, req.Token)
	if site != nil {
		if err := a.Store.BumpVisualSiteTokenGeneration(site.ID); err != nil {
			log.Printf("bump visual site token generation error: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
