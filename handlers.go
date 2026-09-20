package main

import (
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
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
	"t":              T,
	"sub":            func(a, b int) int { return a - b },
	"formatTime":     func(t time.Time) string { return t.Local().Format("02/01/2006 15:04") },
}

func NewApp(store *Store) (*App, error) {
	a := &App{store: store, templates: map[string]*template.Template{}}
	pages := []string{"login.html", "register.html", "dashboard.html", "profile.html", "integration.html", "workspaces.html", "workspace_detail.html", "workspace_members.html", "members.html", "datasource_table.html", "activity.html"}
	for _, page := range pages {
		tmpl, err := template.New("layout.html").Funcs(templateFuncs).ParseFiles("templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		a.templates[page] = tmpl
	}
	return a, nil
}

func (a *App) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	tmpl, ok := a.templates[page]
	if !ok {
		http.Error(w, "template introuvable", http.StatusInternalServerError)
		return
	}
	// Every page's data map gets a "Lang" key so templates can call
	// {{t .Lang "some.key"}} without every handler having to set it
	// itself — resolved once, here, from the account setting or the
	// pre-login cookie fallback.
	if m, ok := data.(map[string]any); ok {
		if _, exists := m["Lang"]; !exists {
			m["Lang"] = a.resolveLang(r)
		}
		// The "Membres" nav link and role badge shown next to the username
		// (sidebar footer, header user menu) both depend on the same
		// per-account workspace membership lookup, so it's done once here
		// for every page rather than duplicated in every handler.
		if _, exists := m["CanAccessMembers"]; !exists {
			if u, ok := m["CurrentUser"].(*User); ok {
				workspaces, err := a.store.ListWorkspacesForUser(u.ID)
				if err != nil {
					log.Printf("list workspaces for user error: %v", err)
				} else {
					owner := false
					for _, ws := range workspaces {
						if ws.Role == RoleOwner {
							owner = true
							break
						}
					}
					m["CanAccessMembers"] = owner
					if len(workspaces) > 0 {
						m["CurrentUserRoleLabel"] = workspaces[0].RoleLabel(m["Lang"].(string))
					}
				}
			}
		}
		// Pages that set "Workspace" (workspace_detail, datasource_table) get
		// a storage gauge for free in the sidebar, computed once here rather
		// than duplicated in every handler that builds one of those pages.
		if ws, ok := m["Workspace"].(*Workspace); ok {
			if _, exists := m["WorkspaceStorageUsedLabel"]; !exists {
				if usage, err := workspaceStorageUsage(ws.ID); err != nil {
					log.Printf("workspace storage usage error: %v", err)
				} else {
					limit := workspaceStorageLimit()
					percent := 0
					if limit > 0 {
						percent = int(usage * 100 / limit)
					}
					if percent > 100 {
						percent = 100
					}
					m["WorkspaceStorageUsedLabel"] = formatBytesHuman(usage)
					m["WorkspaceStorageLimitLabel"] = formatBytesHuman(limit)
					m["WorkspaceStoragePercent"] = percent
				}
			}
		}
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
	a.render(w, r, "login.html", map[string]any{})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	lang := a.resolveLang(r)
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := a.store.GetUserByUsername(username)
	if err != nil {
		log.Printf("login lookup error: %v", err)
		a.render(w, r, "login.html", map[string]any{"Error": T(lang, "common.error_generic_retry")})
		return
	}
	if user == nil || !checkPassword(user.PasswordHash, password) {
		a.render(w, r, "login.html", map[string]any{"Error": T(lang, "login.invalid_credentials")})
		return
	}

	if err := a.startSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		a.render(w, r, "login.html", map[string]any{"Error": T(lang, "common.error_generic_retry")})
		return
	}
	a.logActivity(logActivityParams{UserID: user.ID, Action: ActionLogin})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Public self-registration is open to anyone, at any time — this is a
// multi-user, multi-tenant platform: signing up gives you your own
// workspace to administer, not a seat in someone else's. The very first
// account ever created additionally becomes the *global* Owner (there's
// nobody yet to grant that role, and the instance needs one) — every
// account after that gets the least-privileged global role (Viewer),
// since that role only governs instance-wide things like the global
// members panel, not what you can do inside your own workspace. Either
// way, every new account is immediately made Owner of a freshly created
// personal workspace, exactly as if they'd clicked "+ Nouveau" themselves
// — full data.create/update/delete and members.manage rights over their
// own data from the moment they sign up.
func (a *App) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if user := a.userFromRequest(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "register.html", map[string]any{})
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	lang := a.resolveLang(r)
	count, err := a.store.CountUsers()
	if err != nil {
		log.Printf("count users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	role := RoleViewer
	if count == 0 {
		role = RoleOwner
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	formData := map[string]any{"Username": username, "Email": email}

	if username == "" || email == "" || len(password) < 8 {
		formData["Error"] = T(lang, "common.user_form_validation")
		a.render(w, r, "register.html", formData)
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		formData["Error"] = T(lang, "common.error_generic_retry")
		a.render(w, r, "register.html", formData)
		return
	}

	user, err := a.store.CreateUser(username, email, hash, role, 0)
	if err != nil {
		if err == ErrUserExists {
			formData["Error"] = T(lang, "common.user_exists")
		} else {
			log.Printf("create user error: %v", err)
			formData["Error"] = T(lang, "common.error_generic_retry")
		}
		a.render(w, r, "register.html", formData)
		return
	}
	// Carry over whatever language the visitor had already picked via the
	// pre-login FR/EN toggle, rather than silently reverting to the 'fr'
	// column default the instant the account exists.
	if lang != defaultLang {
		if err := a.store.UpdateUserLanguage(user.ID, lang); err != nil {
			log.Printf("set initial language error: %v", err)
		}
	}

	if err := a.startSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.logActivity(logActivityParams{UserID: user.ID, Action: ActionRegister})

	// Every new account gets its own workspace to administer immediately —
	// this is a self-service multi-tenant platform, not an invite-only org
	// where a freshly registered account would otherwise land with nothing
	// to do. Best-effort: a failure here shouldn't block the account itself
	// from being usable — worst case they create one manually.
	if ws, err := a.store.CreateWorkspace(username, user.ID); err != nil {
		log.Printf("auto-create personal workspace error: %v", err)
	} else {
		a.logActivity(logActivityParams{WorkspaceID: ws.ID, UserID: user.ID, Action: ActionWorkspaceCreate, Details: map[string]any{"name": ws.Name}})
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if user := a.userFromRequest(r); user != nil {
		a.logActivity(logActivityParams{UserID: user.ID, Action: ActionLogout})
	}
	a.endSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleDashboard is intentionally the same for every account — there is
// no instance-wide administrator role anymore. Whoever's logged in sees an
// overview of their own workspaces only; managing members happens per
// workspace (see handleAddWorkspaceMember), never across the whole
// instance.
func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	currentUser := userFromContext(r)
	lang := a.resolveLang(r)

	workspaces, err := a.store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"CurrentUser":    currentUser,
		"ActiveNav":      "dashboard",
		"PageTitle":      T(lang, "nav.dashboard"),
		"WorkspaceCount": len(workspaces),
		"Workspaces":     workspaces,
	}
	if len(workspaces) > 0 {
		data["WorkspaceRoleLabel"] = workspaces[0].RoleLabel(lang)
	}

	a.render(w, r, "dashboard.html", data)
}
