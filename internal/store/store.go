package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/roles"
	"errors"
	"net/url"
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

	-- A tracked site is a website embedding the analytics SDK (see
	-- tracking_store.go / handlers_tracking.go / static/track.js): its
	-- events land as rows in a normal table (table_id), reusing the exact
	-- same JSON record store every other table uses — no dedicated event
	-- schema. Public tracking key auth mirrors api_keys.go's pattern
	-- (random token + SHA-256 hash, prefix kept in clear for display).
	CREATE TABLE IF NOT EXISTS tracked_sites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		table_id INTEGER NOT NULL REFERENCES tables(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		domain TEXT NOT NULL DEFAULT '',
		key_prefix TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_tracked_sites_workspace ON tracked_sites(workspace_id);

	-- A visual site is a website embedding the visual-editing SDK (see
	-- visual_store.go / handlers_visual.go / static/visual.js). Unlike a
	-- tracked site (an append-only event log, a natural fit for the JSON
	-- RecordStore every other table uses), this is current-state
	-- key→value data — one row per edited element, upserted in place — so
	-- it gets its own small SQL table instead of a datasource/table entry;
	-- nothing here ever appears in the generic grid.
	CREATE TABLE IF NOT EXISTS visual_sites (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		domain TEXT NOT NULL DEFAULT '',
		key_prefix TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME,
		-- Set the first time "Appliquer vers la datasource" is used (see
		-- handleApplyVisualSiteToDataSource) — the same table is reused on
		-- every later click so repeated syncs overwrite one snapshot rather
		-- than creating a fresh table each time.
		synced_table_id INTEGER REFERENCES tables(id) ON DELETE SET NULL,
		-- Bumped every time an admin explicitly exits edit mode (see
		-- handleVisualLogout) — an edit token embeds the generation it was
		-- issued under (visual_signing.go), so once this changes every
		-- previously issued link (including the one just used to log out)
		-- stops verifying, even though it hasn't reached its normal
		-- expiry yet.
		token_generation INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_visual_sites_workspace ON visual_sites(workspace_id);

	-- One row per edited element on the client site: page_url + selector
	-- (a structural CSS path, see static/visual.js's cssPath) identifies
	-- "this element", value_type says whether value is plain text or an
	-- uploaded file's public URL.
	CREATE TABLE IF NOT EXISTS visual_fields (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		site_id INTEGER NOT NULL REFERENCES visual_sites(id) ON DELETE CASCADE,
		page_url TEXT NOT NULL,
		selector TEXT NOT NULL,
		value_type TEXT NOT NULL DEFAULT 'text',
		value TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (site_id, page_url, selector)
	);
	CREATE INDEX IF NOT EXISTS idx_visual_fields_lookup ON visual_fields(site_id, page_url);

	-- CRM+ (see crm_store.go / handlers_crm.go) is a second, deliberately
	-- workspace-independent top-level concept: a team of its own members
	-- (crm_team_members, mirroring workspace_members's shape and reusing
	-- the exact same Role constants/permissions) manages "entities" (e.g.
	-- "Blog", "Projects") that are each a single JSON tree of elements
	-- (text/image/nested list) — a more ergonomic data-entry mechanism
	-- than the fixed-column grid, not a page renderer. No FK to
	-- workspaces anywhere in this group.
	CREATE TABLE IF NOT EXISTS crm_teams (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		created_by INTEGER NOT NULL REFERENCES users(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS crm_team_members (
		crm_team_id INTEGER NOT NULL REFERENCES crm_teams(id) ON DELETE CASCADE,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (crm_team_id, user_id)
	);

	-- content_json holds one JSON array of elements, each
	-- {type: "text"|"markdown"|"image"|"number"|"boolean"|"list"|"object",
	-- key, value, children, required, readOnly, order} — key is an
	-- optional free-form field label (e.g. "titre", "image"); order is a
	-- 1-based position among siblings, kept in sync with (redundant with,
	-- but explicit alongside) the element's actual array position; children
	-- is itself such an array for "list"/"object", recursive to any depth.
	-- Stored and returned as-is, unvalidated beyond well-formedness, same
	-- philosophy
	-- as ColumnTypeJSON in jsondata.go.
	CREATE TABLE IF NOT EXISTS crm_entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		crm_team_id INTEGER NOT NULL REFERENCES crm_teams(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		slug TEXT NOT NULL DEFAULT '',
		content_json TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (crm_team_id, slug)
	);
	CREATE INDEX IF NOT EXISTS idx_crm_entities_team ON crm_entities(crm_team_id);

	-- A design is a saved, reusable AI-generated rendering (see nvidia.go /
	-- handlers_crm_design.go) — team-scoped so it can be applied to more
	-- than the one entity it was first generated for. Every successful
	-- generation is persisted here immediately (not just when applied),
	-- forming a library an entity can pick from instead of only ever
	-- regenerating from scratch.
	CREATE TABLE IF NOT EXISTS crm_designs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		crm_team_id INTEGER NOT NULL REFERENCES crm_teams(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		html TEXT NOT NULL,
		prompt TEXT NOT NULL DEFAULT '',
		created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_crm_designs_team ON crm_designs(crm_team_id);

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
		// SQLite rejects a non-constant default (CURRENT_TIMESTAMP) on ADD
		// COLUMN — it's fine in CREATE TABLE (a fresh install's webhooks
		// table above already has it), but an existing install backfilling
		// this column needs a constant placeholder here, fixed up for real
		// by backfillWebhookUpdatedAt below.
		`ALTER TABLE webhooks ADD COLUMN updated_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00'`,
		// visual_sites already existed (without this column) on any install
		// that created a visual site before "Appliquer vers la datasource"
		// was added — CREATE TABLE IF NOT EXISTS above wouldn't retrofit it.
		`ALTER TABLE visual_sites ADD COLUMN synced_table_id INTEGER REFERENCES tables(id) ON DELETE SET NULL`,
		`ALTER TABLE visual_sites ADD COLUMN token_generation INTEGER NOT NULL DEFAULT 0`,
		// design_html is an AI-generated, self-contained HTML/CSS/JS
		// rendering that replaces the default tree view for an entity once
		// applied (see handlers_crm.go's design endpoints / nvidia.go);
		// design_prompt keeps the description last used to generate it, so
		// regenerating starts from what was asked for rather than blank.
		`ALTER TABLE crm_entities ADD COLUMN design_html TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE crm_entities ADD COLUMN design_prompt TEXT NOT NULL DEFAULT ''`,
		// active_design_id points at the crm_designs row currently applied,
		// if any — design_html/design_prompt above stay as a denormalized
		// cache of that row's content for fast reads without a join.
		`ALTER TABLE crm_entities ADD COLUMN active_design_id INTEGER REFERENCES crm_designs(id) ON DELETE SET NULL`,
	} {
		if _, err := s.db.Exec(alter); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	if err := s.backfillWebhookUpdatedAt(); err != nil {
		return err
	}
	if err := s.backfillCRMEntityDesigns(); err != nil {
		return err
	}
	return s.backfillDataSourceSlugs()
}

// backfillCRMEntityDesigns migrates any entity that already has an applied
// design_html from before the crm_designs library existed (active_design_id
// still NULL): it becomes that entity's first library entry instead of
// being orphaned outside the new reusable-templates model.
func (s *Store) backfillCRMEntityDesigns() error {
	rows, err := s.db.Query(`SELECT id, crm_team_id, name, design_html, design_prompt FROM crm_entities WHERE design_html != '' AND active_design_id IS NULL`)
	if err != nil {
		return err
	}
	type pending struct {
		id, teamID     int64
		name, html, pr string
	}
	var toFill []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.teamID, &p.name, &p.html, &p.pr); err != nil {
			rows.Close()
			return err
		}
		toFill = append(toFill, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, p := range toFill {
		designName := p.name
		if designName == "" {
			designName = "design"
		}
		res, err := s.db.Exec(
			`INSERT INTO crm_designs (crm_team_id, name, html, prompt) VALUES (?, ?, ?, ?)`,
			p.teamID, designName, p.html, p.pr,
		)
		if err != nil {
			return err
		}
		designID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE crm_entities SET active_design_id = ? WHERE id = ?`, designID, p.id); err != nil {
			return err
		}
	}
	return nil
}

// backfillWebhookUpdatedAt gives any webhook row still carrying the ADD
// COLUMN placeholder (see above) a real updated_at — its own created_at,
// the best available approximation for "last changed" on a row that
// predates this column existing at all.
func (s *Store) backfillWebhookUpdatedAt() error {
	_, err := s.db.Exec(`UPDATE webhooks SET updated_at = created_at WHERE updated_at = '1970-01-01 00:00:00'`)
	return err
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
	`, viewerUserID, roles.RoleOwner, roles.RoleAdmin, limit)
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

// ---- Activity log domain type (moved from root activity.go) ----

const (
	ActionLogin          = "auth.login"
	ActionLogout         = "auth.logout"
	ActionRegister       = "auth.register"
	ActionPasswordChange = "profile.password_change"
	ActionEmailChange    = "profile.email_change"
	ActionLanguageChange = "profile.language_change"
	ActionAvatarChange   = "profile.avatar_change"
	ActionAPIKeyCreate   = "apikey.create"
	ActionAPIKeyDelete   = "apikey.delete"

	ActionWorkspaceCreate  = "workspace.create"
	ActionMemberAdd        = "member.add"
	ActionMemberRemove     = "member.remove"
	ActionMemberRoleChange = "member.role_change"
	ActionMemberCreate     = "member.create"
	ActionMemberDelete     = "member.delete"

	ActionDataSourceCreate = "datasource.create"
	ActionDataSourceRename = "datasource.rename"
	ActionDataSourceDelete = "datasource.delete"
	ActionTableRename      = "table.rename"
	ActionTableDelete      = "table.delete"
	ActionImport           = "datasource.import"
	ActionColumnAdd        = "column.add"
	ActionColumnUpdate     = "column.update"
	ActionColumnDelete     = "column.delete"
	ActionValueUpdate      = "value.update"
	ActionValueDelete      = "value.delete"
	ActionValueAppend      = "value.append"
	ActionValueMove        = "value.move"

	ActionImageUpload = "upload.image"
	ActionFileUpload  = "upload.file"

	ActionWebhookCreate           = "webhook.create"
	ActionWebhookDelete           = "webhook.delete"
	ActionWebhookEnable           = "webhook.enable"
	ActionWebhookDisable          = "webhook.disable"
	ActionWebhookManualTrigger    = "webhook.manual_trigger"
	ActionWebhookAutoTrigger      = "webhook.auto_trigger"
	ActionWebhookURLUpdate        = "webhook.url_update"
	ActionWebhookSecretRegenerate = "webhook.secret_regenerate"

	ActionSiteCreate        = "site.create"
	ActionSiteDelete        = "site.delete"
	ActionSiteKeyRegenerate = "site.key_regenerate"
	ActionSiteDataReset     = "site.data_reset"
	// ActionSiteTrackEvent fires once per visitor enter/exit event — high
	// volume by design (see handleTrackCollect), which is why logActivity
	// excludes it from also firing the workspace's outbound webhooks: a
	// webhook is "notify my own server of a workspace change", not "relay
	// every anonymous pageview", and would flood a configured endpoint.
	ActionSiteTrackEvent = "site.track_event"

	ActionVisualSiteCreate        = "visual_site.create"
	ActionVisualSiteDelete        = "visual_site.delete"
	ActionVisualSiteKeyRegenerate = "visual_site.key_regenerate"
	ActionVisualSiteApply         = "visual_site.apply"

	ActionCRMTeamCreate       = "crm_team.create"
	ActionCRMEntityCreate     = "crm_entity.create"
	ActionCRMEntityUpdate     = "crm_entity.update"
	ActionCRMEntityDelete     = "crm_entity.delete"
	ActionCRMMemberAdd        = "crm_member.add"
	ActionCRMMemberRemove     = "crm_member.remove"
	ActionCRMMemberRoleChange = "crm_member.role_change"

	ActionCRMEntityDesignApply = "crm_entity.design_apply"
	ActionCRMEntityDesignClear = "crm_entity.design_clear"
)

// ActivityEntry is one row of activity_log, plus the joined username for
// display (the log survives a user's deletion — RevertedByUsername /
// Username fall back to empty rather than failing the join).
type ActivityEntry struct {
	ID             int64
	WorkspaceID    sql.NullInt64
	DataSourceID   sql.NullInt64
	TableID        sql.NullInt64
	UserID         sql.NullInt64
	Username       string
	Action         string
	Details        string
	Reversible     bool
	RevertedAt     sql.NullTime
	RevertedBy     sql.NullInt64
	RevertedByName string
	CreatedAt      time.Time
	WorkspaceName  string
	WorkspaceSlug  string
}

func (e *ActivityEntry) DetailsMap() map[string]any {
	var d map[string]any
	_ = json.Unmarshal([]byte(e.Details), &d)
	if d == nil {
		d = map[string]any{}
	}
	return d
}

func DetailString(d map[string]any, key string) string {
	s, _ := d[key].(string)
	return s
}

// Describe renders a one-line, human-readable sentence for this entry,
// e.g. "Jean a modifié la valeur de nom[2]" — built from a translated
// template plus whatever fields that action's details carry.
func (e *ActivityEntry) Describe(lang string) string {
	d := e.DetailsMap()
	actor := e.Username
	if actor == "" {
		actor = i18n.T(lang, "activity.unknown_user")
	}
	switch e.Action {
	case ActionLogin:
		return i18n.T(lang, "activity.desc.auth.login", actor)
	case ActionLogout:
		return i18n.T(lang, "activity.desc.auth.logout", actor)
	case ActionRegister:
		return i18n.T(lang, "activity.desc.auth.register", actor)
	case ActionPasswordChange:
		return i18n.T(lang, "activity.desc.profile.password_change", actor)
	case ActionEmailChange:
		return i18n.T(lang, "activity.desc.profile.email_change", actor, DetailString(d, "old"), DetailString(d, "new"))
	case ActionLanguageChange:
		return i18n.T(lang, "activity.desc.profile.language_change", actor, DetailString(d, "old"), DetailString(d, "new"))
	case ActionAvatarChange:
		return i18n.T(lang, "activity.desc.profile.avatar_change", actor)
	case ActionAPIKeyCreate:
		return i18n.T(lang, "activity.desc.apikey.create", actor, DetailString(d, "name"))
	case ActionAPIKeyDelete:
		return i18n.T(lang, "activity.desc.apikey.delete", actor, DetailString(d, "name"))
	case ActionWorkspaceCreate:
		return i18n.T(lang, "activity.desc.workspace.create", actor, DetailString(d, "name"))
	case ActionMemberAdd:
		return i18n.T(lang, "activity.desc.member.add", actor, DetailString(d, "username"), DetailString(d, "role"))
	case ActionMemberRemove:
		return i18n.T(lang, "activity.desc.member.remove", actor, DetailString(d, "username"))
	case ActionMemberRoleChange:
		return i18n.T(lang, "activity.desc.member.role_change", actor, DetailString(d, "username"), DetailString(d, "role"))
	case ActionMemberCreate:
		return i18n.T(lang, "activity.desc.member.create", actor, DetailString(d, "username"))
	case ActionMemberDelete:
		return i18n.T(lang, "activity.desc.member.delete", actor, DetailString(d, "username"))
	case ActionDataSourceCreate:
		return i18n.T(lang, "activity.desc.datasource.create", actor, DetailString(d, "name"))
	case ActionDataSourceRename:
		return i18n.T(lang, "activity.desc.datasource.rename", actor, DetailString(d, "oldName"), DetailString(d, "newName"))
	case ActionDataSourceDelete:
		return i18n.T(lang, "activity.desc.datasource.delete", actor, DetailString(d, "name"))
	case ActionTableRename:
		return i18n.T(lang, "activity.desc.table.rename", actor, DetailString(d, "oldName"), DetailString(d, "newName"))
	case ActionTableDelete:
		return i18n.T(lang, "activity.desc.table.delete", actor, DetailString(d, "name"))
	case ActionImport:
		return i18n.T(lang, "activity.desc.datasource.import", actor, DetailString(d, "tableName"))
	case ActionColumnAdd:
		return i18n.T(lang, "activity.desc.column.add", actor, DetailString(d, "key"))
	case ActionColumnUpdate:
		return i18n.T(lang, "activity.desc.column.update", actor, DetailString(d, "key"))
	case ActionColumnDelete:
		return i18n.T(lang, "activity.desc.column.delete", actor, DetailString(d, "key"))
	case ActionValueUpdate:
		return i18n.T(lang, "activity.desc.value.update", actor, DetailString(d, "column"))
	case ActionValueDelete:
		return i18n.T(lang, "activity.desc.value.delete", actor, DetailString(d, "column"))
	case ActionValueAppend:
		return i18n.T(lang, "activity.desc.value.append", actor, DetailString(d, "column"))
	case ActionValueMove:
		return i18n.T(lang, "activity.desc.value.move", actor, DetailString(d, "column"))
	case ActionImageUpload:
		return i18n.T(lang, "activity.desc.upload.image", actor)
	case ActionFileUpload:
		return i18n.T(lang, "activity.desc.upload.file", actor, DetailString(d, "filename"))
	case ActionWebhookCreate:
		return i18n.T(lang, "activity.desc.webhook.create", actor, DetailString(d, "url"))
	case ActionWebhookDelete:
		return i18n.T(lang, "activity.desc.webhook.delete", actor, DetailString(d, "url"))
	case ActionWebhookEnable:
		return i18n.T(lang, "activity.desc.webhook.enable", actor, DetailString(d, "url"))
	case ActionWebhookDisable:
		return i18n.T(lang, "activity.desc.webhook.disable", actor, DetailString(d, "url"))
	case ActionWebhookManualTrigger:
		return i18n.T(lang, "activity.desc.webhook.manual_trigger", actor, DetailString(d, "url"))
	case ActionWebhookAutoTrigger:
		url := DetailString(d, "url")
		if success, _ := d["success"].(bool); success {
			return i18n.T(lang, "activity.desc.webhook.auto_trigger_success", url)
		}
		return i18n.T(lang, "activity.desc.webhook.auto_trigger_failed", url, DetailString(d, "error"))
	case ActionWebhookURLUpdate:
		return i18n.T(lang, "activity.desc.webhook.url_update", actor, DetailString(d, "oldUrl"), DetailString(d, "newUrl"))
	case ActionWebhookSecretRegenerate:
		return i18n.T(lang, "activity.desc.webhook.secret_regenerate", actor, DetailString(d, "url"))
	case ActionSiteCreate:
		return i18n.T(lang, "activity.desc.site.create", actor, DetailString(d, "name"))
	case ActionSiteDelete:
		return i18n.T(lang, "activity.desc.site.delete", actor, DetailString(d, "name"))
	case ActionSiteKeyRegenerate:
		return i18n.T(lang, "activity.desc.site.key_regenerate", actor, DetailString(d, "name"))
	case ActionSiteDataReset:
		return i18n.T(lang, "activity.desc.site.data_reset", actor, DetailString(d, "name"))
	case ActionSiteTrackEvent:
		// No actor: this is an anonymous visitor, not a signed-in account —
		// unlike every other entry, the sentence deliberately doesn't start
		// with "%s a...".
		eventType := DetailString(d, "eventType")
		if eventType == "exit" {
			return i18n.T(lang, "activity.desc.site.track_event_exit", DetailString(d, "siteName"), DetailString(d, "url"))
		}
		return i18n.T(lang, "activity.desc.site.track_event_enter", DetailString(d, "siteName"), DetailString(d, "url"))
	case ActionVisualSiteCreate:
		return i18n.T(lang, "activity.desc.visual_site.create", actor, DetailString(d, "name"))
	case ActionVisualSiteDelete:
		return i18n.T(lang, "activity.desc.visual_site.delete", actor, DetailString(d, "name"))
	case ActionVisualSiteKeyRegenerate:
		return i18n.T(lang, "activity.desc.visual_site.key_regenerate", actor, DetailString(d, "name"))
	case ActionVisualSiteApply:
		return i18n.T(lang, "activity.desc.visual_site.apply", actor, DetailString(d, "name"))
	case ActionCRMTeamCreate:
		return i18n.T(lang, "activity.desc.crm_team.create", actor, DetailString(d, "name"))
	case ActionCRMEntityCreate:
		return i18n.T(lang, "activity.desc.crm_entity.create", actor, DetailString(d, "name"), DetailString(d, "teamName"))
	case ActionCRMEntityUpdate:
		return i18n.T(lang, "activity.desc.crm_entity.update", actor, DetailString(d, "name"), DetailString(d, "teamName"))
	case ActionCRMEntityDelete:
		return i18n.T(lang, "activity.desc.crm_entity.delete", actor, DetailString(d, "name"), DetailString(d, "teamName"))
	case ActionCRMMemberAdd:
		return i18n.T(lang, "activity.desc.crm_member.add", actor, DetailString(d, "username"), DetailString(d, "role"), DetailString(d, "teamName"))
	case ActionCRMMemberRemove:
		return i18n.T(lang, "activity.desc.crm_member.remove", actor, DetailString(d, "username"), DetailString(d, "teamName"))
	case ActionCRMMemberRoleChange:
		return i18n.T(lang, "activity.desc.crm_member.role_change", actor, DetailString(d, "username"), DetailString(d, "role"), DetailString(d, "teamName"))
	case ActionCRMEntityDesignApply:
		return i18n.T(lang, "activity.desc.crm_entity.design_apply", actor, DetailString(d, "name"), DetailString(d, "teamName"))
	case ActionCRMEntityDesignClear:
		return i18n.T(lang, "activity.desc.crm_entity.design_clear", actor, DetailString(d, "name"), DetailString(d, "teamName"))
	default:
		return actor + " — " + e.Action
	}
}

// AvatarURL returns a DiceBear-generated avatar for the given seed (e.g. a
// username), so every user gets a distinct, stable illustrated avatar
// instead of a plain initial letter. The same seed always produces the same
// image.
func AvatarURL(seed string) string {
	return "https://api.dicebear.com/9.x/notionists/svg?seed=" + url.QueryEscape(seed) + "&backgroundType=gradientLinear"
}
