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

// maxGenericUploadSize is more generous than maxUploadSize: a "file" cell
// routinely holds a PDF, a spreadsheet or an archive, which run larger than
// the icons/photos the image cell type expects.
const maxGenericUploadSize = 25 << 20 // 25 MB

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
var ErrFileTooLarge = errors.New("fichier trop volumineux (25 Mo maximum)")

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

// sanitizeUploadName strips any directory components and unsafe characters
// from a caller-supplied filename, keeping it short and filesystem-safe —
// used only to keep the ORIGINAL name readable in the stored filename
// (uniqueness itself still comes entirely from the random prefix, so this
// never needs to be collision-proof on its own).
func sanitizeUploadName(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, name)
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	return name
}

// SaveUploadedFile stores an arbitrary file under the workspace's upload
// directory — same collision-safe random naming as SaveUploadedImage, but
// with no extension whitelist (any file type) and a larger size cap, for
// the table-cell "file" column type. The original filename is kept as a
// suffix (after the random prefix) purely so it stays human-readable —
// GridCellHTML strips that prefix back off again for display.
func SaveUploadedFile(workspaceID int64, originalName string, r io.Reader, size int64) (string, error) {
	if size > maxGenericUploadSize {
		return "", ErrFileTooLarge
	}

	dir := uploadDir(workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	filename := hex.EncodeToString(suffix) + "_" + sanitizeUploadName(originalName)

	dest, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	defer dest.Close()

	limited := io.LimitReader(r, maxGenericUploadSize+1)
	n, err := io.Copy(dest, limited)
	if err != nil {
		os.Remove(filepath.Join(dir, filename))
		return "", err
	}
	if n > maxGenericUploadSize {
		os.Remove(filepath.Join(dir, filename))
		return "", ErrFileTooLarge
	}

	return filename, nil
}
