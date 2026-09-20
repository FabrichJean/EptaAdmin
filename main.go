package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	dbPath := os.Getenv("EPTAADMIN_DB")
	if dbPath == "" {
		dbPath = "eptaadmin.db"
	}

	store, err := NewStore(dbPath)
	if err != nil {
		log.Fatalf("impossible d'initialiser la base de données: %v", err)
	}

	app, err := NewApp(store)
	if err != nil {
		log.Fatalf("impossible de charger les templates: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /login", app.handleLoginPage)
	mux.HandleFunc("POST /login", app.handleLogin)
	mux.HandleFunc("GET /register", app.handleRegisterPage)
	mux.HandleFunc("POST /register", app.handleRegister)
	mux.HandleFunc("POST /logout", app.handleLogout)
	mux.HandleFunc("GET /{$}", app.requireAuth(app.handleDashboard))
	mux.HandleFunc("GET /integration", app.requireAuth(app.handleIntegrationPage))
	mux.HandleFunc("GET /profile", app.requireAuth(app.handleProfilePage))
	mux.HandleFunc("POST /profile/email", app.requireAuth(app.handleUpdateProfileEmail))
	mux.HandleFunc("POST /profile/password", app.requireAuth(app.handleUpdateProfilePassword))
	mux.HandleFunc("POST /profile/avatar/seed", app.requireAuth(app.handleUpdateProfileAvatarSeed))
	mux.HandleFunc("POST /profile/avatar/upload", app.requireAuth(app.handleUploadProfileAvatar))
	mux.HandleFunc("GET /avatars/{filename}", app.requireAuth(app.handleServeAvatar))
	mux.HandleFunc("POST /profile/api-keys", app.requireAuth(app.handleCreateAPIKey))
	mux.HandleFunc("POST /profile/api-keys/{id}/delete", app.requireAuth(app.handleDeleteAPIKey))
	mux.HandleFunc("POST /profile/language", app.requireAuth(app.handleUpdateProfileLanguage))
	mux.HandleFunc("GET /lang/{lang}", app.handleSetLangCookie)

	// Public read-only API (personal API key auth) — consumed by the JS SDK.
	mux.HandleFunc("GET /api/v1/workspaces", app.requireAPIKey(app.handleAPIListWorkspaces))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources", app.requireAPIKey(app.handleAPIListDataSources))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}", app.requireAPIKey(app.handleAPIGetDataSource))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}/columns/{key}", app.requireAPIKey(app.handleAPIGetColumn))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}/columns/{key}/{index}", app.requireAPIKey(app.handleAPIGetColumnValue))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/uploads/{filename}", app.requireAPIKey(app.handleAPIServeUpload))
	mux.HandleFunc("GET /workspaces", app.requireAuth(app.handleWorkspacesPage))
	mux.HandleFunc("GET /activity", app.requireAuth(app.handleGlobalActivity))
	mux.HandleFunc("GET /api/search", app.requireAuth(app.handleGlobalSearch))
	mux.HandleFunc("POST /workspaces", app.requireAuth(app.handleCreateWorkspace))
	mux.HandleFunc("GET /workspaces/{slug}", app.requireAuth(app.handleWorkspaceDetail))
	mux.HandleFunc("GET /workspaces/{slug}/activity", app.requireAuth(app.handleWorkspaceActivity))
	mux.HandleFunc("POST /workspaces/{slug}/activity/{id}/revert", app.requireAuth(app.handleRevertActivity))
	mux.HandleFunc("GET /members", app.requireAuth(app.handleMembersPage))
	mux.HandleFunc("POST /members", app.requireAuth(app.handleCreateMember))
	mux.HandleFunc("POST /members/{userID}/delete", app.requireAuth(app.handleDeleteMember))
	mux.HandleFunc("GET /workspaces/{slug}/members", app.requireAuth(app.handleWorkspaceMembersPage))
	mux.HandleFunc("POST /workspaces/{slug}/members", app.requireAuth(app.handleAddWorkspaceMember))
	mux.HandleFunc("POST /workspaces/{slug}/members/{userID}/remove", app.requireAuth(app.handleRemoveWorkspaceMember))
	mux.HandleFunc("POST /workspaces/{slug}/members/{userID}/role", app.requireAuth(app.handleUpdateWorkspaceMemberRole))
	mux.HandleFunc("POST /workspaces/{slug}/datasources", app.requireAuth(app.handleCreateDataSource))
	mux.HandleFunc("GET /workspaces/{slug}/datasources/{dsSlug}", app.requireAuth(app.handleDataSourceTable))
	mux.HandleFunc("GET /workspaces/{slug}/datasources/{dsSlug}/export", app.requireAuth(app.handleExportDataSource))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/import", app.requireAuth(app.handleImportDataSource))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/records", app.requireAuth(app.handleSaveRecords))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/columns", app.requireAuth(app.handleAddDataSourceColumn))
	mux.HandleFunc("PATCH /workspaces/{slug}/datasources/{dsSlug}/columns/{key}", app.requireAuth(app.handleUpdateDataSourceColumn))
	mux.HandleFunc("DELETE /workspaces/{slug}/datasources/{dsSlug}/columns/{key}", app.requireAuth(app.handleDeleteDataSourceColumn))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/columns/{key}/position", app.requireAuth(app.handleSetColumnPosition))
	mux.HandleFunc("POST /workspaces/{slug}/uploads", app.requireAuth(app.handleUploadImage))
	mux.HandleFunc("GET /workspaces/{slug}/uploads/{filename}", app.requireAuth(app.handleServeUpload))

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	log.Printf("EptaAdmin démarré sur http://localhost%s", addr)
	if err := http.ListenAndServe(addr, withAPICORS(mux)); err != nil {
		log.Fatal(err)
	}
}

// withAPICORS lets the public read-only API (/api/v1/...) be called
// cross-origin — from the JS SDK running in someone else's frontend, on
// its own domain, not EptaAdmin's. Wildcard is safe here specifically
// because this API authenticates via an explicit Authorization header
// (never a cookie a browser would attach automatically), so there's no
// CSRF-style risk in allowing any origin to read it — the caller's own
// page still has to know and send the API key itself. Every other route
// uses session cookies and is same-origin from the EptaAdmin web UI, so
// it's left untouched.
func withAPICORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
