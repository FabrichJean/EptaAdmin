package workspace

import (
	"encoding/csv"
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

// loadTableInWorkspace fetches a table by slug, scoped to the given
// workspace, writing a 404 otherwise. store.Table slugs are workspace-unique
// (see uniqueTableSlug), so this never needs to know which data source
// folder the table lives under — the same flat addressing the public API
// has always used.
func loadTableInWorkspace(a *app.App, w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.Table, bool) {
	slug := r.PathValue("tableSlug")
	t, err := a.Store.GetTableByWorkspaceSlug(ws.ID, slug)
	if err != nil {
		log.Printf("get table error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if t == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return t, true
}

// handleCreateTable adds a new table under a data source — the sidebar
// tree's "+" button on a data source node. A table starts with no columns;
// its schema is built up via handleAddTableColumn (or bootstrapped from an
// import).
func HandleCreateTable(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	ds, ok := loadDataSourceInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "sidebar2.table_name_required")})
		return
	}

	t, err := a.Store.CreateTable(ds.ID, ws.ID, name)
	if err != nil {
		if err == store.ErrTableExists {
			webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "sidebar2.table_exists")})
		} else {
			log.Printf("create table error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	if err := store.SaveRecordStore(t.StoragePath, store.RecordStore{}); err != nil {
		log.Printf("init table store error: %v", err)
	}

	webutil.WriteJSON(w, http.StatusOK, map[string]any{"name": t.Name, "slug": t.Slug})
}

