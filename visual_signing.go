package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// visualEditTokenTTL bounds how long an "Ouvrir en mode édition" link
// stays usable — unlike computeUploadSignature (upload_signing.go), which
// is deliberately never time-bound because it points at one static file
// forever, this token grants active write access while it's valid, so it
// needs a short leash. After it expires the admin just generates a new
// link from EptaAdmin.
const visualEditTokenTTL = 60 * time.Minute

func (a *App) visualEditSigningSecret() (string, error) {
	return a.store.GetOrCreateSecret("visual_edit_secret")
}

// generateEditToken builds a self-contained, tamper-evident token for one
// visual site: base64("<siteID>.<generation>.<issuerUserID>.<expiryUnix>.<hmac>")
// — verifyEditToken recomputes the HMAC and checks integrity, expiry, AND
// that generation still matches the site's current one before trusting
// it. Embedding the generation (VisualSite.TokenGeneration, bumped by
// handleVisualLogout on explicit "Quitter") is what lets a token be
// revoked on demand instead of only ever expiring on its own schedule.
// Embedding the issuing admin's user ID (rather than leaving edits
// attributed to "unknown user") is what lets handleVisualWriteCell/
// handleVisualClearCell log activity the same way a session-authenticated
// grid edit does.
func (a *App) generateEditToken(siteID, generation, issuerUserID int64) (string, error) {
	secret, err := a.visualEditSigningSecret()
	if err != nil {
		return "", err
	}
	expiry := time.Now().Add(visualEditTokenTTL).Unix()
	payload := fmt.Sprintf("%d.%d.%d.%d", siteID, generation, issuerUserID, expiry)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + sig)), nil
}

// verifyEditToken decodes and checks a token produced by generateEditToken
// against the given siteID and its CURRENT generation — pass the
// generation freshly loaded from the database (not cached), since the
// whole point is noticing when it's been bumped since the token was
// issued. Returns the issuing admin's user ID on success (0 on failure).
func (a *App) verifyEditToken(token string, siteID, generation int64) (issuerUserID int64, ok bool, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false, nil
	}
	parts := strings.SplitN(string(raw), ".", 5)
	if len(parts) != 5 {
		return 0, false, nil
	}
	tokenSiteID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || tokenSiteID != siteID {
		return 0, false, nil
	}
	tokenGeneration, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || tokenGeneration != generation {
		return 0, false, nil
	}
	tokenUserID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false, nil
	}
	expiry, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, false, nil
	}
	if time.Now().Unix() > expiry {
		return 0, false, nil
	}

	secret, err := a.visualEditSigningSecret()
	if err != nil {
		return 0, false, err
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2] + "." + parts[3]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[4])) {
		return 0, false, nil
	}
	return tokenUserID, true, nil
}
