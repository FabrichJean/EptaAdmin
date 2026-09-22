package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Action identifiers stored in activity_log.action. Every one of them must
// have a matching "activity.<action>" translation key in i18n.go (used to
// build the human-readable description) and, for the reversible ones, a
// case in revertActivity below.
const (
	ActionLogin          = "auth.login"
	ActionLogout         = "auth.logout"
	ActionRegister       = "auth.register"
	ActionPasswordChange = "profile.password_change"
	ActionEmailChange    = "profile.email_change"
	ActionLanguageChange = "profile.language_change"
	ActionAvatarChange   = "profile.avatar_change"
	ActionAPIKeyCreate   = "apikey.create"
	ActionAPIKeyDelete   = "apikey.delete"

	ActionWorkspaceCreate  = "workspace.create"
	ActionMemberAdd        = "member.add"
	ActionMemberRemove     = "member.remove"
	ActionMemberRoleChange = "member.role_change"
	ActionMemberCreate     = "member.create"
	ActionMemberDelete     = "member.delete"

	ActionDataSourceCreate = "datasource.create"
	ActionDataSourceRename = "datasource.rename"
	ActionDataSourceDelete = "datasource.delete"
	ActionTableRename      = "table.rename"
	ActionTableDelete      = "table.delete"
	ActionImport           = "datasource.import"
	ActionColumnAdd        = "column.add"
	ActionColumnUpdate     = "column.update"
	ActionColumnDelete     = "column.delete"
	ActionValueUpdate      = "value.update"
	ActionValueDelete      = "value.delete"
	ActionValueAppend      = "value.append"
	ActionValueMove        = "value.move"

	ActionImageUpload = "upload.image"
	ActionFileUpload  = "upload.file"

	ActionWebhookCreate           = "webhook.create"
	ActionWebhookDelete           = "webhook.delete"
	ActionWebhookEnable           = "webhook.enable"
	ActionWebhookDisable          = "webhook.disable"
	ActionWebhookManualTrigger    = "webhook.manual_trigger"
	ActionWebhookAutoTrigger      = "webhook.auto_trigger"
	ActionWebhookURLUpdate        = "webhook.url_update"
	ActionWebhookSecretRegenerate = "webhook.secret_regenerate"

	ActionSiteCreate        = "site.create"
	ActionSiteDelete        = "site.delete"
	ActionSiteKeyRegenerate = "site.key_regenerate"
	// ActionSiteTrackEvent fires once per visitor enter/exit event — high
	// volume by design (see handleTrackCollect), which is why logActivity
	// excludes it from also firing the workspace's outbound webhooks: a
	// webhook is "notify my own server of a workspace change", not "relay
	// every anonymous pageview", and would flood a configured endpoint.
	ActionSiteTrackEvent = "site.track_event"

	ActionVisualSiteCreate        = "visual_site.create"
	ActionVisualSiteDelete        = "visual_site.delete"
	ActionVisualSiteKeyRegenerate = "visual_site.key_regenerate"
	ActionVisualSiteApply         = "visual_site.apply"
)

// reversibleActions is the confirmed scope: real undo for actions on data
// (values, columns, imports); everything else is logged in full detail but
// has no Undo button — either because reversing it doesn't make sense
// (login/logout) or isn't safe/meaningful (password change, API key
// creation, since the plaintext key is never stored to restore).
var reversibleActions = map[string]bool{
	ActionColumnAdd:    true,
	ActionColumnUpdate: true,
	ActionColumnDelete: true,
	ActionValueUpdate:  true,
	ActionValueDelete:  true,
	ActionValueAppend:  true,
	ActionValueMove:    true,
	ActionImport:       true,
}

// ActivityEntry is one row of activity_log, plus the joined username for
// display (the log survives a user's deletion — RevertedByUsername /
// Username fall back to empty rather than failing the join).
type ActivityEntry struct {
	ID             int64
	WorkspaceID    sql.NullInt64
	DataSourceID   sql.NullInt64
	TableID        sql.NullInt64
	UserID         sql.NullInt64
	Username       string
	Action         string
	Details        string
	Reversible     bool
	RevertedAt     sql.NullTime
	RevertedBy     sql.NullInt64
	RevertedByName string
	CreatedAt      time.Time
	WorkspaceName  string
	WorkspaceSlug  string
}

