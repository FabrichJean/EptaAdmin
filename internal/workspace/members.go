package workspace

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// The members roster page is where an account manages the member accounts
// it has personally created (see store.User.CreatedBy). It is deliberately
// separate from any single workspace: a member you create here can then be
// assigned to (or removed from) any of your workspaces from that
// workspace's own members page — creating an account and granting it
// access to a particular workspace are two different actions.
func membersPageData(a *app.App, lang string, currentUser *store.User) (map[string]any, error) {
	roster, err := a.Store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "members",
		"PageTitle":   i18n.T(lang, "members.page_title"),
		"Members":     roster,
		"HeaderTitle": i18n.T(lang, "members.page_title"),
		"HeaderIcon":  "person",
	}, nil
}

// requireWorkspaceOwner blocks the members roster for anyone who doesn't
// own at least one workspace — someone with nowhere to assign a member has
// no legitimate reason to create platform accounts. Writes the access-denied
// response itself when refusing.
func requireWorkspaceOwner(a *app.App, w http.ResponseWriter, r *http.Request, currentUser *store.User) bool {
	owner, err := a.Store.IsWorkspaceOwner(currentUser.ID)
	if err != nil {
		log.Printf("check workspace owner error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return false
	}
	if !owner {
		http.Error(w, i18n.T(a.ResolveLang(r), "common.access_denied"), http.StatusForbidden)
		return false
	}
	return true
}

func HandleMembersPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	if !requireWorkspaceOwner(a, w, r, currentUser) {
		return
	}
	lang := a.ResolveLang(r)
	data, err := membersPageData(a, lang, currentUser)
	if err != nil {
		log.Printf("members page data error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.Render(w, r, "members.html", data)
}

func HandleCreateMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	if !requireWorkspaceOwner(a, w, r, currentUser) {
		return
	}
	lang := a.ResolveLang(r)

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	renderError := func(msg string) {
		data, err := membersPageData(a, lang, currentUser)
		if err != nil {
			log.Printf("members page data error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data["MemberError"] = msg
		data["Username"] = username
		data["Email"] = email
		a.Render(w, r, "members.html", data)
	}

	if username == "" || email == "" || len(password) < 8 {
		renderError(i18n.T(lang, "common.user_form_validation"))
		return
	}

	hash, err := app.HashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError(i18n.T(lang, "common.error_generic_retry"))
		return
	}

	created, err := a.Store.CreateUser(username, email, hash, roles.RoleViewer, currentUser.ID)
	if err != nil {
		if err == store.ErrUserExists {
			renderError(i18n.T(lang, "common.user_exists"))
		} else {
			log.Printf("create member error: %v", err)
			renderError(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionMemberCreate, Details: map[string]any{"username": created.Username}})

	http.Redirect(w, r, "/members", http.StatusSeeOther)
}

// handleDeleteMember permanently removes an account this caller created —
// never someone else's, and never an account nobody created (i.e. a
// self-registered account, which the caller might otherwise happen upon by
// guessing an ID).
func HandleDeleteMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	if !requireWorkspaceOwner(a, w, r, currentUser) {
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := a.Store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if target == nil || target.CreatedBy != currentUser.ID {
		http.Error(w, i18n.T(a.ResolveLang(r), "common.access_denied"), http.StatusForbidden)
		return
	}
	if err := a.Store.DeleteUser(target.ID); err != nil {
		log.Printf("delete member error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionMemberDelete, Details: map[string]any{"username": target.Username}})

	http.Redirect(w, r, "/members", http.StatusSeeOther)
}
