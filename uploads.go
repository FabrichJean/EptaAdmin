package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxUploadSize = 5 << 20 // 5 MB

var allowedImageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".svg":  true,
}

var ErrUnsupportedImageType = errors.New("format d'image non supporté (png, jpg, jpeg, gif, webp, svg)")
var ErrImageTooLarge = errors.New("image trop volumineuse (5 Mo maximum)")

func uploadDir(workspaceID int64) string {
	return filepath.Join(dataRoot(), "ws_"+strconv.FormatInt(workspaceID, 10), "uploads")
}

// SaveUploadedImage validates and stores an uploaded image under the
// workspace's upload directory, returning the stored (unique) filename —
// never the caller-supplied one, to avoid collisions and path traversal.
func SaveUploadedImage(workspaceID int64, originalName string, r io.Reader, size int64) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	if !allowedImageExtensions[ext] {
		return "", ErrUnsupportedImageType
	}
	if size > maxUploadSize {
		return "", ErrImageTooLarge
	}

	dir := uploadDir(workspaceID)
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

func uploadURL(workspaceSlug, filename string) string {
	return fmt.Sprintf("/workspaces/%s/uploads/%s", workspaceSlug, filename)
}
