package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// trackedSitesDataSourceName groups every tracked site's events table
// under one shared data source folder per workspace, auto-created on
// first use — so "Sites suivis" shows up in the sidebar tree exactly like
// any other data source the user created themselves.
const trackedSitesDataSourceName = "Sites suivis"

// GetOrCreateSitesDataSource returns the workspace's shared "Sites
// suivis" data source, creating it the first time a tracked site is
// provisioned.
func (s *Store) GetOrCreateSitesDataSource(workspaceID int64) (*DataSource, error) {
	sources, err := s.ListDataSources(workspaceID)
	if err != nil {
		return nil, err
	}
	for _, ds := range sources {
		if ds.Name == trackedSitesDataSourceName {
			return ds, nil
		}
	}
	return s.CreateDataSource(workspaceID, trackedSitesDataSourceName, "json")
}

// siteKeyTokenPrefix makes a leaked or pasted tracking key recognizable at
// a glance, same idea as apiKeyTokenPrefix in api_keys.go.
const siteKeyTokenPrefix = "eptk_"

// siteKeyPrefixDisplayLen mirrors apiKeyPrefixDisplayLen: enough of the
// token is kept in clear (in key_prefix) to tell keys apart in a list
// without the full secret ever being stored or shown again.
const siteKeyPrefixDisplayLen = 12

// TrackedSite is one website embedding the analytics SDK (static/track.js).
// Its events are appended as rows into TableID's normal record store —
// see handleTrackCollect in handlers_tracking.go.
type TrackedSite struct {
	ID          int64
	WorkspaceID int64
	TableID     int64
	Name        string
	Domain      string
	KeyPrefix   string
	CreatedAt   time.Time
	LastUsedAt  sql.NullTime
}

func generateSiteKeyToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return siteKeyTokenPrefix + hex.EncodeToString(b), nil
}

// hashSiteKeyToken is a plain SHA-256, not bcrypt — same reasoning as
// hashAPIKeyToken: the token is already a 192-bit random secret, so it
// only needs a fast, stable lookup key, not slow salted hashing.
func hashSiteKeyToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateTrackedSite provisions a new tracked site, returning the plaintext
// key exactly once — only its hash is ever persisted.
func (s *Store) CreateTrackedSite(workspaceID, tableID int64, name, domain string) (plaintext string, site *TrackedSite, err error) {
	token, err := generateSiteKeyToken()
	if err != nil {
		return "", nil, err
	}
	hash := hashSiteKeyToken(token)
	prefix := token
	if len(prefix) > siteKeyPrefixDisplayLen {
		prefix = prefix[:siteKeyPrefixDisplayLen]
	}

	res, err := s.db.Exec(
		`INSERT INTO tracked_sites (workspace_id, table_id, name, domain, key_prefix, key_hash) VALUES (?, ?, ?, ?, ?, ?)`,
		workspaceID, tableID, name, domain, prefix, hash,
	)
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	site, err = s.GetTrackedSite(id)
	if err != nil {
		return "", nil, err
	}
	return token, site, nil
}

const trackedSiteColumns = `id, workspace_id, table_id, name, domain, key_prefix, created_at, last_used_at`

func scanTrackedSite(scan func(dest ...any) error) (*TrackedSite, error) {
	site := &TrackedSite{}
	err := scan(&site.ID, &site.WorkspaceID, &site.TableID, &site.Name, &site.Domain, &site.KeyPrefix, &site.CreatedAt, &site.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return site, nil
}

func (s *Store) GetTrackedSite(id int64) (*TrackedSite, error) {
	row := s.db.QueryRow(`SELECT `+trackedSiteColumns+` FROM tracked_sites WHERE id = ?`, id)
	site, err := scanTrackedSite(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return site, err
}

// ListTrackedSites returns a workspace's tracked sites, newest first — the
// settings page's "Sites suivis" section.
func (s *Store) ListTrackedSites(workspaceID int64) ([]*TrackedSite, error) {
	rows, err := s.db.Query(`SELECT `+trackedSiteColumns+` FROM tracked_sites WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TrackedSite
	for rows.Next() {
		site, err := scanTrackedSite(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// DeleteTrackedSite revokes a site, scoped to its workspace so one
// workspace can never delete another's site even by guessing its ID. The
// underlying events table is left untouched — deleting a tracker just
// stops new events from being accepted.
func (s *Store) DeleteTrackedSite(id, workspaceID int64) error {
	_, err := s.db.Exec(`DELETE FROM tracked_sites WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	return err
}

// RegenerateTrackedSiteKey replaces a tracked site's public key — e.g.
// after a suspected leak — and returns the new plaintext once (same
// principle as RegenerateWebhookSecret: only its hash is ever persisted
// again after this call returns).
func (s *Store) RegenerateTrackedSiteKey(id int64) (string, error) {
	token, err := generateSiteKeyToken()
	if err != nil {
		return "", err
	}
	hash := hashSiteKeyToken(token)
	prefix := token
	if len(prefix) > siteKeyPrefixDisplayLen {
		prefix = prefix[:siteKeyPrefixDisplayLen]
	}
	if _, err := s.db.Exec(`UPDATE tracked_sites SET key_hash = ?, key_prefix = ? WHERE id = ?`, hash, prefix, id); err != nil {
		return "", err
	}
	return token, nil
}

// GetTrackedSiteByKeyToken resolves a plaintext tracking key (as sent by
// the embedded SDK) to its site, for handleTrackCollect. It also touches
// last_used_at so the sites list reflects which trackers are actually
// receiving traffic.
func (s *Store) GetTrackedSiteByKeyToken(token string) (*TrackedSite, error) {
	hash := hashSiteKeyToken(token)
	row := s.db.QueryRow(`SELECT `+trackedSiteColumns+` FROM tracked_sites WHERE key_hash = ?`, hash)
	site, err := scanTrackedSite(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || site == nil {
		return site, err
	}
	_, _ = s.db.Exec(`UPDATE tracked_sites SET last_used_at = CURRENT_TIMESTAMP WHERE key_hash = ?`, hash)
	return site, nil
}
