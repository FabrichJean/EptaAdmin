package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// dataWriteMu serializes read-modify-write-then-bump-version sequences
// across all tables. A single process-wide lock is enough here since the
// SQLite connection pool is already capped at 1 (see store.go); this just
// extends that same "one writer at a time" guarantee to the on-disk JSON
// file, closing the race between the version check and the write.
var dataWriteMu sync.Mutex

// loadTableInWorkspace fetches a table by slug, scoped to the given
// workspace, writing a 404 otherwise. Table slugs are workspace-unique
// (see uniqueTableSlug), so this never needs to know which data source
// folder the table lives under — the same flat addressing the public API
// has always used.
func (a *App) loadTableInWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) (*Table, bool) {
	slug := r.PathValue("tableSlug")
	t, err := a.store.GetTableByWorkspaceSlug(ws.ID, slug)
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
func (a *App) handleCreateTable(w http.ResponseWriter, r *http.Request) {
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
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "sidebar2.table_name_required")})
		return
	}

	t, err := a.store.CreateTable(ds.ID, ws.ID, name)
	if err != nil {
		if err == ErrTableExists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": T(lang, "sidebar2.table_exists")})
		} else {
			log.Printf("create table error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	if err := SaveRecordStore(t.StoragePath, RecordStore{}); err != nil {
		log.Printf("init table store error: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"name": t.Name, "slug": t.Slug})
}

func (a *App) handleTableGrid(w http.ResponseWriter, r *http.Request) {
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
	t, ok := a.loadTableInWorkspace(w, r, ws)
	if !ok {
		return
	}
	ds, err := a.store.GetDataSource(t.DataSourceID)
	if err != nil || ds == nil {
		log.Printf("get parent data source error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	records, err := LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		http.Error(w, T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	schemaCols, err := a.store.ListTableColumns(t.ID)
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
		for _, guess := range InferSchemaFromColumns(records) {
			if _, err := a.store.AddTableColumn(t.ID, guess.Key, guess.Type, ""); err != nil && err != ErrTableColumnExists {
				log.Printf("bootstrap schema column error: %v", err)
				http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
				return
			}
		}
		schemaCols, err = a.store.ListTableColumns(t.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
	}

	columns := BuildColumns(schemaCols, records)

	members, err := a.store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	allDataSources, err := a.store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("list data sources error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	dataSourceTree, err := a.buildDataSourceTree(allDataSources)
	if err != nil {
		log.Printf("build data source tree error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.render(w, r, "datasource_table.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "workspaces",
		"PageTitle":        t.Name,
		"Workspace":        ws,
		"Table":            t,
		"Columns":          columns,
		"Rows":             BuildGridRows(columns),
		"DataSourceTree":   dataSourceTree,
		"SidebarCollapsed": true,
		"ActiveTableID":    t.ID,
		"CanManageSource":  hasPermission(role, PermSettingsManage),
		"CanEdit":          hasPermission(role, PermDataUpdate),
		"CanEditData":      hasPermission(role, PermDataUpdate),
		"CanDelete":        hasPermission(role, PermDataDelete),
		"CanCreate":        hasPermission(role, PermDataCreate),
		"CanImportData":    hasPermission(role, PermDataCreate),
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: ds.Name},
			{Label: t.Name},
		},
		"HeaderTitle":       t.Name,
		"HeaderIcon":        "table",
		"HeaderBadge":       T(lang, "datasource.active_badge"),
		"HeaderDescription": T(lang, "datasource.column_count", len(columns), pluralS(len(columns))),
		"MemberCount":       len(members),
	})
}

// handleExportTable downloads a table's current data as either a JSON file
// (the raw record store, byte-for-byte what's on disk — a plain array of
// {field: value} objects) or a CSV file (columns laid out side by side,
// schema columns first in their declared order then any unmanaged keys,
// each row a real record).
func (a *App) handleExportTable(w http.ResponseWriter, r *http.Request) {
	lang := a.resolveLang(r)
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataRead) {
		http.Error(w, T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	t, ok := a.loadTableInWorkspace(w, r, ws)
	if !ok {
		return
	}

	records, err := LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		http.Error(w, T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "csv":
		schemaCols, err := a.store.ListTableColumns(t.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		exportDataSourceCSV(w, t.Slug, BuildColumns(schemaCols, records))
	case "json", "":
		exportDataSourceJSON(w, t.Slug, records)
	default:
		http.Error(w, T(lang, "datasource.export_unsupported_format"), http.StatusBadRequest)
	}
}

func exportDataSourceJSON(w http.ResponseWriter, slug string, records RecordStore) {
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

func exportDataSourceCSV(w http.ResponseWriter, slug string, columns []Column) {
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
				row[c] = FormatValue(col.Values[i])
			} else {
				row[c] = ""
			}
		}
		_ = cw.Write(row)
	}
	cw.Flush()
}

// maxImportSize bounds a single import request independently of the
// workspace's overall storage cap (see EnsureWorkspaceStorageWithinLimit,
// checked separately below) — this just limits how much one request can
// pull into memory before that check even runs.
const maxImportSize = 20 << 20

// handleImportTable appends an uploaded JSON file's records to a table's
// existing content — the imported records are added after whatever was
// already there, and nothing existing is ever replaced or removed,
// however the uploaded file is shaped (either the row-array shape this app
// now writes, or the legacy independent-columns object shape, both parsed
// the same way — see ParseRecordStoreJSON). Any field key present in the
// import that isn't already declared gets auto-registered, the same
// bootstrap handleTableGrid applies to pre-existing data, so imported
// values are immediately editable rather than showing up as read-only.
func (a *App) handleImportTable(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "datasource.access_denied_create")})
		return
	}
	t, ok := a.loadTableInWorkspace(w, r, ws)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize+1<<20)
	if err := r.ParseMultipartForm(maxImportSize + 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.import_too_large")})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxImportSize+1))
	if err != nil {
		log.Printf("read import file error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if len(data) > maxImportSize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.import_too_large")})
		return
	}

	imported, err := ParseRecordStoreJSON(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.import_invalid_json")})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetTable(t.ID)
	if err != nil {
		log.Printf("get table error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	records, err := LoadRecordStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load record store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	// Append rather than assign: every record already present is kept, the
	// imported ones are simply added after them.
	records = append(records, imported...)

	newData, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		log.Printf("marshal record store error: %v", err)
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
		log.Printf("save record store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "datasource.write_error")})
		return
	}

	schemaCols, err := a.store.ListTableColumns(fresh.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	known := map[string]bool{}
	for _, c := range schemaCols {
		known[c.Key] = true
	}
	for _, guess := range InferSchemaFromColumns(imported) {
		if known[guess.Key] {
			continue
		}
		if _, err := a.store.AddTableColumn(fresh.ID, guess.Key, guess.Type, ""); err != nil && err != ErrTableColumnExists {
			log.Printf("bootstrap imported column error: %v", err)
		}
	}

	if _, err := a.store.BumpTableVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, TableID: fresh.ID, UserID: currentUser.ID, Action: ActionImport, Details: map[string]any{
