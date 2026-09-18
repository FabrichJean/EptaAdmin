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
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataRead) {
		http.Error(w, "Accès refusé.", http.StatusForbidden)
		return
	}
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	cs, err := LoadColumnStore(ds.StoragePath)
	if err != nil {
		log.Printf("load column store error: %v", err)
		http.Error(w, "Impossible de lire ce data source.", http.StatusInternalServerError)
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
			if _, err := a.store.AddDataSourceColumn(ds.ID, guess.Key, guess.Type); err != nil && err != ErrColumnExists {
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

	a.render(w, "datasource_table.html", map[string]any{
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
	})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Requête invalide."})
		return
	}

	if len(req.Updates) > 0 && !hasPermission(role, PermDataUpdate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Accès refusé (modification)."})
		return
	}
	if len(req.Deletes) > 0 && !hasPermission(role, PermDataDelete) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Accès refusé (suppression)."})
		return
	}
	if len(req.Appends) > 0 && !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Accès refusé (création)."})
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
			"error":          "Cette donnée a été modifiée par un autre utilisateur.",
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
			return nil, fmt.Errorf("la colonne %q n'existe pas encore — créez-la avec + Column", column)
		}
		return CoerceTyped(valueType, raw)
	}

	for _, ch := range req.Updates {
		col := cs[ch.Column]
		if ch.Index < 0 || ch.Index >= len(col) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Valeur inconnue."})
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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Valeur inconnue."})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Impossible d'écrire le fichier."})
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
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Cette donnée a été modifiée par un autre utilisateur."})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"version": req.BaseVersion + 1})
}

type addColumnRequest struct {
	Key string `json:"key"`
}

func (a *App) handleAddDataSourceColumn(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataUpdate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Accès refusé."})
		return
	}
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	var req addColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Requête invalide."})
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Le nom de la colonne est requis."})
		return
	}

	// Columns no longer carry a single type — each value picks its own
	// (text/long_text/number/boolean) when it's entered. The stored "text"
	// here is just a DB placeholder, unused by validation.
	col, err := a.store.AddDataSourceColumn(ds.ID, key, ColumnTypeText)
	if err != nil {
		if err == ErrColumnExists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		} else {
			log.Printf("add column error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"key": col.Key, "type": col.Type})
}