// handleRenameTable changes a table's display name only — its slug (and
// therefore every existing SDK/API link and record-store file path) stays
// exactly as it was.
func HandleRenameTable(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "sidebar2.table_name_required")})
		return
	}
	oldName := t.Name

	if err := a.Store.RenameTable(t.ID, newName); err != nil {
		if err == store.ErrTableExists {
			webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "sidebar2.table_exists")})
		} else {
			log.Printf("rename table error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: t.ID, UserID: currentUser.ID, Action: store.ActionTableRename, Details: map[string]any{"oldName": oldName, "newName": newName}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"name": newName})
}

// handleDeleteTable permanently removes a table: its schema, its on-disk
// record store, and (via ON DELETE CASCADE) every activity log entry tied
// to it. There is no undo — same as every other destructive, non-record
// level action in this app (workspaces, members).
func HandleDeleteTable(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataDelete) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	if err := a.Store.DeleteTable(t.ID); err != nil {
		log.Printf("delete table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := os.Remove(t.StoragePath); err != nil && !os.IsNotExist(err) {
		log.Printf("remove table storage file error: %v", err)
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionTableDelete, Details: map[string]any{"name": t.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func HandleTableGrid(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	ds, err := a.Store.GetDataSource(t.DataSourceID)
	if err != nil || ds == nil {
		log.Printf("get parent data source error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	records, err := store.LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		http.Error(w, i18n.T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	schemaCols, err := a.Store.ListTableColumns(t.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	// First time this table is opened with pre-existing values but no
	// declared schema yet (e.g. bootstrapped straight from an import):
	// infer the schema once from the data, then treat the schema as the
	// source of truth from here on.
	if len(schemaCols) == 0 && len(records) > 0 {
		for _, guess := range store.InferSchemaFromColumns(records) {
			if _, err := a.Store.AddTableColumn(t.ID, guess.Key, guess.Type, ""); err != nil && err != store.ErrTableColumnExists {
				log.Printf("bootstrap schema column error: %v", err)
				http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
				return
			}
		}
		schemaCols, err = a.Store.ListTableColumns(t.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
	}

	columns := store.BuildColumns(schemaCols, records)

	members, err := a.Store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	allDataSources, err := a.Store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("list data sources error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSourceTree, err := buildDataSourceTree(a, allDataSources)
	if err != nil {
		log.Printf("build data source tree error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.Render(w, r, "datasource_table.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "workspaces",
		"PageTitle":        t.Name,
		"Workspace":        ws,
		"Table":            t,
		"Columns":          columns,
		"Rows":             store.BuildGridRows(columns),
		"DataSourceTree":   dataSourceTree,
		"SidebarCollapsed": true,
		"ActiveTableID":    t.ID,
		"CanManageSource":  roles.HasPermission(role, roles.PermSettingsManage),
		"CanEdit":          roles.HasPermission(role, roles.PermDataUpdate),
		"CanEditData":      roles.HasPermission(role, roles.PermDataUpdate),
		"CanDeleteData":    roles.HasPermission(role, roles.PermDataDelete),
		"CanDelete":        roles.HasPermission(role, roles.PermDataDelete),
		"CanCreate":        roles.HasPermission(role, roles.PermDataCreate),
		"CanImportData":    roles.HasPermission(role, roles.PermDataCreate),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: ds.Name},
			{Label: t.Name},
		},
		"HeaderTitle":       t.Name,
		"HeaderIcon":        "table",
		"HeaderBadge":       i18n.T(lang, "datasource.active_badge"),
		"HeaderDescription": i18n.T(lang, "datasource.column_count", len(columns), pluralS(len(columns))),
		"MemberCount":       len(members),
	})
}

// handleExportTable downloads a table's current data as either a JSON file
// (the raw record store, byte-for-byte what's on disk — a plain array of
// {field: value} objects) or a CSV file (columns laid out side by side,
// schema columns first in their declared order then any unmanaged keys,
// each row a real record).
func HandleExportTable(a *app.App, w http.ResponseWriter, r *http.Request) {
	lang := a.ResolveLang(r)
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataRead) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	records, err := store.LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		http.Error(w, i18n.T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "csv":
		schemaCols, err := a.Store.ListTableColumns(t.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		exportDataSourceCSV(w, t.Slug, store.BuildColumns(schemaCols, records))
	case "json", "":
		exportDataSourceJSON(w, t.Slug, records)
	default:
		http.Error(w, i18n.T(lang, "datasource.export_unsupported_format"), http.StatusBadRequest)
	}
}

func exportDataSourceJSON(w http.ResponseWriter, slug string, records store.RecordStore) {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("export json marshal error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, slug))
	_, _ = w.Write(data)
}

func exportDataSourceCSV(w http.ResponseWriter, slug string, columns []store.Column) {
	maxRows := 0
	for _, col := range columns {
		if len(col.Values) > maxRows {
			maxRows = len(col.Values)
		}
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, slug))

	cw := csv.NewWriter(w)
	header := make([]string, len(columns))
	for i, col := range columns {
		header[i] = col.Key
	}
	_ = cw.Write(header)

	row := make([]string, len(columns))
	for i := 0; i < maxRows; i++ {
		for c, col := range columns {
			if i < len(col.Values) {
				row[c] = store.FormatValue(col.Values[i])
			} else {
				row[c] = ""
			}
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

// maxImportSize bounds a single import request independently of the
// workspace's overall storage cap (see store.EnsureWorkspaceStorageWithinLimit,
// checked separately below) — this just limits how much one request can
// pull into memory before that check even runs.
const maxImportSize = 20 << 20

// handleImportTable appends an uploaded JSON file's records to a table's
// existing content — the imported records are added after whatever was
// already there, and nothing existing is ever replaced or removed,
// however the uploaded file is shaped (either the row-array shape this app
// now writes, or the legacy independent-columns object shape, both parsed
// the same way — see store.ParseRecordStoreJSON). Any field key present in the
// import that isn't already declared gets auto-registered, the same
// bootstrap handleTableGrid applies to pre-existing data, so imported
// values are immediately editable rather than showing up as read-only.
func HandleImportTable(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataCreate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "datasource.access_denied_create")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize+1<<20)
	if err := r.ParseMultipartForm(maxImportSize + 1<<20); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.import_too_large")})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImportSize+1))
	if err != nil {
		log.Printf("read import file error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if len(data) > maxImportSize {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.import_too_large")})
		return
	}

	imported, err := store.ParseRecordStoreJSON(data)
	if err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.import_invalid_json")})
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	fresh, err := a.Store.GetTable(t.ID)
	if err != nil {
		log.Printf("get table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	records, err := store.LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	// Append rather than assign: every record already present is kept, the
	// imported ones are simply added after them.
	records = append(records, imported...)

	newData, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, fresh.StoragePath, int64(len(newData))); err != nil {
		if err == store.ErrWorkspaceStorageLimitExceeded {
			webutil.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": i18n.T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "datasource.write_error")})
		return
	}

	schemaCols, err := a.Store.ListTableColumns(fresh.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	known := map[string]bool{}
	for _, c := range schemaCols {
		known[c.Key] = true
	}
	for _, guess := range store.InferSchemaFromColumns(imported) {
		if known[guess.Key] {
			continue
		}
		if _, err := a.Store.AddTableColumn(fresh.ID, guess.Key, guess.Type, ""); err != nil && err != store.ErrTableColumnExists {
			log.Printf("bootstrap imported column error: %v", err)
		}
	}

	if _, err := a.Store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: currentUser.ID, Action: store.ActionImport, Details: map[string]any{
		"tableName": fresh.Name, "addedCount": len(imported),
	}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pluralS is language-agnostic on purpose: "column(s)" and "colonne(s)"
// both happen to pluralize with a trailing "s".
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type saveColumnsRequest struct {
	BaseVersion int `json:"baseVersion"`
	Updates     []struct {
		Column string `json:"column"`
		Index  int    `json:"index"`
		Value  string `json:"value"`
		Type   string `json:"type"`
	} `json:"updates"`
	Appends []struct {
		Column string `json:"column"`
		Value  string `json:"value"`
		Type   string `json:"type"`
	} `json:"appends"`
	Deletes []struct {
		Column string `json:"column"`
		Index  int    `json:"index"`
	} `json:"deletes"`
	Moves []struct {
		Column string `json:"column"`
		From   int    `json:"from"`
		To     int    `json:"to"`
	} `json:"moves"`
}

func HandleSaveRecords(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	var req saveColumnsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}

	if len(req.Updates) > 0 && !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "datasource.access_denied_update")})
		return
	}
	if len(req.Deletes) > 0 && !roles.HasPermission(role, roles.PermDataDelete) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "datasource.access_denied_delete")})
		return
	}
	if len(req.Appends) > 0 && !roles.HasPermission(role, roles.PermDataCreate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "datasource.access_denied_create")})
		return
	}
	if len(req.Moves) > 0 && !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "datasource.access_denied_update")})
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	// Re-fetch after acquiring the lock: another request may have bumped
	// the version while we were waiting.
	fresh, err := a.Store.GetTable(t.ID)
	if err != nil {
		log.Printf("get table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if fresh.Version != req.BaseVersion {
		webutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":          i18n.T(lang, "datasource.conflict"),
			"currentVersion": fresh.Version,
		})
		return
	}

	records, err := store.LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	schemaCols, err := a.Store.ListTableColumns(fresh.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	knownColumn := map[string]bool{}
	for _, c := range schemaCols {
		knownColumn[c.Key] = true
	}
	// The type is chosen per value, not per column (a column may freely mix
	// text/number/boolean values) — but the column itself must still be
	// declared first via + Column, so a typo doesn't silently create a new
	// field.
	typedValue := func(column, raw, valueType string) (any, error) {
		if !knownColumn[column] {
			return nil, fmt.Errorf(i18n.T(lang, "datasource.unknown_column"), column)
		}
		return store.CoerceTyped(lang, valueType, raw)
	}

	// Collected as we go and only actually logged after the save below
	// succeeds, so a failed/conflicting request never leaves a phantom
	// activity entry behind.
	type pendingActivity struct {
		action  string
		details map[string]any
	}
	var toLog []pendingActivity

	for _, ch := range req.Updates {
		col := records.Column(ch.Column)
		if ch.Index < 0 || ch.Index >= len(col) {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.unknown_value")})
			return
		}
		oldValue := col[ch.Index]
		v, err := typedValue(ch.Column, ch.Value, ch.Type)
		if err != nil {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		_ = records.UpdateField(ch.Column, ch.Index, v)
		toLog = append(toLog, pendingActivity{store.ActionValueUpdate, map[string]any{"column": ch.Column, "index": ch.Index, "oldValue": oldValue, "newValue": v}})
	}

	// Drag-and-drop reordering: since every field of a record travels
	// together, reordering one field's displayed value now means
	// reordering the whole record it belongs to — every other field's card
	// visibly reorders along with it, which is the correct consequence of
	// index i actually being a shared, aligned position across fields now.
	// Sent as its own isolated request by the client (never combined with
	// updates/deletes/appends in the same call).
	for _, mv := range req.Moves {
		if mv.From < 0 || mv.From >= len(records) {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.unknown_value")})
			return
		}
		records, err = records.MoveRecord(mv.From, mv.To)
		if err != nil {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.unknown_value")})
			return
		}
		toLog = append(toLog, pendingActivity{store.ActionValueMove, map[string]any{"column": mv.Column, "from": mv.From, "to": mv.To}})
	}

	// Group deletes per column and remove highest index first — not to
	// avoid shifting other indices (deleting a field no longer shifts
	// anything: the record stays put, just without that key, so every
	// other field's alignment to it is undisturbed), but so two deletes
	// requested for the very same column+index don't double-log a value
	// that's already gone.
	deletesByColumn := map[string][]int{}
	for _, d := range req.Deletes {
		col := records.Column(d.Column)
		if d.Index < 0 || d.Index >= len(col) {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.unknown_value")})
			return
		}
		deletesByColumn[d.Column] = append(deletesByColumn[d.Column], d.Index)
	}
	for column, indices := range deletesByColumn {
		sort.Sort(sort.Reverse(sort.IntSlice(indices)))
		for _, idx := range indices {
			old, err := records.DeleteField(column, idx)
			if err != nil {
				continue
			}
			toLog = append(toLog, pendingActivity{store.ActionValueDelete, map[string]any{"column": column, "index": idx, "value": old}})
		}
	}

	for _, ap := range req.Appends {
		v, err := typedValue(ap.Column, ap.Value, ap.Type)
		if err != nil {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var index int
		records, index = records.AppendField(ap.Column, v)
		toLog = append(toLog, pendingActivity{store.ActionValueAppend, map[string]any{"column": ap.Column, "index": index, "value": v}})
	}

	newData, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, fresh.StoragePath, int64(len(newData))); err != nil {
		if err == store.ErrWorkspaceStorageLimitExceeded {
			webutil.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": i18n.T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "datasource.write_error")})
		return
	}

	bumped, err := a.Store.BumpTableVersion(fresh.ID, req.BaseVersion)
	if err != nil {
		log.Printf("bump version error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if !bumped {
		// Should not happen under the lock, but guard anyway.
		webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "datasource.conflict")})
		return
	}

	for _, pa := range toLog {
		a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: currentUser.ID, Action: pa.action, Details: pa.details})
	}

	webutil.WriteJSON(w, http.StatusOK, map[string]any{"version": req.BaseVersion + 1})
}

