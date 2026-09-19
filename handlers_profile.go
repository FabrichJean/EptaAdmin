package main

import (
	"log"
	"net/http"
	"strings"
)

// profilePageData builds the common template data for profile.html, reused
// by the initial render and by both form-error redisplays below.
func profilePageData(currentUser *User) map[string]any {
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "",
		"PageTitle":   "Mon profil",
		"HeaderIcon":  "person",
	}
}

func (a *App) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	data := profilePageData(currentUser)
	if updated := r.URL.Query().Get("updated"); updated != "" {
		data["Updated"] = updated
	}
	a.render(w, "profile.html", data)
}

func (a *App) handleUpdateProfileEmail(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	email := strings.TrimSpace(r.FormValue("email"))

	renderError := func(msg string) {
		data := profilePageData(currentUser)
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
		data := profilePageData(currentUser)
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
