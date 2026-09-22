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
// visual site: base64("<siteID>.<generation>.<expiryUnix>.<hmac>") —
// verifyEditToken recomputes the HMAC and checks integrity, expiry, AND
// that generation still matches the site's current one before trusting
// it. Embedding the generation (VisualSite.TokenGeneration, bumped by
// handleVisualLogout on explicit "Quitter") is what lets a token be
// revoked on demand instead of only ever expiring on its own schedule.
func (a *App) generateEditToken(siteID, generation int64) (string, error) {
	secret, err := a.visualEditSigningSecret()
	if err != nil {
		return "", err
	}
	expiry := time.Now().Add(visualEditTokenTTL).Unix()
	payload := fmt.Sprintf("%d.%d.%d", siteID, generation, expiry)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + sig)), nil