type addColumnRequest struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

func HandleAddTableColumn(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	var req addColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.column_name_required")})
		return
	}

	// Columns no longer carry a single type — each value picks its own
	// (text/long_text/number/boolean) when it's entered. The stored "text"
	// here is just a DB placeholder, unused by validation.
	col, err := a.Store.AddTableColumn(t.ID, key, store.ColumnTypeText, strings.TrimSpace(req.Description))
	if err != nil {
		if err == store.ErrTableColumnExists {
			webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "datasource.column_exists")})
		} else {
			log.Printf("add column error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: t.ID, UserID: currentUser.ID, Action: store.ActionColumnAdd, Details: map[string]any{"key": col.Key, "type": col.Type}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{"key": col.Key, "type": col.Type})
}

type updateColumnRequest struct {
	Description *string `json:"description"`
	Key         *string `json:"key"`
}

// handleUpdateTableColumn updates a column's description and/or renames
// its key. A rename touches both the schema (table_columns.key) and every
// record's data (see store.RecordStore.RenameField) — the two must move
// together, under the same lock as every other record-store write.
func HandleUpdateTableColumn(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")

	var req updateColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.invalid_request")})
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	oldDescription := ""
	if cols, err := a.Store.ListTableColumns(t.ID); err == nil {
		for _, c := range cols {
			if c.Key == key {
				oldDescription = c.Description
				break
			}
		}
	}

	details := map[string]any{"key": key}
	currentKey := key

	if req.Key != nil {
		newKey := strings.TrimSpace(*req.Key)
		if newKey == "" {
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "datasource.column_name_required")})
			return
		}
		if newKey != key {
			fresh, err := a.Store.GetTable(t.ID)
			if err != nil {
				log.Printf("get table error: %v", err)
				webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
				return
			}
			records, err := store.LoadRecordStore(fresh.StoragePath)
			if err != nil {
				log.Printf("load record store error: %v", err)
				webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
				return
			}
			if err := a.Store.RenameTableColumn(t.ID, key, newKey); err != nil {
				if err == store.ErrTableColumnExists {
					webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "datasource.column_exists")})
				} else {
					log.Printf("rename column error: %v", err)
					webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
				}
				return
			}
			records.RenameField(key, newKey)
			if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
				log.Printf("save record store error: %v", err)
				webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "datasource.write_error")})
				return
			}
			details["oldKey"] = key
			details["newKey"] = newKey
			currentKey = newKey
		}
	}

	if req.Description != nil {
		newDescription := strings.TrimSpace(*req.Description)
		if err := a.Store.UpdateTableColumnDescription(t.ID, currentKey, newDescription); err != nil {
			log.Printf("update column error: %v", err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
			return
		}
		details["oldDescription"] = oldDescription
		details["newDescription"] = newDescription
	}

	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: t.ID, UserID: currentUser.ID, Action: store.ActionColumnUpdate, Details: details})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"key": currentKey})
}

func HandleDeleteTableColumn(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataDelete) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	t, ok := loadTableInWorkspace(a, w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	fresh, err := a.Store.GetTable(t.ID)
	if err != nil {
		log.Printf("get table error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	records, err := store.LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	colType, description := store.ColumnTypeText, ""
	if cols, err := a.Store.ListTableColumns(t.ID); err == nil {
		for _, c := range cols {
			if c.Key == key {
				colType, description = c.Type, c.Description
				break
			}
		}
	}
	deletedValues := records.DeleteColumn(key)
	if err := store.SaveRecordStore(fresh.StoragePath, records); err != nil {
		log.Printf("save record store error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "datasource.write_error")})
		return
	}

	if err := a.Store.DeleteTableColumn(t.ID, key); err != nil {
		log.Printf("delete column error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if _, err := a.Store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, TableID: t.ID, UserID: currentUser.ID, Action: store.ActionColumnDelete, Details: map[string]any{
		"key": key, "type": colType, "description": description, "values": deletedValues,
	}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
