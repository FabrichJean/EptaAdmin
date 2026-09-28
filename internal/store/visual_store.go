package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// visualKeyTokenPrefix mirrors siteKeyTokenPrefix (tracking_store.go) —
// same idea, different plugin, so a leaked key's origin is obvious at a
// glance.
const visualKeyTokenPrefix = "eptv_"
const visualKeyPrefixDisplayLen = 12

// VisualSite is one website embedding the visual-editing SDK
// (static/visual.js). Its edited elements live in visual_fields, keyed by
// (page URL, structural selector) — see visual_fields' schema comment in
// store.go for why this isn't a datasource/table entry like tracked
// sites' events are.
type VisualSite struct {
	ID              int64
	WorkspaceID     int64
	Name            string
	Domain          string
	KeyPrefix       string
	CreatedAt       time.Time
	LastUsedAt      sql.NullTime
	SyncedTableID   sql.NullInt64
	TokenGeneration int64
}

func generateVisualKeyToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return visualKeyTokenPrefix + hex.EncodeToString(b), nil
}

func hashVisualKeyToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateVisualSite provisions a new visual site, returning the plaintext
// key exactly once — only its hash is ever persisted (same principle as
// CreateTrackedSite/CreateAPIKey).
func (s *Store) CreateVisualSite(workspaceID int64, name, domain string) (plaintext string, site *VisualSite, err error) {
	token, err := generateVisualKeyToken()
	if err != nil {
		return "", nil, err
	}
	hash := hashVisualKeyToken(token)
	prefix := token
	if len(prefix) > visualKeyPrefixDisplayLen {
		prefix = prefix[:visualKeyPrefixDisplayLen]
	}
	res, err := s.db.Exec(
		`INSERT INTO visual_sites (workspace_id, name, domain, key_prefix, key_hash) VALUES (?, ?, ?, ?, ?)`,
		workspaceID, name, domain, prefix, hash,
	)
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	site, err = s.GetVisualSite(id)
	if err != nil {
		return "", nil, err
	}
	return token, site, nil
}

const visualSiteColumns = `id, workspace_id, name, domain, key_prefix, created_at, last_used_at, synced_table_id, token_generation`

func scanVisualSite(scan func(dest ...any) error) (*VisualSite, error) {
	site := &VisualSite{}
	err := scan(&site.ID, &site.WorkspaceID, &site.Name, &site.Domain, &site.KeyPrefix, &site.CreatedAt, &site.LastUsedAt, &site.SyncedTableID, &site.TokenGeneration)
	if err != nil {
		return nil, err
	}
	return site, nil
}

// BumpVisualSiteTokenGeneration invalidates every previously issued edit
// token for this site (see visual_signing.go) — called when an admin
// explicitly exits edit mode, so the link they used (and any other copy
// of it, even one that hasn't expired yet) stops working immediately.
func (s *Store) BumpVisualSiteTokenGeneration(id int64) error {
	_, err := s.db.Exec(`UPDATE visual_sites SET token_generation = token_generation + 1 WHERE id = ?`, id)
	return err
}

func (s *Store) GetVisualSite(id int64) (*VisualSite, error) {
	row := s.db.QueryRow(`SELECT `+visualSiteColumns+` FROM visual_sites WHERE id = ?`, id)
	site, err := scanVisualSite(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return site, err
}

// ListVisualSites returns a workspace's visual sites, newest first.
func (s *Store) ListVisualSites(workspaceID int64) ([]*VisualSite, error) {
	rows, err := s.db.Query(`SELECT `+visualSiteColumns+` FROM visual_sites WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*VisualSite
	for rows.Next() {
		site, err := scanVisualSite(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) DeleteVisualSite(id, workspaceID int64) error {
	_, err := s.db.Exec(`DELETE FROM visual_sites WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	return err
}

// UpdateVisualSiteDomain lets the admin set/change the domain after
// creation — the "Générer un lien d'édition" button needs one, and
// nothing required entering it up front at creation time.
func (s *Store) UpdateVisualSiteDomain(id, workspaceID int64, domain string) error {
	_, err := s.db.Exec(`UPDATE visual_sites SET domain = ? WHERE id = ? AND workspace_id = ?`, domain, id, workspaceID)
	return err
}

// RegenerateVisualSiteKey mirrors RegenerateTrackedSiteKey — rotates a
// leaked/rotated key, returning the new plaintext once.
func (s *Store) RegenerateVisualSiteKey(id int64) (string, error) {
	token, err := generateVisualKeyToken()
	if err != nil {
		return "", err
	}
	hash := hashVisualKeyToken(token)
	prefix := token
	if len(prefix) > visualKeyPrefixDisplayLen {
		prefix = prefix[:visualKeyPrefixDisplayLen]
	}
	if _, err := s.db.Exec(`UPDATE visual_sites SET key_hash = ?, key_prefix = ? WHERE id = ?`, hash, prefix, id); err != nil {
		return "", err
	}
	return token, nil
}

// GetVisualSiteByKeyToken resolves a plaintext public key (as sent by the
// SDK) to its site — used by every public /api/v1/visual/* call.
func (s *Store) GetVisualSiteByKeyToken(token string) (*VisualSite, error) {
	hash := hashVisualKeyToken(token)
	row := s.db.QueryRow(`SELECT `+visualSiteColumns+` FROM visual_sites WHERE key_hash = ?`, hash)
	site, err := scanVisualSite(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || site == nil {
		return site, err
	}
	_, _ = s.db.Exec(`UPDATE visual_sites SET last_used_at = CURRENT_TIMESTAMP WHERE key_hash = ?`, hash)
	return site, nil
}
