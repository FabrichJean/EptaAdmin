package main

import (
	"encoding/json"
	"fmt"
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

	for _, ch := range req.Updates {
		col := cs[ch.Column]
		if ch.Index < 0 || ch.Index >= len(col) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "datasource.unknown_value")})
			return
		}
		v, err := typedValue(ch.Column, ch.Value, ch.Type)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		col[ch.Index] = v
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
		cs[ap.Column] = append(cs[ap.Column], v)
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

	if err := a.store.UpdateDataSourceColumnDescription(ds.ID, key, strings.TrimSpace(req.Description)); err != nil {
		log.Printf("update column error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

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

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