func (e *ActivityEntry) details() map[string]any {
	var d map[string]any
	_ = json.Unmarshal([]byte(e.Details), &d)
	if d == nil {
		d = map[string]any{}
	}
	return d
}

func detailString(d map[string]any, key string) string {
	s, _ := d[key].(string)
	return s
}

// Describe renders a one-line, human-readable sentence for this entry,
// e.g. "Jean a modifié la valeur de nom[2]" — built from a translated
// template plus whatever fields that action's details carry.
func (e *ActivityEntry) Describe(lang string) string {
	d := e.details()
	actor := e.Username
	if actor == "" {
		actor = T(lang, "activity.unknown_user")
	}
	switch e.Action {
	case ActionLogin:
		return T(lang, "activity.desc.auth.login", actor)
	case ActionLogout:
		return T(lang, "activity.desc.auth.logout", actor)
	case ActionRegister:
		return T(lang, "activity.desc.auth.register", actor)
	case ActionPasswordChange:
		return T(lang, "activity.desc.profile.password_change", actor)
	case ActionEmailChange:
		return T(lang, "activity.desc.profile.email_change", actor, detailString(d, "old"), detailString(d, "new"))
	case ActionLanguageChange:
		return T(lang, "activity.desc.profile.language_change", actor, detailString(d, "old"), detailString(d, "new"))
	case ActionAvatarChange:
		return T(lang, "activity.desc.profile.avatar_change", actor)
	case ActionAPIKeyCreate:
		return T(lang, "activity.desc.apikey.create", actor, detailString(d, "name"))
	case ActionAPIKeyDelete:
		return T(lang, "activity.desc.apikey.delete", actor, detailString(d, "name"))
	case ActionWorkspaceCreate:
		return T(lang, "activity.desc.workspace.create", actor, detailString(d, "name"))
	case ActionMemberAdd:
		return T(lang, "activity.desc.member.add", actor, detailString(d, "username"), detailString(d, "role"))
	case ActionMemberRemove:
		return T(lang, "activity.desc.member.remove", actor, detailString(d, "username"))
	case ActionMemberRoleChange:
		return T(lang, "activity.desc.member.role_change", actor, detailString(d, "username"), detailString(d, "role"))
	case ActionMemberCreate:
		return T(lang, "activity.desc.member.create", actor, detailString(d, "username"))
	case ActionMemberDelete:
		return T(lang, "activity.desc.member.delete", actor, detailString(d, "username"))
	case ActionDataSourceCreate:
		return T(lang, "activity.desc.datasource.create", actor, detailString(d, "name"))
	case ActionDataSourceRename:
		return T(lang, "activity.desc.datasource.rename", actor, detailString(d, "oldName"), detailString(d, "newName"))
	case ActionDataSourceDelete:
		return T(lang, "activity.desc.datasource.delete", actor, detailString(d, "name"))
	case ActionTableRename:
		return T(lang, "activity.desc.table.rename", actor, detailString(d, "oldName"), detailString(d, "newName"))
	case ActionTableDelete:
		return T(lang, "activity.desc.table.delete", actor, detailString(d, "name"))
	case ActionImport:
		return T(lang, "activity.desc.datasource.import", actor, detailString(d, "tableName"))
	case ActionColumnAdd:
		return T(lang, "activity.desc.column.add", actor, detailString(d, "key"))
	case ActionColumnUpdate:
		return T(lang, "activity.desc.column.update", actor, detailString(d, "key"))
	case ActionColumnDelete:
		return T(lang, "activity.desc.column.delete", actor, detailString(d, "key"))
	case ActionValueUpdate:
		return T(lang, "activity.desc.value.update", actor, detailString(d, "column"))
	case ActionValueDelete:
		return T(lang, "activity.desc.value.delete", actor, detailString(d, "column"))
	case ActionValueAppend:
		return T(lang, "activity.desc.value.append", actor, detailString(d, "column"))
	case ActionValueMove:
		return T(lang, "activity.desc.value.move", actor, detailString(d, "column"))
	case ActionImageUpload:
		return T(lang, "activity.desc.upload.image", actor)
	case ActionFileUpload:
		return T(lang, "activity.desc.upload.file", actor, detailString(d, "filename"))
	case ActionWebhookCreate:
		return T(lang, "activity.desc.webhook.create", actor, detailString(d, "url"))
	case ActionWebhookDelete:
		return T(lang, "activity.desc.webhook.delete", actor, detailString(d, "url"))
	case ActionWebhookEnable:
		return T(lang, "activity.desc.webhook.enable", actor, detailString(d, "url"))
	case ActionWebhookDisable:
		return T(lang, "activity.desc.webhook.disable", actor, detailString(d, "url"))
	case ActionWebhookManualTrigger:
		return T(lang, "activity.desc.webhook.manual_trigger", actor, detailString(d, "url"))
	case ActionWebhookAutoTrigger:
		url := detailString(d, "url")
		if success, _ := d["success"].(bool); success {
			return T(lang, "activity.desc.webhook.auto_trigger_success", url)
		}
		return T(lang, "activity.desc.webhook.auto_trigger_failed", url, detailString(d, "error"))
	case ActionWebhookURLUpdate:
		return T(lang, "activity.desc.webhook.url_update", actor, detailString(d, "oldUrl"), detailString(d, "newUrl"))
	case ActionWebhookSecretRegenerate:
		return T(lang, "activity.desc.webhook.secret_regenerate", actor, detailString(d, "url"))
	case ActionSiteCreate:
		return T(lang, "activity.desc.site.create", actor, detailString(d, "name"))
	case ActionSiteDelete:
		return T(lang, "activity.desc.site.delete", actor, detailString(d, "name"))
	case ActionSiteKeyRegenerate:
		return T(lang, "activity.desc.site.key_regenerate", actor, detailString(d, "name"))
	case ActionSiteTrackEvent:
		// No actor: this is an anonymous visitor, not a signed-in account —
		// unlike every other entry, the sentence deliberately doesn't start
		// with "%s a...".
		eventType := detailString(d, "eventType")
		if eventType == "exit" {
			return T(lang, "activity.desc.site.track_event_exit", detailString(d, "siteName"), detailString(d, "url"))
		}
		return T(lang, "activity.desc.site.track_event_enter", detailString(d, "siteName"), detailString(d, "url"))
	case ActionVisualSiteCreate:
		return T(lang, "activity.desc.visual_site.create", actor, detailString(d, "name"))
	case ActionVisualSiteDelete:
		return T(lang, "activity.desc.visual_site.delete", actor, detailString(d, "name"))
	case ActionVisualSiteKeyRegenerate:
		return T(lang, "activity.desc.visual_site.key_regenerate", actor, detailString(d, "name"))
	case ActionVisualSiteApply:
		return T(lang, "activity.desc.visual_site.apply", actor, detailString(d, "name"))
	default:
		return actor + " — " + e.Action
	}
}

