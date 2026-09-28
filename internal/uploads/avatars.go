package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"eptaadmin/internal/store"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AvatarUploadDir is where custom avatar uploads live — global (not
// per-workspace) since a user's avatar is the same everywhere in the app.
func AvatarUploadDir() string {
	return filepath.Join(store.DataRoot(), "avatars")
}

// SaveAvatarUpload validates and stores a custom avatar image, returning the
// stored (unique) filename — never the caller-supplied one — following the
// same rules as workspace data-cell image uploads (uploads.go).
func SaveAvatarUpload(originalName string, r io.Reader, size int64) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	if !allowedImageExtensions[ext] {
		return "", ErrUnsupportedImageType
	}
	if size > MaxUploadSize {
		return "", ErrImageTooLarge
	}

	dir := AvatarUploadDir()
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

	limited := io.LimitReader(r, MaxUploadSize+1)
	n, err := io.Copy(dest, limited)
	if err != nil {
		os.Remove(filepath.Join(dir, filename))
		return "", err
	}
	if n > MaxUploadSize {
		os.Remove(filepath.Join(dir, filename))
		return "", ErrImageTooLarge
	}

	return filename, nil
}

func AvatarUploadURL(filename string) string {
	return fmt.Sprintf("/avatars/%s", filename)
}

// RemoveAvatarUpload best-effort deletes a previously uploaded avatar file
// (identified by its serving URL) once it's replaced, so switching avatars
// repeatedly doesn't leave orphaned files behind.
func RemoveAvatarUpload(uploadURL string) {
	if uploadURL == "" {
		return
	}
	filename := filepath.Base(uploadURL)
	_ = os.Remove(filepath.Join(AvatarUploadDir(), filename))
}
