package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrUserExists = errors.New("un utilisateur avec cet identifiant existe déjà")
var ErrInvalidCredentials = errors.New("identifiant ou mot de passe incorrect")
var ErrEmailExists = errors.New("cet email est déjà utilisé par un autre compte")

type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	Role         string
	AvatarSeed   string
	AvatarUpload string
	Language     string
	CreatedAt    time.Time
}

func (u *User) RoleLabel(lang string) string {
	return roleLabel(lang, u.Role)
}

// AvatarImageURL resolves the avatar to display for this user: a custom
// upload takes priority over the generated one, which falls back to the
// username itself when no seed has been explicitly chosen.
func (u *User) AvatarImageURL() string {
	if u.AvatarUpload != "" {
		return u.AvatarUpload
	}
	seed := u.AvatarSeed
	if seed == "" {
		seed = u.Username
	}
	return AvatarURL(seed)
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: avoid concurrent write locks
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS workspaces (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		created_by INTEGER NOT NULL REFERENCES users(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS workspace_members (
		workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (workspace_id, user_id)
	);

	CREATE TABLE IF NOT EXISTS data_sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		slug TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'json',
		storage_path TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (workspace_id, name)
	);

	CREATE TABLE IF NOT EXISTS data_source_columns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data_source_id INTEGER NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'text',
		position INTEGER NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (data_source_id, key)
	);

	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL UNIQUE,
		token_prefix TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS activity_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER REFERENCES workspaces(id) ON DELETE CASCADE,
		data_source_id INTEGER REFERENCES data_sources(id) ON DELETE CASCADE,
		user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
		action TEXT NOT NULL,
		details TEXT NOT NULL DEFAULT '{}',
		reversible INTEGER NOT NULL DEFAULT 0,
		reverted_at DATETIME,
		reverted_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_activity_log_workspace ON activity_log(workspace_id, id DESC);
	CREATE INDEX IF NOT EXISTS idx_activity_log_user ON activity_log(user_id, id DESC);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Columns added after the tables already existed for some installs —
	// add them defensively (SQLite has no "ADD COLUMN IF NOT EXISTS").
	for _, alter := range []string{
		`ALTER TABLE data_sources ADD COLUMN slug TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE data_source_columns ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN avatar_seed TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN avatar_upload TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN language TEXT NOT NULL DEFAULT 'fr'`,
	} {
		if _, err := s.db.Exec(alter); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	return s.backfillDataSourceSlugs()
}

func (s *Store) backfillDataSourceSlugs() error {
	rows, err := s.db.Query(`SELECT id, workspace_id, name FROM data_sources WHERE slug = ''`)
	if err != nil {
		return err
	}
	type pending struct {
		id, workspaceID int64
		name            string
	}
	var toFill []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.workspaceID, &p.name); err != nil {
			rows.Close()
			return err
		}
		toFill = append(toFill, p)
	}
	rows.Close()

	for _, p := range toFill {
		slug, err := s.uniqueDataSourceSlug(p.workspaceID, p.name)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE data_sources SET slug = ? WHERE id = ?`, slug, p.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateUser(username, email, passwordHash, role string) (*User, error) {
	res, err := s.db.Exec(
		`INSERT INTO users (username, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		username, email, passwordHash, role,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrUserExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

const userColumns = `id, username, email, password_hash, role, avatar_seed, avatar_upload, language, created_at`

func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) GetUserByID(id int64) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarSeed, &u.AvatarUpload, &u.Language, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUserAvatarSeed switches the user to a generated avatar variant,
// discarding any custom upload since the two are mutually exclusive.
func (s *Store) UpdateUserAvatarSeed(id int64, seed string) error {
	_, err := s.db.Exec(`UPDATE users SET avatar_seed = ?, avatar_upload = '' WHERE id = ?`, seed, id)
	return err
}

// UpdateUserAvatarUpload sets a custom uploaded image as the user's avatar,
// which takes priority over any generated variant.
func (s *Store) UpdateUserAvatarUpload(id int64, url string) error {
	_, err := s.db.Exec(`UPDATE users SET avatar_upload = ? WHERE id = ?`, url, id)
	return err
}

// UpdateUserEmail changes a user's own email address (self-service profile
// editing), rejecting a value already taken by another account.
func (s *Store) UpdateUserEmail(id int64, email string) error {
	_, err := s.db.Exec(`UPDATE users SET email = ? WHERE id = ?`, email, id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrEmailExists
		}
		return err
	}
	return nil
}

