package main

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

// SetVisualSiteSyncedTableID records which table "Appliquer vers la
// datasource" writes its snapshot into — set once, on the first sync,
// then reused so every later click overwrites that same table instead of
// creating a new one.
func (s *Store) SetVisualSiteSyncedTableID(id, tableID int64) error {
	_, err := s.db.Exec(`UPDATE visual_sites SET synced_table_id = ? WHERE id = ?`, tableID, id)
	return err
}

// visualSitesDataSourceName groups every visual site's synced snapshot
// table under one shared data source per workspace, mirroring
// trackedSitesDataSourceName's exact same reasoning (tracking_store.go).
const visualSitesDataSourceName = "Sites visuels"

// GetOrCreateVisualSitesDataSource returns the workspace's shared "Sites
// visuels" data source, creating it the first time any visual site syncs.
func (s *Store) GetOrCreateVisualSitesDataSource(workspaceID int64) (*DataSource, error) {
	sources, err := s.ListDataSources(workspaceID)
	if err != nil {
		return nil, err
	}
	for _, ds := range sources {
		if ds.Name == visualSitesDataSourceName {
			return ds, nil
		}
	}
	return s.CreateDataSource(workspaceID, visualSitesDataSourceName, "json")
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

// VisualField is one edited element's current state.
type VisualField struct {
	ID        int64
	SiteID    int64
	PageURL   string
	Selector  string
	ValueType string
	Value     string
	UpdatedAt time.Time
}

const visualFieldColumns = `id, site_id, page_url, selector, value_type, value, updated_at`

func scanVisualField(scan func(dest ...any) error) (*VisualField, error) {
	f := &VisualField{}
	err := scan(&f.ID, &f.SiteID, &f.PageURL, &f.Selector, &f.ValueType, &f.Value, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// UpsertVisualField creates or overwrites the current value for one
// (site, page, selector) — an edit always replaces the whole prior state
// for that element, there's no history kept (unlike tracking's append-only
// events, visual fields are current-state only, see store.go's schema
// comment).
func (s *Store) UpsertVisualField(siteID int64, pageURL, selector, valueType, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO visual_fields (site_id, page_url, selector, value_type, value) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (site_id, page_url, selector) DO UPDATE SET value_type = excluded.value_type, value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		siteID, pageURL, selector, valueType, value,
	)
	return err
}

func (s *Store) DeleteVisualField(siteID int64, pageURL, selector string) error {
	_, err := s.db.Exec(`DELETE FROM visual_fields WHERE site_id = ? AND page_url = ? AND selector = ?`, siteID, pageURL, selector)
	return err
}

// ListVisualFieldsForPage is the SDK's per-pageview lookup: every override
// to apply for one exact URL.
func (s *Store) ListVisualFieldsForPage(siteID int64, pageURL string) ([]*VisualField, error) {
	rows, err := s.db.Query(`SELECT `+visualFieldColumns+` FROM visual_fields WHERE site_id = ? AND page_url = ?`, siteID, pageURL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*VisualField
	for rows.Next() {
		f, err := scanVisualField(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListVisualFields is the EptaAdmin-side management view: every mapped
// element across the whole site, newest-updated first.
func (s *Store) ListVisualFields(siteID int64) ([]*VisualField, error) {
	rows, err := s.db.Query(`SELECT `+visualFieldColumns+` FROM visual_fields WHERE site_id = ? ORDER BY updated_at DESC`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*VisualField
	for rows.Next() {
		f, err := scanVisualField(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteVisualFieldByID lets the admin remove a mapping from EptaAdmin's
// own UI, not only from the live site — scoped to siteID so one site's
// admin can never delete another's field even by guessing an id.
func (s *Store) DeleteVisualFieldByID(id, siteID int64) error {
	_, err := s.db.Exec(`DELETE FROM visual_fields WHERE id = ? AND site_id = ?`, id, siteID)
	return err
}