type logActivityParams struct {
	WorkspaceID  int64 // 0 = account-level, not tied to a workspace
	DataSourceID int64 // 0 = not applicable — a data source folder's own creation
	TableID      int64 // 0 = not applicable — every actual data action (columns, values, import)
	UserID       int64
	Action       string
	Details      map[string]any
}

// logActivity records one entry. It never fails the caller's request: a
// logging error is printed and swallowed, since losing an audit entry is
// far less harmful than failing (or worse, partially rolling back) the
// action that was actually requested.
func (a *App) logActivity(p logActivityParams) {
	if p.Details == nil {
		p.Details = map[string]any{}
	}
	data, err := json.Marshal(p.Details)
	if err != nil {
		log.Printf("activity log marshal error: %v", err)
		return
	}
	if err := a.store.InsertActivity(p.WorkspaceID, p.DataSourceID, p.TableID, p.UserID, p.Action, string(data), reversibleActions[p.Action]); err != nil {
		log.Printf("activity log insert error: %v", err)
	}
	// Every workspace-scoped activity is a "workspace update" for webhook
	// purposes — fireWebhooks itself no-ops when WorkspaceID is 0
	// (account-level actions like login), and delivery runs in its own
	// goroutine so a slow external server never delays this request.
	// Webhook management actions are excluded: handleTriggerWebhook already
	// delivers its one webhook synchronously, and broadcasting create/
	// delete/manual-trigger to every OTHER webhook too would be surprising
	// noise rather than a real "workspace update".
	if !strings.HasPrefix(p.Action, "webhook.") && p.Action != ActionSiteTrackEvent {
		a.fireWebhooks(p.WorkspaceID, p.Action, p.Details)
	}
}