// UpdateUserPassword replaces a user's password hash (self-service profile
// editing, after the caller has verified the current password).
func (s *Store) UpdateUserPassword(id int64, passwordHash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	return err
}

// UpdateUserLanguage sets the account-wide UI language, persisted so it
// follows the user across devices (unlike the pre-login cookie fallback).
func (s *Store) UpdateUserLanguage(id int64, lang string) error {
	_, err := s.db.Exec(`UPDATE users SET language = ? WHERE id = ?`, lang, id)
	return err
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarSeed, &u.AvatarUpload, &u.Language, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expiresAt,
	)
	return err
}

func (s *Store) GetSessionUser(token string) (*User, error) {
	row := s.db.QueryRow(`
		SELECT users.id, users.username, users.email, users.password_hash, users.role, users.avatar_seed, users.avatar_upload, users.language, users.created_at
		FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.token = ? AND sessions.expires_at > CURRENT_TIMESTAMP
	`, token)
	return scanUser(row)
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// InsertActivity records one audit-log entry. workspaceID/dataSourceID of
// 0 are stored as NULL (account-level actions like login aren't tied to
// either).
func (s *Store) InsertActivity(workspaceID, dataSourceID, userID int64, action, details string, reversible bool) error {
	var wsID, dsID any
	if workspaceID != 0 {
		wsID = workspaceID
	}
	if dataSourceID != 0 {
		dsID = dataSourceID
	}
	_, err := s.db.Exec(
		`INSERT INTO activity_log (workspace_id, data_source_id, user_id, action, details, reversible) VALUES (?, ?, ?, ?, ?, ?)`,
		wsID, dsID, userID, action, details, reversible,
	)
	return err
}

const activityColumns = `
	activity_log.id, activity_log.workspace_id, activity_log.data_source_id, activity_log.user_id,
	activity_log.action, activity_log.details, activity_log.reversible,
	activity_log.reverted_at, activity_log.reverted_by,
	activity_log.created_at,
	COALESCE(u.username, ''), COALESCE(ru.username, ''),
	COALESCE(w.name, ''), COALESCE(w.slug, '')
`

const activityJoins = `
	LEFT JOIN users u ON u.id = activity_log.user_id
	LEFT JOIN users ru ON ru.id = activity_log.reverted_by
	LEFT JOIN workspaces w ON w.id = activity_log.workspace_id
`

func scanActivity(scan func(dest ...any) error) (*ActivityEntry, error) {
	e := &ActivityEntry{}
	err := scan(
		&e.ID, &e.WorkspaceID, &e.DataSourceID, &e.UserID,
		&e.Action, &e.Details, &e.Reversible,
		&e.RevertedAt, &e.RevertedBy,
		&e.CreatedAt,
		&e.Username, &e.RevertedByName,
		&e.WorkspaceName, &e.WorkspaceSlug,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ListWorkspaceActivity returns a workspace's most recent activity, newest
// first, joined with usernames for display.
func (s *Store) ListWorkspaceActivity(workspaceID int64, limit int) ([]*ActivityEntry, error) {
	rows, err := s.db.Query(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.workspace_id = ?
		ORDER BY activity_log.id DESC
		LIMIT ?
	`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ActivityEntry
	for rows.Next() {
		e, err := scanActivity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListUserActivity returns one user's own account-level activity (login,
// profile changes, API keys) — entries with no workspace, shown on their
// profile page.
func (s *Store) ListUserActivity(userID int64, limit int) ([]*ActivityEntry, error) {
	rows, err := s.db.Query(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.user_id = ? AND activity_log.workspace_id IS NULL
		ORDER BY activity_log.id DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ActivityEntry
	for rows.Next() {
		e, err := scanActivity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListActivityForUserWorkspaces returns the most recent activity across
// every workspace the given user is a member of — the feed behind the
// global "Activité" sidebar page, spanning workspaces rather than being
// scoped to one.
func (s *Store) ListActivityForUserWorkspaces(userID int64, limit int) ([]*ActivityEntry, error) {
	rows, err := s.db.Query(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id = ?)
		ORDER BY activity_log.id DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ActivityEntry
	for rows.Next() {
		e, err := scanActivity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetActivity(id int64) (*ActivityEntry, error) {
	row := s.db.QueryRow(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.id = ?
	`, id)
	e, err := scanActivity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Store) MarkActivityReverted(id, revertedBy int64) error {
	_, err := s.db.Exec(
		`UPDATE activity_log SET reverted_at = CURRENT_TIMESTAMP, reverted_by = ? WHERE id = ?`,
		revertedBy, id,
	)
	return err
}
