package workspace

import (
	"eptaadmin/internal/app"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
	"log"
	"net/http"
	"strings"
)

// maxSearchResultsPerSource and maxSearchResults bound how much work/response
// a single global-search request can produce, since it walks every data
// source the user can see and reads its JSON file from disk.
const (
	maxSearchResultsPerSource = 5
	maxSearchResults          = 30
)

type searchResult struct {
	WorkspaceName  string `json:"workspaceName"`
	WorkspaceSlug  string `json:"workspaceSlug"`
	DataSourceName string `json:"dataSourceName"`
	DataSourceSlug string `json:"dataSourceSlug"`
	Column         string `json:"column"`
	Index          int    `json:"index"`
	Value          string `json:"value"`
}

// handleGlobalSearch looks for a query string inside the actual data (column
// values) of every data source across every workspace the requesting user is
// a member of — not just workspace names. Scoped to workspaces.html's search
// box.
func HandleGlobalSearch(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	results := []searchResult{}
	if query == "" {
		webutil.WriteJSON(w, http.StatusOK, results)
		return
	}

	workspaces, err := a.Store.ListWorkspacesForUser(currentUser.ID)
	if err != nil {
		log.Printf("global search: list workspaces error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	for _, ws := range workspaces {
		if !roles.HasPermission(ws.Role, roles.PermDataRead) {
			continue
		}
		tables, err := a.Store.ListTablesByWorkspace(ws.ID)
		if err != nil {
			log.Printf("global search: list tables error: %v", err)
			continue
		}
		for _, t := range tables {
			records, err := store.LoadRecordStore(t.StoragePath)
			if err != nil {
				log.Printf("global search: load record store error: %v", err)
				continue
			}
			hits := store.SearchRecords(records, query, maxSearchResultsPerSource)
			for _, hit := range hits {
				value := hit.Value
				if len(value) > 120 {
					value = value[:120] + "…"
				}
				results = append(results, searchResult{
					WorkspaceName:  ws.Name,
					WorkspaceSlug:  ws.Slug,
					DataSourceName: t.Name,
					DataSourceSlug: t.Slug,
					Column:         hit.Column,
					Index:          hit.Index,
					Value:          value,
				})
				if len(results) >= maxSearchResults {
					webutil.WriteJSON(w, http.StatusOK, results)
					return
				}
			}
		}
	}

	webutil.WriteJSON(w, http.StatusOK, results)
}
