package main

import (
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// profilePageData builds the common template data for profile.html, reused
// by the initial render and by every form-error redisplay below — including
// the user's API keys, since that section is always visible on the page.
func (a *App) profilePageData(currentUser *User) map[string]any {
	keys, err := a.store.ListAPIKeys(currentUser.ID)
	if err != nil {
		log.Printf("list api keys error: %v", err)
	}
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "",
		"PageTitle":   "Mon profil",
		"HeaderIcon":  "person",
		"APIKeys":     keys,
	}
}

func (a *App) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	data := a.profilePageData(currentUser)
	if updated := r.URL.Query().Get("updated"); updated != "" {
		data["Updated"] = updated
	}
	if avatarError := r.URL.Query().Get("avatarError"); avatarError != "" {
		data["AvatarError"] = avatarError
	}
	a.render(w, "profile.html", data)
}

func (a *App) handleUpdateProfileEmail(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	email := strings.TrimSpace(r.FormValue("email"))

	renderError := func(msg string) {
		data := a.profilePageData(currentUser)
		data["EmailError"] = msg
		data["Email"] = email
		a.render(w, "profile.html", data)
	}

	if email == "" {
		renderError("Merci de renseigner un email.")
		return
	}

	if err := a.store.UpdateUserEmail(currentUser.ID, email); err != nil {
		if err == ErrEmailExists {
			renderError(err.Error())
		} else {
			log.Printf("update email error: %v", err)
			renderError("Une erreur est survenue, réessayez.")
		}
		return
	}

	http.Redirect(w, r, "/profile?updated=email", http.StatusSeeOther)
}

func (a *App) handleUpdateProfilePassword(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	renderError := func(msg string) {
		data := a.profilePageData(currentUser)
		data["PasswordError"] = msg
		a.render(w, "profile.html", data)
	}

	if !checkPassword(currentUser.PasswordHash, current) {
		renderError("Mot de passe actuel incorrect.")
		return
	}
	if len(next) < 8 {
		renderError("Le nouveau mot de passe doit contenir au moins 8 caractères.")
		return
	}
	if next != confirm {
		renderError("La confirmation ne correspond pas au nouveau mot de passe.")
		return
	}

	hash, err := hashPassword(next)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError("Une erreur est survenue, réessayez.")
		return
	}
	if err := a.store.UpdateUserPassword(currentUser.ID, hash); err != nil {
		log.Printf("update password error: %v", err)
		renderError("Une erreur est survenue, réessayez.")
		return
	}

	http.Redirect(w, r, "/profile?updated=password", http.StatusSeeOther)
}

// handleUpdateProfileAvatarSeed switches the user to a generated avatar
// variant (a gallery of candidate seeds is built client-side in
// profile.html; this just persists the one the user clicked).
func (a *App) handleUpdateProfileAvatarSeed(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	seed := strings.TrimSpace(r.FormValue("seed"))
	if seed == "" {
		http.Error(w, "Graine d'avatar manquante.", http.StatusBadRequest)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.store.UpdateUserAvatarSeed(currentUser.ID, seed); err != nil {
		log.Printf("update avatar seed error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	removeAvatarUpload(oldUpload)

	http.Redirect(w, r, "/profile?updated=avatar", http.StatusSeeOther)
}

// handleUploadProfileAvatar sets a custom uploaded image as the user's
// avatar, replacing (and cleaning up) any previous custom upload.
func (a *App) handleUploadProfileAvatar(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1<<20)
	if err := r.ParseMultipartForm(maxUploadSize + 1<<20); err != nil {
		http.Redirect(w, r, "/profile?avatarError=Fichier+trop+volumineux+ou+requête+invalide.", http.StatusSeeOther)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/profile?avatarError=Aucun+fichier+reçu.", http.StatusSeeOther)
		return
	}
	defer file.Close()

	filename, err := SaveAvatarUpload(header.Filename, file, header.Size)
	if err != nil {
		if err != ErrUnsupportedImageType && err != ErrImageTooLarge {
			log.Printf("save avatar upload error: %v", err)
		}
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.store.UpdateUserAvatarUpload(currentUser.ID, avatarUploadURL(filename)); err != nil {
		log.Printf("update avatar upload error: %v", err)
		removeAvatarUpload(avatarUploadURL(filename))
		http.Redirect(w, r, "/profile?avatarError=Une+erreur+est+survenue,+réessayez.", http.StatusSeeOther)
		return
	}
	removeAvatarUpload(oldUpload)

	http.Redirect(w, r, "/profile?updated=avatar", http.StatusSeeOther)
}

// handleServeAvatar serves a custom avatar upload. Unlike workspace data
// uploads, avatars aren't workspace-scoped — any authenticated user can see
// any other user's avatar, since that's exactly what member lists do.
func (a *App) handleServeAvatar(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(r.PathValue("filename"))
	http.ServeFile(w, r, filepath.Join(avatarUploadDir(), filename))
}

// handleCreateAPIKey generates a new personal API key and renders it
// in-page (never via a redirect query param, which would leak the secret
// into the URL/browser history) so the plaintext value can be shown to the
// user exactly once.
func (a *App) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Sans nom"
	}

	plaintext, _, err := a.store.CreateAPIKey(currentUser.ID, name)
	if err != nil {
		log.Printf("create api key error: %v", err)
		data := a.profilePageData(currentUser)
		data["APIKeyError"] = "Une erreur est survenue, réessayez."
		a.render(w, "profile.html", data)
		return
	}

	data := a.profilePageData(currentUser)
	data["NewAPIKey"] = plaintext
	a.render(w, "profile.html", data)
}

// handleDeleteAPIKey revokes a key. DeleteAPIKey scopes the deletion to the
// current user, so this can't be used to revoke someone else's key even by
// guessing an ID.
func (a *App) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.DeleteAPIKey(id, currentUser.ID); err != nil {
		log.Printf("delete api key error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/profile?updated=apikey", http.StatusSeeOther)
}
