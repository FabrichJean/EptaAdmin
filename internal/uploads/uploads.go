package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"eptaadmin/internal/i18n"
	"eptaadmin/internal/store"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxUploadSize = 5 << 20 // 5 MB

// MaxGenericUploadSize is more generous than MaxUploadSize: a "file" cell
// routinely holds a PDF, a spreadsheet or an archive, which run larger than
// the icons/photos the image cell type expects.
const MaxGenericUploadSize = 25 << 20 // 25 MB

// AllowedImageExtensions lives in the store package (internal/store) since
// jsondata.go's looksLikeImage (type inference on stored values) needs the
// exact same set — this file's own validation reuses that single source of
// truth instead of keeping a second copy in sync by hand.
var allowedImageExtensions = store.AllowedImageExtensions

var ErrUnsupportedImageType = errors.New("format d'image non supporté (png, jpg, jpeg, gif, webp, svg)")
var ErrImageTooLarge = errors.New("image trop volumineuse (5 Mo maximum)")
var ErrFileTooLarge = errors.New("fichier trop volumineux (25 Mo maximum)")

// ErrorMessage translates the sentinel errors SaveUploadedImage and
// SaveAvatarUpload can return; anything else is an internal error that
// shouldn't be echoed to the client verbatim.
func ErrorMessage(lang string, err error) string {
	switch err {
	case ErrUnsupportedImageType:
		return i18n.T(lang, "upload.unsupported_type")
	case ErrImageTooLarge:
		return i18n.T(lang, "upload.too_large")
	case ErrFileTooLarge:
		return i18n.T(lang, "upload.file_too_large")
	default:
		return i18n.T(lang, "common.error_generic")
	}
}

func UploadDir(workspaceID int64) string {
	return filepath.Join(store.DataRoot(), "ws_"+strconv.FormatInt(workspaceID, 10), "uploads")
}

// SaveUploadedImage validates and stores an uploaded image under the
// workspace's upload directory, returning the stored (unique) filename —
// never the caller-supplied one, to avoid collisions and path traversal.
func SaveUploadedImage(workspaceID int64, originalName string, r io.Reader, size int64) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	if !allowedImageExtensions[ext] {
		return "", ErrUnsupportedImageType
	}
	if size > MaxUploadSize {
		return "", ErrImageTooLarge
	}

	dir := UploadDir(workspaceID)
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

func UploadURL(workspaceSlug, filename string) string {
	return fmt.Sprintf("/workspaces/%s/uploads/%s", workspaceSlug, filename)
}

// SanitizeUploadName strips any directory components and unsafe characters
// from a caller-supplied filename, keeping it short and filesystem-safe —
// used only to keep the ORIGINAL name readable in the stored filename
// (uniqueness itself still comes entirely from the random prefix, so this
// never needs to be collision-proof on its own).
func SanitizeUploadName(name string) string {
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
// store.GridCellHTML strips that prefix back off again for display.
func SaveUploadedFile(workspaceID int64, originalName string, r io.Reader, size int64) (string, error) {
	if size > MaxGenericUploadSize {
		return "", ErrFileTooLarge
	}

	dir := UploadDir(workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	filename := hex.EncodeToString(suffix) + "_" + SanitizeUploadName(originalName)

	dest, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	defer dest.Close()

	limited := io.LimitReader(r, MaxGenericUploadSize+1)
	n, err := io.Copy(dest, limited)
	if err != nil {
		os.Remove(filepath.Join(dir, filename))
		return "", err
	}
	if n > MaxGenericUploadSize {
		os.Remove(filepath.Join(dir, filename))
		return "", ErrFileTooLarge
	}

	return filename, nil
}
