package profile

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
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
func profilePageData(a *app.App, lang string, currentUser *store.User) map[string]any {
	keys, err := a.Store.ListAPIKeys(currentUser.ID)
	if err != nil {
		log.Printf("list api keys error: %v", err)
	}
	activity, err := a.Store.ListUserActivity(currentUser.ID, 50)
	if err != nil {
		log.Printf("list user activity error: %v", err)
	}
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "",
		"PageTitle":   i18n.T(lang, "profile.title"),
		"HeaderIcon":  "person",
		"APIKeys":     keys,
		"MyActivity":  activity,
	}
}

func HandleProfilePage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	data := profilePageData(a, a.ResolveLang(r), currentUser)
	if updated := r.URL.Query().Get("updated"); updated != "" {
		data["Updated"] = updated
	}
	if avatarError := r.URL.Query().Get("avatarError"); avatarError != "" {
		data["AvatarError"] = avatarError
	}
	a.Render(w, r, "profile.html", data)
}

func HandleUpdateProfileEmail(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	email := strings.TrimSpace(r.FormValue("email"))

	renderError := func(msg string) {
		data := profilePageData(a, lang, currentUser)
		data["EmailError"] = msg
		data["Email"] = email
		a.Render(w, r, "profile.html", data)
	}

	if email == "" {
		renderError(i18n.T(lang, "profile.email_required"))
		return
	}

	oldEmail := currentUser.Email
	if err := a.Store.UpdateUserEmail(currentUser.ID, email); err != nil {
		if err == store.ErrEmailExists {
			renderError(i18n.T(lang, "profile.email_exists"))
		} else {
			log.Printf("update email error: %v", err)
			renderError(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionEmailChange, Details: map[string]any{"old": oldEmail, "new": email}})

	http.Redirect(w, r, "/profile?updated=email", http.StatusSeeOther)
}

func HandleUpdateProfilePassword(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	renderError := func(msg string) {
		data := profilePageData(a, lang, currentUser)
		data["PasswordError"] = msg
		a.Render(w, r, "profile.html", data)
	}

	if !app.CheckPassword(currentUser.PasswordHash, current) {
		renderError(i18n.T(lang, "profile.wrong_current_password"))
		return
	}
	if len(next) < 8 {
		renderError(i18n.T(lang, "profile.password_too_short"))
		return
	}
	if next != confirm {
		renderError(i18n.T(lang, "profile.password_mismatch"))
		return
	}

	hash, err := app.HashPassword(next)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError(i18n.T(lang, "common.error_generic_retry"))
		return
	}
	if err := a.Store.UpdateUserPassword(currentUser.ID, hash); err != nil {
		log.Printf("update password error: %v", err)
		renderError(i18n.T(lang, "common.error_generic_retry"))
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionPasswordChange})

	http.Redirect(w, r, "/profile?updated=password", http.StatusSeeOther)
}

// handleUpdateProfileLanguage switches the account's persisted UI
// language. Unlike the pre-login cookie fallback, this follows the user
// across devices since it's stored on the account itself.
func HandleUpdateProfileLanguage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := strings.TrimSpace(r.FormValue("language"))
	if !i18n.SupportedLangs[lang] {
		http.Error(w, "Unsupported language.", http.StatusBadRequest)
		return
	}
	oldLang := currentUser.Language
	if err := a.Store.UpdateUserLanguage(currentUser.ID, lang); err != nil {
		log.Printf("update language error: %v", err)
		http.Error(w, i18n.T(lang, "common.error_generic"), http.StatusInternalServerError)
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionLanguageChange, Details: map[string]any{"old": oldLang, "new": lang}})
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
func HandleUpdateProfileAvatarSeed(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	seed := strings.TrimSpace(r.FormValue("seed"))
	if seed == "" {
		http.Error(w, i18n.T(a.ResolveLang(r), "profile.avatar_seed_missing"), http.StatusBadRequest)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.Store.UpdateUserAvatarSeed(currentUser.ID, seed); err != nil {
		log.Printf("update avatar seed error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	uploads.RemoveAvatarUpload(oldUpload)
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionAvatarChange})

	http.Redirect(w, r, "/profile?updated=avatar", http.StatusSeeOther)
}

// handleUploadProfileAvatar sets a custom uploaded image as the user's
// avatar, replacing (and cleaning up) any previous custom upload.
func HandleUploadProfileAvatar(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)

	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxUploadSize+1<<20)
	if err := r.ParseMultipartForm(uploads.MaxUploadSize + 1<<20); err != nil {
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(i18n.T(lang, "profile.avatar_upload_too_large")), http.StatusSeeOther)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(i18n.T(lang, "profile.avatar_upload_missing")), http.StatusSeeOther)
		return
	}
	defer file.Close()

	filename, err := uploads.SaveAvatarUpload(header.Filename, file, header.Size)
	if err != nil {
		if err != uploads.ErrUnsupportedImageType && err != uploads.ErrImageTooLarge {
			log.Printf("save avatar upload error: %v", err)
		}
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(uploads.ErrorMessage(lang, err)), http.StatusSeeOther)
		return
	}

	oldUpload := currentUser.AvatarUpload
	if err := a.Store.UpdateUserAvatarUpload(currentUser.ID, uploads.AvatarUploadURL(filename)); err != nil {
		log.Printf("update avatar upload error: %v", err)
		uploads.RemoveAvatarUpload(uploads.AvatarUploadURL(filename))
		http.Redirect(w, r, "/profile?avatarError="+url.QueryEscape(i18n.T(lang, "common.error_generic_retry")), http.StatusSeeOther)
		return
	}
	uploads.RemoveAvatarUpload(oldUpload)
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionAvatarChange})

	http.Redirect(w, r, "/profile?updated=avatar", http.StatusSeeOther)
}

// handleServeAvatar serves a custom avatar upload. Unlike workspace data
// uploads, avatars aren't workspace-scoped — any authenticated user can see
// any other user's avatar, since that's exactly what member lists do.
func HandleServeAvatar(a *app.App, w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(r.PathValue("filename"))
	http.ServeFile(w, r, filepath.Join(uploads.AvatarUploadDir(), filename))
}

// handleCreateAPIKey generates a new personal API key and renders it
// in-page (never via a redirect query param, which would leak the secret
// into the URL/browser history) so the plaintext value can be shown to the
// user exactly once.
func HandleCreateAPIKey(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = i18n.T(lang, "profile.unnamed_key")
	}

	plaintext, _, err := a.Store.CreateAPIKey(currentUser.ID, name)
	if err != nil {
		log.Printf("create api key error: %v", err)
		data := profilePageData(a, lang, currentUser)
		data["APIKeyError"] = i18n.T(lang, "common.error_generic_retry")
		a.Render(w, r, "profile.html", data)
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionAPIKeyCreate, Details: map[string]any{"name": name}})

	data := profilePageData(a, lang, currentUser)
	data["NewAPIKey"] = plaintext
	a.Render(w, r, "profile.html", data)
}

// handleDeleteAPIKey revokes a key. DeleteAPIKey scopes the deletion to the
// current user, so this can't be used to revoke someone else's key even by
// guessing an ID.
func HandleDeleteAPIKey(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	keyName := ""
	if key, err := a.Store.GetAPIKeyByID(id); err == nil && key != nil {
		keyName = key.Name
	}
	if err := a.Store.DeleteAPIKey(id, currentUser.ID); err != nil {
		log.Printf("delete api key error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionAPIKeyDelete, Details: map[string]any{"name": keyName}})
	http.Redirect(w, r, "/profile?updated=apikey", http.StatusSeeOther)
}
