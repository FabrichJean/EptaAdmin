package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/store"
	"eptaadmin/internal/webutil"
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

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *App) StartSession(w http.ResponseWriter, userID int64) error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(sessionDuration)
	if err := a.Store.CreateSession(token, userID, expiresAt); err != nil {
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

func (a *App) EndSession(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.Store.DeleteSession(cookie.Value)
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

func (a *App) UserFromRequest(r *http.Request) *store.User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	user, err := a.Store.GetSessionUser(cookie.Value)
	if err != nil || user == nil {
		return nil
	}
	return user
}

// RequireAuth redirects unauthenticated visitors to /login and injects
// the current user into the request context for authenticated ones.
func (a *App) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := a.UserFromRequest(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

func UserFromContext(r *http.Request) *store.User {
	u, _ := r.Context().Value(userContextKey).(*store.User)
	return u
}

const bearerPrefix = "Bearer "

// RequireAPIKey authenticates requests via a personal API key sent as a
// Bearer token, for the public read-only API (internal/api). External
// callers — the JS SDK, a script, another backend — can't hold a session
// cookie, so this is a separate auth path from RequireAuth, sharing only
// the same userContextKey downstream handlers already read from.
//
// A "?apiKey=" query parameter is accepted as a fallback when there's no
// Authorization header — needed for the upload-serving route, whose
// resolved URL the SDK hands to callers to drop straight into an <img>/
// <video> src. A browser's native resource fetch for those never attaches
// custom headers, so a bearer-header-only scheme would make that URL
// pretty unusable in the most common real use case for it.
func (a *App) RequireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.AuthenticateAPIKeyRequest(w, r)
		if !ok {
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next(w, r.WithContext(ctx))
	}
}

// AuthenticateAPIKeyRequest resolves the personal API key on a request
// (Authorization header or "?apiKey=" fallback, see RequireAPIKey) to its
// owning user, writing the appropriate JSON error response and returning
// ok=false itself on failure — shared by the RequireAPIKey middleware and
// handleAPIServeUpload, which can't use that middleware directly since it
// needs to try a signature first (see internal/uploads) before falling
// back to requiring a key at all.
func (a *App) AuthenticateAPIKeyRequest(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	// No authenticated user yet at this point, so ResolveLang only has the
	// pre-login cookie (or French) to go on — there's no account to read a
	// saved preference from until the key resolves below.
	lang := a.ResolveLang(r)
	token := strings.TrimSpace(r.URL.Query().Get("apiKey"))
	if token == "" {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, bearerPrefix) {
			webutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": i18n.T(lang, "api.key_missing")})
			return nil, false
		}
		token = strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	}
	if token == "" {
		webutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": i18n.T(lang, "api.key_missing")})
		return nil, false
	}

	user, err := a.Store.GetUserByAPIKeyToken(token)
	if err != nil {
		log.Printf("api key lookup error: %v", err)
		webutil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": i18n.T(lang, "common.error_generic")})
		return nil, false
	}
	if user == nil {
		webutil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": i18n.T(lang, "api.key_invalid")})
		return nil, false
	}
	return user, true
}
