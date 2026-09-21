package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// Webhook is one workspace's subscription to an external server: every
// workspace-scoped activity (see logActivity) fires an HTTP POST to URL,
// signed with Secret so the receiving server can verify it really came
// from this EptaAdmin instance (see webhooks.go).
type Webhook struct {
	ID              int64
	WorkspaceID     int64
	URL             string
	Secret          string
	Enabled         bool
	LastTriggeredAt sql.NullTime
	LastStatus      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func newWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) CreateWebhook(workspaceID int64, url string) (*Webhook, error) {
	secret, err := newWebhookSecret()
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`INSERT INTO webhooks (workspace_id, url, secret) VALUES (?, ?, ?)`,
		workspaceID, url, secret,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetWebhook(id)
}

const webhookColumns = `id, workspace_id, url, secret, enabled, last_triggered_at, last_status, created_at, updated_at`

func scanWebhook(scan func(dest ...any) error) (*Webhook, error) {
	h := &Webhook{}
	err := scan(&h.ID, &h.WorkspaceID, &h.URL, &h.Secret, &h.Enabled, &h.LastTriggeredAt, &h.LastStatus, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Store) GetWebhook(id int64) (*Webhook, error) {
	row := s.db.QueryRow(`SELECT `+webhookColumns+` FROM webhooks WHERE id = ?`, id)
	h, err := scanWebhook(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// ListWebhooks returns a workspace's webhooks, newest first — used both
// by the settings page and by fireWebhooks (see webhooks.go), which needs
// every enabled one to deliver a workspace-scoped event to.
func (s *Store) ListWebhooks(workspaceID int64) ([]*Webhook, error) {
	rows, err := s.db.Query(`SELECT `+webhookColumns+` FROM webhooks WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Webhook
	for rows.Next() {
		h, err := scanWebhook(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWebhook(id int64) error {
	_, err := s.db.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	return err
}

func (s *Store) SetWebhookEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE webhooks SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, enabled, id)
	return err
}

// UpdateWebhookURL changes the destination URL — the secret and delivery
// history stay untouched, only where the next delivery is sent changes.
func (s *Store) UpdateWebhookURL(id int64, url string) error {
	_, err := s.db.Exec(`UPDATE webhooks SET url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, url, id)
	return err
}

// RegenerateWebhookSecret replaces a webhook's signing secret — e.g. after
// a suspected leak — and returns the new one so the caller can show it (the
// only time it's ever visible again is right after this call, same as at
// creation).
func (s *Store) RegenerateWebhookSecret(id int64) (string, error) {
	secret, err := newWebhookSecret()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`UPDATE webhooks SET secret = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, secret, id); err != nil {
		return "", err
	}
	return secret, nil
}

// MarkWebhookTriggered records the outcome of the most recent delivery
// attempt — shown on the settings page so members can tell a broken
// webhook (e.g. a stale URL) from a healthy one without checking logs.
func (s *Store) MarkWebhookTriggered(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE webhooks SET last_triggered_at = CURRENT_TIMESTAMP, last_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}
