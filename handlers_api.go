package main

import (
	"log"
	"net/http"
	"path/filepath"
	"strconv"
)

// This file is the public, read-only REST API (auth via requireAPIKey,
// never a session cookie) that the JS SDK and external projects consume to
// read a workspace's data. It mirrors the internal handlers' permission
// checks but always responds JSON, never HTML. Error messages are
// localized via the API key owner's saved language preference (the same
// resolveLang used everywhere else), since a personal API key always
// resolves to a *User with a Language field.

type apiWorkspace struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

func (a *App) handleAPIListWorkspaces(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("api list workspaces error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(a.resolveLang(r), "common.error_generic")})
		return
	}
	out := make([]apiWorkspace, 0, len(workspaces))
	for _, ws := range workspaces {
		out = append(out, apiWorkspace{Name: ws.Name, Slug: ws.Slug, Role: ws.Role})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiLoadWorkspace resolves the {slug} path value to a workspace the caller
// can read, writing the appropriate JSON error response otherwise.
func (a *App) apiLoadWorkspace(w http.ResponseWriter, r *http.Request, currentUser *User) (*Workspace, bool) {
	lang := a.resolveLang(r)
	ws, err := a.store.GetWorkspaceBySlug(r.PathValue("slug"))
	if err != nil {
		log.Printf("api get workspace error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return nil, false
	}
	if ws == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.workspace_not_found")})
		return nil, false
	}
	role, err := a.store.GetWorkspaceMemberRole(ws.ID, currentUser.ID)
	if err != nil {
		log.Printf("api get role error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return nil, false
	}
	if role == "" || !hasPermission(role, PermDataRead) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return nil, false
	}
	return ws, true
}

type apiDataSource struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (a *App) handleAPIListDataSources(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	dataSources, err := a.store.ListDataSources(ws.ID)
	if err != nil {
		log.Printf("api list data sources error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(a.resolveLang(r), "common.error_generic")})
		return
	}
	out := make([]apiDataSource, 0, len(dataSources))
	for _, ds := range dataSources {
		out = append(out, apiDataSource{Name: ds.Name, Slug: ds.Slug})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiLoadColumnStore resolves {dsSlug} within an already-loaded workspace
// and reads its column store, writing the appropriate JSON error response
// otherwise. Shared by every endpoint below the data-source level.
func (a *App) apiLoadColumnStore(w http.ResponseWriter, r *http.Request, ws *Workspace) (*DataSource, ColumnStore, bool) {
	lang := a.resolveLang(r)
	ds, err := a.store.GetDataSourceBySlug(ws.ID, r.PathValue("dsSlug"))
	if err != nil {
		log.Printf("api get data source error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return nil, nil, false
	}
	if ds == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.datasource_not_found")})
		return nil, nil, false
	}
	cs, err := LoadColumnStore(ds.StoragePath)
	if err != nil {
		log.Printf("api load column store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "api.datasource_read_error")})
		return nil, nil, false
	}
	return ds, cs, true
}

// handleAPIGetDataSource returns a data source's raw column store — each
// column is its own independent list of values (see jsondata.go), so the
// response is exactly that shape rather than a fabricated row/object list.
func (a *App) handleAPIGetDataSource(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	ds, cs, ok := a.apiLoadColumnStore(w, r, ws)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    ds.Name,
		"slug":    ds.Slug,
		"columns": a.signImageValuesInColumnStore(cs),
	})
}

// handleAPIGetColumn returns one column's full list of values — the
// building block behind the SDK's single-parameter client.getValue(path),
// for callers who already know exactly which column they want.
func (a *App) handleAPIGetColumn(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	_, cs, ok := a.apiLoadColumnStore(w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")
	values, exists := cs[key]
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(a.resolveLang(r), "api.column_not_found")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"column": key,
		"values": a.signImageValues(values),
	})
}

// handleAPIGetColumnValue returns exactly one value from one column, by its
// index in that column's independent list.
func (a *App) handleAPIGetColumnValue(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	_, cs, ok := a.apiLoadColumnStore(w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")
	values, exists := cs[key]
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.column_not_found")})
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(values) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.index_out_of_range")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"column": key,
		"index":  index,
		"value":  a.signImageValue(values[index]),
	})
}

// handleAPIServeUpload serves an uploaded image (a "type.image" column
// value). It is deliberately NOT behind the requireAPIKey middleware other
// /api/v1 routes use — image values returned by getDataSource/getValue are
// pre-signed (see upload_signing.go) precisely so they can be dropped into
// an <img>/<video> src, which can never carry a bearer key. A request
// presenting a valid "?sig=" is authorized by that signature alone — it
// never expires (see upload_signing.go); anything else falls back to
// requiring a personal API key exactly
// as before (Authorization header or "?apiKey="), for direct programmatic
// access with your own key.
func (a *App) handleAPIServeUpload(w http.ResponseWriter, r *http.Request) {
	lang := a.resolveLang(r)
	slug := r.PathValue("slug")
	// filepath.Base strips any directory components the client might sneak
	// into the path value, closing off path traversal.
	filename := filepath.Base(r.PathValue("filename"))

	validSig, err := a.verifyUploadSignature(slug, filename, r.URL.Query().Get("sig"))
	if err != nil {
		log.Printf("verify upload signature error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return
	}
	if validSig {
		ws, err := a.store.GetWorkspaceBySlug(slug)
		if err != nil {
			log.Printf("api get workspace error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
			return
		}
		if ws == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.workspace_not_found")})
			return
		}
		http.ServeFile(w, r, filepath.Join(uploadDir(ws.ID), filename))
		return
	}

	currentUser, ok := a.authenticateAPIKeyRequest(w, r)
	if !ok {
		return
	}
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	http.ServeFile(w, r, filepath.Join(uploadDir(ws.ID), filename))
}
