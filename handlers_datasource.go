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
// across all data sources. A single process-wide lock is enough here since
// the SQLite connection pool is already capped at 1 (see store.go); this
// just extends that same "one writer at a time" guarantee to the on-disk
// JSON file, closing the race between the version check and the write.
var dataWriteMu sync.Mutex

// loadDataSourceInWorkspace fetches a data source by slug, scoped to the
// given workspace, writing a 404 otherwise.
func (a *App) loadDataSourceInWorkspace(w http.ResponseWriter, r *http.Request, ws *Workspace) (*DataSource, bool) {
	slug := r.PathValue("dsSlug")
	ds, err := a.store.GetDataSourceBySlug(ws.ID, slug)
	if err != nil {
		log.Printf("get data source error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	if ds == nil {
		http.NotFound(w, r)
		return nil, false
	}
	return ds, true
}

func (a *App) handleDataSourceTable(w http.ResponseWriter, r *http.Request) {
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
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	cs, err := LoadColumnStore(ds.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		http.Error(w, T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	schemaCols, err := a.store.ListDataSourceColumns(ds.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	// First time this data source is opened with pre-existing values but no
	// declared schema yet (e.g. data written before this feature, or a
	// legacy row-based file just migrated by LoadColumnStore): bootstrap
	// the schema once from the data, then treat the schema as the source
	// of truth from here on.
	if len(schemaCols) == 0 && len(cs) > 0 {
		for _, guess := range InferSchemaFromColumns(cs) {
			if _, err := a.store.AddDataSourceColumn(ds.ID, guess.Key, guess.Type, ""); err != nil && err != ErrColumnExists {
				log.Printf("bootstrap schema column error: %v", err)
				http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
				return
			}
		}
		schemaCols, err = a.store.ListDataSourceColumns(ds.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
	}

	columns := BuildColumns(schemaCols, cs)

	members, err := a.store.ListWorkspaceMembers(ws.ID)
	if err != nil {
		log.Printf("list workspace members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.render(w, r, "datasource_table.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "workspaces",
		"PageTitle":        ds.Name,
		"CanManageMembers": canManageMembers(currentUser.Role),
		"Workspace":        ws,
		"DataSource":       ds,
		"Columns":          columns,
		"CanEdit":          hasPermission(role, PermDataUpdate),
		"CanDelete":        hasPermission(role, PermDataDelete),
		"CanCreate":        hasPermission(role, PermDataCreate),
		"Breadcrumb": []Breadcrumb{
			{Label: T(lang, "nav.workspaces"), URL: "/workspaces"},
			{Label: ws.Name, URL: "/workspaces/" + ws.Slug},
			{Label: ds.Name},
		},
		"HeaderTitle":       ds.Name,
		"HeaderIcon":        "database",
		"HeaderBadge":       T(lang, "datasource.active_badge"),
		"HeaderDescription": T(lang, "datasource.column_count", len(columns), pluralS(len(columns))),
		"MemberCount":       len(members),
	})
}

// handleExportDataSource downloads a data source's current data as either a
// JSON file (the raw column store, byte-for-byte what's on disk) or a CSV
// file (columns laid out side by side, schema columns first in their
// declared order then any unmanaged keys, padded with empty cells since
// columns aren't guaranteed to share the same length).
func (a *App) handleExportDataSource(w http.ResponseWriter, r *http.Request) {
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
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	cs, err := LoadColumnStore(ds.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		http.Error(w, T(lang, "datasource.read_error"), http.StatusInternalServerError)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "csv":
		schemaCols, err := a.store.ListDataSourceColumns(ds.ID)
		if err != nil {
			log.Printf("list schema columns error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		exportDataSourceCSV(w, ds.Slug, BuildColumns(schemaCols, cs))
	case "json", "":
		exportDataSourceJSON(w, ds.Slug, cs)
	default:
		http.Error(w, T(lang, "datasource.export_unsupported_format"), http.StatusBadRequest)
	}
}

func exportDataSourceJSON(w http.ResponseWriter, slug string, cs ColumnStore) {
	data, err := json.MarshalIndent(cs, "", "  ")
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

// handleImportDataSource merges an uploaded JSON file's columns into a
// data source's existing content — imported values are appended to each
// column's existing list, and any column already present keeps everything
// it had before. Nothing already stored is ever replaced or removed by an
// import, however the uploaded file is shaped, so it's safe to run against
// a data source that already has columns and data. Any column key present
// in the import that isn't already declared gets auto-registered, the same
// bootstrap handleDataSourceTable applies to pre-existing data, so
// imported values are immediately editable rather than showing up as
// read-only.
func (a *App) handleImportDataSource(w http.ResponseWriter, r *http.Request) {
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
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
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

	imported, err := ParseColumnStoreJSON(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.import_invalid_json")})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetDataSource(ds.ID)
	if err != nil {
		log.Printf("get data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	cs, err := LoadColumnStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	// Append rather than assign: a column already present in cs keeps every
	// value it had, the imported ones are simply added after them.
	for key, values := range imported {
		cs[key] = append(cs[key], values...)
	}

	newData, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		log.Printf("marshal column store error: %v", err)
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

	if err := SaveColumnStore(fresh.StoragePath, cs); err != nil {
		log.Printf("save column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "datasource.write_error")})
		return
	}

	schemaCols, err := a.store.ListDataSourceColumns(fresh.ID)
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
		if _, err := a.store.AddDataSourceColumn(fresh.ID, guess.Key, guess.Type, ""); err != nil && err != ErrColumnExists {
			log.Printf("bootstrap imported column error: %v", err)
		}
	}

	if _, err := a.store.BumpDataSourceVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	addedByColumn := make(map[string]any, len(imported))
	for key, values := range imported {
		addedByColumn[key] = values
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: fresh.ID, UserID: currentUser.ID, Action: ActionImport, Details: map[string]any{
		"dataSourceName": fresh.Name, "added": addedByColumn,
	}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *App) handleSaveRecords(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	var req saveColumnsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}

	if len(req.Updates) > 0 && !hasPermission(role, PermDataUpdate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "datasource.access_denied_update")})
		return
	}
	if len(req.Deletes) > 0 && !hasPermission(role, PermDataDelete) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "datasource.access_denied_delete")})
		return
	}
	if len(req.Appends) > 0 && !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "datasource.access_denied_create")})
		return
	}
	if len(req.Moves) > 0 && !hasPermission(role, PermDataUpdate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "datasource.access_denied_update")})
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	// Re-fetch after acquiring the lock: another request may have bumped
	// the version while we were waiting.
	fresh, err := a.store.GetDataSource(ds.ID)
	if err != nil {
		log.Printf("get data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if fresh.Version != req.BaseVersion {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          T(lang, "datasource.conflict"),
			"currentVersion": fresh.Version,
		})
		return
	}

	cs, err := LoadColumnStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	schemaCols, err := a.store.ListDataSourceColumns(fresh.ID)
	if err != nil {
		log.Printf("list schema columns error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
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
			return nil, fmt.Errorf(T(lang, "datasource.unknown_column"), column)
		}
		return CoerceTyped(lang, valueType, raw)
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
		col := cs[ch.Column]
		if ch.Index < 0 || ch.Index >= len(col) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.unknown_value")})
			return
		}
		oldValue := col[ch.Index]
		v, err := typedValue(ch.Column, ch.Value, ch.Type)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		col[ch.Index] = v
		toLog = append(toLog, pendingActivity{ActionValueUpdate, map[string]any{"column": ch.Column, "index": ch.Index, "oldValue": oldValue, "newValue": v}})
	}

	// Drag-and-drop reordering: pull the value out of its old slot and
	// re-insert it at the new one. Sent as its own isolated request by the
	// client (never combined with updates/deletes/appends in the same
	// call), so there's no index-shifting interaction to reconcile here.
	for _, mv := range req.Moves {
		col := cs[mv.Column]
		if mv.From < 0 || mv.From >= len(col) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.unknown_value")})
			return
		}
		v := col[mv.From]
		col = append(col[:mv.From], col[mv.From+1:]...)
		to := mv.To
		if to < 0 {
			to = 0
		}
		if to > len(col) {
			to = len(col)
		}
		col = append(col[:to], append([]any{v}, col[to:]...)...)
		cs[mv.Column] = col
		toLog = append(toLog, pendingActivity{ActionValueMove, map[string]any{"column": mv.Column, "from": mv.From, "to": mv.To}})
	}

	// Group deletes per column and remove highest index first, so removing
	// one doesn't shift the position of another pending delete in the same
	// column out from under it.
	deletesByColumn := map[string][]int{}
	for _, d := range req.Deletes {
		col := cs[d.Column]
		if d.Index < 0 || d.Index >= len(col) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.unknown_value")})
			return
		}
		deletesByColumn[d.Column] = append(deletesByColumn[d.Column], d.Index)
	}
	for column, indices := range deletesByColumn {
		sort.Sort(sort.Reverse(sort.IntSlice(indices)))
		col := cs[column]
		for _, idx := range indices {
			toLog = append(toLog, pendingActivity{ActionValueDelete, map[string]any{"column": column, "index": idx, "value": col[idx]}})
			col = append(col[:idx], col[idx+1:]...)
		}
		cs[column] = col
	}

	for _, ap := range req.Appends {
		v, err := typedValue(ap.Column, ap.Value, ap.Type)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		index := len(cs[ap.Column])
		cs[ap.Column] = append(cs[ap.Column], v)
		toLog = append(toLog, pendingActivity{ActionValueAppend, map[string]any{"column": ap.Column, "index": index, "value": v}})
	}

	newData, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		log.Printf("marshal column store error: %v", err)
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

	if err := SaveColumnStore(fresh.StoragePath, cs); err != nil {
		log.Printf("save column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "datasource.write_error")})
		return
	}

	bumped, err := a.store.BumpDataSourceVersion(fresh.ID, req.BaseVersion)
	if err != nil {
		log.Printf("bump version error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	if !bumped {
		// Should not happen under the lock, but guard anyway.
		writeJSON(w, http.StatusConflict, map[string]string{"error": T(lang, "datasource.conflict")})
		return
	}

	for _, pa := range toLog {
		a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: fresh.ID, UserID: currentUser.ID, Action: pa.action, Details: pa.details})
	}

	writeJSON(w, http.StatusOK, map[string]any{"version": req.BaseVersion + 1})
}

