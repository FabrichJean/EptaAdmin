package crm

import (
	"encoding/base64"
	"encoding/json"
	"eptaadmin/internal/app"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/nvidia"
	"eptaadmin/internal/roles"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
	"eptaadmin/internal/webutil"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file is CRM+'s "design" feature: an entity's data can be shown
// through a custom, AI-generated HTML/CSS/JS rendering instead of the
// default tree view — see the plan discussed with the user: private to
// EptaAdmin (not a public page), generated from a text description plus
// an optional reference image via NVIDIA's API (nvidia.go), previewed
// client-side before being applied. The generated template never receives
// data through Go template substitution — it reads it from a JS global
// (window.__CRM_ENTITY_DATA__) injected at render time, so nothing the
// model outputs can break template execution (see crm_entity_editor.html).

const crmDesignMaxSampleLen = 400 // per-string-value cap when building the data-shape sample sent to the model

// truncateForPrompt walks an arbitrary JSON value (as decoded by
// encoding/json — map[string]any / []any / string / float64 / bool / nil)
// and shortens long strings, keeping the prompt sent to the model bounded
// regardless of how much markdown content an entity holds.
func truncateForPrompt(v any) any {
	switch val := v.(type) {
	case string:
		if len(val) > crmDesignMaxSampleLen {
			return val[:crmDesignMaxSampleLen] + "…"
		}
		return val
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = truncateForPrompt(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = truncateForPrompt(item)
		}
		return out
	default:
		return v
	}
}

func buildCRMDesignSample(contentJSON string) (string, error) {
	var parsed any
	if err := json.Unmarshal([]byte(contentJSON), &parsed); err != nil {
		return "", err
	}
	truncated := truncateForPrompt(parsed)
	out, err := json.MarshalIndent(truncated, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// crmDesignThemePrompt describes EptaAdmin's own dark theme (exact values
// from templates/layout.html's :root CSS variables) so generated designs
// read as part of the same platform instead of a generic light template.
const crmDesignThemePrompt = `Charte visuelle à respecter pour que le rendu s'intègre à la plateforme (EptaAdmin, thème sombre) :
- Fond de page : #0a0a0b
- Fond des cartes/blocs : #131316 (variante plus claire pour un bloc imbriqué : #1c1c20)
- Bordures : rgba(255,255,255,0.08) (rgba(255,255,255,0.16) pour une bordure plus marquée)
- Texte principal : #f4f4f5
- Texte secondaire/discret : #9ca3af
- Couleur d'accent (liens, boutons principaux, badges de mise en avant) : #34d399 (variante plus foncée au survol : #10b981)
- Police : sans-serif système (font-family: -apple-system, "Segoe UI", Inter, sans-serif)
- Coins arrondis (8-12px), espacements généreux, esthétique sobre et professionnelle — pas de fond blanc, pas de couleurs criardes hors de cette palette, sauf si la description demandée l'exige explicitement.`

const crmDesignSystemPrompt = `Tu es un générateur de template HTML pour un CMS interne. On te fournit :
1. Un échantillon des données actuelles de l'entité, sous forme d'un tableau JSON d'éléments récursifs {type, key, value, children} — "type" vaut "text", "markdown", "image", "number", "boolean", "list" ou "object" ; "value" contient le contenu pour les types simples ; "children" est un tableau du même format pour "list"/"object".
2. Une description du rendu souhaité, éventuellement accompagnée d'une image de référence pour le style visuel.
3. La charte visuelle de la plateforme à respecter (voir plus bas), sauf si la description demande explicitement un style différent.

Génère un document HTML autonome et complet (<!DOCTYPE html>, CSS inline dans une balise <style>, JS inline dans une balise <script>) qui affiche joliment ces données selon la description, dans le thème sombre de la plateforme.

Règle absolue : ne mets AUCUNE valeur des données en dur dans le HTML. Le document doit lire ses données au moment de l'affichage depuis la variable JavaScript globale window.__CRM_ENTITY_DATA__, qui contiendra un tableau exactement dans ce même format {type, key, value, children}. Écris le JavaScript nécessaire pour parcourir cette structure récursivement (children pour "list"/"object") et construire l'affichage dynamiquement — jamais de contenu texte copié depuis l'échantillon directement dans le HTML.

Autre règle absolue : ce document n'est PAS une page de site autonome, c'est un aperçu de contenu affiché dans un cadre interne — il n'y a pas d'autre page vers laquelle naviguer. Ne génère donc aucun élément de navigation VERS L'EXTÉRIEUR de ce document : pas de <nav>, pas de menu de site, pas de header/footer de site, pas de lien <a href="..."> vers une autre page, une autre URL ou un autre document.

En revanche, à l'intérieur de ce même document, les interactions en JavaScript restent encouragées quand elles servent à explorer les données : un clic sur un élément de liste (ex : un article) qui affiche son détail complet (contenu markdown entier, etc.) dans une modale, un panneau qui se déplie, ou en remplaçant la vue courante par la vue détaillée — tout cela reste dans CE document, ne charge aucune autre page, et n'est donc pas de la "navigation" au sens interdit ci-dessus. Si les données contiennent un champ de type "markdown" ou un texte long qui n'est pas montré en entier dans une vue de liste, prévois systématiquement un moyen (clic, bouton "Lire la suite", etc.) d'en afficher le contenu complet quelque part dans le document.

Robustesse du JavaScript : si tu écris un petit convertisseur markdown→HTML (gras, italique, titres, etc.), fais bien attention à la chaîne sur laquelle chaque expression régulière s'applique — une regex ancrée en début de chaîne (^) doit s'appliquer sur le texte original, jamais sur une version déjà transformée/enveloppée (ex : déjà entourée de balises <p>), sous peine de ne jamais matcher. Plus généralement, ne suppose jamais qu'un appel à .match(), .querySelector(), .exec() ou similaire renvoie un résultat non nul : vérifie-le avant d'accéder à une propriété ou un index dessus.

Échappement obligatoire : les valeurs de données sont du texte réel (souvent en français, donc avec des apostrophes, guillemets, accents) — ne les insère JAMAIS brutes dans un attribut HTML construit par interpolation de chaîne (ex : data-title='${title}' ou class="${value}"), car une seule apostrophe dans la valeur termine l'attribut prématurément et corrompt tout le reste du HTML généré. De même, insérer une valeur brute via innerHTML sans échappement peut casser la structure si elle contient des caractères <, >, & ou des guillemets. Deux approches sûres : (1) construire les éléments avec createElement/textContent/setAttribute (le DOM échappe automatiquement), ou (2) si tu construis du HTML par concaténation de chaînes, passe systématiquement chaque valeur dynamique par une fonction d'échappement HTML (remplaçant au minimum & < > " ') avant de l'insérer, y compris à l'intérieur d'attributs comme data-*.

Réponds UNIQUEMENT avec le document HTML complet, sans aucune explication avant ou après, sans balises de code markdown (pas de blocs ` + "```" + `).

` + crmDesignThemePrompt

type crmDesignGenerateResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	HTML string `json:"html"`
}

// crmDesignNameFromPrompt derives a short, human-readable library entry
// name from the free-form description — just enough to tell entries apart
// in the picker, not a real summary.
func crmDesignNameFromPrompt(description string) string {
	name := description
	if len(name) > 60 {
		name = name[:60] + "…"
	}
	return name
}

// handleGenerateCRMEntityDesign calls the NVIDIA model to draft a design —
// never persisted here; the browser previews it and a separate call
// (handleApplyCRMEntityDesign) is what actually saves it. Every step is
// logged with a "[crm-design]" prefix (team/entity/user identifying the
// request) so a failed or slow generation can be traced server-side,
// alongside the matching console logs the browser prints for the same call
// (see crm_entity_editor.html).
func HandleGenerateCRMEntityDesign(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	logPrefix := fmt.Sprintf("[crm-design] team=%s user=%d", team.Slug, currentUser.ID)
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		log.Printf("%s entity=%s denied: role=%s lacks data.update", logPrefix, r.PathValue("entitySlug"), role)
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	entity, err := a.Store.GetCRMEntityBySlug(team.ID, r.PathValue("entitySlug"))
	if err != nil {
		log.Printf("%s get entity error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if entity == nil {
		http.NotFound(w, r)
		return
	}
	logPrefix = fmt.Sprintf("%s entity=%s", logPrefix, entity.Slug)
	log.Printf("%s generate: request received", logPrefix)

	r.Body = http.MaxBytesReader(w, r.Body, uploads.MaxUploadSize+1<<20)
	if err := r.ParseMultipartForm(uploads.MaxUploadSize + 1<<20); err != nil {
		log.Printf("%s generate: form too large: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_form_too_large")})
		return
	}
	description := r.FormValue("description")
	if description == "" {
		log.Printf("%s generate: missing description", logPrefix)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_description_required")})
		return
	}

	sample, err := buildCRMDesignSample(entity.ContentJSON)
	if err != nil {
		log.Printf("%s generate: build data sample error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}

	userText := "Échantillon des données actuelles :\n" + sample + "\n\nDescription du rendu souhaité :\n" + description

	model := nvidia.Model()
	var content any = userText
	hasImage := false

	if file, header, ferr := r.FormFile("image"); ferr == nil {
		defer file.Close()
		hasImage = true
		if header.Size > uploads.MaxUploadSize {
			log.Printf("%s generate: reference image too large (%d bytes)", logPrefix, header.Size)
			webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_image_too_large")})
			return
		}
		data := make([]byte, header.Size)
		if _, err := io.ReadFull(file, data); err != nil {
			log.Printf("%s generate: read reference image error: %v", logPrefix, err)
			webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
			return
		}
		mimeType := header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "image/png"
		}
		dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
		model = nvidia.VisionModel()
		content = []nvidia.ContentPart{
			{Type: "text", Text: userText},
			{Type: "image_url", ImageURL: &nvidia.ImageURL{URL: dataURL}},
		}
	}

	log.Printf("%s generate: calling NVIDIA model=%s description_len=%d sample_len=%d has_image=%v",
		logPrefix, model, len(description), len(sample), hasImage)

	messages := []nvidia.Message{
		{Role: "system", Content: crmDesignSystemPrompt},
		{Role: "user", Content: content},
	}

	start := time.Now()
	reply, err := nvidia.CallChat(r.Context(), model, messages, 8192)
	elapsed := time.Since(start)
	if err != nil {
		if errors.Is(err, nvidia.ErrNotConfigured) {
			log.Printf("%s generate: NVIDIA_API_KEY not configured", logPrefix)
			webutil.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": i18n.T(lang, "crm.design_not_configured")})
			return
		}
		log.Printf("%s generate: NVIDIA call failed after %s: %v", logPrefix, elapsed, err)
		webutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": i18n.T(lang, "crm.design_generate_failed")})
		return
	}
	log.Printf("%s generate: NVIDIA replied in %s, reply_len=%d", logPrefix, elapsed, len(reply))

	html := nvidia.StripCodeFence(reply)
	if html == "" {
		log.Printf("%s generate: empty HTML after stripping code fence (raw reply_len=%d)", logPrefix, len(reply))
		webutil.WriteJSON(w, http.StatusBadGateway, map[string]string{"error": i18n.T(lang, "crm.design_generate_failed")})
		return
	}
	log.Printf("%s generate: success, html_len=%d", logPrefix, len(html))

	// Persisted immediately into the team's reusable library — a generation
	// is never lost just because the browser never applies it.
	design, err := a.Store.CreateCRMDesign(team.ID, crmDesignNameFromPrompt(description), html, description, currentUser.ID)
	if err != nil {
		log.Printf("%s generate: save to library error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	log.Printf("%s generate: saved to library as design_id=%d", logPrefix, design.ID)

	webutil.WriteJSON(w, http.StatusOK, crmDesignGenerateResponse{ID: design.ID, Name: design.Name, HTML: design.HTML})
}

type crmDesignApplyRequest struct {
	DesignID int64 `json:"designId"`
}

// handleApplyCRMEntityDesign applies a design already saved in the team's
// library (every generation is persisted there — see
// handleGenerateCRMEntityDesign) as the entity's active rendering. This is
// also how "choisir un autre modèle" works: the same endpoint, just a
// different designId, picked from the library instead of freshly generated.
func HandleApplyCRMEntityDesign(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
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

	logPrefix := fmt.Sprintf("[crm-design] team=%s entity=%s user=%d", team.Slug, entity.Slug, currentUser.ID)

	var body crmDesignApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil || body.DesignID == 0 {
		log.Printf("%s apply: invalid body: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.invalid_content")})
		return
	}

	// Scoped to this team — a designId from another team's library (even
	// one an attacker merely guesses) can never be applied here.
	design, err := a.Store.GetCRMDesignForTeam(team.ID, body.DesignID)
	if err != nil {
		log.Printf("%s apply: lookup design error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if design == nil {
		log.Printf("%s apply: design_id=%d not found in this team's library", logPrefix, body.DesignID)
		webutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(lang, "crm.design_not_found")})
		return
	}

	if err := a.Store.SetCRMEntityActiveDesign(entity.ID, design.ID, design.HTML, design.Prompt); err != nil {
		log.Printf("%s apply: update error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	log.Printf("%s apply: applied design_id=%d (%s)", logPrefix, design.ID, design.Name)
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityDesignApply, Details: map[string]any{"teamName": team.Name, "name": entity.Name, "designName": design.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "html": design.HTML, "name": design.Name})
}

const crmDesignMaxImportSize = 2 << 20 // 2 MB — generous for a static HTML/CSS/JS template

type crmDesignImportResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	HTML string `json:"html"`
}

// handleImportCRMDesign adds a hand-written or previously exported HTML
// template straight to the team's reusable library — the same library
// AI-generated designs are saved to (see handleGenerateCRMEntityDesign),
// so it's picked, previewed and applied through the exact same flow
// afterward. Team-scoped, not entity-scoped: importing doesn't need an
// entity's data at all, only NVIDIA generation does (to build the sample).
func HandleImportCRMDesign(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	logPrefix := fmt.Sprintf("[crm-design] team=%s user=%d", team.Slug, currentUser.ID)
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		log.Printf("%s import: denied, role=%s lacks data.update", logPrefix, role)
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, crmDesignMaxImportSize+1<<20)
	if err := r.ParseMultipartForm(crmDesignMaxImportSize + 1<<20); err != nil {
		log.Printf("%s import: form too large: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_form_too_large")})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("%s import: missing file: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_import_file_required")})
		return
	}
	defer file.Close()
	if header.Size > crmDesignMaxImportSize {
		log.Printf("%s import: file too large (%d bytes)", logPrefix, header.Size)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_import_too_large")})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, crmDesignMaxImportSize+1))
	if err != nil {
		log.Printf("%s import: read error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	html := strings.TrimSpace(string(data))
	if html == "" {
		log.Printf("%s import: empty file", logPrefix)
		webutil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": i18n.T(lang, "crm.design_import_empty")})
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	}
	if name == "" {
		name = "design importé"
	}

	design, err := a.Store.CreateCRMDesign(team.ID, name, html, "", currentUser.ID)
	if err != nil {
		log.Printf("%s import: save error: %v", logPrefix, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	log.Printf("%s import: saved to library as design_id=%d, html_len=%d", logPrefix, design.ID, len(html))

	webutil.WriteJSON(w, http.StatusOK, crmDesignImportResponse{ID: design.ID, Name: design.Name, HTML: design.HTML})
}

type crmDesignListItem struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	CreatedAt string `json:"createdAt"`
}

// handleListCRMDesigns returns the team's whole reusable design library —
// used to populate the "choisir un autre modèle" picker in the entity
// editor. Deliberately not scoped to one entity: the whole point of
// persisting every generation is that it can be applied to any entity in
// the team, not just the one it was first generated for.
func HandleListCRMDesigns(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataRead) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	designs, err := a.Store.ListCRMDesignsForTeam(team.ID)
	if err != nil {
		log.Printf("[crm-design] team=%s list error: %v", team.Slug, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	out := make([]crmDesignListItem, 0, len(designs))
	for _, d := range designs {
		out = append(out, crmDesignListItem{ID: d.ID, Name: d.Name, Prompt: d.Prompt, CreatedAt: d.CreatedAt.Local().Format("02/01/2006 15:04")})
	}
	webutil.WriteJSON(w, http.StatusOK, out)
}

// handleDeleteCRMDesign permanently removes a design from the team's
// library — e.g. one found to be broken (see the diagnostic conversation
// this was added for: two generations with real JS bugs needed removing).
// Any entity currently showing it is reverted to the default tree view
// (see store.Store.DeleteCRMDesign).
func HandleDeleteCRMDesign(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
	if !roles.HasPermission(role, roles.PermDataUpdate) {
		webutil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": i18n.T(lang, "common.access_denied")})
		return
	}
	designID, err := strconv.ParseInt(r.PathValue("designID"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	design, err := a.Store.GetCRMDesignForTeam(team.ID, designID)
	if err != nil {
		log.Printf("[crm-design] team=%s get design error: %v", team.Slug, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	if design == nil {
		webutil.WriteJSON(w, http.StatusNotFound, map[string]string{"error": i18n.T(lang, "crm.design_not_found")})
		return
	}
	if err := a.Store.DeleteCRMDesign(team.ID, designID); err != nil {
		log.Printf("[crm-design] team=%s delete design_id=%d error: %v", team.Slug, designID, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	log.Printf("[crm-design] team=%s user=%d deleted design_id=%d (%s)", team.Slug, currentUser.ID, designID, design.Name)
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityDesignClear, Details: map[string]any{"teamName": team.Name, "name": design.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClearCRMEntityDesign reverts an entity to the default tree view.
func HandleClearCRMEntityDesign(a *app.App, w http.ResponseWriter, r *http.Request) {
	currentUser := app.UserFromContext(r)
	lang := a.ResolveLang(r)
	team, role, ok := loadCRMTeamMembership(a, w, r, currentUser)
	if !ok {
		return
	}
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
	if err := a.Store.ClearCRMEntityDesign(entity.ID); err != nil {
		log.Printf("[crm-design] team=%s entity=%s user=%d clear: error: %v", team.Slug, entity.Slug, currentUser.ID, err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return
	}
	log.Printf("[crm-design] team=%s entity=%s user=%d clear: reverted to default view", team.Slug, entity.Slug, currentUser.ID)
	a.LogActivity(app.LogActivityParams{UserID: currentUser.ID, Action: store.ActionCRMEntityDesignClear, Details: map[string]any{"teamName": team.Name, "name": entity.Name}})

	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
