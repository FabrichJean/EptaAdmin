package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
)

type App struct {
	store     *Store
	templates map[string]*template.Template
}

// Breadcrumb is one link (or the current page, when URL is empty) in the
// header's breadcrumb trail.
type Breadcrumb struct {
	Label string
	URL   string
}

var templateFuncs = template.FuncMap{
	"formatValue":    FormatValue,
	"valueType":      ValueType,
	"typeLabel":      ColumnTypeLabel,
	"columnIcon":     ColumnIconName,
	"columnColorHex": ColumnColorHex,
	"avatarURL":      AvatarURL,
	"sub":            func(a, b int) int { return a - b },
}

func NewApp(store *Store) (*App, error) {
	a := &App{store: store, templates: map[string]*template.Template{}}
	pages := []string{"login.html", "register.html", "dashboard.html", "members.html", "profile.html", "workspaces.html", "workspace_detail.html", "datasource_table.html"}
	for _, page := range pages {
		tmpl, err := template.New("layout.html").Funcs(templateFuncs).ParseFiles("templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		a.templates[page] = tmpl
	}
	return a, nil
}

func (a *App) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := a.templates[page]
	if !ok {
		http.Error(w, "template introuvable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, page, data); err != nil {
		log.Printf("render error (%s): %v", page, err)
	}
}

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if user := a.userFromRequest(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, "login.html", map[string]any{})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := a.store.GetUserByUsername(username)
	if err != nil {
		log.Printf("login lookup error: %v", err)
		a.render(w, "login.html", map[string]any{"Error": "Une erreur est survenue, réessayez."})
		return
	}
	if user == nil || !checkPassword(user.PasswordHash, password) {
		a.render(w, "login.html", map[string]any{"Error": ErrInvalidCredentials.Error()})
		return
	}

	if err := a.startSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		a.render(w, "login.html", map[string]any{"Error": "Une erreur est survenue, réessayez."})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Public self-registration only ever creates the very first account (the
// Owner). Once an Owner exists, further members are created from the
// members panel by an Owner or Admin.
func (a *App) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if user := a.userFromRequest(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	count, err := a.store.CountUsers()
	if err != nil {
		log.Printf("count users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.render(w, "register.html", map[string]any{})
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	count, err := a.store.CountUsers()
	if err != nil {
		log.Printf("count users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	formData := map[string]any{"Username": username, "Email": email}

	if username == "" || email == "" || len(password) < 8 {
		formData["Error"] = "Merci de renseigner un identifiant, un email valide et un mot de passe d'au moins 8 caractères."
		a.render(w, "register.html", formData)
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		formData["Error"] = "Une erreur est survenue, réessayez."
		a.render(w, "register.html", formData)
		return
	}

	user, err := a.store.CreateUser(username, email, hash, RoleOwner)
	if err != nil {
		if err == ErrUserExists {
			formData["Error"] = err.Error()
		} else {
			log.Printf("create user error: %v", err)
			formData["Error"] = "Une erreur est survenue, réessayez."
		}
		a.render(w, "register.html", formData)
		return
	}

	if err := a.startSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.endSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	users, err := a.store.ListUsers()
	if err != nil {
		log.Printf("list users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.render(w, "dashboard.html", map[string]any{
		"CurrentUser":      currentUser,
		"Users":            users,
		"CanManageMembers": canManageMembers(currentUser.Role),
		"ActiveNav":        "dashboard",
		"PageTitle":        "Tableau de bord",
	})
}

func (a *App) handleMembersPage(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	if !canManageMembers(currentUser.Role) {
		http.Error(w, "Accès refusé.", http.StatusForbidden)
		return
	}
	users, err := a.store.ListUsers()
	if err != nil {
		log.Printf("list users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.render(w, "members.html", map[string]any{
		"CurrentUser":      currentUser,
		"Users":            users,
		"AssignableRoles":  assignableRolesWithLabels(currentUser.Role),
		"CanManageMembers": true,
		"ActiveNav":        "members",
		"PageTitle":        "Membres",
	})
}

func (a *App) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	if !canManageMembers(currentUser.Role) {
		http.Error(w, "Accès refusé.", http.StatusForbidden)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	role := strings.TrimSpace(r.FormValue("role"))

	renderError := func(msg string) {
		users, err := a.store.ListUsers()
		if err != nil {
			log.Printf("list users error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		a.render(w, "members.html", map[string]any{
			"CurrentUser":      currentUser,
			"Users":            users,
			"AssignableRoles":  assignableRolesWithLabels(currentUser.Role),
			"CanManageMembers": true,
			"ActiveNav":        "members",
			"PageTitle":        "Membres",
			"Error":            msg,
			"Username":         username,
			"Email":            email,
			"Role":             role,
		})
	}

	if username == "" || email == "" || len(password) < 8 {
		renderError("Merci de renseigner un identifiant, un email valide et un mot de passe d'au moins 8 caractères.")
		return
	}
	if !canAssignRole(currentUser.Role, role) {
		renderError("Vous n'êtes pas autorisé à attribuer ce rôle.")
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		renderError("Une erreur est survenue, réessayez.")
		return
	}

	if _, err := a.store.CreateUser(username, email, hash, role); err != nil {
		if err == ErrUserExists {
			renderError(err.Error())
		} else {
			log.Printf("create member error: %v", err)
			renderError("Une erreur est survenue, réessayez.")
		}
		return
	}

	http.Redirect(w, r, "/members", http.StatusSeeOther)
}