type addColumnRequest struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

func (a *App) handleAddDataSourceColumn(w http.ResponseWriter, r *http.Request) {
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

	var req addColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.column_name_required")})
		return
	}

	// Columns no longer carry a single type — each value picks its own
	// (text/long_text/number/boolean) when it's entered. The stored "text"
	// here is just a DB placeholder, unused by validation.
	col, err := a.store.AddDataSourceColumn(ds.ID, key, ColumnTypeText, strings.TrimSpace(req.Description))
	if err != nil {
		if err == ErrColumnExists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": T(lang, "datasource.column_exists")})
		} else {
			log.Printf("add column error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: ActionColumnAdd, Details: map[string]any{"key": col.Key, "type": col.Type}})

	writeJSON(w, http.StatusOK, map[string]any{"key": col.Key, "type": col.Type})
}

type updateColumnRequest struct {
	Description string `json:"description"`
}

func (a *App) handleUpdateDataSourceColumn(w http.ResponseWriter, r *http.Request) {
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
	key := r.PathValue("key")

	var req updateColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "common.invalid_request")})
		return
	}
	newDescription := strings.TrimSpace(req.Description)

	oldDescription := ""
	if cols, err := a.store.ListDataSourceColumns(ds.ID); err == nil {
		for _, c := range cols {
			if c.Key == key {
				oldDescription = c.Description
				break
			}
		}
	}

	if err := a.store.UpdateDataSourceColumnDescription(ds.ID, key, newDescription); err != nil {
		log.Printf("update column error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: ActionColumnUpdate, Details: map[string]any{"key": key, "oldDescription": oldDescription, "newDescription": newDescription}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDeleteDataSourceColumn(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataDelete) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	fresh, err := a.store.GetDataSource(ds.ID)
	if err != nil {
		log.Printf("get data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	cs, err := LoadColumnStore(fresh.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	colType, description := ColumnTypeText, ""
	if cols, err := a.store.ListDataSourceColumns(ds.ID); err == nil {
		for _, c := range cols {
			if c.Key == key {
				colType, description = c.Type, c.Description
				break
			}
		}
	}
	deletedValues := cs[key]

	delete(cs, key)
	if err := SaveColumnStore(fresh.StoragePath, cs); err != nil {
		log.Printf("save column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "datasource.write_error")})
		return
	}

	if err := a.store.DeleteDataSourceColumn(ds.ID, key); err != nil {
		log.Printf("delete column error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	if _, err := a.store.BumpDataSourceVersion(fresh.ID, fresh.Version); err != nil {
		log.Printf("bump version error: %v", err)
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: ActionColumnDelete, Details: map[string]any{
		"key": key, "type": colType, "description": description, "values": deletedValues,
	}})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
