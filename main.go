package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("impossible de charger .env: %v", err)
	}

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
	app.publicURL = strings.TrimRight(os.Getenv("EPTAADMIN_PUBLIC_URL"), "/")

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
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/uploads/{filename}", app.handleAPIServeUpload)
	// Public analytics ingestion — no session, no personal API key: auth is
	// the site's own public tracking key, resolved from the JSON body (see
	// handleTrackCollect). Reachable directly from arbitrary third-party
	// browsers, which is why it's placed under /api/v1/ (see withAPICORS
	// below, which grants it cross-origin access same as the read-only API).
	mux.HandleFunc("POST /api/v1/track", app.handleTrackCollect)
	// Public visual-editing API — same trust model as /api/v1/track (a
	// site's own public key, no session/Authorization header), plus a
	// short-lived signed token (see visual_signing.go) for the write
	// endpoints only. handleVisualListFields is what makes an edit show
	// up for every visitor, not just the admin who made it.
	mux.HandleFunc("GET /api/v1/visual/fields", app.handleVisualListFields)
	mux.HandleFunc("POST /api/v1/visual/fields", app.handleVisualSaveField)
	mux.HandleFunc("DELETE /api/v1/visual/fields", app.handleVisualDeleteField)
	mux.HandleFunc("POST /api/v1/visual/upload", app.handleVisualUpload)
	mux.HandleFunc("POST /api/v1/visual/verify", app.handleVisualVerifyToken)
	mux.HandleFunc("POST /api/v1/visual/logout", app.handleVisualLogout)
	mux.HandleFunc("GET /workspaces", app.requireAuth(app.handleWorkspacesPage))
	mux.HandleFunc("GET /activity", app.requireAuth(app.handleGlobalActivity))
	mux.HandleFunc("GET /api/search", app.requireAuth(app.handleGlobalSearch))
	mux.HandleFunc("GET /api/webhook-status", app.requireAuth(app.handleWebhookStatus))
	mux.HandleFunc("GET /api/activity/recent", app.requireAuth(app.handleRecentActivity))
	mux.HandleFunc("POST /api/activity/mark-read", app.requireAuth(app.handleMarkActivitySeen))
	mux.HandleFunc("POST /workspaces", app.requireAuth(app.handleCreateWorkspace))
	mux.HandleFunc("GET /workspaces/{slug}", app.requireAuth(app.handleWorkspaceDetail))
	mux.HandleFunc("GET /webhooks", app.requireAuth(app.handleGlobalWebhooks))
	mux.HandleFunc("GET /workspaces/{slug}/settings", app.requireAuth(app.handleSettingsPage))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks", app.requireAuth(app.handleCreateWebhook))
	mux.HandleFunc("DELETE /workspaces/{slug}/webhooks/{id}", app.requireAuth(app.handleDeleteWebhook))
	mux.HandleFunc("PATCH /workspaces/{slug}/webhooks/{id}", app.requireAuth(app.handleUpdateWebhook))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks/{id}/trigger", app.requireAuth(app.handleTriggerWebhook))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks/{id}/regenerate-secret", app.requireAuth(app.handleRegenerateWebhookSecret))
	mux.HandleFunc("POST /workspaces/{slug}/sites", app.requireAuth(app.handleCreateTrackedSite))
	mux.HandleFunc("DELETE /workspaces/{slug}/sites/{id}", app.requireAuth(app.handleDeleteTrackedSite))
	mux.HandleFunc("POST /workspaces/{slug}/sites/{id}/regenerate-key", app.requireAuth(app.handleRegenerateTrackedSiteKey))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/dashboard", app.requireAuth(app.handleTrackingDashboard))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/dashboard/data", app.requireAuth(app.handleTrackingDashboardData))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/events/recent", app.requireAuth(app.handleTrackingRecentEvents))
	mux.HandleFunc("GET /plugins", app.requireAuth(app.handleGlobalPlugins))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites", app.requireAuth(app.handleCreateVisualSite))
	mux.HandleFunc("DELETE /workspaces/{slug}/visual-sites/{id}", app.requireAuth(app.handleDeleteVisualSite))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites/{id}/regenerate-key", app.requireAuth(app.handleRegenerateVisualSiteKey))
	mux.HandleFunc("PATCH /workspaces/{slug}/visual-sites/{id}/domain", app.requireAuth(app.handleUpdateVisualSiteDomain))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites/{id}/apply-to-datasource", app.requireAuth(app.handleApplyVisualSiteToDataSource))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites/{id}/edit-link", app.requireAuth(app.handleGenerateVisualEditLink))
	mux.HandleFunc("GET /workspaces/{slug}/visual-sites/{id}/dashboard", app.requireAuth(app.handleVisualSiteDashboard))
	mux.HandleFunc("DELETE /workspaces/{slug}/visual-sites/{id}/fields/{fieldID}", app.requireAuth(app.handleDeleteVisualFieldFromDashboard))
	// Public callback a webhook receiver posts real-time progress updates
	// to — see webhooks.go's progressUrl convention. No session auth: the
	// external server calling this isn't a logged-in browser. Guarded only
	// by the deliveryId being an unguessable random token, the same trust
	// model as a signed one-off upload URL.
	mux.HandleFunc("POST /api/webhooks/deliveries/{deliveryID}/progress", app.handleWebhookDeliveryProgress)

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
	mux.HandleFunc("PATCH /workspaces/{slug}/datasources/{dsSlug}", app.requireAuth(app.handleRenameDataSource))
	mux.HandleFunc("DELETE /workspaces/{slug}/datasources/{dsSlug}", app.requireAuth(app.handleDeleteDataSource))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/tables", app.requireAuth(app.handleCreateTable))
	mux.HandleFunc("GET /workspaces/{slug}/tables/{tableSlug}", app.requireAuth(app.handleTableGrid))
	mux.HandleFunc("PATCH /workspaces/{slug}/tables/{tableSlug}", app.requireAuth(app.handleRenameTable))
	mux.HandleFunc("DELETE /workspaces/{slug}/tables/{tableSlug}", app.requireAuth(app.handleDeleteTable))
	mux.HandleFunc("GET /workspaces/{slug}/tables/{tableSlug}/export", app.requireAuth(app.handleExportTable))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/import", app.requireAuth(app.handleImportTable))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/records", app.requireAuth(app.handleSaveRecords))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/columns", app.requireAuth(app.handleAddTableColumn))
	mux.HandleFunc("PATCH /workspaces/{slug}/tables/{tableSlug}/columns/{key}", app.requireAuth(app.handleUpdateTableColumn))
	mux.HandleFunc("DELETE /workspaces/{slug}/tables/{tableSlug}/columns/{key}", app.requireAuth(app.handleDeleteTableColumn))
	mux.HandleFunc("POST /workspaces/{slug}/uploads", app.requireAuth(app.handleUploadImage))
	mux.HandleFunc("POST /workspaces/{slug}/uploads/file", app.requireAuth(app.handleUploadFile))
	// Legacy browser upload URLs are redirected to signed API URLs so older
	// frontend bundles keep working after the upload API migration.
	mux.HandleFunc("GET /workspaces/{slug}/uploads/{filename}", app.handleLegacyUploadRedirect)

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
			if r.URL.Path == "/api/v1/track" {
				// The tracking SDK sends a plain JSON POST with no
				// Authorization header (its key travels in the body, see
				// handleTrackCollect) — only Content-Type needs allowing.
				w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			} else if strings.HasPrefix(r.URL.Path, "/api/v1/visual/") {
				// Same body-only auth story as tracking (key/token travel in
				// the JSON body or multipart form, never a header), but the
				// visual SDK also needs GET (read overrides) and DELETE
				// (unlink a field) alongside POST.
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			} else {
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
