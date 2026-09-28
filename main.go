package main

import (
	"eptaadmin/internal/api"
	"eptaadmin/internal/app"
	"eptaadmin/internal/crm"
	"eptaadmin/internal/profile"
	"eptaadmin/internal/shell"
	"eptaadmin/internal/store"
	"eptaadmin/internal/tracking"
	"eptaadmin/internal/visual"
	"eptaadmin/internal/webhook"
	"eptaadmin/internal/workspace"
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

	st, err := store.NewStore(dbPath)
	if err != nil {
		log.Fatalf("impossible d'initialiser la base de données: %v", err)
	}

	a, err := app.New(st)
	if err != nil {
		log.Fatalf("impossible de charger les templates: %v", err)
	}
	a.PublicURL = strings.TrimRight(os.Getenv("EPTAADMIN_PUBLIC_URL"), "/")

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /login", a.HandleLoginPage)
	mux.HandleFunc("POST /login", a.HandleLogin)
	mux.HandleFunc("GET /register", a.HandleRegisterPage)
	mux.HandleFunc("POST /register", a.HandleRegister)
	mux.HandleFunc("POST /logout", a.HandleLogout)
	mux.HandleFunc("GET /{$}", a.RequireAuth(a.HandleDashboard))
	mux.HandleFunc("GET /integration", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { shell.HandleIntegrationPage(a, w, r) }))
	mux.HandleFunc("GET /profile", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleProfilePage(a, w, r) }))
	mux.HandleFunc("POST /profile/email", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleUpdateProfileEmail(a, w, r) }))
	mux.HandleFunc("POST /profile/password", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleUpdateProfilePassword(a, w, r) }))
	mux.HandleFunc("POST /profile/avatar/seed", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleUpdateProfileAvatarSeed(a, w, r) }))
	mux.HandleFunc("POST /profile/avatar/upload", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleUploadProfileAvatar(a, w, r) }))
	mux.HandleFunc("GET /avatars/{filename}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleServeAvatar(a, w, r) }))
	mux.HandleFunc("POST /profile/api-keys", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleCreateAPIKey(a, w, r) }))
	mux.HandleFunc("POST /profile/api-keys/{id}/delete", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleDeleteAPIKey(a, w, r) }))
	mux.HandleFunc("POST /profile/language", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { profile.HandleUpdateProfileLanguage(a, w, r) }))
	mux.HandleFunc("GET /lang/{lang}", a.HandleSetLangCookie)

	// Public read-only API (personal API key auth) — consumed by the JS SDK.
	mux.HandleFunc("GET /api/v1/workspaces", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { api.HandleAPIListWorkspaces(a, w, r) }))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { api.HandleAPIListDataSources(a, w, r) }))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { api.HandleAPIGetDataSource(a, w, r) }))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}/columns/{key}", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { api.HandleAPIGetColumn(a, w, r) }))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/datasources/{dsSlug}/columns/{key}/{index}", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { api.HandleAPIGetColumnValue(a, w, r) }))
	mux.HandleFunc("GET /api/v1/workspaces/{slug}/uploads/{filename}", func(w http.ResponseWriter, r *http.Request) { api.HandleAPIServeUpload(a, w, r) })
	// Public analytics ingestion — no session, no personal API key: auth is
	// the site's own public tracking key, resolved from the JSON body (see
	// handleTrackCollect). Reachable directly from arbitrary third-party
	// browsers, which is why it's placed under /api/v1/ (see withAPICORS
	// below, which grants it cross-origin access same as the read-only API).
	mux.HandleFunc("POST /api/v1/track", func(w http.ResponseWriter, r *http.Request) { tracking.HandleTrackCollect(a, w, r) })
	// Public visual-editing API — same trust model as /api/v1/track (a
	// site's own public key, no session/Authorization header), plus a
	// short-lived signed token (see visual_signing.go) for the write
	// endpoints. Unlike an earlier version of this feature, there is no
	// "list fields" endpoint here: edits land directly in the same
	// datasource table the site already reads via the public read-only
	// API (internal/api) — the site's own next fetch sees them, no
	// separate apply step needed.
	mux.HandleFunc("POST /api/v1/visual/write", func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualWriteCell(a, w, r) })
	mux.HandleFunc("POST /api/v1/visual/clear", func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualClearCell(a, w, r) })
	mux.HandleFunc("POST /api/v1/visual/upload", func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualUpload(a, w, r) })
	mux.HandleFunc("POST /api/v1/visual/verify", func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualVerifyToken(a, w, r) })
	mux.HandleFunc("POST /api/v1/visual/logout", func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualLogout(a, w, r) })
	mux.HandleFunc("GET /workspaces", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleWorkspacesPage(a, w, r) }))
	mux.HandleFunc("GET /activity", a.RequireAuth(a.HandleGlobalActivity))
	mux.HandleFunc("GET /api/search", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleGlobalSearch(a, w, r) }))
	mux.HandleFunc("GET /api/webhook-status", a.RequireAuth(a.HandleWebhookStatus))
	mux.HandleFunc("GET /api/activity/recent", a.RequireAuth(a.HandleRecentActivity))
	mux.HandleFunc("POST /api/activity/mark-read", a.RequireAuth(a.HandleMarkActivitySeen))
	mux.HandleFunc("POST /workspaces", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleCreateWorkspace(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleWorkspaceDetail(a, w, r) }))
	mux.HandleFunc("GET /webhooks", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleGlobalWebhooks(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/settings", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleSettingsPage(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleCreateWebhook(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/webhooks/{id}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleDeleteWebhook(a, w, r) }))
	mux.HandleFunc("PATCH /workspaces/{slug}/webhooks/{id}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleUpdateWebhook(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks/{id}/trigger", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleTriggerWebhook(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/webhooks/{id}/regenerate-secret", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { webhook.HandleRegenerateWebhookSecret(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/sites", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleCreateTrackedSite(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/sites/{id}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleDeleteTrackedSite(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/sites/{id}/regenerate-key", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleRegenerateTrackedSiteKey(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/sites/{id}/reset-data", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleResetTrackedSiteData(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/dashboard", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleTrackingDashboard(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/dashboard/data", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleTrackingDashboardData(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/sites/{id}/events/recent", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { tracking.HandleTrackingRecentEvents(a, w, r) }))
	mux.HandleFunc("GET /plugins", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { shell.HandleGlobalPlugins(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleCreateVisualSite(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/visual-sites/{id}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleDeleteVisualSite(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites/{id}/regenerate-key", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleRegenerateVisualSiteKey(a, w, r) }))
	mux.HandleFunc("PATCH /workspaces/{slug}/visual-sites/{id}/domain", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleUpdateVisualSiteDomain(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/visual-sites/{id}/edit-link", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleGenerateVisualEditLink(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/visual-sites/{id}/dashboard", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { visual.HandleVisualSiteDashboard(a, w, r) }))
	mux.HandleFunc("GET /crm", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCRMTeamsPage(a, w, r) }))
	mux.HandleFunc("POST /crm", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCreateCRMTeam(a, w, r) }))
	mux.HandleFunc("GET /crm/{teamSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCRMTeamDetail(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/entities", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCreateCRMEntity(a, w, r) }))
	mux.HandleFunc("DELETE /crm/{teamSlug}/entities/{entitySlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleDeleteCRMEntity(a, w, r) }))
	mux.HandleFunc("GET /crm/{teamSlug}/{entitySlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCRMEntityEditor(a, w, r) }))
	mux.HandleFunc("PATCH /crm/{teamSlug}/{entitySlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleSaveCRMEntityContent(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/uploads", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCRMUpload(a, w, r) }))
	// Nested under "entities/" (not bare {entitySlug}) so these unambiguously
	// extend the existing DELETE /crm/{teamSlug}/entities/{entitySlug}
	// pattern instead of colliding with it — Go's ServeMux refuses to start
	// when a bare-wildcard path and a same-depth literal-segment path could
	// both match the same URL (e.g. .../entities/design).
	mux.HandleFunc("POST /crm/{teamSlug}/entities/{entitySlug}/design/generate", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleGenerateCRMEntityDesign(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/entities/{entitySlug}/design", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleApplyCRMEntityDesign(a, w, r) }))
	mux.HandleFunc("DELETE /crm/{teamSlug}/entities/{entitySlug}/design", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleClearCRMEntityDesign(a, w, r) }))
	mux.HandleFunc("GET /crm/{teamSlug}/designs", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleListCRMDesigns(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/designs/import", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleImportCRMDesign(a, w, r) }))
	mux.HandleFunc("DELETE /crm/{teamSlug}/designs/{designID}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleDeleteCRMDesign(a, w, r) }))
	mux.HandleFunc("GET /crm/{teamSlug}/members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleCRMTeamMembersPage(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleAddCRMTeamMember(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/members/{userID}/remove", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleRemoveCRMTeamMember(a, w, r) }))
	mux.HandleFunc("POST /crm/{teamSlug}/members/{userID}/role", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { crm.HandleUpdateCRMTeamMemberRole(a, w, r) }))
	mux.HandleFunc("GET /api/v1/crm/teams/{teamSlug}/entities", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { crm.HandleAPIListCRMEntities(a, w, r) }))
	mux.HandleFunc("GET /api/v1/crm/teams/{teamSlug}/entities/{entitySlug}", a.RequireAPIKey(func(w http.ResponseWriter, r *http.Request) { crm.HandleAPIGetCRMEntity(a, w, r) }))
	mux.HandleFunc("GET /api/v1/crm/teams/{teamSlug}/uploads/{filename}", func(w http.ResponseWriter, r *http.Request) { crm.HandleAPIServeCRMUpload(a, w, r) })
	// Public callback a webhook receiver posts real-time progress updates
	// to — see webhooks.go's progressUrl convention. No session auth: the
	// external server calling this isn't a logged-in browser. Guarded only
	// by the deliveryId being an unguessable random token, the same trust
	// model as a signed one-off upload URL.
	mux.HandleFunc("POST /api/webhooks/deliveries/{deliveryID}/progress", a.HandleWebhookDeliveryProgress)

	mux.HandleFunc("GET /workspaces/{slug}/activity", a.RequireAuth(a.HandleWorkspaceActivity))
	mux.HandleFunc("POST /workspaces/{slug}/activity/{id}/revert", a.RequireAuth(a.HandleRevertActivity))
	mux.HandleFunc("GET /members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleMembersPage(a, w, r) }))
	mux.HandleFunc("POST /members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleCreateMember(a, w, r) }))
	mux.HandleFunc("POST /members/{userID}/delete", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleDeleteMember(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleWorkspaceMembersPage(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/members", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleAddWorkspaceMember(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/members/{userID}/remove", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleRemoveWorkspaceMember(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/members/{userID}/role", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleUpdateWorkspaceMemberRole(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/datasources", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleCreateDataSource(a, w, r) }))
	mux.HandleFunc("PATCH /workspaces/{slug}/datasources/{dsSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleRenameDataSource(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/datasources/{dsSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleDeleteDataSource(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/tables", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleCreateTable(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/tables/{tableSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleTableGrid(a, w, r) }))
	mux.HandleFunc("PATCH /workspaces/{slug}/tables/{tableSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleRenameTable(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/tables/{tableSlug}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleDeleteTable(a, w, r) }))
	mux.HandleFunc("GET /workspaces/{slug}/tables/{tableSlug}/export", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleExportTable(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/import", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleImportTable(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/records", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleSaveRecords(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/tables/{tableSlug}/columns", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleAddTableColumn(a, w, r) }))
	mux.HandleFunc("PATCH /workspaces/{slug}/tables/{tableSlug}/columns/{key}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleUpdateTableColumn(a, w, r) }))
	mux.HandleFunc("DELETE /workspaces/{slug}/tables/{tableSlug}/columns/{key}", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleDeleteTableColumn(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/uploads", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleUploadImage(a, w, r) }))
	mux.HandleFunc("POST /workspaces/{slug}/uploads/file", a.RequireAuth(func(w http.ResponseWriter, r *http.Request) { workspace.HandleUploadFile(a, w, r) }))
	// Legacy browser upload URLs are redirected to signed API URLs so older
	// frontend bundles keep working after the upload API migration.
	mux.HandleFunc("GET /workspaces/{slug}/uploads/{filename}", func(w http.ResponseWriter, r *http.Request) { workspace.HandleLegacyUploadRedirect(a, w, r) })

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
				// the JSON body or multipart form, never a header) — every
				// visual endpoint is a POST (write/clear/upload/verify/logout).
				w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
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
