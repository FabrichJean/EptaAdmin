package main

import (
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrWorkspaceExists = errors.New("un workspace avec ce nom existe déjà")
var ErrAlreadyMember = errors.New("cet utilisateur est déjà membre de ce workspace")
var ErrDataSourceExists = errors.New("un data source avec ce nom existe déjà dans ce workspace")
var ErrNotFound = errors.New("introuvable")

type Workspace struct {
	ID        int64
	Name      string
	Slug      string
	CreatedBy int64
	CreatedAt time.Time
}

// UserWorkspace is a workspace paired with the requesting user's role in it.
type UserWorkspace struct {
	Workspace
	Role string
}

func (w *UserWorkspace) RoleLabel() string {
	return roleLabel(w.Role)
}

type WorkspaceMember struct {
	UserID       int64
	Username     string
	Email        string
	Role         string
	AvatarSeed   string
	AvatarUpload string
	CreatedAt    time.Time
}

func (m *WorkspaceMember) RoleLabel() string {
	return roleLabel(m.Role)
}

// AvatarImageURL mirrors User.AvatarImageURL — a member's avatar follows
// the same custom-upload-then-generated-seed-then-username priority.
func (m *WorkspaceMember) AvatarImageURL() string {
	if m.AvatarUpload != "" {
		return m.AvatarUpload
	}
	seed := m.AvatarSeed
	if seed == "" {
		seed = m.Username
	}
	return AvatarURL(seed)
}

type DataSource struct {
	ID          int64
	WorkspaceID int64
	Name        string
	Slug        string
	Type        string
	StoragePath string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

var slugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := slugSanitizer.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

// CreateWorkspace creates a workspace and adds the creator as its Owner,
// atomically.
func (s *Store) CreateWorkspace(name string, creatorID int64) (*Workspace, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	base := slugify(name)
	if base == "" {
		base = "workspace"
	}
	slug := base
	for suffix := 2; ; suffix++ {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE slug = ?`, slug).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			break
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}

	res, err := tx.Exec(
		`INSERT INTO workspaces (name, slug, created_by) VALUES (?, ?, ?)`,
		name, slug, creatorID,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrWorkspaceExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, ?)`,
		id, creatorID, RoleOwner,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetWorkspaceByID(id)
}

func (s *Store) GetWorkspaceByID(id int64) (*Workspace, error) {
	row := s.db.QueryRow(
		`SELECT id, name, slug, created_by, created_at FROM workspaces WHERE id = ?`,
		id,
	)
	return scanWorkspace(row)
}

func (s *Store) GetWorkspaceBySlug(slug string) (*Workspace, error) {
	row := s.db.QueryRow(
		`SELECT id, name, slug, created_by, created_at FROM workspaces WHERE slug = ?`,
		slug,
	)
	return scanWorkspace(row)
}

func scanWorkspace(row *sql.Row) (*Workspace, error) {
	w := &Workspace{}
	err := row.Scan(&w.ID, &w.Name, &w.Slug, &w.CreatedBy, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

// ListWorkspacesForUser returns every workspace the given user belongs to,
// along with their role in each.
func (s *Store) ListWorkspacesForUser(userID int64) ([]*UserWorkspace, error) {
	rows, err := s.db.Query(`
		SELECT workspaces.id, workspaces.name, workspaces.slug, workspaces.created_by, workspaces.created_at, workspace_members.role
		FROM workspace_members
		JOIN workspaces ON workspaces.id = workspace_members.workspace_id
		WHERE workspace_members.user_id = ?
		ORDER BY workspaces.created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*UserWorkspace
	for rows.Next() {
		uw := &UserWorkspace{}
		if err := rows.Scan(&uw.ID, &uw.Name, &uw.Slug, &uw.CreatedBy, &uw.CreatedAt, &uw.Role); err != nil {
			return nil, err
		}
		out = append(out, uw)
	}
	return out, rows.Err()
}

// GetWorkspaceMemberRole returns the caller's role in a workspace, or ""
// if they are not a member.
func (s *Store) GetWorkspaceMemberRole(workspaceID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		workspaceID, userID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (s *Store) ListWorkspaceMembers(workspaceID int64) ([]*WorkspaceMember, error) {
	rows, err := s.db.Query(`
		SELECT users.id, users.username, users.email, workspace_members.role, users.avatar_seed, users.avatar_upload, workspace_members.created_at
		FROM workspace_members
		JOIN users ON users.id = workspace_members.user_id
		WHERE workspace_members.workspace_id = ?
		ORDER BY workspace_members.created_at
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*WorkspaceMember
	for rows.Next() {
		m := &WorkspaceMember{}
		if err := rows.Scan(&m.UserID, &m.Username, &m.Email, &m.Role, &m.AvatarSeed, &m.AvatarUpload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddWorkspaceMember(workspaceID, userID int64, role string) error {
	_, err := s.db.Exec(
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, ?)`,
		workspaceID, userID, role,
	)
	if isUniqueConstraintErr(err) {
		return ErrAlreadyMember
	}
	return err
}

// uniqueDataSourceSlug computes a slug for name that isn't already used by
// another data source in the same workspace.
func (s *Store) uniqueDataSourceSlug(workspaceID int64, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "data"
	}
	slug := base
	for suffix := 2; ; suffix++ {
		var exists int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM data_sources WHERE workspace_id = ? AND slug = ?`,
			workspaceID, slug,
		).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}
}

// CreateDataSource registers a new data source. The server decides where the
// underlying JSON file lives — callers never supply a path (see spec2 §3/§5:
// "le frontend ne connaît même pas le chemin réel").
func (s *Store) CreateDataSource(workspaceID int64, name, sourceType string) (*DataSource, error) {
	storagePath, err := newDataSourcePath(workspaceID, name)
	if err != nil {
		return nil, err
	}
	slug, err := s.uniqueDataSourceSlug(workspaceID, name)
	if err != nil {
		return nil, err
	}

	res, err := s.db.Exec(
		`INSERT INTO data_sources (workspace_id, name, slug, type, storage_path) VALUES (?, ?, ?, ?, ?)`,
		workspaceID, name, slug, sourceType, storagePath,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrDataSourceExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetDataSource(id)
}

const dataSourceColumns = `id, workspace_id, name, slug, type, storage_path, version, created_at, updated_at`

func scanDataSource(row *sql.Row) (*DataSource, error) {
	ds := &DataSource{}
	err := row.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &ds.Slug, &ds.Type, &ds.StoragePath, &ds.Version, &ds.CreatedAt, &ds.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ds, nil
}

func (s *Store) GetDataSource(id int64) (*DataSource, error) {
	row := s.db.QueryRow(`SELECT `+dataSourceColumns+` FROM data_sources WHERE id = ?`, id)
	return scanDataSource(row)
}

func (s *Store) GetDataSourceBySlug(workspaceID int64, slug string) (*DataSource, error) {
	row := s.db.QueryRow(
		`SELECT `+dataSourceColumns+` FROM data_sources WHERE workspace_id = ? AND slug = ?`,
		workspaceID, slug,
	)
	return scanDataSource(row)
}

func (s *Store) ListDataSources(workspaceID int64) ([]*DataSource, error) {
	rows, err := s.db.Query(
		`SELECT `+dataSourceColumns+` FROM data_sources WHERE workspace_id = ? ORDER BY created_at`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DataSource
	for rows.Next() {
		ds := &DataSource{}
		if err := rows.Scan(&ds.ID, &ds.WorkspaceID, &ds.Name, &ds.Slug, &ds.Type, &ds.StoragePath, &ds.Version, &ds.CreatedAt, &ds.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}

// BumpDataSourceVersion increments a data source's version, but only if it
// currently matches expectedVersion. It reports whether the bump happened —
// false means a concurrent writer already moved the version on (409 case).
func (s *Store) BumpDataSourceVersion(id int64, expectedVersion int) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE data_sources SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND version = ?`,
		id, expectedVersion,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
