package store

import (
	"database/sql"
	"errors"
	"time"
)

// CRMTeamWebhook is one CRM+ team's subscription to an external server —
// the CRM+ equivalent of Webhook (webhook_store.go), scoped to a team
// instead of a workspace. Every modification to that team's entities
// (content save, create/delete, design apply/clear — see
// internal/crm/crm.go and design.go) fires an HTTP POST to URL, signed
// with Secret so the receiving server can verify it really came from this
// EptaAdmin instance (see internal/app/webhooks.go's DeliverCRMTeamWebhook).
type CRMTeamWebhook struct {
	ID              int64
	CRMTeamID       int64
	URL             string
	Secret          string
	Enabled         bool
	LastTriggeredAt sql.NullTime
	LastStatus      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (s *Store) CreateCRMTeamWebhook(teamID int64, url string) (*CRMTeamWebhook, error) {
	secret, err := newWebhookSecret()
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`INSERT INTO crm_team_webhooks (crm_team_id, url, secret) VALUES (?, ?, ?)`,
		teamID, url, secret,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetCRMTeamWebhook(id)
}

const crmTeamWebhookColumns = `id, crm_team_id, url, secret, enabled, last_triggered_at, last_status, created_at, updated_at`

func scanCRMTeamWebhook(scan func(dest ...any) error) (*CRMTeamWebhook, error) {
	h := &CRMTeamWebhook{}
	err := scan(&h.ID, &h.CRMTeamID, &h.URL, &h.Secret, &h.Enabled, &h.LastTriggeredAt, &h.LastStatus, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Store) GetCRMTeamWebhook(id int64) (*CRMTeamWebhook, error) {
	row := s.db.QueryRow(`SELECT `+crmTeamWebhookColumns+` FROM crm_team_webhooks WHERE id = ?`, id)
	h, err := scanCRMTeamWebhook(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// ListCRMTeamWebhooks returns a team's webhooks, newest first — used both
// by the team page and by fireCRMTeamWebhooks (see internal/app/webhooks.go),
// which needs every enabled one to deliver a team-scoped event to.
func (s *Store) ListCRMTeamWebhooks(teamID int64) ([]*CRMTeamWebhook, error) {
	rows, err := s.db.Query(`SELECT `+crmTeamWebhookColumns+` FROM crm_team_webhooks WHERE crm_team_id = ? ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*CRMTeamWebhook
	for rows.Next() {
		h, err := scanCRMTeamWebhook(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCRMTeamWebhook(id int64) error {
	_, err := s.db.Exec(`DELETE FROM crm_team_webhooks WHERE id = ?`, id)
	return err
}

func (s *Store) SetCRMTeamWebhookEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE crm_team_webhooks SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, enabled, id)
	return err
}

// RegenerateCRMTeamWebhookSecret replaces a webhook's signing secret — e.g.
// after a suspected leak — and returns the new one so the caller can show
// it (the only time it's ever visible again is right after this call, same
// as at creation).
func (s *Store) RegenerateCRMTeamWebhookSecret(id int64) (string, error) {
	secret, err := newWebhookSecret()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`UPDATE crm_team_webhooks SET secret = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, secret, id); err != nil {
		return "", err
	}
	return secret, nil
}

// MarkCRMTeamWebhookTriggered records the outcome of the most recent
// delivery attempt — shown on the team page so members can tell a broken
// webhook (e.g. a stale URL) from a healthy one without checking logs.
func (s *Store) MarkCRMTeamWebhookTriggered(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE crm_team_webhooks SET last_triggered_at = CURRENT_TIMESTAMP, last_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}
