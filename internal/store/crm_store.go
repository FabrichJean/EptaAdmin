package store

import (
	"database/sql"
	"eptaadmin/internal/roles"
	"errors"
	"strconv"
	"time"
)

var ErrCRMTeamExists = errors.New("une équipe CRM+ avec ce nom existe déjà")
var ErrAlreadyCRMMember = errors.New("cet utilisateur est déjà membre de cette équipe")
var ErrCRMEntityExists = errors.New("une entité avec ce nom existe déjà dans cette équipe")
var ErrLastCRMOwner = errors.New("impossible de retirer le dernier propriétaire de l'équipe")

// CRMTeam is CRM+'s equivalent of Workspace: a top-level, self-contained
// group with its own membership roster — deliberately not tied to
// workspaces or workspace_members (see store.go's crm_teams table comment).
type CRMTeam struct {
	ID        int64
	Name      string
	Slug      string
	CreatedBy int64
	CreatedAt time.Time
}

type UserCRMTeam struct {
	CRMTeam
	Role string
}

func (t *UserCRMTeam) RoleLabel(lang string) string {
	return roles.RoleLabel(lang, t.Role)
}

type CRMTeamMember struct {
	UserID       int64
	Username     string
	Email        string
	Role         string
	AvatarSeed   string
	AvatarUpload string
	CreatedAt    time.Time
}

func (m *CRMTeamMember) RoleLabel(lang string) string {
	return roles.RoleLabel(lang, m.Role)
}

func (m *CRMTeamMember) AvatarImageURL() string {
	if m.AvatarUpload != "" {
		return m.AvatarUpload
	}
	seed := m.AvatarSeed
	if seed == "" {
		seed = m.Username
	}
	return AvatarURL(seed)
}

type CRMEntity struct {
	ID             int64
	CRMTeamID      int64
	Name           string
	Slug           string
	ContentJSON    string
	DesignHTML     string
	DesignPrompt   string
	ActiveDesignID sql.NullInt64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CRMDesign is a saved, reusable AI-generated rendering — team-scoped so
// it can be applied to more than the one entity it was generated for. See
// store.go's crm_designs table comment.
type CRMDesign struct {
	ID        int64
	CRMTeamID int64
	Name      string
	HTML      string
	Prompt    string
	CreatedBy sql.NullInt64
	CreatedAt time.Time
}

// CreateCRMTeam creates a CRM+ team and adds the creator as its Owner,
// atomically — mirrors Store.CreateWorkspace exactly.
func (s *Store) CreateCRMTeam(name string, creatorID int64) (*CRMTeam, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	base := slugify(name)
	if base == "" {
		base = "team"
	}
	slug := base
	for suffix := 2; ; suffix++ {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM crm_teams WHERE slug = ?`, slug).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			break
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}

	res, err := tx.Exec(
		`INSERT INTO crm_teams (name, slug, created_by) VALUES (?, ?, ?)`,
		name, slug, creatorID,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrCRMTeamExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO crm_team_members (crm_team_id, user_id, role) VALUES (?, ?, ?)`,
		id, creatorID, roles.RoleOwner,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetCRMTeamByID(id)
}

func scanCRMTeam(row *sql.Row) (*CRMTeam, error) {
	t := &CRMTeam{}
	err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedBy, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) GetCRMTeamByID(id int64) (*CRMTeam, error) {
	row := s.db.QueryRow(`SELECT id, name, slug, created_by, created_at FROM crm_teams WHERE id = ?`, id)
	return scanCRMTeam(row)
}

func (s *Store) GetCRMTeamBySlug(slug string) (*CRMTeam, error) {
	row := s.db.QueryRow(`SELECT id, name, slug, created_by, created_at FROM crm_teams WHERE slug = ?`, slug)
	return scanCRMTeam(row)
}

func (s *Store) ListCRMTeamsForUser(userID int64) ([]*UserCRMTeam, error) {
	rows, err := s.db.Query(`
		SELECT crm_teams.id, crm_teams.name, crm_teams.slug, crm_teams.created_by, crm_teams.created_at, crm_team_members.role
		FROM crm_team_members
		JOIN crm_teams ON crm_teams.id = crm_team_members.crm_team_id
		WHERE crm_team_members.user_id = ?
		ORDER BY crm_teams.created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*UserCRMTeam
	for rows.Next() {
		ut := &UserCRMTeam{}
		if err := rows.Scan(&ut.ID, &ut.Name, &ut.Slug, &ut.CreatedBy, &ut.CreatedAt, &ut.Role); err != nil {
			return nil, err
		}
		out = append(out, ut)
	}
	return out, rows.Err()
}

func (s *Store) GetCRMTeamMemberRole(teamID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(
		`SELECT role FROM crm_team_members WHERE crm_team_id = ? AND user_id = ?`,
		teamID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (s *Store) ListCRMTeamMembers(teamID int64) ([]*CRMTeamMember, error) {
	rows, err := s.db.Query(`
		SELECT users.id, users.username, users.email, crm_team_members.role, users.avatar_seed, users.avatar_upload, crm_team_members.created_at
		FROM crm_team_members
		JOIN users ON users.id = crm_team_members.user_id
		WHERE crm_team_members.crm_team_id = ?
		ORDER BY crm_team_members.created_at
	`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CRMTeamMember
	for rows.Next() {
		m := &CRMTeamMember{}
		if err := rows.Scan(&m.UserID, &m.Username, &m.Email, &m.Role, &m.AvatarSeed, &m.AvatarUpload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddCRMTeamMember(teamID, userID int64, role string) error {
	_, err := s.db.Exec(
		`INSERT INTO crm_team_members (crm_team_id, user_id, role) VALUES (?, ?, ?)`,
		teamID, userID, role,
	)
	if isUniqueConstraintErr(err) {
		return ErrAlreadyCRMMember
	}
	return err
}

func (s *Store) UpdateCRMTeamMemberRole(teamID, userID int64, newRole string) error {
	current, err := s.GetCRMTeamMemberRole(teamID, userID)
	if err != nil {
		return err
	}
	if current == roles.RoleOwner && newRole != roles.RoleOwner {
		var ownerCount int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM crm_team_members WHERE crm_team_id = ? AND role = ?`,
			teamID, roles.RoleOwner,
		).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastCRMOwner
		}
	}
	_, err = s.db.Exec(
		`UPDATE crm_team_members SET role = ? WHERE crm_team_id = ? AND user_id = ?`,
		newRole, teamID, userID,
	)
	return err
}

