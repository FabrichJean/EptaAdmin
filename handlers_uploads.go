package main

import (
	"log"
	"net/http"
	"path/filepath"
)

func (a *App) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	ws, role, ok := a.loadWorkspaceMembership(w, r, currentUser)
	if !ok {
		return
	}
	if !hasPermission(role, PermDataCreate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Accès refusé."})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1<<20) // leave headroom for multipart overhead
	if err := r.ParseMultipartForm(maxUploadSize + 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Fichier trop volumineux ou requête invalide."})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Aucun fichier reçu."})
		return
	}
	defer file.Close()

	filename, err := SaveUploadedImage(ws.ID, header.Filename, file, header.Size)
	if err != nil {
		status := http.StatusBadRequest
		if err != ErrUnsupportedImageType && err != ErrImageTooLarge {
			status = http.StatusInternalServerError
			log.Printf("save uploaded image error: %v", err)
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
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
		http.Error(w, "Accès refusé.", http.StatusForbidden)
		return
	}

	// filepath.Base strips any directory components the client might sneak
	// into the path value, closing off path traversal.
	filename := filepath.Base(r.PathValue("filename"))
	http.ServeFile(w, r, filepath.Join(uploadDir(ws.ID), filename))
}
