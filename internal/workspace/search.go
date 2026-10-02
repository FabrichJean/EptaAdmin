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
	// Kind tells the client which fields below are populated — "workspace"
	// (data source column values) or "crm" (CRM+ entity content). Added
	// alongside the original workspace-only fields rather than replacing
	// them, so a client that doesn't know about "crm" yet just never sees
	// that kind (CRM+ hits still set Kind, nothing silently defaults to
	// "workspace").
	Kind string `json:"kind"`

	WorkspaceName  string `json:"workspaceName,omitempty"`
	WorkspaceSlug  string `json:"workspaceSlug,omitempty"`
	DataSourceName string `json:"dataSourceName,omitempty"`
	DataSourceSlug string `json:"dataSourceSlug,omitempty"`
	Column         string `json:"column,omitempty"`
	Index          int    `json:"index,omitempty"`
	Value          string `json:"value,omitempty"`

	CRMTeamName   string `json:"crmTeamName,omitempty"`
	CRMTeamSlug   string `json:"crmTeamSlug,omitempty"`
	CRMEntityName string `json:"crmEntityName,omitempty"`
	CRMEntitySlug string `json:"crmEntitySlug,omitempty"`
	// CRMField is where inside the entity the match was found — the
	// matched field's own path (e.g. "testimonials › #2 › quote"), or
	// "Nom" when the entity's own Name matched rather than its content.
	CRMField string `json:"crmField,omitempty"`
	// CRMPath is the same match addressed as [index, "children", index,
	// ...] (store.CRMSearchHit.NodePath) — omitted for a name match, which
	// has no specific field node to deep-link to. The entity editor reads
	// this back as ?highlightPath= to scroll straight to the field.
	CRMPath []any `json:"crmPath,omitempty"`
}

func truncateSearchValue(value string) string {
	if len(value) > 120 {
		return value[:120] + "…"
	}
	return value
}

// handleGlobalSearch looks for a query string inside the actual data (column
// values) of every data source across every workspace the requesting user is
// a member of, and inside every CRM+ entity's content across every CRM+ team
// they're a member of — not just workspace/entity names. Scoped to
// workspaces.html's search box and the header's ⌘K global search.
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
				results = append(results, searchResult{
					Kind:           "workspace",
					WorkspaceName:  ws.Name,
					WorkspaceSlug:  ws.Slug,
					DataSourceName: t.Name,
					DataSourceSlug: t.Slug,
					Column:         hit.Column,
					Index:          hit.Index,
					Value:          truncateSearchValue(hit.Value),
				})
				if len(results) >= maxSearchResults {
					webutil.WriteJSON(w, http.StatusOK, results)
					return
				}
			}
		}
	}

	crmTeams, err := a.Store.ListCRMTeamsForUser(currentUser.ID)
	if err != nil {
		log.Printf("global search: list crm teams error: %v", err)
		webutil.WriteJSON(w, http.StatusOK, results)
		return
	}
	lowerQuery := strings.ToLower(query)
	for _, team := range crmTeams {
		if !roles.HasPermission(team.Role, roles.PermDataRead) {
			continue
		}
		entities, err := a.Store.ListCRMEntitiesForTeam(team.ID)
		if err != nil {
			log.Printf("global search: list crm entities error: %v", err)
			continue
		}
		for _, entity := range entities {
			perSourceCount := 0
			appendHit := func(field, value string, nodePath []any) bool {
				results = append(results, searchResult{
					Kind:          "crm",
					CRMTeamName:   team.Name,
					CRMTeamSlug:   team.Slug,
					CRMEntityName: entity.Name,
					CRMEntitySlug: entity.Slug,
					CRMField:      field,
					CRMPath:       nodePath,
					Value:         truncateSearchValue(value),
				})
				perSourceCount++
				return len(results) >= maxSearchResults
			}
			if strings.Contains(strings.ToLower(entity.Name), lowerQuery) {
				if appendHit("Nom", entity.Name, nil) {
					webutil.WriteJSON(w, http.StatusOK, results)
					return
				}
			}
			hits := store.SearchCRMEntityContent(entity.ContentJSON, query, maxSearchResultsPerSource)
			for _, hit := range hits {
				if perSourceCount >= maxSearchResultsPerSource {
					break
				}
				if appendHit(hit.Label, hit.Value, hit.NodePath) {
					webutil.WriteJSON(w, http.StatusOK, results)
					return
				}
			}
		}
	}

	webutil.WriteJSON(w, http.StatusOK, results)
}
