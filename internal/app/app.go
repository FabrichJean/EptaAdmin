package app

import (
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

// App is the shared application kernel: the one Store handle, the parsed
// page templates, and the small pieces of cross-cutting infrastructure
// (sessions, activity logging, webhook delivery) every domain package needs
// but none of them owns. Domain packages (internal/workspace,
// internal/tracking, internal/crm, ...) receive *App as their first
// parameter and call its exported methods — they never construct or embed
// an App themselves.
type App struct {
	Store     *store.Store
	templates map[string]*template.Template
	// WebhookDeploy is a coarse, global "a webhook delivery is happening
	// right now" indicator, polled by every page's layout (see
	// /api/webhook-status) — see webhook_status.go.
	WebhookDeploy webhookDeployStatus
	// PublicURL is this instance's own externally-reachable base URL (e.g.
	// "https://admin.example.com"), used only to build the progressUrl sent
	// to webhook receivers (see webhooks.go) — never required, since a
	// receiver that ignores it still gets delivered to normally. Empty
	// means "unknown", in which case progressUrl is simply omitted.
	PublicURL string
}

// Breadcrumb is one link (or the current page, when URL is empty) in the
// header's breadcrumb trail.
type Breadcrumb struct {
	Label string
	URL   string
}

var templateFuncs = template.FuncMap{
	"formatValue":    store.FormatValue,
	"valueType":      store.ValueType,
	"gridCell":       store.GridCellHTML,
	"jsonAttr":       store.JSONAttr,
	"typeLabel":      store.ColumnTypeLabel,
	"columnIcon":     ColumnIconName,
	"columnColorHex": ColumnColorHex,
	"t":              i18n.T,
	"sub":            func(a, b int) int { return a - b },
	"add1":           func(a int) int { return a + 1 },
	"formatTime":     func(t time.Time) string { return t.Local().Format("02/01/2006 15:04") },
	// safeCSS marks a server-generated CSS value as trusted so html/template
	// doesn't replace it with its "ZgotmplZ" placeholder — only ever used on
	// values this app built itself from fixed formats (e.g. countryFillColor
	// in tracking_stats.go), never on anything a user typed.
	"safeCSS": func(s string) template.CSS { return template.CSS(s) },
}

func New(s *store.Store) (*App, error) {
	a := &App{Store: s, templates: map[string]*template.Template{}}
	pages := []string{"login.html", "register.html", "dashboard.html", "profile.html", "integration.html", "workspaces.html", "workspace_detail.html", "workspace_members.html", "members.html", "datasource_table.html", "activity.html", "settings.html", "webhooks.html", "plugins.html", "tracking_dashboard.html", "visual_dashboard.html", "crm_teams.html", "crm_team_detail.html", "crm_team_members.html", "crm_entity_editor.html"}
	for _, page := range pages {
		tmpl, err := template.New("layout.html").Funcs(templateFuncs).ParseFiles("templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		a.templates[page] = tmpl
	}
	return a, nil
}

func (a *App) Render(w http.ResponseWriter, r *http.Request, page string, data any) {
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
			m["Lang"] = a.ResolveLang(r)
		}
		// The "Membres" nav link and role badge shown next to the username
		// (sidebar footer, header user menu) both depend on the same
		// per-account workspace membership lookup, so it's done once here
		// for every page rather than duplicated in every handler.
		if _, exists := m["CanAccessMembers"]; !exists {
			if u, ok := m["CurrentUser"].(*store.User); ok {
				workspaces, err := a.Store.ListWorkspacesForUser(u.ID)
				if err != nil {
					log.Printf("list workspaces for user error: %v", err)
				} else {
					owner := false
					for _, ws := range workspaces {
						if ws.Role == roles.RoleOwner {
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
		if ws, ok := m["Workspace"].(*store.Workspace); ok {
			if _, exists := m["WorkspaceStorageUsedLabel"]; !exists {
				if usage, err := store.WorkspaceStorageUsage(ws.ID); err != nil {
					log.Printf("workspace storage usage error: %v", err)
				} else {
					limit := store.WorkspaceStorageLimit()
					percent := 0
					if limit > 0 {
						percent = int(usage * 100 / limit)
					}
					if percent > 100 {
						percent = 100
					}
					m["WorkspaceStorageUsedLabel"] = store.FormatBytesHuman(usage)
					m["WorkspaceStorageLimitLabel"] = store.FormatBytesHuman(limit)
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

// AbsoluteURL resolves this instance's own externally-visible origin: the
// configured EPTAADMIN_PUBLIC_URL when set (see webhooks.go's identical
// preference for progressUrl), otherwise derived from the incoming
// request itself. The derived fallback matters for callers that embed this
// URL cross-origin (e.g. the visual SDK's <img src> on a completely
// different domain, see internal/visual) — a bare relative path would
// resolve against the CLIENT site instead of EptaAdmin and break.
func (a *App) AbsoluteURL(r *http.Request) string {
	if a.PublicURL != "" {
		return a.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *App) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	if user := a.UserFromRequest(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.Render(w, r, "login.html", map[string]any{})
}

func (a *App) HandleLogin(w http.ResponseWriter, r *http.Request) {
	lang := a.ResolveLang(r)
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := a.Store.GetUserByUsername(username)
	if err != nil {
		log.Printf("login lookup error: %v", err)
		a.Render(w, r, "login.html", map[string]any{"Error": i18n.T(lang, "common.error_generic_retry")})
		return
	}
	if user == nil || !CheckPassword(user.PasswordHash, password) {
		a.Render(w, r, "login.html", map[string]any{"Error": i18n.T(lang, "login.invalid_credentials")})
		return
	}

	if err := a.StartSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		a.Render(w, r, "login.html", map[string]any{"Error": i18n.T(lang, "common.error_generic_retry")})
		return
	}
	a.LogActivity(LogActivityParams{UserID: user.ID, Action: store.ActionLogin})
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
func (a *App) HandleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if user := a.UserFromRequest(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.Render(w, r, "register.html", map[string]any{})
}

func (a *App) HandleRegister(w http.ResponseWriter, r *http.Request) {
	lang := a.ResolveLang(r)
	count, err := a.Store.CountUsers()
	if err != nil {
		log.Printf("count users error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	role := roles.RoleViewer
	if count == 0 {
		role = roles.RoleOwner
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	formData := map[string]any{"Username": username, "Email": email}

	if username == "" || email == "" || len(password) < 8 {
		formData["Error"] = i18n.T(lang, "common.user_form_validation")
		a.Render(w, r, "register.html", formData)
		return
	}

	hash, err := HashPassword(password)
	if err != nil {
		log.Printf("hash error: %v", err)
		formData["Error"] = i18n.T(lang, "common.error_generic_retry")
		a.Render(w, r, "register.html", formData)
		return
	}

	user, err := a.Store.CreateUser(username, email, hash, role, 0)
	if err != nil {
		if err == store.ErrUserExists {
			formData["Error"] = i18n.T(lang, "common.user_exists")
		} else {
			log.Printf("create user error: %v", err)
			formData["Error"] = i18n.T(lang, "common.error_generic_retry")
		}
		a.Render(w, r, "register.html", formData)
		return
	}
	// Carry over whatever language the visitor had already picked via the
	// pre-login FR/EN toggle, rather than silently reverting to the 'fr'
	// column default the instant the account exists.
	if lang != i18n.DefaultLang {
		if err := a.Store.UpdateUserLanguage(user.ID, lang); err != nil {
			log.Printf("set initial language error: %v", err)
		}
	}

	if err := a.StartSession(w, user.ID); err != nil {
		log.Printf("session error: %v", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.LogActivity(LogActivityParams{UserID: user.ID, Action: store.ActionRegister})

	// Every new account gets its own workspace to administer immediately —
	// this is a self-service multi-tenant platform, not an invite-only org
	// where a freshly registered account would otherwise land with nothing
	// to do. Best-effort: a failure here shouldn't block the account itself
	// from being usable — worst case they create one manually.
	if ws, err := a.Store.CreateWorkspace(username, user.ID); err != nil {
		log.Printf("auto-create personal workspace error: %v", err)
	} else {
		a.LogActivity(LogActivityParams{WorkspaceID: ws.ID, UserID: user.ID, Action: store.ActionWorkspaceCreate, Details: map[string]any{"name": ws.Name}})
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if user := a.UserFromRequest(r); user != nil {
		a.LogActivity(LogActivityParams{UserID: user.ID, Action: store.ActionLogout})
	}
	a.EndSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// HandleDashboard is intentionally the same for every account — there is
// no instance-wide administrator role anymore. Whoever's logged in sees an
// overview of their own workspaces only; managing members happens per
// workspace (see handleAddWorkspaceMember), never across the whole
// instance.
func (a *App) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	currentUser := UserFromContext(r)
	lang := a.ResolveLang(r)

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"CurrentUser":    currentUser,
		"ActiveNav":      "dashboard",
		"PageTitle":      i18n.T(lang, "nav.dashboard"),
		"WorkspaceCount": len(workspaces),
		"Workspaces":     workspaces,
	}
	if len(workspaces) > 0 {
		data["WorkspaceRoleLabel"] = workspaces[0].RoleLabel(lang)
	}

	// Same merged, newest-first timeline as the global Activité page and the
	// header notification bell — just capped to a handful for a compact
	// dashboard widget, not a full page.
	if entries, err := a.recentActivityForUser(currentUser.ID, 6); err != nil {
		log.Printf("dashboard recent activity error: %v", err)
	} else {
		rows := make([]activityRow, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, activityRow{ActivityEntry: e})
		}
		data["RecentActivity"] = rows
	}

	a.Render(w, r, "dashboard.html", data)
}
