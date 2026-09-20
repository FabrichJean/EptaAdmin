package main

import (
	"log"
	"net/http"
	"path/filepath"
)

// uploadErrorMessage translates the sentinel errors SaveUploadedImage and
// SaveAvatarUpload can return; anything else is an internal error that
// shouldn't be echoed to the client verbatim.
func uploadErrorMessage(lang string, err error) string {
	switch err {
	case ErrUnsupportedImageType:
		return T(lang, "upload.unsupported_type")
	case ErrImageTooLarge:
		return T(lang, "upload.too_large")
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
