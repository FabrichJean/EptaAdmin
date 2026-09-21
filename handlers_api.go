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

// handleAPIListDataSources lists every table across every data source in
// the workspace, flattened — "datasource" in this public API has always
// meant "the thing with columns and rows", which is now a Table (a
// DataSource is just the admin UI's folder grouping, invisible here).
func (a *App) handleAPIListDataSources(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	tables, err := a.store.ListTablesByWorkspace(ws.ID)
	if err != nil {
		log.Printf("api list tables error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(a.resolveLang(r), "common.error_generic")})
		return
	}
	out := make([]apiDataSource, 0, len(tables))
	for _, t := range tables {
		out = append(out, apiDataSource{Name: t.Name, Slug: t.Slug})
	}
	writeJSON(w, http.StatusOK, out)
}

// apiLoadRecords resolves {dsSlug} to a table (by its workspace-wide
// unique slug — see GetTableByWorkspaceSlug) within an already-loaded
// workspace and reads its record store, writing the appropriate JSON
// error response otherwise. Shared by every endpoint below the
// data-source level.
func (a *App) apiLoadRecords(w http.ResponseWriter, r *http.Request, ws *Workspace) (*Table, RecordStore, bool) {
	lang := a.resolveLang(r)
	t, err := a.store.GetTableByWorkspaceSlug(ws.ID, r.PathValue("dsSlug"))
	if err != nil {
		log.Printf("api get table error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return nil, nil, false
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.datasource_not_found")})
		return nil, nil, false
	}
	records, err := LoadRecordStore(t.StoragePath)
	if err != nil {
		log.Printf("api load record store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "api.datasource_read_error")})
		return nil, nil, false
	}
	return t, records, true
}

// handleAPIGetDataSource returns a table's content projected as
// independent, aligned columns (see RecordStore.ToColumnsMap) — the same
// externally-facing shape the SDK has always exposed, even though the
// server now stores real records on a Table rather than a DataSource.
func (a *App) handleAPIGetDataSource(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	t, records, ok := a.apiLoadRecords(w, r, ws)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    t.Name,
		"slug":    t.Slug,
		"columns": a.signImageValuesInColumnsMap(records.ToColumnsMap()),
	})
}

// handleAPIGetColumn returns one field's full, aligned list of values — the
// building block behind the SDK's single-parameter client.getValue(path),
// for callers who already know exactly which column they want.
func (a *App) handleAPIGetColumn(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	_, records, ok := a.apiLoadRecords(w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !records.HasField(key) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(a.resolveLang(r), "api.column_not_found")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"column": key,
		"values": a.signImageValues(records.Column(key)),
	})
}

// handleAPIGetColumnValue returns exactly one value from one field, by its
// index in that field's aligned list.
func (a *App) handleAPIGetColumnValue(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, ok := a.apiLoadWorkspace(w, r, currentUser)
	if !ok {
		return
	}
	_, records, ok := a.apiLoadRecords(w, r, ws)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if !records.HasField(key) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": T(lang, "api.column_not_found")})
		return
	}
	values := records.Column(key)
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
