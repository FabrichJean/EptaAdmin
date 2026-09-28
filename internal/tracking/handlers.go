package tracking

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/ratelimit"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// maxRecentEventsPerPoll bounds how many rows the Real-time Stream tab's
// polling endpoint returns per request — it always wants "everything
// since last time", but a client that's been away for a while (or a
// stale afterIndex) shouldn't be able to force a full-table dump.
const maxRecentEventsPerPoll = 200

// trackedSiteColumnKeys are the fixed columns auto-provisioned on a
// tracked site's events table. The type is per-value everywhere else in
// this app (see handleAddTableColumn), but these are still declared with
// a sensible default type so store.CoerceTyped/the grid render them correctly
// from the very first event, before any human ever opens the table.
//
// Only the visitor's public IP is captured (ip_public, from clientIP
// below) — there is no "private/local IP" column: that's only reachable
// client-side via a WebRTC/STUN leak, a heavier fingerprinting technique
// than the light profile this SDK deliberately sticks to, and one modern
// browsers increasingly block anyway (Chrome/Firefox now hide the real
// local IP behind a random per-connection mDNS name in most cases).
var trackedSiteColumnKeys = []struct {
	key         string
	colType     string
	description string
}{
	{"ip_public", store.ColumnTypeText, "Adresse IP publique du visiteur, captée côté serveur (X-Forwarded-For ou adresse de connexion)."},
	{"user_agent", store.ColumnTypeText, ""},
	{"language", store.ColumnTypeText, ""},
	{"screen", store.ColumnTypeText, ""},
	{"url", store.ColumnTypeText, ""},
	{"referrer", store.ColumnTypeText, ""},
	{"event_type", store.ColumnTypeText, ""},
	{"session_id", store.ColumnTypeText, ""},
	{"time_on_page", store.ColumnTypeNumber, ""},
	{"timestamp", store.ColumnTypeText, ""},
}

// TrackedSiteView pairs a tracked site with its events table's slug, so
// the settings page can link straight to "Voir les données collectées"
// without the template needing a second lookup.
type TrackedSiteView struct {
	*store.TrackedSite
	TableSlug string
}

