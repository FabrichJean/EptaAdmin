package app

import (
	"encoding/json"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// LoadWorkspaceMembership resolves the {slug} path value to a workspace and
// the current user's role in it, writing the appropriate error response
// (404 or 403) and returning ok=false when either lookup fails — the shared
// entry point every workspace-scoped handler across every domain package
// starts with.
func (a *App) LoadWorkspaceMembership(w http.ResponseWriter, r *http.Request, currentUser *store.User) (ws *store.Workspace, role string, ok bool) {
	slug := r.PathValue("slug")
	ws, err := a.Store.GetWorkspaceBySlug(slug)
	if err != nil {
		log.Printf("get workspace error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if ws == nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	role, err = a.Store.GetWorkspaceMemberRole(ws.ID, currentUser.ID)
	if err != nil {
		log.Printf("get workspace role error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if role == "" {
		http.Error(w, i18n.T(a.ResolveLang(r), "common.access_denied"), http.StatusForbidden)
		return nil, "", false
	}
	return ws, role, true
}

// reversibleActions is the confirmed scope: real undo for actions on data
// (values, columns, imports); everything else is logged in full detail but
// has no Undo button — either because reversing it doesn't make sense
// (login/logout) or isn't safe/meaningful (password change, API key
// creation, since the plaintext key is never stored to restore). The
// Action* identifiers themselves live in internal/store alongside
// store.ActivityEntry and its Describe() method — every one of them must have a
// matching "activity.<action>" translation key (see Describe) and, for the
// reversible ones, a case in revertActivity below.
var reversibleActions = map[string]bool{
	store.ActionColumnAdd:    true,
	store.ActionColumnUpdate: true,
	store.ActionColumnDelete: true,
	store.ActionValueUpdate:  true,
	store.ActionValueDelete:  true,
	store.ActionValueAppend:  true,
	store.ActionValueMove:    true,
	store.ActionImport:       true,
}

// LogActivityParams is the shared shape every domain handler passes to
// LogActivity to record one audit entry.
type LogActivityParams struct {
	WorkspaceID  int64 // 0 = account-level, not tied to a workspace
	DataSourceID int64 // 0 = not applicable — a data source folder's own creation
	TableID      int64 // 0 = not applicable — every actual data action (columns, values, import)
	UserID       int64
	Action       string
	Details      map[string]any
}

// LogActivity records one entry. It never fails the caller's request: a
// logging error is printed and swallowed, since losing an audit entry is
// far less harmful than failing (or worse, partially rolling back) the
// action that was actually requested.
func (a *App) LogActivity(p LogActivityParams) {
	if p.Details == nil {
		p.Details = map[string]any{}
	}
	data, err := json.Marshal(p.Details)
	if err != nil {
		log.Printf("activity log marshal error: %v", err)
		return
	}
	if err := a.Store.InsertActivity(p.WorkspaceID, p.DataSourceID, p.TableID, p.UserID, p.Action, string(data), reversibleActions[p.Action]); err != nil {
		log.Printf("activity log insert error: %v", err)
	}
	// Every workspace-scoped activity is a "workspace update" for webhook
	// purposes — fireWebhooks itself no-ops when WorkspaceID is 0
	// (account-level actions like login), and delivery runs in its own
	// goroutine so a slow external server never delays this request.
	// store.Webhook management actions are excluded: handleTriggerWebhook already
	// delivers its one webhook synchronously, and broadcasting create/
	// delete/manual-trigger to every OTHER webhook too would be surprising
	// noise rather than a real "workspace update".
	if !strings.HasPrefix(p.Action, "webhook.") && p.Action != store.ActionSiteTrackEvent {
		a.fireWebhooks(p.WorkspaceID, p.Action, p.Details)
	}
}

var errActivityCannotRevert = errors.New("cette action ne peut pas être annulée")
var errActivityAlreadyReverted = errors.New("cette action a déjà été annulée")

// revertActivity performs the inverse of a reversible activity entry,
// directly against the record store / schema — never through HTTP — so it
// can share the exact same on-disk write (store.SaveRecordStore) the original
// action used. Callers are responsible for holding webutil.DataWriteMu and for
// only calling this once per entry (checked again here defensively via
// RevertedAt).
func (a *App) revertActivity(entry *store.ActivityEntry, t *store.Table) error {
	if entry.RevertedAt.Valid {
		return errActivityAlreadyReverted
	}
	if !reversibleActions[entry.Action] {
		return errActivityCannotRevert
	}
	d := entry.DetailsMap()

	switch entry.Action {
	case store.ActionColumnAdd:
		key := store.DetailString(d, "key")
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		records.DeleteColumn(key)
		if err := store.SaveRecordStore(t.StoragePath, records); err != nil {
			return err
		}
		return a.Store.DeleteTableColumn(t.ID, key)

	case store.ActionColumnUpdate:
		key := store.DetailString(d, "key")
		return a.Store.UpdateTableColumnDescription(t.ID, key, store.DetailString(d, "oldDescription"))

	case store.ActionColumnDelete:
		key := store.DetailString(d, "key")
		colType := store.DetailString(d, "type")
		description := store.DetailString(d, "description")
		values, _ := d["values"].([]any)
		if _, err := a.Store.AddTableColumn(t.ID, key, colType, description); err != nil && err != store.ErrTableColumnExists {
			return err
		}
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		records = records.SetColumn(key, values)
		return store.SaveRecordStore(t.StoragePath, records)

	case store.ActionValueUpdate:
		column := store.DetailString(d, "column")
		index := int(d["index"].(float64))
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if err := records.UpdateField(column, index, d["oldValue"]); err != nil {
			return errActivityCannotRevert
		}
		return store.SaveRecordStore(t.StoragePath, records)

	case store.ActionValueDelete:
		column := store.DetailString(d, "column")
		index := int(d["index"].(float64))
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		// The record itself was never removed by a delete (only the field
		// on it), so restoring is just setting that field back in place.
		if err := records.UpdateField(column, index, d["value"]); err != nil {
			return errActivityCannotRevert
		}
		return store.SaveRecordStore(t.StoragePath, records)

	case store.ActionValueAppend:
		column := store.DetailString(d, "column")
		index := int(d["index"].(float64))
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if _, err := records.DeleteField(column, index); err != nil {
			return errActivityCannotRevert
		}
		return store.SaveRecordStore(t.StoragePath, records)

	case store.ActionValueMove:
		from := int(d["from"].(float64))
		to := int(d["to"].(float64))
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		// The whole record that ended up at "to" moves back to "from".
		records, err = records.MoveRecord(to, from)
		if err != nil {
			return errActivityCannotRevert
		}
		return store.SaveRecordStore(t.StoragePath, records)

	case store.ActionImport:
		// Entries logged before this field existed (when imports were
		// tracked per-column instead of by record count) simply can't be
		// reverted anymore — same as any other detail shape a future
		// version might no longer recognize.
		addedCountF, ok := d["addedCount"].(float64)
		if !ok {
			return errActivityCannotRevert
		}
		addedCount := int(addedCountF)
		records, err := store.LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if addedCount < 0 || addedCount > len(records) {
			return errActivityCannotRevert
		}
		records = records[:len(records)-addedCount]
		return store.SaveRecordStore(t.StoragePath, records)

	default:
		return errActivityCannotRevert
	}
}

// activityRow pairs an entry with whether the CURRENT user may revert it —
// computed per-row on the global feed since it spans workspaces the user
// can hold different roles in, unlike the single-workspace page where one
// flag applies to every row.
type activityRow struct {
	*store.ActivityEntry
	CanRevert bool
}

// activityPageSize is how many entries the Activité page shows per page —
// both the global and per-workspace feeds are paginated over their already
// newest-first-sorted, capped fetch rather than ever rendering it all at
// once.
const activityPageSize = 8

// paginateActivity slices rows down to one page, clamping an out-of-range
// page number back into range, and reports the paging metadata the template
// needs to render Prev/Next controls.
func paginateActivity(rows []activityRow, r *http.Request) (page []activityRow, current, total int) {
	total = (len(rows) + activityPageSize - 1) / activityPageSize
	if total == 0 {
		total = 1
	}
	current, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if current < 1 {
		current = 1
	}
	if current > total {
		current = total
	}
	start := (current - 1) * activityPageSize
	end := start + activityPageSize
	if start > len(rows) {
		start = len(rows)
	}
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], current, total
}

// recentActivityForUser returns the newest-first timeline of everything
// this user can see: every workspace-scoped entry across every workspace
// they're a member of, merged with their own account-level entries (login,
// logout, register, profile changes...), which have no workspace at all and
// so would otherwise never show up in a workspace-scoped query. Shared by
// the global Activité page and the header notification dropdown so both
// stay in exact agreement about "what's recent."
func (a *App) recentActivityForUser(userID int64, limit int) ([]*store.ActivityEntry, error) {
	entries, err := a.Store.ListActivityForUserWorkspaces(userID, limit)
	if err != nil {
		return nil, err
	}
	accountEntries, err := a.Store.ListUserActivity(userID, limit)
	if err != nil {
		return nil, err
	}
	entries = append(entries, accountEntries...)

	// Owner/Admin sees activity "without restriction" — including other
	// members' account-level events (login, logout, register...), which
	// have no workspace of their own to be granted visibility through.
	// ListUserActivity above already covers the viewer's own such events
	// regardless of role, so this only adds other people's; dedup by ID
	// below covers the overlap for a viewer who is Owner/Admin somewhere.
	coMemberEntries, err := a.Store.ListCoMemberAccountActivity(userID, limit)
	if err != nil {
		return nil, err
	}
	entries = append(entries, coMemberEntries...)

	seen := make(map[int64]bool, len(entries))
	deduped := entries[:0]
	for _, e := range entries {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		deduped = append(deduped, e)
	}
	entries = deduped

	sort.Slice(entries, func(i, j int) bool { return entries[i].ID > entries[j].ID })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (a *App) HandleGlobalActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)
	lang := a.ResolveLang(r)

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	roleByWorkspace := make(map[int64]string, len(workspaces))
	for _, ws := range workspaces {
		roleByWorkspace[ws.ID] = ws.Role
	}

	entries, err := a.recentActivityForUser(currentUser.ID, 300)
	if err != nil {
		log.Printf("list global activity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	rows := make([]activityRow, 0, len(entries))
	for _, e := range entries {
		canRevert := e.WorkspaceID.Valid && roles.HasPermission(roleByWorkspace[e.WorkspaceID.Int64], roles.PermDataUpdate)
		rows = append(rows, activityRow{ActivityEntry: e, CanRevert: canRevert})
	}
	pageRows, page, totalPages := paginateActivity(rows, r)

	a.Render(w, r, "activity.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "activity",
		"PageTitle":   i18n.T(lang, "activity.title"),
		"Rows":        pageRows,
		"Global":      true,
		"Page":        page,
		"TotalPages":  totalPages,
		"HeaderTitle": i18n.T(lang, "activity.title"),
		"HeaderIcon":  "code",
	})
}

// recentActivityItem is the header notification dropdown's shape — just
// enough to show and link to each entry, not the full store.ActivityEntry (no
// undo button there, so Reversible/RevertedAt etc. would be dead weight).
type recentActivityItem struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
	CreatedAt   string `json:"createdAt"`
	URL         string `json:"url"`
	Unread      bool   `json:"unread"`
}

// handleRecentActivity is the header bell's dropdown data — the same
// merged, newest-first timeline as the global Activité page, just capped
// to 5 and shaped for a compact list instead of a full page.
// recentActivityWindow bounds how far back the unread count looks — an
// account inactive long enough to rack up more unread entries than this
// just sees the count clamp at this number instead of growing forever.
const recentActivityWindow = 300

func (a *App) HandleRecentActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)
	lang := a.ResolveLang(r)

	entries, err := a.recentActivityForUser(currentUser.ID, recentActivityWindow)
	if err != nil {
		log.Printf("list recent activity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	lastSeenID, err := a.Store.GetUserLastSeenActivityID(currentUser.ID)
	if err != nil {
		log.Printf("get last seen activity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	unreadCount := 0
	for _, e := range entries {
		if e.ID > lastSeenID {
			unreadCount++
		}
	}
	if len(entries) > 5 {
		entries = entries[:5]
	}

	items := make([]recentActivityItem, 0, len(entries))
	for _, e := range entries {
		url := "/activity"
		if e.WorkspaceSlug != "" {
			url = "/workspaces/" + e.WorkspaceSlug + "/activity"
		}
		items = append(items, recentActivityItem{
			ID:          e.ID,
			Description: e.Describe(lang),
			CreatedAt:   e.CreatedAt.Local().Format("02/01/2006 15:04"),
			URL:         url,
			Unread:      e.ID > lastSeenID,
		})
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"unreadCount": unreadCount,
	})
}

// HandleMarkActivitySeen advances the current user's read boundary to the
// newest entry they can currently see — called when the notification
// dropdown is opened. Deliberately doesn't trust a client-supplied id:
// re-deriving "the newest one" server-side means a stale or replayed call
// can't mark something unread that a more recent one already covered
// (MarkActivitySeen is itself a monotonic advance-only update too).
func (a *App) HandleMarkActivitySeen(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)

	entries, err := a.recentActivityForUser(currentUser.ID, 1)
	if err != nil {
		log.Printf("list recent activity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if len(entries) > 0 {
		if err := a.Store.MarkActivitySeen(currentUser.ID, entries[0].ID); err != nil {
			log.Printf("mark activity seen error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) HandleWorkspaceActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}

	entries, err := a.Store.ListWorkspaceActivity(ws.ID, 200)
	if err != nil {
		log.Printf("list workspace activity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	// Owner/Admin sees this workspace's activity without restriction —
	// including members' account-level events (login, logout, register...),
	// which have no workspace_id of their own and so are otherwise excluded
	// from ListWorkspaceActivity above entirely.
	if roles.HasPermission(role, roles.PermMembersManage) {
		memberEntries, err := a.Store.ListMemberAccountActivity(ws.ID, 200)
		if err != nil {
			log.Printf("list member account activity error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		entries = append(entries, memberEntries...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID > entries[j].ID })
		if len(entries) > 200 {
			entries = entries[:200]
		}
	}
	canRevert := roles.HasPermission(role, roles.PermDataUpdate)

	tables, err := a.Store.ListTablesByWorkspace(ws.ID)
	if err != nil {
		log.Printf("list tables error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	tableNames := map[int64]string{}
	for _, t := range tables {
		tableNames[t.ID] = t.Name
	}

	// ?table=<slug> narrows the list to one table's entries — filtered
	// here rather than in SQL since the list is already capped to 200 rows.
	if filterSlug := r.URL.Query().Get("table"); filterSlug != "" {
		var filterID int64 = -1
		for _, t := range tables {
			if t.Slug == filterSlug {
				filterID = t.ID
				break
			}
		}
		filtered := entries[:0]
		for _, e := range entries {
			if e.TableID.Valid && e.TableID.Int64 == filterID {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	rows := make([]activityRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, activityRow{ActivityEntry: e, CanRevert: canRevert})
	}
	pageRows, page, totalPages := paginateActivity(rows, r)

	a.Render(w, r, "activity.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "workspaces",
		"PageTitle":   i18n.T(lang, "activity.title"),
		"Workspace":   ws,
		"Rows":        pageRows,
		"Tables":      tables,
		"TableNames":  tableNames,
		"FilterTable": r.URL.Query().Get("table"),
		"Page":        page,
		"TotalPages":  totalPages,
		"Breadcrumb": []Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: i18n.T(lang, "activity.title")},
		},
		"HeaderTitle": i18n.T(lang, "activity.title"),
		"HeaderIcon":  "code",
	})
}

func (a *App) HandleRevertActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	entry, err := a.Store.GetActivity(id)
	if err != nil {
		log.Printf("get activity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if entry == nil || !entry.WorkspaceID.Valid || entry.WorkspaceID.Int64 != ws.ID {
		http.NotFound(w, r)
		return
	}
	if !entry.TableID.Valid {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "activity.cannot_revert")})
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	t, err := a.Store.GetTable(entry.TableID.Int64)
	if err != nil {
		log.Printf("get table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if t == nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "activity.cannot_revert")})
		return
	}

	if err := a.revertActivity(entry, t); err != nil {
		if err == errActivityCannotRevert || err == errActivityAlreadyReverted {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "activity.cannot_revert")})
			return
		}
		log.Printf("revert activity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if _, err := a.Store.BumpTableVersion(t.ID, t.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	if err := a.Store.MarkActivityReverted(entry.ID, currentUser.ID); err != nil {
		log.Printf("mark activity reverted error: %v", err)
	}

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
