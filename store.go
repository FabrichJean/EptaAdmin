package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
	CreatedBy    int64 // 0 for self-registered accounts
	CreatedAt    time.Time
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

	-- A data source is just a folder grouping real tables (see the
	-- secondary sidebar's tree: data source > table). Each table owns its
	-- own schema (table_columns) and its own on-disk record store — a data
	-- source itself never holds rows or columns directly.
	CREATE TABLE IF NOT EXISTS tables (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data_source_id INTEGER NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		slug TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'json',
		storage_path TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (data_source_id, name)
	);
	CREATE INDEX IF NOT EXISTS idx_tables_data_source ON tables(data_source_id);

	CREATE TABLE IF NOT EXISTS table_columns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		table_id INTEGER NOT NULL REFERENCES tables(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'text',
		position INTEGER NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (table_id, key)
	);

	CREATE TABLE IF NOT EXISTS app_secrets (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	-- A webhook fires an HTTP POST to an external server whenever its
	-- workspace changes (see logActivity) or on a manual "Envoyer" click
	-- (see handleTriggerWebhook) — settings.html is where members manage
	-- them.
	CREATE TABLE IF NOT EXISTS webhooks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		url TEXT NOT NULL,
		secret TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_triggered_at DATETIME,
		last_status TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_webhooks_workspace ON webhooks(workspace_id);

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
		`ALTER TABLE users ADD COLUMN avatar_seed TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN avatar_upload TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN language TEXT NOT NULL DEFAULT 'fr'`,
		`ALTER TABLE users ADD COLUMN created_by INTEGER REFERENCES users(id)`,
		`ALTER TABLE activity_log ADD COLUMN table_id INTEGER REFERENCES tables(id) ON DELETE CASCADE`,
		`ALTER TABLE users ADD COLUMN last_seen_activity_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE webhooks ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP`,
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

// CreateUser inserts a new account. createdBy is 0 for a self-registered
// account, or the ID of the workspace owner who created this member on
// their own roster (see ListMembersCreatedBy).
func (s *Store) CreateUser(username, email, passwordHash, role string, createdBy int64) (*User, error) {
	var createdByArg any
	if createdBy != 0 {
		createdByArg = createdBy
	}
	res, err := s.db.Exec(
		`INSERT INTO users (username, email, password_hash, role, created_by) VALUES (?, ?, ?, ?, ?)`,
		username, email, passwordHash, role, createdByArg,
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

const userColumns = `id, username, email, password_hash, role, avatar_seed, avatar_upload, language, created_by, created_at`

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
		u, createdBy := &User{}, sql.NullInt64{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarSeed, &u.AvatarUpload, &u.Language, &createdBy, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.CreatedBy = createdBy.Int64
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListMembersCreatedBy returns the accounts a given user personally created
// via the members page — their own roster, assignable across any of their
// workspaces. Self-registered accounts (created_by NULL) never appear here:
// nobody manages another independent user's account for them.
func (s *Store) ListMembersCreatedBy(creatorID int64) ([]*User, error) {
	rows, err := s.db.Query(`SELECT `+userColumns+` FROM users WHERE created_by = ? ORDER BY username`, creatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u, createdBy := &User{}, sql.NullInt64{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarSeed, &u.AvatarUpload, &u.Language, &createdBy, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.CreatedBy = createdBy.Int64
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser permanently removes an account (cascading to its sessions and
// workspace memberships). Callers must verify the caller is allowed to
// delete this specific account before calling this.
func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
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

// GetUserLastSeenActivityID returns how far into the activity feed this
// user has already looked — the header notification bell's read/unread
// boundary, stored per-account (not per-browser) so it's the same on every
// device they're logged into.
func (s *Store) GetUserLastSeenActivityID(userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT last_seen_activity_id FROM users WHERE id = ?`, userID).Scan(&id)
	return id, err
}

// MarkActivitySeen advances the user's read boundary to id — never
// backwards, so a stale/slow request can't un-read something newer that a
// more recent request already marked read.
func (s *Store) MarkActivitySeen(userID, id int64) error {
	_, err := s.db.Exec(`UPDATE users SET last_seen_activity_id = ? WHERE id = ? AND last_seen_activity_id < ?`, id, userID, id)
	return err
}

func scanUser(row *sql.Row) (*User, error) {
	u, createdBy := &User{}, sql.NullInt64{}
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarSeed, &u.AvatarUpload, &u.Language, &createdBy, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedBy = createdBy.Int64
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
		SELECT users.id, users.username, users.email, users.password_hash, users.role, users.avatar_seed, users.avatar_upload, users.language, users.created_by, users.created_at
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

// GetOrCreateSecret returns a persistent, random value for the given key,
// generating and storing one the first time it's asked for. Used for
// server-only signing material (see upload_signing.go) that must survive
// process restarts — unlike a per-session token, URLs signed with it are
// meant to keep working across the server's entire lifetime.
func (s *Store) GetOrCreateSecret(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM app_secrets WHERE key = ?`, key).Scan(&value)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value = hex.EncodeToString(buf)
	// Two processes could race here (extremely unlikely for a single-writer
	// SQLite app, but cheap to guard): if another one already inserted a
	// value for this key first, just read back whatever it stored instead
	// of erroring, so both end up agreeing on the same secret.
	if _, err := s.db.Exec(`INSERT INTO app_secrets (key, value) VALUES (?, ?)`, key, value); err != nil {
		if isUniqueConstraintErr(err) {
			return s.GetOrCreateSecret(key)
		}
		return "", err
	}
	return value, nil
}

// InsertActivity records one audit-log entry. workspaceID/dataSourceID/
// tableID of 0 are stored as NULL (account-level actions like login aren't
// tied to any of them; a data source folder's own creation has no table
// yet; every other data action has a table but no reason to also name its
// parent folder).
func (s *Store) InsertActivity(workspaceID, dataSourceID, tableID, userID int64, action, details string, reversible bool) error {
	var wsID, dsID, tID any
	if workspaceID != 0 {
		wsID = workspaceID
	}
	if dataSourceID != 0 {
		dsID = dataSourceID
	}
	if tableID != 0 {
		tID = tableID
	}
	_, err := s.db.Exec(
		`INSERT INTO activity_log (workspace_id, data_source_id, table_id, user_id, action, details, reversible) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		wsID, dsID, tID, userID, action, details, reversible,
	)
	return err
}

const activityColumns = `
	activity_log.id, activity_log.workspace_id, activity_log.data_source_id, activity_log.table_id, activity_log.user_id,
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
		&e.ID, &e.WorkspaceID, &e.DataSourceID, &e.TableID, &e.UserID,
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

// ListCoMemberAccountActivity returns the account-level activity (login,
// logout, register, profile changes — entries with no workspace at all) of
// every OTHER user who shares at least one workspace with viewerUserID in
// which the viewer holds Owner or Admin — i.e. what "owners/admins see
// everything, without restriction" means for events that otherwise have no
// workspace to scope them by. A Viewer/Editor role never grants this; each
// account's own activity is already visible to itself via ListUserActivity
// regardless of role, so this is purely the "see it for other people too"
// extension.
func (s *Store) ListCoMemberAccountActivity(viewerUserID int64, limit int) ([]*ActivityEntry, error) {
	rows, err := s.db.Query(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.workspace_id IS NULL
		AND activity_log.user_id IN (
			SELECT DISTINCT wm2.user_id
			FROM workspace_members wm1
			JOIN workspace_members wm2 ON wm2.workspace_id = wm1.workspace_id
			WHERE wm1.user_id = ? AND wm1.role IN (?, ?)
		)
		ORDER BY activity_log.id DESC
		LIMIT ?
	`, viewerUserID, RoleOwner, RoleAdmin, limit)
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

// ListMemberAccountActivity returns the account-level activity (login,
// logout, register, profile changes) of every member of one specific
// workspace — used on that workspace's own Activité page so an Owner/Admin
// there sees a member's login/logout alongside the workspace's own data
// activity, not just the data activity.
func (s *Store) ListMemberAccountActivity(workspaceID int64, limit int) ([]*ActivityEntry, error) {
	rows, err := s.db.Query(`
		SELECT `+activityColumns+`
		FROM activity_log
		`+activityJoins+`
		WHERE activity_log.workspace_id IS NULL
		AND activity_log.user_id IN (SELECT user_id FROM workspace_members WHERE workspace_id = ?)
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
