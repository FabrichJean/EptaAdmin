package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

// handleRenameDataSource changes a data source's display name while preserving
// its slug, so table/API addresses and SDK consumers remain stable.
func (a *App) handleRenameDataSource(w http.ResponseWriter, r *http.Request) {
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
	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "workspace_detail.datasource_name_required")})
		return
	}
	oldName := ds.Name
	if err := a.store.RenameDataSource(ds.ID, newName); err != nil {
		if err == ErrDataSourceExists {
			writeJSON(w, http.StatusConflict, map[string]string{"error": T(lang, "workspace_detail.datasource_exists")})
			return
		}
		log.Printf("rename data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: ActionDataSourceRename, Details: map[string]any{"oldName": oldName, "newName": newName}})
	writeJSON(w, http.StatusOK, map[string]string{"name": newName, "slug": ds.Slug})
}

// handleDeleteDataSource removes a data source, all tables below it, their
// schemas, and every JSON storage file. The operation is intentionally
// irreversible and protected by the workspace settings permission.
func (a *App) handleDeleteDataSource(w http.ResponseWriter, r *http.Request) {
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
	ds, ok := a.loadDataSourceInWorkspace(w, r, ws)
	if !ok {
		return
	}

	dataWriteMu.Lock()
	defer dataWriteMu.Unlock()

	tables, err := a.store.ListTablesByDataSource(ds.ID)
	if err != nil {
		log.Printf("list data source tables before delete error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return
	}
	if err := a.store.DeleteDataSource(ds.ID); err != nil {
		log.Printf("delete data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return
	}
	for _, table := range tables {
		if err := os.Remove(table.StoragePath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove table storage file error: %v", err)
		}
	}
	if err := os.Remove(ds.StoragePath); err != nil && !os.IsNotExist(err) {
		log.Printf("remove data source storage file error: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
