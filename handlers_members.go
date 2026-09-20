package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
)

// The members roster page is where an account manages the member accounts
// it has personally created (see User.CreatedBy). It is deliberately
// separate from any single workspace: a member you create here can then be
// assigned to (or removed from) any of your workspaces from that
// workspace's own members page — creating an account and granting it
// access to a particular workspace are two different actions.
func (a *App) membersPageData(lang string, currentUser *User) (map[string]any, error) {
	roster, err := a.store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "members",
		"PageTitle":   T(lang, "members.page_title"),
		"Members":     roster,
		"HeaderTitle": T(lang, "members.page_title"),
		"HeaderIcon":  "person",
	}, nil
}

// requireWorkspaceOwner blocks the members roster for anyone who doesn't
// own at least one workspace — someone with nowhere to assign a member has
// no legitimate reason to create platform accounts. Writes the access-denied
// response itself when refusing.
func (a *App) requireWorkspaceOwner(w http.ResponseWriter, r *http.Request, currentUser *User) bool {
	owner, err := a.store.IsWorkspaceOwner(currentUser.ID)
	if err != nil {
		log.Printf("check workspace owner error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return false
	}
	if !owner {
		http.Error(w, T(a.resolveLang(r), "common.access_denied"), http.StatusForbidden)
		return false
	}
	return true
}

func (a *App) handleMembersPage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	if !a.requireWorkspaceOwner(w, r, currentUser) {
		return
	}
	lang := a.resolveLang(r)
	data, err := a.membersPageData(lang, currentUser)
	if err != nil {
		log.Printf("members page data error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "members.html", data)
}

func (a *App) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	if !a.requireWorkspaceOwner(w, r, currentUser) {
		return
	}
	lang := a.resolveLang(r)

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	renderError := func(msg string) {
		data, err := a.membersPageData(lang, currentUser)
		if err != nil {
			log.Printf("members page data error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data["MemberError"] = msg
		data["Username"] = username
		data["Email"] = email
		a.render(w, r, "members.html", data)
	}

	if username == "" || email == "" || len(password) < 8 {
		renderError(T(lang, "common.user_form_validation"))
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError(T(lang, "common.error_generic_retry"))
		return
	}

	created, err := a.store.CreateUser(username, email, hash, RoleViewer, currentUser.ID)
	if err != nil {
		if err == ErrUserExists {
			renderError(T(lang, "common.user_exists"))
		} else {
			log.Printf("create member error: %v", err)
			renderError(T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionMemberCreate, Details: map[string]any{"username": created.Username}})

	http.Redirect(w, r, "/members", http.StatusSeeOther)
}

// handleDeleteMember permanently removes an account this caller created —
// never someone else's, and never an account nobody created (i.e. a
// self-registered account, which the caller might otherwise happen upon by
// guessing an ID).
func (a *App) handleDeleteMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	if !a.requireWorkspaceOwner(w, r, currentUser) {
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := a.store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if target == nil || target.CreatedBy != currentUser.ID {
		http.Error(w, T(a.resolveLang(r), "common.access_denied"), http.StatusForbidden)
		return
	}
	if err := a.store.DeleteUser(target.ID); err != nil {
		log.Printf("delete member error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.logActivity(logActivityParams{UserID: currentUser.ID, Action: ActionMemberDelete, Details: map[string]any{"username": target.Username}})

	http.Redirect(w, r, "/members", http.StatusSeeOther)
}