// loadTrackedSiteInWorkspace mirrors loadWebhookInWorkspace: fetch a
// tracked site by id, scoped to the given workspace, 404 otherwise.
func loadTrackedSiteInWorkspace(a *app.App, w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.TrackedSite, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	site, err := a.Store.GetTrackedSite(id)
	if err != nil {
		log.Printf("get tracked site error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if site == nil || site.WorkspaceID != ws.ID {
		http.NotFound(w, r)
		return nil, false
	}
	return site, true
}

// handleCreateTrackedSite provisions a new tracked site: a dedicated
// events table (fixed schema, see trackedSiteColumnKeys) plus a public
// tracking key, returned exactly once so the caller can build the
// embeddable <script> snippet around it.
func HandleCreateTrackedSite(a *app.App, w http.ResponseWriter, r *http.Request) {
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

	ds, err := a.Store.GetOrCreateSitesDataSource(ws.ID)
	if err != nil {
		log.Printf("get or create sites data source error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	t, err := a.Store.CreateTable(ds.ID, ws.ID, name)
	if err != nil {
		if err == store.ErrTableExists {
			webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "sites.name_exists")})
		} else {
			log.Printf("create tracked site table error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	if err := store.SaveRecordStore(t.StoragePath, store.RecordStore{}); err != nil {
		log.Printf("init tracked site table store error: %v", err)
	}
	for _, c := range trackedSiteColumnKeys {
		if _, err := a.Store.AddTableColumn(t.ID, c.key, c.colType, c.description); err != nil && err != store.ErrTableColumnExists {
			log.Printf("add tracked site column error: %v", err)
		}
	}

	token, site, err := a.Store.CreateTrackedSite(ws.ID, t.ID, name, strings.TrimSpace(req.Domain))
	if err != nil {
		log.Printf("create tracked site error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: t.ID, UserID: currentUser.ID, Action: store.ActionSiteCreate, Details: map[string]any{"name": name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":        site.ID,
		"name":      site.Name,
		"domain":    site.Domain,
		"key":       token,
		"keyPrefix": site.KeyPrefix,
		"tableSlug": t.Slug,
	})
}

// handleDeleteTrackedSite revokes a tracking key. The events table itself
// (and everything already collected in it) is left in place — same as
// deleting a webhook doesn't touch anything it already delivered.
func HandleDeleteTrackedSite(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadTrackedSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	if err := a.Store.DeleteTrackedSite(site.ID, ws.ID); err != nil {
		log.Printf("delete tracked site error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionSiteDelete, Details: map[string]any{"name": site.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleResetTrackedSiteData wipes every recorded event for a tracked
// site back to an empty table — the site, its key, and its column schema
// are all left untouched, only the collected rows are gone. Deliberately
// not reversible (unlike a grid value edit/delete, there's no single
// activity entry to undo a bulk wipe against), so this is logged but not
// added to reversibleActions.
func HandleResetTrackedSiteData(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadTrackedSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	t, err := a.Store.GetTable(site.TableID)
	if err != nil || t == nil {
		log.Printf("get tracked site table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	fresh, err := a.Store.GetTable(t.ID)
	if err != nil || fresh == nil {
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := store.SaveRecordStore(fresh.StoragePath, store.RecordStore{}); err != nil {
		log.Printf("reset tracked site data error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "datasource.write_error")})
		return
	}
	if _, err := a.Store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump table version error: %v", err)
	}

	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: currentUser.ID, Action: store.ActionSiteDataReset, Details: map[string]any{"name": site.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clientIP resolves the visitor's IP for the ingestion endpoint: honors
// X-Forwarded-For (first entry) for deployments sitting behind a reverse
// proxy, falling back to the raw connection address. The browser SDK never
// sends this itself — it's not something client-side JS can know.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.Index(fwd, ","); i >= 0 {
			fwd = fwd[:i]
		}
		return strings.TrimSpace(fwd)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

type trackCollectRequest struct {
	Key        string `json:"key"`
	EventType  string `json:"event_type"`
	URL        string `json:"url"`
	Referrer   string `json:"referrer"`
	SessionID  string `json:"session_id"`
	Language   string `json:"language"`
	Screen     string `json:"screen"`
	TimeOnPage string `json:"time_on_page"`
	Timestamp  string `json:"timestamp"`
}

// handleTrackCollect is the public ingestion endpoint the embedded SDK
// (static/track.js) posts every enter/exit event to. It's deliberately
// unauthenticated by session — auth here is the site's own public
// tracking key, resolved from the JSON body (not a header, so the
// snippet's fetch/sendBeacon call stays as simple as possible for an
// arbitrary third-party page). It always answers 204 regardless of
// internal outcome: a public endpoint hit by anonymous browsers has
// nothing to usefully tell a caller, and echoing failure detail back
// would just help someone probing for valid keys.
func HandleTrackCollect(a *app.App, w http.ResponseWriter, r *http.Request) {
	var req trackCollectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	site, err := a.Store.GetTrackedSiteByKeyToken(req.Key)
	if err != nil {
		log.Printf("resolve tracked site error: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if site == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !ratelimit.TrackRateLimiter.Allow(site.ID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	t, err := a.Store.GetTable(site.TableID)
	if err != nil || t == nil {
		log.Printf("get tracked site table error: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	fields := []struct {
		key string
		val string
	}{
		{"ip_public", clientIP(r)},
		{"user_agent", r.Header.Get("User-Agent")},
		{"language", req.Language},
		{"screen", req.Screen},
		{"url", req.URL},
		{"referrer", req.Referrer},
		{"event_type", req.EventType},
		{"session_id", req.SessionID},
		{"time_on_page", req.TimeOnPage},
		{"timestamp", req.Timestamp},
	}

	// Scoped in a closure so webutil.DataWriteMu (the process-wide JSON-write lock)
	// is released before logActivity runs below — logActivity does its own
	// separate SQLite insert and doesn't touch the record store, so there's
	// no reason to hold this lock any longer than the actual file write.
	saved := func() bool {
		webutil.DataWriteMu.Lock()
		defer webutil.DataWriteMu.Unlock()

		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			log.Printf("load tracked site record store error: %v", err)
			return false
		}

		for _, f := range fields {
			if f.val == "" {
				continue
			}
			colType := store.ColumnTypeText
			if f.key == "time_on_page" {
				colType = store.ColumnTypeNumber
			}
			v, err := store.CoerceTyped("en", colType, f.val)
			if err != nil {
				continue
			}
			records, _ = records.AppendField(f.key, v)
		}

		newData, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			log.Printf("marshal tracked site record store error: %v", err)
			return false
		}
		if err := store.EnsureWorkspaceStorageWithinLimit(site.WorkspaceID, t.StoragePath, int64(len(newData))); err != nil {
			return false
		}
		if err := store.SaveRecordStore(t.StoragePath, records); err != nil {
			log.Printf("save tracked site record store error: %v", err)
			return false
		}
		if _, err := a.Store.BumpTableVersion(t.ID, t.Version); err != nil {
			log.Printf("bump tracked site table version error: %v", err)
		}
		return true
	}()

	if saved {
		// One notification per event (enter AND exit), surfaced through the
		// existing header bell/activity feed — every workspace member with
		// data-read access sees visits to their tracked sites arrive live
		// (the bell already polls every 15s, see templates/layout.html).
		// UserID 0: there is no acting account, this is an anonymous visitor.
		a.LogActivity(app.LogActivityParams{
			WorkspaceID: site.WorkspaceID,
			TableID:     t.ID,
			UserID:      0,
			Action:      store.ActionSiteTrackEvent,
			Details:     map[string]any{"siteName": site.Name, "eventType": req.EventType, "url": req.URL},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// loadTrackedSiteForDashboard resolves and permission-checks everything
// the analytics dashboard's handlers need: the workspace, the site (scoped
// to it), and its events table. Viewing analytics is gated the same as
// viewing the raw table (roles.PermDataRead) — any workspace member who can see
// data can see its analytics, unlike creating/deleting/regenerating a
// site's key, which stays roles.PermSettingsManage (see handleCreateTrackedSite).
func loadTrackedSiteForDashboard(a *app.App, w http.ResponseWriter, r *http.Request) (ws *store.Workspace, site *store.TrackedSite, table *store.Table, ok bool) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return nil, nil, nil, false
	}
	if !roles.HasPermission(role, roles.PermDataRead) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return nil, nil, nil, false
	}
	site, ok = loadTrackedSiteInWorkspace(a, w, r, ws)
	if !ok {
		return nil, nil, nil, false
	}
	t, err := a.Store.GetTable(site.TableID)
	if err != nil || t == nil {
		log.Printf("get tracked site table error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, nil, nil, false
	}
	return ws, site, t, true
}

// handleTrackingDashboard renders the analytics dashboard for one tracked
// site: KPIs/donut/timeline (Overview), a live-polling event table
// (Real-time Stream), a per-session table (Session Analytics), and site
// management (Settings) — all four are rendered into one page and toggled
// client-side (see templates/tracking_dashboard.html), consistent with
// this app's no-JS-framework convention.
func HandleTrackingDashboard(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, site, t, ok := loadTrackedSiteForDashboard(a, w, r)
	if !ok {
		return
	}

	records, err := store.LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load tracked site record store error: %v", err)
		http.Error(w, i18n.T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}
	cutoff, rangeLabel := ParseRangeParam(r.URL.Query().Get("range"))
	stats := ComputeDashboardStats(records, cutoff, rangeLabel, parseActivityPageParam(r))

	a.Render(w, r, "tracking_dashboard.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "plugins",
		"PageTitle":   site.Name,
		"Workspace":   ws,
		"Site":        site,
		"TableSlug":   t.Slug,
		"Stats":       stats,
		"WorldMapSVG": WorldMapSVG,
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

// handleTrackingDashboardData is the JSON twin of handleTrackingDashboard,
// used by the Overview tab's range dropdown to refresh KPIs/donut without
// a full page reload.
func HandleTrackingDashboardData(a *app.App, w http.ResponseWriter, r *http.Request) {
	_, _, t, ok := loadTrackedSiteForDashboard(a, w, r)
	if !ok {
		return
	}
	records, err := store.LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load tracked site record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	cutoff, rangeLabel := ParseRangeParam(r.URL.Query().Get("range"))
	webutil.WriteJSON(w, http.StatusOK, ComputeDashboardStats(records, cutoff, rangeLabel, parseActivityPageParam(r)))
}

// parseActivityPageParam reads the Overview tab's "recent activity"
// pagination query param (?activityPage=N), defaulting to page 1 for a
// missing or malformed value rather than erroring — same leniency as
// parseRangeParam.
func parseActivityPageParam(r *http.Request) int {
	page, err := strconv.Atoi(r.URL.Query().Get("activityPage"))
	if err != nil || page < 1 {
		return 1
	}
	return page
}

// handleTrackingRecentEvents backs the Real-time Stream tab's polling: the
// client remembers the highest record index it has already shown and asks
// for everything after it (?afterIndex=N). A first call with no/negative
// afterIndex primes the stream with the most recent events instead of
// dumping the whole table.
func HandleTrackingRecentEvents(a *app.App, w http.ResponseWriter, r *http.Request) {
	_, _, t, ok := loadTrackedSiteForDashboard(a, w, r)
	if !ok {
		return
	}
	records, err := store.LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load tracked site record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	afterIndex := -1
	if v := r.URL.Query().Get("afterIndex"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			afterIndex = parsed
		}
	}

	start := afterIndex + 1
	if afterIndex < 0 {
		start = len(records) - maxRecentEventsPerPoll
		if start < 0 {
			start = 0
		}
	}
	if start > len(records) {
		start = len(records)
	}
	end := len(records)
	if end-start > maxRecentEventsPerPoll {
		end = start + maxRecentEventsPerPoll
	}

	events := make([]map[string]any, 0, end-start)
	for i := start; i < end; i++ {
		rec := records[i]
		ua, _ := rec["user_agent"].(string)
		browser, os, _ := ParseUserAgent(ua)
		events = append(events, map[string]any{
			"index":      i,
			"ip_public":  rec["ip_public"],
			"event_type": rec["event_type"],
			"url":        rec["url"],
			"session_id": rec["session_id"],
			"timestamp":  rec["timestamp"],
			"browser":    browser,
			"os":         os,
		})
	}

	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"events":    events,
		"lastIndex": len(records) - 1,
	})
}

// handleRegenerateTrackedSiteKey mirrors handleRegenerateWebhookSecret:
// rotates a leaked/rotated key, returning the new plaintext exactly once.
func HandleRegenerateTrackedSiteKey(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	site, ok := loadTrackedSiteInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	token, err := a.Store.RegenerateTrackedSiteKey(site.ID)
	if err != nil {
		log.Printf("regenerate tracked site key error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionSiteKeyRegenerate, Details: map[string]any{"name": site.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"key": token})
}