func (s *Store) RemoveCRMTeamMember(teamID, userID int64) error {
	role, err := s.GetCRMTeamMemberRole(teamID, userID)
	if err != nil {
		return err
	}
	if role == roles.RoleOwner {
		var ownerCount int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM crm_team_members WHERE crm_team_id = ? AND role = ?`,
			teamID, roles.RoleOwner,
		).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastCRMOwner
		}
	}
	_, err = s.db.Exec(`DELETE FROM crm_team_members WHERE crm_team_id = ? AND user_id = ?`, teamID, userID)
	return err
}

// uniqueCRMEntitySlug computes a slug for name that isn't already used by
// another entity in the same team.
func (s *Store) uniqueCRMEntitySlug(teamID int64, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "entity"
	}
	slug := base
	for suffix := 2; ; suffix++ {
		var exists int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM crm_entities WHERE crm_team_id = ? AND slug = ?`,
			teamID, slug,
		).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}
}

func (s *Store) CreateCRMEntity(teamID int64, name string) (*CRMEntity, error) {
	slug, err := s.uniqueCRMEntitySlug(teamID, name)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`INSERT INTO crm_entities (crm_team_id, name, slug) VALUES (?, ?, ?)`,
		teamID, name, slug,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrCRMEntityExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetCRMEntity(id)
}

const crmEntityColumns = `id, crm_team_id, name, slug, content_json, design_html, design_prompt, active_design_id, created_at, updated_at`

