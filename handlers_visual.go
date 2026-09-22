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
		if err != nil {
			log.Printf("get synced visual table error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		t = existing
	}
	if t == nil {
		ds, err := a.store.GetOrCreateVisualSitesDataSource(ws.ID)
		if err != nil {
			log.Printf("get or create visual sites data source error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		newTable, err := a.store.CreateTable(ds.ID, ws.ID, site.Name)
		if err != nil {
			log.Printf("create visual sync table error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		if err := SaveRecordStore(newTable.StoragePath, RecordStore{}); err != nil {
			log.Printf("init visual sync table store error: %v", err)
		}
		for _, key := range visualSyncColumnKeys {
			if _, err := a.store.AddTableColumn(newTable.ID, key, ColumnTypeText, ""); err != nil && err != ErrTableColumnExists {
				log.Printf("add visual sync column error: %v", err)
			}
		}
		if err := a.store.SetVisualSiteSyncedTableID(site.ID, newTable.ID); err != nil {
			log.Printf("set visual site synced table error: %v", err)
		}
		t = newTable
	}

	fields, err := a.store.ListVisualFields(site.ID)
	if err != nil {
		log.Printf("list visual fields error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetTable(t.ID)
	if err != nil || fresh == nil {
		log.Printf("get table error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	records := RecordStore{}
	for _, f := range fields {
		values := map[string]string{
			"page_url":   f.PageURL,
			"selector":   f.Selector,
			"type":       f.ValueType,
			"value":      f.Value,
			"updated_at": f.UpdatedAt.Format(time.RFC3339),
		}
		for _, key := range visualSyncColumnKeys {
			records, _ = records.AppendField(key, values[key])
		}
	}

	newData, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal visual sync record store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := EnsureWorkspaceStorageWithinLimit(ws.ID, fresh.StoragePath, int64(len(newData))); err != nil {
		if err == ErrWorkspaceStorageLimitExceeded {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save visual sync record store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "datasource.write_error")})
		return
	}
	if _, err := a.store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump visual sync table version error: %v", err)
	}

	a.logActivity(logActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: currentUser.ID, Action: ActionVisualSiteApply, Details: map[string]any{"name": site.Name, "count": len(fields)}})

	writeJSON(w, http.StatusOK, map[string]any{"tableSlug": fresh.Slug, "count": len(fields)})
}

// --- Public API, called cross-origin from the client site by
// static/visual.js. See main.go's withAPICORS for the /api/v1/visual/
// CORS allowance (GET/POST/DELETE/OPTIONS, Content-Type only — no
// Authorization header, everything travels in the body/query since a
// bare <script> tag on an arbitrary third-party page can't manage one).

type visualFieldPublic struct {
	Selector string `json:"selector"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

// handleVisualListFields is what EVERY visitor's page load calls (edit
// mode or not) — it's how an edit becomes visible to everyone, not just
// the admin who made it. Public and read-only, same trust level as the
// tracking key: knowing it only lets you read this site's own edited
// content, nothing else.
func (a *App) handleVisualListFields(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	url := r.URL.Query().Get("url")
	site, err := a.store.GetVisualSiteByKeyToken(key)
	if err != nil {
		log.Printf("resolve visual site error: %v", err)
		writeJSON(w, http.StatusOK, []visualFieldPublic{})
		return
	}
	if site == nil {
		writeJSON(w, http.StatusOK, []visualFieldPublic{})
		return
	}
	fields, err := a.store.ListVisualFieldsForPage(site.ID, url)
	if err != nil {
		log.Printf("list visual fields for page error: %v", err)
		writeJSON(w, http.StatusOK, []visualFieldPublic{})
		return
	}
	out := make([]visualFieldPublic, len(fields))
	for i, f := range fields {
		out[i] = visualFieldPublic{Selector: f.Selector, Type: f.ValueType, Value: f.Value}
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveVisualWrite validates a write-capable public call (save/delete
// field, upload): the key must resolve to a real site AND the token must
// verify for that exact site. Both fail the same way (site=nil) so a
// caller can't distinguish "bad key" from "bad token" — no reason to help
// someone probing for valid keys/tokens tell those apart.
func (a *App) resolveVisualWrite(key, token string) *VisualSite {
	site, err := a.store.GetVisualSiteByKeyToken(key)
	if err != nil || site == nil {
		return nil
	}
	ok, err := a.verifyEditToken(token, site.ID, site.TokenGeneration)
	if err != nil || !ok {
		return nil
	}
	return site
}

type visualSaveFieldRequest struct {
	Key      string `json:"key"`
	Token    string `json:"token"`
	URL      string `json:"url"`
	Selector string `json:"selector"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

func (a *App) handleVisualSaveField(w http.ResponseWriter, r *http.Request) {
	var req visualSaveFieldRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site := a.resolveVisualWrite(req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if req.URL == "" || req.Selector == "" || (req.Type != "text" && req.Type != "image") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := a.store.UpsertVisualField(site.ID, req.URL, req.Selector, req.Type, req.Value); err != nil {
		log.Printf("upsert visual field error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleVisualDeleteField(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key      string `json:"key"`
		Token    string `json:"token"`
		URL      string `json:"url"`
		Selector string `json:"selector"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	site := a.resolveVisualWrite(req.Key, req.Token)
	if site == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := a.store.DeleteVisualField(site.ID, req.URL, req.Selector); err != nil {
		log.Printf("delete visual field error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
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
	site := a.resolveVisualWrite(key, token)
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
	site := a.resolveVisualWrite(req.Key, req.Token)
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
	site := a.resolveVisualWrite(req.Key, req.Token)
	if site != nil {
		if err := a.store.BumpVisualSiteTokenGeneration(site.ID); err != nil {
			log.Printf("bump visual site token generation error: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