var ErrActivityCannotRevert = errors.New("cette action ne peut pas être annulée")
var ErrActivityAlreadyReverted = errors.New("cette action a déjà été annulée")

// revertActivity performs the inverse of a reversible activity entry,
// directly against the record store / schema — never through HTTP — so it
// can share the exact same on-disk write (SaveRecordStore) the original
// action used. Callers are responsible for holding dataWriteMu and for
// only calling this once per entry (checked again here defensively via
// RevertedAt).
func (a *App) revertActivity(entry *ActivityEntry, t *Table) error {
	if entry.RevertedAt.Valid {
		return ErrActivityAlreadyReverted
	}
	if !reversibleActions[entry.Action] {
		return ErrActivityCannotRevert
	}
	d := entry.details()

	switch entry.Action {
	case ActionColumnAdd:
		key := detailString(d, "key")
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		records.DeleteColumn(key)
		if err := SaveRecordStore(t.StoragePath, records); err != nil {
			return err
		}
		return a.store.DeleteTableColumn(t.ID, key)

	case ActionColumnUpdate:
		key := detailString(d, "key")
		return a.store.UpdateTableColumnDescription(t.ID, key, detailString(d, "oldDescription"))

	case ActionColumnDelete:
		key := detailString(d, "key")
		colType := detailString(d, "type")
		description := detailString(d, "description")
		values, _ := d["values"].([]any)
		if _, err := a.store.AddTableColumn(t.ID, key, colType, description); err != nil && err != ErrTableColumnExists {
			return err
		}
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		records = records.SetColumn(key, values)
		return SaveRecordStore(t.StoragePath, records)

	case ActionValueUpdate:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if err := records.UpdateField(column, index, d["oldValue"]); err != nil {
			return ErrActivityCannotRevert
		}
		return SaveRecordStore(t.StoragePath, records)

	case ActionValueDelete:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		// The record itself was never removed by a delete (only the field
		// on it), so restoring is just setting that field back in place.
		if err := records.UpdateField(column, index, d["value"]); err != nil {
			return ErrActivityCannotRevert
		}
		return SaveRecordStore(t.StoragePath, records)

	case ActionValueAppend:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if _, err := records.DeleteField(column, index); err != nil {
			return ErrActivityCannotRevert
		}
		return SaveRecordStore(t.StoragePath, records)

	case ActionValueMove:
		from := int(d["from"].(float64))
		to := int(d["to"].(float64))
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		// The whole record that ended up at "to" moves back to "from".
		records, err = records.MoveRecord(to, from)
		if err != nil {
			return ErrActivityCannotRevert
		}
		return SaveRecordStore(t.StoragePath, records)

	case ActionImport:
		// Entries logged before this field existed (when imports were
		// tracked per-column instead of by record count) simply can't be
		// reverted anymore — same as any other detail shape a future
		// version might no longer recognize.
		addedCountF, ok := d["addedCount"].(float64)
		if !ok {
			return ErrActivityCannotRevert
		}
		addedCount := int(addedCountF)
		records, err := LoadRecordStore(t.StoragePath)
		if err != nil {
			return err
		}
		if addedCount < 0 || addedCount > len(records) {
			return ErrActivityCannotRevert
		}
		records = records[:len(records)-addedCount]
		return SaveRecordStore(t.StoragePath, records)

	default:
		return ErrActivityCannotRevert
	}
}

