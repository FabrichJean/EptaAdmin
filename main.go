package main

import (
	"log"
	"net/http"
	"os"
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
	mux.HandleFunc("GET /login", app.handleLoginPage)
	mux.HandleFunc("POST /login", app.handleLogin)
	mux.HandleFunc("GET /register", app.handleRegisterPage)
	mux.HandleFunc("POST /register", app.handleRegister)
	mux.HandleFunc("POST /logout", app.handleLogout)
	mux.HandleFunc("GET /{$}", app.requireAuth(app.handleDashboard))
	mux.HandleFunc("GET /members", app.requireAuth(app.handleMembersPage))
	mux.HandleFunc("POST /members", app.requireAuth(app.handleCreateMember))
	mux.HandleFunc("GET /workspaces", app.requireAuth(app.handleWorkspacesPage))
	mux.HandleFunc("POST /workspaces", app.requireAuth(app.handleCreateWorkspace))
	mux.HandleFunc("GET /workspaces/{slug}", app.requireAuth(app.handleWorkspaceDetail))
	mux.HandleFunc("POST /workspaces/{slug}/members", app.requireAuth(app.handleAddWorkspaceMember))
	mux.HandleFunc("POST /workspaces/{slug}/datasources", app.requireAuth(app.handleCreateDataSource))
	mux.HandleFunc("GET /workspaces/{slug}/datasources/{dsSlug}", app.requireAuth(app.handleDataSourceTable))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/records", app.requireAuth(app.handleSaveRecords))
	mux.HandleFunc("POST /workspaces/{slug}/datasources/{dsSlug}/columns", app.requireAuth(app.handleAddDataSourceColumn))
	mux.HandleFunc("POST /workspaces/{slug}/uploads", app.requireAuth(app.handleUploadImage))
	mux.HandleFunc("GET /workspaces/{slug}/uploads/{filename}", app.requireAuth(app.handleServeUpload))

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	log.Printf("EptaAdmin démarré sur http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
