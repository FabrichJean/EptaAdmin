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
func (a *App) profilePageData(lang string, currentUser *User) map[string]any {
	keys, err := a.store.ListAPIKeys(currentUser.ID)
	if err != nil {
		log.Printf("list api keys error: %v", err)
	}
	activity, err := a.store.ListUserActivity(currentUser.ID, 50)
	if err != nil {
		log.Printf("list user activity error: %v", err)
	}
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "",
		"PageTitle":   T(lang, "profile.title"),
		"HeaderIcon":  "person",
		"APIKeys":     keys,
		"MyActivity":  activity,
	}
}

func (a *App) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	data := a.profilePageData(a.resolveLang(r), currentUser)
	if updated := r.URL.Query().Get("updated"); updated != "" {
		data["Updated"] = updated
	}
	if avatarError := r.URL.Query().Get("avatarError"); avatarError != "" {
		data["AvatarError"] = avatarError
	}
	a.render(w, r, "profile.html", data)
}

func (a *App) handleUpdateProfileEmail(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	email := strings.TrimSpace(r.FormValue("email"))

	renderError := func(msg string) {
		data := a.profilePageData(lang, currentUser)
		data["EmailError"] = msg
		data["Email"] = email
		a.render(w, r, "profile.html", data)
	}

	if email == "" {
		renderError(T(lang, "profile.email_required"))
		return
	}

	oldEmail := currentUser.Email
	if err := a.store.UpdateUserEmail(currentUser.ID, email); err != nil {
		if err == ErrEmailExists {
			renderError(T(lang, "profile.email_exists"))
		} else {
			log.Printf("update email error: %v", err)
			renderError(T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionEmailChange, Details: map[string]any{"old": oldEmail, "new": email}})

	http.Redirect(w, r, "/profile?updated=email", http.StatusSeeOther)
}

func (a *App) handleUpdateProfilePassword(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	renderError := func(msg string) {
		data := a.profilePageData(lang, currentUser)
		data["PasswordError"] = msg
		a.render(w, r, "profile.html", data)
	}

	if !checkPassword(currentUser.PasswordHash, current) {
		renderError(T(lang, "profile.wrong_current_password"))
		return
	}
	if len(next) < 8 {
		renderError(T(lang, "profile.password_too_short"))
		return
	}
	if next != confirm {
		renderError(T(lang, "profile.password_mismatch"))
		return
	}

	hash, err := hashPassword(next)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError(T(lang, "common.error_generic_retry"))
		return
	}
	if err := a.store.UpdateUserPassword(currentUser.ID, hash); err != nil {
		log.Printf("update password error: %v", err)
		renderError(T(lang, "common.error_generic_retry"))
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionPasswordChange})

	http.Redirect(w, r, "/profile?updated=password", http.StatusSeeOther)
}

// handleUpdateProfileLanguage switches the account's persisted UI
// language. Unlike the pre-login cookie fallback, this follows the user
// across devices since it's stored on the account itself.
func (a *App) handleUpdateProfileLanguage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := strings.TrimSpace(r.FormValue("language"))
	if !supportedLangs[lang] {
		http.Error(w, "Unsupported language.", http.StatusBadRequest)
		return
	}
	oldLang := currentUser.Language
	if err := a.store.UpdateUserLanguage(currentUser.ID, lang); err != nil {
		log.Printf("update language error: %v", err)
		http.Error(w, T(lang, "common.error_generic"), http.StatusInternalServerError)
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionLanguageChange, Details: map[string]any{"old": oldLang, "new": lang}})
	// Switched from the profile page's own dropdown: show the confirmation
	// banner there. Switched from the header (any other page): stay put
	// instead of jumping to /profile.
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Path != "" && ref.Path != "/profile" {
		http.Redirect(w, r, ref.RequestURI(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/profile?updated=language", http.StatusSeeOther)
}

// handleUpdateProfileAvatarSeed switches the user to a generated avatar
// variant (a gallery of candidate seeds is built client-side in
// profile.html; this just persists the one the user clicked).
func (a *App) handleUpdateProfileAvatarSeed(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	seed := strings.TrimSpace(r.FormValue("seed"))
	if seed == "" {
		http.Error(w, T(a.resolveLang(r), "profile.avatar_seed_missing"), http.StatusBadRequest)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.store.UpdateUserAvatarSeed(currentUser.ID, seed); err != nil {
		log.Printf("update avatar seed error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	removeAvatarUpload(oldUpload)
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionAvatarChange})

	http.Redirect(w, r, "/profile?updated=avatar", http.StatusSeeOther)
}

// handleUploadProfileAvatar sets a custom uploaded image as the user's
// avatar, replacing (and cleaning up) any previous custom upload.
func (a *App) handleUploadProfileAvatar(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+1<<20)
	if err := r.ParseMultipartForm(maxUploadSize + 1<<20); err != nil {
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(T(lang, "profile.avatar_upload_too_large")), http.StatusSeeOther)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(T(lang, "profile.avatar_upload_missing")), http.StatusSeeOther)
		return
	}
	defer file.Close()

	filename, err := SaveAvatarUpload(header.Filename, file, header.Size)
	if err != nil {
		if err != ErrUnsupportedImageType && err != ErrImageTooLarge {
			log.Printf("save avatar upload error: %v", err)
		}
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(uploadErrorMessage(lang, err)), http.StatusSeeOther)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.store.UpdateUserAvatarUpload(currentUser.ID, avatarUploadURL(filename)); err != nil {
		log.Printf("update avatar upload error: %v", err)
		removeAvatarUpload(avatarUploadURL(filename))
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(T(lang, "common.error_generic_retry")), http.StatusSeeOther)
		return
	}
	removeAvatarUpload(oldUpload)
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionAvatarChange})

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
	lang := a.resolveLang(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = T(lang, "profile.unnamed_key")
	}

	plaintext, _, err := a.store.CreateAPIKey(currentUser.ID, name)
	if err != nil {
		log.Printf("create api key error: %v", err)
		data := a.profilePageData(lang, currentUser)
		data["APIKeyError"] = T(lang, "common.error_generic_retry")
		a.render(w, r, "profile.html", data)
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionAPIKeyCreate, Details: map[string]any{"name": name}})

	data := a.profilePageData(lang, currentUser)
	data["NewAPIKey"] = plaintext
	a.render(w, r, "profile.html", data)
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
	keyName := ""
	if key, err := a.store.getAPIKeyByID(id); err == nil && key != nil {
		keyName = key.Name
	}
	if err := a.store.DeleteAPIKey(id, currentUser.ID); err != nil {
		log.Printf("delete api key error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionAPIKeyDelete, Details: map[string]any{"name": keyName}})
	http.Redirect(w, r, "/profile?updated=apikey", http.StatusSeeOther)
}
