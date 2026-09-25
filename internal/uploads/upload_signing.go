package uploads

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"eptaadmin/internal/store"
	"fmt"
	"regexp"
)

// browserUploadURLPattern matches the internal, session-cookie-only URL a
// value is stored as (see UploadURL in uploads.go) — the shape the public
// API rewrites into a signed link before ever handing a value back to a
// caller.
var browserUploadURLPattern = regexp.MustCompile(`^/workspaces/([^/]+)/uploads/(.+)$`)

// UploadSigningSecret, and every function below, take *store.Store directly
// rather than the app-level *App type — all they ever touched on App was
// its store handle, so there's no need for callers outside the app package
// to depend on App just to sign or verify an upload URL.
func UploadSigningSecret(s *store.Store) (string, error) {
	return s.GetOrCreateSecret("upload_signing_secret")
}

// ComputeUploadSignature is not time-bound on purpose: this link is meant
// to keep working for as long as the static site build that embeds it is
// deployed, which is indefinite and on nobody's schedule. It still bounds
// the damage of a leaked link to "this one file, forever" instead of
// "everything this account can ever read" — which is what putting the raw
// personal API key in the URL would mean instead. Revoking it means
// rotating the app's signing secret (invalidates every signed link at
// once) — there's no per-file revocation.
func ComputeUploadSignature(secret, workspaceSlug, filename string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s/%s", workspaceSlug, filename)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignUploadPath returns the public, self-contained API path for one
// uploaded file — no bearer key needed to use it, just its own signature.
func SignUploadPath(s *store.Store, workspaceSlug, filename string) (string, error) {
	secret, err := UploadSigningSecret(s)
	if err != nil {
		return "", err
	}
	sig := ComputeUploadSignature(secret, workspaceSlug, filename)
	return fmt.Sprintf("/api/v1/workspaces/%s/uploads/%s?sig=%s", workspaceSlug, filename, sig), nil
}

// VerifyUploadSignature checks a request's "?sig=" against what this exact
// workspace+filename should have produced, rejecting anything tampered
// with (wrong file, wrong workspace, or forged outright).
func VerifyUploadSignature(s *store.Store, workspaceSlug, filename, sigParam string) (bool, error) {
	if sigParam == "" {
		return false, nil
	}
	secret, err := UploadSigningSecret(s)
	if err != nil {
		return false, err
	}
	expected := ComputeUploadSignature(secret, workspaceSlug, filename)
	return hmac.Equal([]byte(expected), []byte(sigParam)), nil
}

// SignImageValue rewrites a single value in place if it's a string holding
// the internal browser-upload URL, turning it into the signed public API
// path. Non-matching values (external URLs, plain text, numbers...) pass
// through untouched — this deliberately doesn't need to know a column's
// declared type first, since the pattern it looks for is specific enough
// on its own (mirrors what the SDK used to do client-side).
func SignImageValue(s *store.Store, v any) any {
	str, ok := v.(string)
	if !ok {
		return v
	}
	m := browserUploadURLPattern.FindStringSubmatch(str)
	if m == nil {
		return v
	}
	signed, err := SignUploadPath(s, m[1], m[2])
	if err != nil {
		return v // leave the (unusable-to-external-callers) original rather than fail the whole response
	}
	return signed
}

// SignImageValues applies SignImageValue across a slice, returning a new
// slice (the original backing array is never mutated).
func SignImageValues(s *store.Store, values []any) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = SignImageValue(s, v)
	}
	return out
}

// SignImageValuesInColumnsMap returns a copy of a projected columns map
// (see store.RecordStore.ToColumnsMap) with every column's values passed
// through SignImageValues.
func SignImageValuesInColumnsMap(s *store.Store, cs map[string][]any) map[string][]any {
	out := make(map[string][]any, len(cs))
	for key, values := range cs {
		out[key] = SignImageValues(s, values)
	}
	return out
}
