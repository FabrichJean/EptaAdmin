package workspace

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"os"
	"strings"
)

// handleRenameDataSource changes a data source's display name while preserving
// its slug, so table/API addresses and SDK consumers remain stable.
func HandleRenameDataSource(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	newName := strings.TrimSpace(req.Name)
	if newName == "" {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "workspace_detail.datasource_name_required")})
		return
	}
	oldName := ds.Name
	if err := a.Store.RenameDataSource(ds.ID, newName); err != nil {
		if err == store.ErrDataSourceExists {
			webutil.WriteJSON(w, http.StatusConflict, map[string]string{"error": i18n.T(lang, "workspace_detail.datasource_exists")})
			return
		}
		log.Printf("rename data source error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, DataSourceID: ds.ID, UserID: currentUser.ID, Action: store.ActionDataSourceRename, Details: map[string]any{"oldName": oldName, "newName": newName}})
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"name": newName, "slug": ds.Slug})
}

// handleDeleteDataSource removes a data source, all tables below it, their
// schemas, and every JSON storage file. The operation is intentionally
// irreversible and protected by the workspace settings permission.
func HandleDeleteDataSource(a *app.App, w http.ResponseWriter, r *http.Request) {
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
	ds, ok := loadDataSourceInWorkspace(a, w, r, ws)
	if !ok {
		return
	}

	webutil.DataWriteMu.Lock()
	defer webutil.DataWriteMu.Unlock()

	tables, err := a.Store.ListTablesByDataSource(ds.ID)
	if err != nil {
		log.Printf("list data source tables before delete error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if err := a.Store.DeleteDataSource(ds.ID); err != nil {
		log.Printf("delete data source error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
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
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
