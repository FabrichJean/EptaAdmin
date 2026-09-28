package shell

import (
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/store"
	"log"
	"net/http"
)

// exampleColumnsPreview truncates a record store's projected columns to a
// few fields and a few values each, so the "getDataSource" demo shows real
// data shapes without dumping an entire (possibly large) data source into
// the page.
func exampleColumnsPreview(records store.RecordStore) map[string][]any {
	const maxColumns = 4
	const maxValuesPerColumn = 3

	keys := records.Keys()
	if len(keys) > maxColumns {
		keys = keys[:maxColumns]
	}

	out := make(map[string][]any, len(keys))
	for _, k := range keys {
		values := records.Column(k)
		if len(values) > maxValuesPerColumn {
			values = values[:maxValuesPerColumn]
		}
		out[k] = values
	}
	return out
}

// handleIntegrationPage renders the in-app SDK/API documentation. It tries
// to personalize the code samples — including the per-method demo
// accordion — with one of the user's own workspaces and data sources (so
// the examples are copy-pasteable and actually work), falling back to
// generic placeholders when the user has none yet.
func HandleIntegrationPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)

	exampleWorkspaceSlug := "acme"
	exampleWorkspaceName := "Acme"
	exampleWorkspaceRole := "owner"
	exampleDataSourceSlug := "clients"
	exampleDataSourceName := "Clients"
	exampleColumnKey := "nom"
	exampleColumnValuesJSON := `["Jean Dupont", "Marie Curie"]`
	exampleColumnFirstValueJSON := `"Jean Dupont"`
	exampleColumnsJSON := `{
  "nom": ["Jean Dupont", "Marie Curie"],
  "email": ["jean@example.com", "marie@example.com"]
}`

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("integration page: list workspaces error: %v", err)
	} else if len(workspaces) > 0 {
		ws := workspaces[0]
		exampleWorkspaceSlug = ws.Slug
		exampleWorkspaceName = ws.Name
		exampleWorkspaceRole = ws.Role

		tables, err := a.Store.ListTablesByWorkspace(ws.ID)
		if err != nil {
			log.Printf("integration page: list tables error: %v", err)
		} else if len(tables) > 0 {
			t := tables[0]
			exampleDataSourceSlug = t.Slug
			exampleDataSourceName = t.Name

			if records, err := store.LoadRecordStore(t.StoragePath); err != nil {
				log.Printf("integration page: load record store error: %v", err)
			} else if len(records) > 0 {
				preview := exampleColumnsPreview(records)
				if b, err := json.MarshalIndent(preview, "", "  "); err == nil {
					exampleColumnsJSON = string(b)
				}
				for key, values := range preview {
					exampleColumnKey = key
					if b, err := json.Marshal(values); err == nil {
						exampleColumnValuesJSON = string(b)
					}
					if len(values) > 0 {
						if b, err := json.Marshal(values[0]); err == nil {
							exampleColumnFirstValueJSON = string(b)
						}
					}
					break // any column works for the demo; the first one seen is enough
				}
			}
		}
	}

	a.Render(w, r, "integration.html", map[string]any{
		"CurrentUser":                 currentUser,
		"ActiveNav":                   "integration",
		"PageTitle":                   i18n.T(lang, "nav.integration"),
		"HeaderTitle":                 i18n.T(lang, "nav.integration"),
		"HeaderDescription":           i18n.T(lang, "integration.intro"),
		"HeaderIcon":                  "code",
		"ExampleWorkspaceSlug":        exampleWorkspaceSlug,
		"ExampleWorkspaceName":        exampleWorkspaceName,
		"ExampleWorkspaceRole":        exampleWorkspaceRole,
		"ExampleDataSourceSlug":       exampleDataSourceSlug,
		"ExampleDataSourceName":       exampleDataSourceName,
		"ExampleColumnKey":            exampleColumnKey,
		"ExampleColumnValuesJSON":     exampleColumnValuesJSON,
		"ExampleColumnFirstValueJSON": exampleColumnFirstValueJSON,
		"ExampleColumnsJSON":          exampleColumnsJSON,
	})
}
