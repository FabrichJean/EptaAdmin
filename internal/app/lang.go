package app

import (
	"net/http"

	"eptaadmin/internal/i18n"
)

// ResolveLang picks the language for the current request: the logged-in
// user's saved preference first, then the pre-login cookie, then French.
func (a *App) ResolveLang(r *http.Request) string {
	if user := UserFromContext(r); user != nil && i18n.SupportedLangs[user.Language] {
		return user.Language
	}
	if c, err := r.Cookie(i18n.LangCookieName); err == nil && i18n.SupportedLangs[c.Value] {
		return c.Value
	}
	return i18n.DefaultLang
}

// HandleSetLangCookie is the FR/EN toggle on the login/register pages —
// the only place a language choice can be made before an account (and its
// persisted Language field) exists. It redirects back to wherever the
// visitor came from.
func (a *App) HandleSetLangCookie(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if !i18n.SupportedLangs[lang] {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     i18n.LangCookieName,
		Value:    lang,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	redirectTo := r.Referer()
	if redirectTo == "" {
		redirectTo = "/login"
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}
