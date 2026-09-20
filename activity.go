package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
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

	ActionWorkspaceCreate = "workspace.create"
	ActionMemberAdd       = "member.add"

	ActionDataSourceCreate = "datasource.create"
	ActionImport           = "datasource.import"
	ActionColumnAdd        = "column.add"
	ActionColumnUpdate     = "column.update"
	ActionColumnDelete     = "column.delete"
	ActionValueUpdate      = "value.update"
	ActionValueDelete      = "value.delete"
	ActionValueAppend      = "value.append"
	ActionValueMove        = "value.move"

	ActionImageUpload = "upload.image"
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
	case ActionDataSourceCreate:
		return T(lang, "activity.desc.datasource.create", actor, detailString(d, "name"))
	case ActionImport:
		return T(lang, "activity.desc.datasource.import", actor, detailString(d, "dataSourceName"))
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
	default:
		return actor + " — " + e.Action
	}
}

type logActivityParams struct {
	WorkspaceID  int64 // 0 = account-level, not tied to a workspace
	DataSourceID int64 // 0 = not applicable
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
	if err := a.store.InsertActivity(p.WorkspaceID, p.DataSourceID, p.UserID, p.Action, string(data), reversibleActions[p.Action]); err != nil {
		log.Printf("activity log insert error: %v", err)
	}
}

var ErrActivityCannotRevert = errors.New("cette action ne peut pas être annulée")
var ErrActivityAlreadyReverted = errors.New("cette action a déjà été annulée")

// revertActivity performs the inverse of a reversible activity entry,
// directly against the column store / schema — never through HTTP — so it
// can share the exact same on-disk write (SaveColumnStore) the original
// action used. Callers are responsible for holding dataWriteMu and for
// only calling this once per entry (checked again here defensively via
// RevertedAt).
func (a *App) revertActivity(entry *ActivityEntry, ds *DataSource) error {
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
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		delete(cs, key)
		if err := SaveColumnStore(ds.StoragePath, cs); err != nil {
			return err
		}
		return a.store.DeleteDataSourceColumn(ds.ID, key)

	case ActionColumnUpdate:
		key := detailString(d, "key")
		return a.store.UpdateDataSourceColumnDescription(ds.ID, key, detailString(d, "oldDescription"))

	case ActionColumnDelete:
		key := detailString(d, "key")
		colType := detailString(d, "type")
		description := detailString(d, "description")
		values, _ := d["values"].([]any)
		if _, err := a.store.AddDataSourceColumn(ds.ID, key, colType, description); err != nil && err != ErrColumnExists {
			return err
		}
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		cs[key] = values
		return SaveColumnStore(ds.StoragePath, cs)

	case ActionValueUpdate:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		col := cs[column]
		if index < 0 || index >= len(col) {
			return ErrActivityCannotRevert
		}
		col[index] = d["oldValue"]
		cs[column] = col
		return SaveColumnStore(ds.StoragePath, cs)

	case ActionValueDelete:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		col := cs[column]
		if index < 0 || index > len(col) {
			index = len(col)
		}
		col = append(col[:index], append([]any{d["value"]}, col[index:]...)...)
		cs[column] = col
		return SaveColumnStore(ds.StoragePath, cs)

	case ActionValueAppend:
		column := detailString(d, "column")
		index := int(d["index"].(float64))
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		col := cs[column]
		if index < 0 || index >= len(col) {
			return ErrActivityCannotRevert
		}
		col = append(col[:index], col[index+1:]...)
		cs[column] = col
		return SaveColumnStore(ds.StoragePath, cs)

	case ActionValueMove:
		column := detailString(d, "column")
		from := int(d["from"].(float64))
		to := int(d["to"].(float64))
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		col := cs[column]
		if to < 0 || to >= len(col) {
			return ErrActivityCannotRevert
		}
		v := col[to]
		col = append(col[:to], col[to+1:]...)
		if from > len(col) {
			from = len(col)
		}
		if from < 0 {
			from = 0
		}
		col = append(col[:from], append([]any{v}, col[from:]...)...)
		cs[column] = col
		return SaveColumnStore(ds.StoragePath, cs)

	case ActionImport:
		added, _ := d["added"].(map[string]any)
		cs, err := LoadColumnStore(ds.StoragePath)
		if err != nil {
			return err
		}
		for column, valuesAny := range added {
			values, _ := valuesAny.([]any)
			col := cs[column]
			n := len(values)
			if n > len(col) {
				n = len(col)
			}
			cs[column] = col[:len(col)-n]
		}
		return SaveColumnStore(ds.StoragePath, cs)

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

	entries, err := a.store.ListActivityForUserWorkspaces(currentUser.ID, 300)
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

	a.render(w, r, "activity.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "activity",
		"PageTitle":        T(lang, "activity.title"),
		"Rows":             rows,
		"Global":           true,
		"CanManageMembers": canManageMembers(currentUser.Role),
		"HeaderTitle":      T(lang, "activity.title"),
		"HeaderIcon":       "code",
	})
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
	canRevert := hasPermission(role, PermDataUpdate)

	dataSources, err := a.store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("list data sources error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dsNames := map[int64]string{}
	for _, ds := range dataSources {
		dsNames[ds.ID] = ds.Name
	}

	// ?ds=<slug> narrows the list to one data source's entries — filtered
	// here rather than in SQL since the list is already capped to 200 rows.
	if filterSlug := r.URL.Query().Get("ds"); filterSlug != "" {
		var filterID int64 = -1
		for _, ds := range dataSources {
			if ds.Slug == filterSlug {
				filterID = ds.ID
				break
			}
		}
		filtered := entries[:0]
		for _, e := range entries {
			if e.DataSourceID.Valid && e.DataSourceID.Int64 == filterID {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	rows := make([]ActivityRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, ActivityRow{ActivityEntry: e, CanRevert: canRevert})
	}

	a.render(w, r, "activity.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "workspaces",
		"PageTitle":        T(lang, "activity.title"),
		"Workspace":        ws,
		"Rows":             rows,
		"DataSources":      dataSources,
		"DSNames":          dsNames,
		"FilterDS":         r.URL.Query().Get("ds"),
		"CanManageMembers": canManageMembers(currentUser.Role),
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
	if !entry.DataSourceID.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	ds, err := a.store.GetDataSource(entry.DataSourceID.Int64)
	if err != nil {
		log.Printf("get data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if ds == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
		return
	}

	if err := a.revertActivity(entry, ds); err != nil {
		if err == ErrActivityCannotRevert || err == ErrActivityAlreadyReverted {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "activity.cannot_revert")})
			return
		}
		log.Printf("revert activity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if _, err := a.store.BumpDataSourceVersion(ds.ID, ds.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	if err := a.store.MarkActivityReverted(entry.ID, currentUser.ID); err != nil {
		log.Printf("mark activity reverted error: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
