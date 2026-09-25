package workspace

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// handleLegacyUploadRedirect keeps already-deployed frontend bundles working
// while they still reference the old session-only upload path. Redirecting to
// the signed API URL preserves the public image use case without exposing the
// workspace's session-authenticated browser handler.
func HandleLegacyUploadRedirect(a *app.App, w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	filename := filepath.Base(r.PathValue("filename"))
	if filename == "." || filename == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := a.Store.GetWorkspaceBySlug(slug)
	if err != nil {
		log.Printf("legacy upload workspace lookup error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if ws == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(uploads.UploadDir(ws.ID), filename)); err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		log.Printf("legacy upload stat error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	signed, err := uploads.SignUploadPath(a.Store, slug, filename)
	if err != nil {
		log.Printf("legacy upload signing error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, signed, http.StatusFound)
}

func HandleUploadImage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataCreate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxUploadSize+1<<20) // leave headroom for multipart overhead
	if err := r.ParseMultipartForm(uploads.MaxUploadSize + 1<<20); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "profile.avatar_upload_too_large")})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		if err == store.ErrWorkspaceStorageLimitExceeded {
			webutil.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": i18n.T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	filename, err := uploads.SaveUploadedImage(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		status := http.StatusBadRequest
		if err != uploads.ErrUnsupportedImageType && err != uploads.ErrImageTooLarge {
			status = http.StatusInternalServerError
			log.Printf("save uploaded image error: %v", err)
		}
		webutil.WriteJSON(w, status, map[string]string{"error": uploads.ErrorMessage(lang, err)})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionImageUpload, Details: map[string]any{"filename": filename}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"url": uploads.UploadURL(ws.Slug, filename)})
}

// handleUploadFile is the "file" column type's counterpart to
// handleUploadImage — same shape, but accepts any file (no extension
// whitelist) up to the larger uploads.MaxGenericUploadSize.
func HandleUploadFile(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataCreate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxGenericUploadSize+1<<20)
	if err := r.ParseMultipartForm(uploads.MaxGenericUploadSize + 1<<20); err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "upload.file_too_large")})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "profile.avatar_upload_missing")})
		return
	}
	defer file.Close()

	if err := store.EnsureWorkspaceStorageWithinLimit(ws.ID, "", header.Size); err != nil {
		if err == store.ErrWorkspaceStorageLimitExceeded {
			webutil.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": i18n.T(lang, "datasource.storage_limit_exceeded")})
			return
		}
		log.Printf("check workspace storage limit error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Une erreur est survenue."})
		return
	}

	filename, err := uploads.SaveUploadedFile(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		status := http.StatusBadRequest
		if err != uploads.ErrFileTooLarge {
			status = http.StatusInternalServerError
			log.Printf("save uploaded file error: %v", err)
		}
		webutil.WriteJSON(w, status, map[string]string{"error": uploads.ErrorMessage(lang, err)})
		return
	}
	a.LogActivity(app.LogActivityParams{WorkspaceID: ws.ID, UserID: currentUser.ID, Action: store.ActionFileUpload, Details: map[string]any{"filename": header.Filename}})

	webutil.WriteJSON(w, http.StatusOK, map[string]string{"url": uploads.UploadURL(ws.Slug, filename)})
}

func HandleServeUpload(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	ws, role, ok := a.LoadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataRead) {
		http.Error(w, i18n.T(a.ResolveLang(r), "common.access_denied"), http.StatusForbidden)
		return
	}

	// filepath.Base strips any directory components the client might sneak
	// into the path value, closing off path traversal.
	filename := filepath.Base(r.PathValue("filename"))
	http.ServeFile(w, r, filepath.Join(uploads.UploadDir(ws.ID), filename))
}
