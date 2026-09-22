package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// handleLegacyUploadRedirect keeps already-deployed frontend bundles working
// while they still reference the old session-only upload path. Redirecting to
// the signed API URL preserves the public image use case without exposing the
// workspace's session-authenticated browser handler.
func (a *App) handleLegacyUploadRedirect(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	filename := filepath.Base(r.PathValue("filename"))
	if filename == "." || filename == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := a.store.GetWorkspaceBySlug(slug)
	if err != nil {
		log.Printf("legacy upload workspace lookup error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if ws == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(uploadDir(ws.ID), filename)); err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		log.Printf("legacy upload stat error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	signed, err := a.signUploadPath(slug, filename)
	if err != nil {
		log.Printf("legacy upload signing error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}

// uploadErrorMessage translates the sentinel errors SaveUploadedImage and
// SaveAvatarUpload can return; anything else is an internal error that
// shouldn't be echoed to the client verbatim.
func uploadErrorMessage(lang string, err error) string {
	switch err {
	case ErrUnsupportedImageType:
		return T(lang, "upload.unsupported_type")
	case ErrImageTooLarge:
		return T(lang, "upload.too_large")
	case ErrFileTooLarge:
		return T(lang, "upload.file_too_large")
	default:
		return T(lang, "common.error_generic")
	}
}

func (a *App) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1<<20) // leave headroom for multipart overhead
	if err := r.ParseMultipartForm(maxUploadSize + 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "profile.avatar_upload_too_large")})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	if err := EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		if err == ErrWorkspaceStorageLimitExceeded {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	filename, err := SaveUploadedImage(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		status := http.StatusBadRequest
		if err != ErrUnsupportedImageType && err != ErrImageTooLarge {
			status = http.StatusInternalServerError
			log.Printf("save uploaded image error: %v", err)
		}
		writeJSON(w, status, map[string]string{"error": uploadErrorMessage(lang, err)})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionImageUpload, Details: map[string]any{"filename": filename}})

	writeJSON(w, http.StatusOK, map[string]string{"url": uploadURL(ws.Slug, filename)})
}

// handleUploadFile is the "file" column type's counterpart to
// handleUploadImage — same shape, but accepts any file (no extension
// whitelist) up to the larger maxGenericUploadSize.
func (a *App) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": T(lang, "common.access_denied")})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGenericUploadSize+1<<20)
	if err := r.ParseMultipartForm(maxGenericUploadSize + 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "upload.file_too_large")})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	if err := EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		if err == ErrWorkspaceStorageLimitExceeded {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	filename, err := SaveUploadedFile(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		status := http.StatusBadRequest
		if err != ErrFileTooLarge {
			status = http.StatusInternalServerError
			log.Printf("save uploaded file error: %v", err)
		}
		writeJSON(w, status, map[string]string{"error": uploadErrorMessage(lang, err)})
		return
	}
	a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: ActionFileUpload, Details: map[string]any{"filename": header.Filename}})

	writeJSON(w, http.StatusOK, map[string]string{"url": uploadURL(ws.Slug, filename)})
}

func (a *App) handleServeUpload(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataRead) {
		http.Error(w, T(a.resolveLang(r), "common.access_denied"), http.StatusForbidden)
		return
	}

	// filepath.Base strips any directory components the client might sneak
	// into the path value, closing off path traversal.
	filename := filepath.Base(r.PathValue("filename"))
	http.ServeFile(w, r, filepath.Join(uploadDir(ws.ID), filename))
}
