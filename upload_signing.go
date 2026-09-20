package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

// browserUploadURLPattern matches the internal, session-cookie-only URL a
// value is stored as (see uploadURL in uploads.go) — the shape the public
// API rewrites into a signed link before ever handing a value back to a
// caller.
var browserUploadURLPattern = regexp.MustCompile(`^/workspaces/([^/]+)/uploads/(.+)$`)

func (a *App) uploadSigningSecret() (string, error) {
	return a.store.GetOrCreateSecret("upload_signing_secret")
}

// computeUploadSignature is not time-bound on purpose: this link is meant
// to keep working for as long as the static site build that embeds it is
// deployed, which is indefinite and on nobody's schedule. It still bounds
// the damage of a leaked link to "this one file, forever" instead of
// "everything this account can ever read" — which is what putting the raw
// personal API key in the URL would mean instead. Revoking it means
// rotating the app's signing secret (invalidates every signed link at
// once) — there's no per-file revocation.
func computeUploadSignature(secret, workspaceSlug, filename string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s/%s", workspaceSlug, filename)
	return hex.EncodeToString(mac.Sum(nil))
}

// signUploadPath returns the public, self-contained API path for one
// uploaded file — no bearer key needed to use it, just its own signature.
func (a *App) signUploadPath(workspaceSlug, filename string) (string, error) {
	secret, err := a.uploadSigningSecret()
	if err != nil {
		return "", err
	}
	sig := computeUploadSignature(secret, workspaceSlug, filename)
	return fmt.Sprintf("/api/v1/workspaces/%s/uploads/%s?sig=%s", workspaceSlug, filename, sig), nil
}

// verifyUploadSignature checks a request's "?sig=" against what this exact
// workspace+filename should have produced, rejecting anything tampered
// with (wrong file, wrong workspace, or forged outright).
func (a *App) verifyUploadSignature(workspaceSlug, filename, sigParam string) (bool, error) {
	if sigParam == "" {
		return false, nil
	}
	secret, err := a.uploadSigningSecret()
	if err != nil {
		return false, err
	}
	expected := computeUploadSignature(secret, workspaceSlug, filename)
	return hmac.Equal([]byte(expected), []byte(sigParam)), nil
}

// signImageValue rewrites a single value in place if it's a string holding
// the internal browser-upload URL, turning it into the signed public API
// path. Non-matching values (external URLs, plain text, numbers...) pass
// through untouched — this deliberately doesn't need to know a column's
// declared type first, since the pattern it looks for is specific enough
// on its own (mirrors what the SDK used to do client-side).
func (a *App) signImageValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	m := browserUploadURLPattern.FindStringSubmatch(s)
	if m == nil {
		return v
	}
	signed, err := a.signUploadPath(m[1], m[2])
	if err != nil {
		return v // leave the (unusable-to-external-callers) original rather than fail the whole response
	}
	return signed
}

// signImageValues applies signImageValue across a slice, returning a new
// slice (the original backing array is never mutated).
func (a *App) signImageValues(values []any) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = a.signImageValue(v)
	}
	return out
}

// signImageValuesInColumnsMap returns a copy of a projected columns map
// (see RecordStore.ToColumnsMap) with every column's values passed through
// signImageValues.
func (a *App) signImageValuesInColumnsMap(cs map[string][]any) map[string][]any {
	out := make(map[string][]any, len(cs))
	for key, values := range cs {
		out[key] = a.signImageValues(values)
	}
	return out
}
