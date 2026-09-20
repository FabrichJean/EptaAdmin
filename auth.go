package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "epta_session"
const sessionDuration = 7 * 24 * time.Hour

type contextKey string

const userContextKey contextKey = "user"

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *App) startSession(w http.ResponseWriter, userID int64) error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(sessionDuration)
	if err := a.store.CreateSession(token, userID, expiresAt); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (a *App) endSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.store.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) userFromRequest(r *http.Request) *User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	user, err := a.store.GetSessionUser(cookie.Value)
	if err != nil || user == nil {
		return nil
	}
	return user
}

// requireAuth redirects unauthenticated visitors to /login and injects
// the current user into the request context for authenticated ones.
func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := a.userFromRequest(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

func userFromContext(r *http.Request) *User {
	u, _ := r.Context().Value(userContextKey).(*User)
	return u
}

const bearerPrefix = "Bearer "

// requireAPIKey authenticates requests via a personal API key sent as a
// Bearer token, for the public read-only API (handlers_api.go). External
// callers — the JS SDK, a script, another backend — can't hold a session
// cookie, so this is a separate auth path from requireAuth, sharing only
// the same userContextKey downstream handlers already read from.
//
// A "?apiKey=" query parameter is accepted as a fallback when there's no
// Authorization header — needed for the upload-serving route, whose
// resolved URL the SDK hands to callers to drop straight into an <img>/
// <video> src. A browser's native resource fetch for those never attaches
// custom headers, so a bearer-header-only scheme would make that URL
// pretty unusable in the most common real use case for it.
func (a *App) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.authenticateAPIKeyRequest(w, r)
		if !ok {
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// authenticateAPIKeyRequest resolves the personal API key on a request
// (Authorization header or "?apiKey=" fallback, see requireAPIKey) to its
// owning user, writing the appropriate JSON error response and returning
// ok=false itself on failure — shared by the requireAPIKey middleware and
// handleAPIServeUpload, which can't use that middleware directly since it
// needs to try a signature first (see upload_signing.go) before falling
// back to requiring a key at all.
func (a *App) authenticateAPIKeyRequest(w http.ResponseWriter, r *http.Request) (*User, bool) {
	// No authenticated user yet at this point, so resolveLang only has the
	// pre-login cookie (or French) to go on — there's no account to read a
	// saved preference from until the key resolves below.
	lang := a.resolveLang(r)
	token := strings.TrimSpace(r.URL.Query().Get("apiKey"))
	if token == "" {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, bearerPrefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T(lang, "api.key_missing")})
			return nil, false
		}
		token = strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T(lang, "api.key_missing")})
		return nil, false
	}

	user, err := a.store.GetUserByAPIKeyToken(token)
	if err != nil {
		log.Printf("api key lookup error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": T(lang, "common.error_generic")})
		return nil, false
	}
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T(lang, "api.key_invalid")})
		return nil, false
	}
	return user, true
}
