package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// apiKeyTokenPrefix makes a leaked or pasted key recognizable at a glance,
// the same idea as GitHub's "ghp_" or Stripe's "sk_" prefixes.
const apiKeyTokenPrefix = "eak_"

// apiKeyPrefixDisplayLen is how much of the token is kept in clear (in
// token_prefix) so a user can tell keys apart in their list without the
// full secret ever being stored or shown again.
const apiKeyPrefixDisplayLen = 12

type APIKey struct {
	ID          int64
	UserID      int64
	Name        string
	TokenPrefix string
	CreatedAt   time.Time
	LastUsedAt  sql.NullTime
}

func generateAPIKeyToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return apiKeyTokenPrefix + hex.EncodeToString(b), nil
}

// hashAPIKeyToken is a plain SHA-256, not bcrypt: the token itself is a
// 192-bit random secret (unlike a user-chosen password), so it doesn't need
// slow, salted hashing to resist guessing — it just needs a fast, stable
// lookup key.
func hashAPIKeyToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey generates a new personal API key for a user, returning the
// plaintext token exactly once — only its hash is ever persisted, so it
// can't be recovered later (same principle as a password).
func (s *Store) CreateAPIKey(userID int64, name string) (plaintext string, key *APIKey, err error) {
	token, err := generateAPIKeyToken()
	if err != nil {
		return "", nil, err
	}
	hash := hashAPIKeyToken(token)
	prefix := token
	if len(prefix) > apiKeyPrefixDisplayLen {
		prefix = prefix[:apiKeyPrefixDisplayLen]
	}

	res, err := s.db.Exec(
		`INSERT INTO api_keys (user_id, name, token_hash, token_prefix) VALUES (?, ?, ?, ?)`,
		userID, name, hash, prefix,
	)
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	key, err = s.GetAPIKeyByID(id)
	if err != nil {
		return "", nil, err
	}
	return token, key, nil
}

func (s *Store) GetAPIKeyByID(id int64) (*APIKey, error) {
	row := s.db.QueryRow(`SELECT id, user_id, name, token_prefix, created_at, last_used_at FROM api_keys WHERE id = ?`, id)
	k := &APIKey{}
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (s *Store) ListAPIKeys(userID int64) ([]*APIKey, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, token_prefix, created_at, last_used_at FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*APIKey
	for rows.Next() {
		k := &APIKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey revokes a key, scoped to its owner so one user can never
// revoke another's key even by guessing its ID.
func (s *Store) DeleteAPIKey(id, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// GetUserByAPIKeyToken resolves a plaintext bearer token (as presented by a
// caller) to its owning user, for the public API's requireAPIKey
// middleware. It also touches last_used_at so the key list in the profile
// page reflects which keys are actually in use.
func (s *Store) GetUserByAPIKeyToken(token string) (*User, error) {
	hash := hashAPIKeyToken(token)
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = (SELECT user_id FROM api_keys WHERE token_hash = ?)`, hash)
	user, err := scanUser(row)
	if err != nil || user == nil {
		return user, err
	}
	_, _ = s.db.Exec(`UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE token_hash = ?`, hash)
	return user, nil
}
