package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// AvatarURL returns a DiceBear-generated avatar for the given seed (e.g. a
// username), so every user gets a distinct, stable illustrated avatar
// instead of a plain initial letter. The same seed always produces the same
// image.
func AvatarURL(seed string) string {
	return "https://api.dicebear.com/9.x/notionists/svg?seed=" + url.QueryEscape(seed) + "&backgroundType=gradientLinear"
}

// avatarUploadDir is where custom avatar uploads live — global (not
// per-workspace) since a user's avatar is the same everywhere in the app.
func avatarUploadDir() string {
	return filepath.Join(dataRoot(), "avatars")
}

// SaveAvatarUpload validates and stores a custom avatar image, returning the
// stored (unique) filename — never the caller-supplied one — following the
// same rules as workspace data-cell image uploads (uploads.go).
func SaveAvatarUpload(originalName string, r io.Reader, size int64) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	if !allowedImageExtensions[ext] {
		return "", ErrUnsupportedImageType
	}
	if size > maxUploadSize {
		return "", ErrImageTooLarge
	}

	dir := avatarUploadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	filename := hex.EncodeToString(suffix) + ext

	dest, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	defer dest.Close()

	limited := io.LimitReader(r, maxUploadSize+1)
	n, err := io.Copy(dest, limited)
	if err != nil {
		os.Remove(filepath.Join(dir, filename))
		return "", err
	}
	if n > maxUploadSize {
		os.Remove(filepath.Join(dir, filename))
		return "", ErrImageTooLarge
	}

	return filename, nil
}

func avatarUploadURL(filename string) string {
	return fmt.Sprintf("/avatars/%s", filename)
}

// removeAvatarUpload best-effort deletes a previously uploaded avatar file
// (identified by its serving URL) once it's replaced, so switching avatars
// repeatedly doesn't leave orphaned files behind.
func removeAvatarUpload(uploadURL string) {
	if uploadURL == "" {
		return
	}
	filename := filepath.Base(uploadURL)
	_ = os.Remove(filepath.Join(avatarUploadDir(), filename))
}