func scanCRMEntity(row *sql.Row) (*CRMEntity, error) {
	e := &CRMEntity{}
	err := row.Scan(&e.ID, &e.CRMTeamID, &e.Name, &e.Slug, &e.ContentJSON, &e.DesignHTML, &e.DesignPrompt, &e.ActiveDesignID, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Store) GetCRMEntity(id int64) (*CRMEntity, error) {
	row := s.db.QueryRow(`SELECT `+crmEntityColumns+` FROM crm_entities WHERE id = ?`, id)
	return scanCRMEntity(row)
}

func (s *Store) GetCRMEntityBySlug(teamID int64, slug string) (*CRMEntity, error) {
	row := s.db.QueryRow(`SELECT `+crmEntityColumns+` FROM crm_entities WHERE crm_team_id = ? AND slug = ?`, teamID, slug)
	return scanCRMEntity(row)
}

func (s *Store) ListCRMEntitiesForTeam(teamID int64) ([]*CRMEntity, error) {
	rows, err := s.db.Query(`SELECT `+crmEntityColumns+` FROM crm_entities WHERE crm_team_id = ? ORDER BY created_at`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CRMEntity
	for rows.Next() {
		e := &CRMEntity{}
		if err := rows.Scan(&e.ID, &e.CRMTeamID, &e.Name, &e.Slug, &e.ContentJSON, &e.DesignHTML, &e.DesignPrompt, &e.ActiveDesignID, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateCRMEntityContent replaces the entity's whole content tree — the
// editor is whole-tree-save, not granular (see plan). contentJSON must
// already be validated well-formed JSON by the caller.
func (s *Store) UpdateCRMEntityContent(id int64, contentJSON string) error {
	_, err := s.db.Exec(`UPDATE crm_entities SET content_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, contentJSON, id)
	return err
}

// CreateCRMDesign saves a generated rendering to the team's reusable
// library — called right when generation succeeds (not only when applied),
// so nothing generated is ever lost to a discarded draft.
func (s *Store) CreateCRMDesign(teamID int64, name, html, prompt string, createdBy int64) (*CRMDesign, error) {
	res, err := s.db.Exec(
		`INSERT INTO crm_designs (crm_team_id, name, html, prompt, created_by) VALUES (?, ?, ?, ?, ?)`,
		teamID, name, html, prompt, createdBy,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetCRMDesign(id)
}

const crmDesignColumns = `id, crm_team_id, name, html, prompt, created_by, created_at`

func scanCRMDesign(row *sql.Row) (*CRMDesign, error) {
	d := &CRMDesign{}
	err := row.Scan(&d.ID, &d.CRMTeamID, &d.Name, &d.HTML, &d.Prompt, &d.CreatedBy, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) GetCRMDesign(id int64) (*CRMDesign, error) {
	row := s.db.QueryRow(`SELECT `+crmDesignColumns+` FROM crm_designs WHERE id = ?`, id)
	return scanCRMDesign(row)
}

// GetCRMDesignForTeam scopes the lookup to teamID so an id belonging to
// another team's library can never be applied cross-team.
func (s *Store) GetCRMDesignForTeam(teamID, id int64) (*CRMDesign, error) {
	row := s.db.QueryRow(`SELECT `+crmDesignColumns+` FROM crm_designs WHERE id = ? AND crm_team_id = ?`, id, teamID)
	return scanCRMDesign(row)
}

// ListCRMDesignsForTeam returns the team's whole design library, newest
// first — this is the "choisir un autre modèle" picker's data source.
func (s *Store) ListCRMDesignsForTeam(teamID int64) ([]*CRMDesign, error) {
	rows, err := s.db.Query(`SELECT `+crmDesignColumns+` FROM crm_designs WHERE crm_team_id = ? ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CRMDesign
	for rows.Next() {
		d := &CRMDesign{}
		if err := rows.Scan(&d.ID, &d.CRMTeamID, &d.Name, &d.HTML, &d.Prompt, &d.CreatedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteCRMDesign removes a design from the team's library. Any entity
// currently showing it (active_design_id points at it) has its cached
// design_html/prompt cleared and reverts to the default tree view too —
// ON DELETE SET NULL only clears active_design_id itself, not the
// denormalized cache columns, so those need clearing explicitly or a
// deleted-but-still-cached design would keep rendering.
func (s *Store) DeleteCRMDesign(teamID, id int64) error {
	if _, err := s.db.Exec(
		`UPDATE crm_entities SET active_design_id = NULL, design_html = '', design_prompt = '' WHERE crm_team_id = ? AND active_design_id = ?`,
		teamID, id,
	); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM crm_designs WHERE id = ? AND crm_team_id = ?`, id, teamID)
	return err
}

// SetCRMEntityActiveDesign applies a library design to an entity: it
// records which design is active AND caches its html/prompt directly on
// the entity row (design_html/design_prompt) so reading the entity never
// needs a join just to render it.
func (s *Store) SetCRMEntityActiveDesign(entityID, designID int64, html, prompt string) error {
	_, err := s.db.Exec(
		`UPDATE crm_entities SET active_design_id = ?, design_html = ?, design_prompt = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		designID, html, prompt, entityID,
	)
	return err
}

// ClearCRMEntityDesign reverts an entity to the default tree view — the
// design itself stays in the team's library, only the entity's selection
// is cleared.
func (s *Store) ClearCRMEntityDesign(id int64) error {
	_, err := s.db.Exec(`UPDATE crm_entities SET active_design_id = NULL, design_html = '', design_prompt = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteCRMEntity(id int64) error {
	_, err := s.db.Exec(`DELETE FROM crm_entities WHERE id = ?`, id)
	return err
}
