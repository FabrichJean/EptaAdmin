package crm

import (
	"crypto/hmac"
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
	"eptaadmin/internal/webutil"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// This file is the CRM+ plugin: a second, deliberately workspace-independent
// top-level concept (see crm_store.go) — teams of their own members manage
// "entities", each a single JSON tree of elements (text/image/nested list),
// edited as a whole rather than through the fixed-column grid. It mirrors
// handlers_workspaces.go/handlers_members.go's shape wherever the two
// concepts are structurally identical (team CRUD, member management).

// ---- Teams ----

func crmTeamsPageData(lang string, currentUser *store.User, teams []*store.UserCRMTeam) map[string]any {
	return map[string]any{
		"CurrentUser": currentUser,
		"CRMTeams":    teams,
		"ActiveNav":   "crm",
		"PageTitle":   i18n.T(lang, "nav.crm"),
		"HeaderIcon":  "workspace",
	}
}

func HandleCRMTeamsPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	teams, err := a.Store.ListCRMTeamsForUser(currentUser.ID)
	if err != nil {
		log.Printf("list crm teams error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.Render(w, r, "crm_teams.html", crmTeamsPageData(a.ResolveLang(r), currentUser, teams))
}

// Any authenticated user may create a CRM+ team; they become its Owner —
// mirrors handleCreateWorkspace.
func HandleCreateCRMTeam(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	name := strings.TrimSpace(r.FormValue("name"))

	renderError := func(msg string) {
		teams, err := a.Store.ListCRMTeamsForUser(currentUser.ID)
		if err != nil {
			log.Printf("list crm teams error: %v", err)
			http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
			return
		}
		data := crmTeamsPageData(lang, currentUser, teams)
		data["Error"] = msg
		data["Name"] = name
		a.Render(w, r, "crm_teams.html", data)
	}

	if name == "" {
		renderError(i18n.T(lang, "crm.name_required"))
		return
	}

	team, err := a.Store.CreateCRMTeam(name, currentUser.ID)
	if err != nil {
		if err == store.ErrCRMTeamExists {
			renderError(i18n.T(lang, "crm.name_exists"))
		} else {
			log.Printf("create crm team error: %v", err)
			renderError(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMTeamCreate, Details: map[string]any{"name": team.Name}})

	http.Redirect(w, r, "/crm/"+team.Slug, http.StatusSeeOther)
}

// loadCRMTeamMembership fetches the team and the caller's role in it,
// writing an HTTP error and returning ok=false if either is missing —
// mirrors loadWorkspaceMembership.
func loadCRMTeamMembership(a *app.App, w http.ResponseWriter, r *http.Request, currentUser *store.User) (team *store.CRMTeam, role string, ok bool) {
	slug := r.PathValue("teamSlug")
	team, err := a.Store.GetCRMTeamBySlug(slug)
	if err != nil {
		log.Printf("get crm team error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if team == nil {
		http.NotFound(w, r)
		return nil, "", false
	}
	role, err = a.Store.GetCRMTeamMemberRole(team.ID, currentUser.ID)
	if err != nil {
		log.Printf("get crm team role error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, "", false
	}
	if role == "" {
		http.Error(w, i18n.T(a.ResolveLang(r), "common.access_denied"), http.StatusForbidden)
		return nil, "", false
	}
	return team, role, true
}

func HandleCRMTeamDetail(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)

	entities, err := a.Store.ListCRMEntitiesForTeam(team.ID)
	if err != nil {
		log.Printf("list crm entities error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}

	a.Render(w, r, "crm_team_detail.html", map[string]any{
		"CurrentUser":      currentUser,
		"ActiveNav":        "crm",
		"PageTitle":        team.Name,
		"CRMTeam":          team,
		"Entities":         entities,
		"CanManageData":    roles.HasPermission(role, roles.PermDataCreate),
		"CanManageMembers": roles.HasPermission(role, roles.PermMembersManage),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.crm"), URL: "/crm"},
			{Label: team.Name},
		},
		"HeaderTitle": team.Name,
		"HeaderIcon":  "workspace",
	})
}

func HandleCreateCRMEntity(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermDataCreate) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/crm/"+team.Slug, http.StatusSeeOther)
		return
	}
	entity, err := a.Store.CreateCRMEntity(team.ID, name)
	if err != nil && err != store.ErrCRMEntityExists {
		log.Printf("create crm entity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if err == nil {
		a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityCreate, Details: map[string]any{"teamName": team.Name, "name": entity.Name}})
	}
	http.Redirect(w, r, "/crm/"+team.Slug, http.StatusSeeOther)
}

func HandleDeleteCRMEntity(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermDataDelete) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	entity, err := a.Store.GetCRMEntityBySlug(team.ID, r.PathValue("entitySlug"))
	if err != nil {
		log.Printf("get crm entity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if entity == nil {
		http.NotFound(w, r)
		return
	}
	if err := a.Store.DeleteCRMEntity(entity.ID); err != nil {
		log.Printf("delete crm entity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityDelete, Details: map[string]any{"teamName": team.Name, "name": entity.Name}})
	http.Redirect(w, r, "/crm/"+team.Slug, http.StatusSeeOther)
}

// ---- Entity editor ----

func HandleCRMEntityEditor(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	entity, err := a.Store.GetCRMEntityBySlug(team.ID, r.PathValue("entitySlug"))
	if err != nil {
		log.Printf("get crm entity error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if entity == nil {
		http.NotFound(w, r)
		return
	}

	a.Render(w, r, "crm_entity_editor.html", map[string]any{
		"CurrentUser": currentUser,
		"ActiveNav":   "crm",
		"PageTitle":   entity.Name,
		"CRMTeam":     team,
		"Entity":      entity,
		"CanEdit":     roles.HasPermission(role, roles.PermDataUpdate),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.crm"), URL: "/crm"},
			{Label: team.Name, URL: "/crm/" + team.Slug},
			{Label: entity.Name},
		},
		"HeaderTitle": entity.Name,
		"HeaderIcon":  "workspace",
	})
}

// handleSaveCRMEntityContent replaces the entity's whole content tree —
// the editor is whole-tree-save, not granular (see plan). The body is
// stored as-is once confirmed to be well-formed JSON, same philosophy as
// store.ColumnTypeJSON (jsondata.go): no schema validation of the tree shape.
func HandleSaveCRMEntityContent(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	entity, err := a.Store.GetCRMEntityBySlug(team.ID, r.PathValue("entitySlug"))
	if err != nil {
		log.Printf("get crm entity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if entity == nil {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if !json.Valid(body) {
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.invalid_content")})
		return
	}

	if err := a.Store.UpdateCRMEntityContent(entity.ID, string(body)); err != nil {
		log.Printf("update crm entity content error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityUpdate, Details: map[string]any{"teamName": team.Name, "name": entity.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Uploads ----

// crmUploadSigningNamespace prefixes the CRM slug in the HMAC input so a
// signature computed for a CRM team can never accidentally verify against
// a workspace slug that happens to share the same string (uploadDir/
// crmUploadDir already keep the files themselves apart; this keeps the
// signatures apart too).
const crmUploadSigningNamespace = "crm:"

func signCRMUploadPath(a *app.App, teamSlug, filename string) (string, error) {
	secret, err := uploads.UploadSigningSecret(a.Store)
	if err != nil {
		return "", err
	}
	sig := uploads.ComputeUploadSignature(secret, crmUploadSigningNamespace+teamSlug, filename)
	return fmt.Sprintf("/api/v1/crm/teams/%s/uploads/%s?sig=%s", teamSlug, filename, sig), nil
}

func verifyCRMUploadSignature(a *app.App, teamSlug, filename, sigParam string) (bool, error) {
	if sigParam == "" {
		return false, nil
	}
	secret, err := uploads.UploadSigningSecret(a.Store)
	if err != nil {
		return false, err
	}
	expected := uploads.ComputeUploadSignature(secret, crmUploadSigningNamespace+teamSlug, filename)
	return hmac.Equal([]byte(expected), []byte(sigParam)), nil
}

func HandleCRMUpload(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxGenericUploadSize+1<<20)
	if err := r.ParseMultipartForm(uploads.MaxGenericUploadSize + 1<<20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename, err := SaveCRMUploadedFile(team.ID, header.Filename, file, header.Size)
	if err != nil {
		log.Printf("save crm upload error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	signedPath, err := signCRMUploadPath(a, team.Slug, filename)
	if err != nil {
		log.Printf("sign crm upload path error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"url": a.AbsoluteURL(r) + signedPath})
}

// handleAPIServeCRMUpload serves an uploaded CRM+ image, sig-gated the same
// way handleAPIServeUpload does for workspace uploads — no bearer key
// needed, since image values need to work as a bare <img> src.
func HandleAPIServeCRMUpload(a *app.App, w http.ResponseWriter, r *http.Request) {
	lang := a.ResolveLang(r)
	slug := r.PathValue("teamSlug")
	filename := filepath.Base(r.PathValue("filename"))

	validSig, err := verifyCRMUploadSignature(a, slug, filename, r.URL.Query().Get("sig"))
	if err != nil {
		log.Printf("verify crm upload signature error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if !validSig {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	team, err := a.Store.GetCRMTeamBySlug(slug)
	if err != nil {
		log.Printf("api get crm team error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if team == nil {
		webutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(lang, "crm.team_not_found")})
		return
	}
	http.ServeFile(w, r, filepath.Join(crmUploadDir(team.ID), filename))
}

// ---- Members ----

func crmTeamMembersPageData(a *app.App, w http.ResponseWriter, lang string, currentUser *store.User, team *store.CRMTeam, role string) (map[string]any, bool) {
	members, err := a.Store.ListCRMTeamMembers(team.ID)
	if err != nil {
		log.Printf("list crm team members error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	roster, err := a.Store.ListMembersCreatedBy(currentUser.ID)
	if err != nil {
		log.Printf("list roster error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return nil, false
	}
	alreadyIn := map[int64]bool{}
	for _, m := range members {
		alreadyIn[m.UserID] = true
	}
	var assignable []*store.User
	for _, u := range roster {
		if !alreadyIn[u.ID] {
			assignable = append(assignable, u)
		}
	}
	return map[string]any{
		"CurrentUser":       currentUser,
		"ActiveNav":         "crm",
		"PageTitle":         i18n.T(lang, "crm.members_title"),
		"CRMTeam":           team,
		"Members":           members,
		"AssignableMembers": assignable,
		"CanManageMembers":  roles.HasPermission(role, roles.PermMembersManage),
		"AssignableRoles":   roles.AssignableRolesWithLabels(lang, role),
		"Breadcrumb": []app.Breadcrumb{
			{Label: i18n.T(lang, "nav.crm"), URL: "/crm"},
			{Label: team.Name, URL: "/crm/" + team.Slug},
			{Label: i18n.T(lang, "crm.members_title")},
		},
		"HeaderTitle": i18n.T(lang, "crm.members_title"),
		"HeaderIcon":  "person",
	}, true
}

func HandleCRMTeamMembersPage(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	data, ok := crmTeamMembersPageData(a, w, lang, currentUser, team, role)
	if !ok {
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		data["MemberError"] = errMsg
	}
	a.Render(w, r, "crm_team_members.html", data)
}

func HandleAddCRMTeamMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}

	userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	targetRole := strings.TrimSpace(r.FormValue("role"))

	renderError := func(msg string) {
		data, ok := crmTeamMembersPageData(a, w, lang, currentUser, team, role)
		if !ok {
			return
		}
		data["MemberError"] = msg
		a.Render(w, r, "crm_team_members.html", data)
	}

	if userID == 0 || !roles.CanAssignRole(role, targetRole) {
		renderError(i18n.T(lang, "workspace_detail.invalid_username_or_role"))
		return
	}
	target, err := a.Store.GetUserByID(userID)
	if err != nil {
		log.Printf("lookup user error: %v", err)
		renderError(i18n.T(lang, "common.error_generic_retry"))
		return
	}
	if target == nil || target.CreatedBy != currentUser.ID {
		renderError(i18n.T(lang, "workspace_detail.not_your_member"))
		return
	}
	if err := a.Store.AddCRMTeamMember(team.ID, target.ID, targetRole); err != nil {
		if err == store.ErrAlreadyCRMMember {
			renderError(i18n.T(lang, "workspace_detail.already_member"))
		} else {
			log.Printf("add crm team member error: %v", err)
			renderError(i18n.T(lang, "common.error_generic_retry"))
		}
		return
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMMemberAdd, Details: map[string]any{"teamName": team.Name, "username": target.Username, "role": roles.RoleLabel(lang, targetRole)}})

	http.Redirect(w, r, "/crm/"+team.Slug+"/members", http.StatusSeeOther)
}

func HandleRemoveCRMTeamMember(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := a.Store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if err := a.Store.RemoveCRMTeamMember(team.ID, targetID); err != nil {
		if err == store.ErrLastCRMOwner {
			http.Redirect(w, r, "/crm/"+team.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
			return
		}
		log.Printf("remove crm team member error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	username := ""
	if target != nil {
		username = target.Username
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMMemberRemove, Details: map[string]any{"teamName": team.Name, "username": username}})

	http.Redirect(w, r, "/crm/"+team.Slug+"/members", http.StatusSeeOther)
}

func HandleUpdateCRMTeamMemberRole(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	lang := a.ResolveLang(r)
	if !roles.HasPermission(role, roles.PermMembersManage) {
		http.Error(w, i18n.T(lang, "common.access_denied"), http.StatusForbidden)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("userID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newRole := strings.TrimSpace(r.FormValue("role"))
	if !roles.CanAssignRole(role, newRole) {
		http.Redirect(w, r, "/crm/"+team.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_detail.invalid_username_or_role")), http.StatusSeeOther)
		return
	}
	target, err := a.Store.GetUserByID(targetID)
	if err != nil {
		log.Printf("get user error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	if err := a.Store.UpdateCRMTeamMemberRole(team.ID, targetID, newRole); err != nil {
		if err == store.ErrLastCRMOwner {
			http.Redirect(w, r, "/crm/"+team.Slug+"/members?error="+url.QueryEscape(i18n.T(lang, "workspace_members.cannot_remove_last_owner")), http.StatusSeeOther)
			return
		}
		log.Printf("update crm team member role error: %v", err)
		http.Error(w, "Une erreur est survenue.", http.StatusInternalServerError)
		return
	}
	username := ""
	if target != nil {
		username = target.Username
	}
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMMemberRoleChange, Details: map[string]any{"teamName": team.Name, "username": username, "role": roles.RoleLabel(lang, newRole)}})

	http.Redirect(w, r, "/crm/"+team.Slug+"/members", http.StatusSeeOther)
}

// ---- Public read-only API ----

type apiCRMEntity struct {
	Name    string          `json:"name"`
	Slug    string          `json:"slug"`
	Content json.RawMessage `json:"content,omitempty"`
}

// apiLoadCRMTeam mirrors apiLoadWorkspace: resolves {teamSlug} to a team
// the API-key holder can read, writing the JSON error response otherwise.
func apiLoadCRMTeam(a *app.App, w http.ResponseWriter, r *http.Request, currentUser *store.User) (*store.CRMTeam, bool) {
	lang := a.ResolveLang(r)
	team, err := a.Store.GetCRMTeamBySlug(r.PathValue("teamSlug"))
	if err != nil {
		log.Printf("api get crm team error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return nil, false
	}
	if team == nil {
		webutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(lang, "crm.team_not_found")})
		return nil, false
	}
	role, err := a.Store.GetCRMTeamMemberRole(team.ID, currentUser.ID)
	if err != nil {
		log.Printf("api get crm role error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return nil, false
	}
	if role == "" || !roles.HasPermission(role, roles.PermDataRead) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return nil, false
	}
	return team, true
}

func HandleAPIListCRMEntities(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	team, ok := apiLoadCRMTeam(a, w, r, currentUser)
	if !ok {
		return
	}
	entities, err := a.Store.ListCRMEntitiesForTeam(team.ID)
	if err != nil {
		log.Printf("api list crm entities error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(a.ResolveLang(r), "common.error_generic")})
		return
	}
	out := make([]apiCRMEntity, 0, len(entities))
	for _, e := range entities {
		out = append(out, apiCRMEntity{Name: e.Name, Slug: e.Slug})
	}
	webutil.WriteJSON(w, http.StatusOK, out)
}

func HandleAPIGetCRMEntity(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, ok := apiLoadCRMTeam(a, w, r, currentUser)
	if !ok {
		return
	}
	entity, err := a.Store.GetCRMEntityBySlug(team.ID, r.PathValue("entitySlug"))
	if err != nil {
		log.Printf("api get crm entity error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if entity == nil {
		webutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(lang, "crm.entity_not_found")})
		return
	}
	webutil.WriteJSON(w, http.StatusOK, apiCRMEntity{Name: entity.Name, Slug: entity.Slug, Content: json.RawMessage(entity.ContentJSON)})
}