// ActivityRow pairs an entry with whether the CURRENT user may revert it —
// computed per-row on the global feed since it spans workspaces the user
// can hold different roles in, unlike the single-workspace page where one
// flag applies to every row.
type ActivityRow struct {
	*ActivityEntry
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
func paginateActivity(rows []ActivityRow, r *http.Request) (page []ActivityRow, current, total int) {
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
func (a *App) recentActivityForUser(userID int64, limit int) ([]*ActivityEntry, error) {
	entries, err := a.store.ListActivityForUserWorkspaces(userID, limit)
	if err != nil {
		return nil, err
	}
	accountEntries, err := a.store.ListUserActivity(userID, limit)
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
	coMemberEntries, err := a.store.ListCoMemberAccountActivity(userID, limit)
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

func (a *App) handleGlobalActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
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

	rows := make([]ActivityRow, 0, len(entries))
	for _, e := range entries {
		canRevert := e.WorkspaceID.Valid && hasPermission(roleByWorkspace[e.WorkspaceID.Int64], PermDataUpdate)
		rows = append(rows, ActivityRow{ActivityEntry: e, CanRevert: canRevert})
	}
	pageRows, page, totalPages := paginateActivity(rows, r)

	a.render(w, r, "activity.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "activity",
		"PageTitle":   T(lang, "activity.title"),
		"Rows":        pageRows,
		"Global":      true,
		"Page":        page,
		"TotalPages":  totalPages,
		"HeaderTitle": T(lang, "activity.title"),
		"HeaderIcon":  "code",
	})
}

// recentActivityItem is the header notification dropdown's shape — just
// enough to show and link to each entry, not the full ActivityEntry (no
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

func (a *App) handleRecentActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	entries, err := a.recentActivityForUser(currentUser.ID, recentActivityWindow)
	if err != nil {
		log.Printf("list recent activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	lastSeenID, err := a.store.GetUserLastSeenActivityID(currentUser.ID)
	if err != nil {
		log.Printf("get last seen activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
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
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"unreadCount": unreadCount,
	})
}

// handleMarkActivitySeen advances the current user's read boundary to the
// newest entry they can currently see — called when the notification
// dropdown is opened. Deliberately doesn't trust a client-supplied id:
// re-deriving "the newest one" server-side means a stale or replayed call
// can't mark something unread that a more recent one already covered
// (MarkActivitySeen is itself a monotonic advance-only update too).
func (a *App) handleMarkActivitySeen(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)

	entries, err := a.recentActivityForUser(currentUser.ID, 1)
	if err != nil {
		log.Printf("list recent activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if len(entries) > 0 {
		if err := a.store.MarkActivitySeen(currentUser.ID, entries[0].ID); err != nil {
			log.Printf("mark activity seen error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleWorkspaceActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}

	entries, err := a.store.ListWorkspaceActivity(ws.ID, 200)
	if err != nil {
		log.Printf("list workspace activity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	// Owner/Admin sees this workspace's activity without restriction —
	// including members' account-level events (login, logout, register...),
	// which have no workspace_id of their own and so are otherwise excluded
	// from ListWorkspaceActivity above entirely.
	if hasPermission(role, PermMembersManage) {
		memberEntries, err := a.store.ListMemberAccountActivity(ws.ID, 200)
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
	canRevert := hasPermission(role, PermDataUpdate)

	tables, err := a.store.ListTablesByWorkspace(ws.ID)
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

	rows := make([]ActivityRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, ActivityRow{ActivityEntry: e, CanRevert: canRevert})
	}
	pageRows, page, totalPages := paginateActivity(rows, r)

	a.render(w, r, "activity.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "workspaces",
		"PageTitle":   T(lang, "activity.title"),
		"Workspace":   ws,
		"Rows":        pageRows,
		"Tables":      tables,
		"TableNames":  tableNames,
		"FilterTable": r.URL.Query().Get("table"),
		"Page":        page,
		"TotalPages":  totalPages,
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: T(lang, "activity.title")},
		},
		"HeaderTitle": T(lang, "activity.title"),
		"HeaderIcon":  "code",
	})
}

func (a *App) handleRevertActivity(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataUpdate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	entry, err := a.store.GetActivity(id)
	if err != nil {
		log.Printf("get activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if entry == nil || !entry.WorkspaceID.Valid || entry.WorkspaceID.Int64 != ws.ID {
		http.NotFound(w, r)
		return
	}
	if !entry.TableID.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	t, err := a.store.GetTable(entry.TableID.Int64)
	if err != nil {
		log.Printf("get table error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
		return
	}

	if err := a.revertActivity(entry, t); err != nil {
		if err == ErrActivityCannotRevert || err == ErrActivityAlreadyReverted {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
			return
		}
		log.Printf("revert activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if _, err := a.store.BumpTableVersion(t.ID, t.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	if err := a.store.MarkActivityReverted(entry.ID, currentUser.ID); err != nil {
		log.Printf("mark activity reverted error: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
