package crm

import (
	"crypto/rand"
	"encoding/hex"
	"eptaadmin/internal/store"
	"eptaadmin/internal/uploads"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// crmUploadDir mirrors uploadDir, but namespaced under "crm_<id>" instead
// of "ws_<id>" — CRM+ teams and workspaces are two independent ID
// sequences that both start at 1, so reusing uploadDir directly would risk
// a team and a workspace colliding on the same directory.
func crmUploadDir(teamID int64) string {
	return filepath.Join(store.DataRoot(), "crm_"+strconv.FormatInt(teamID, 10), "uploads")
}

// SaveCRMUploadedFile mirrors SaveUploadedFile: any file type, no
// per-team quota (see plan — assumed simplification for v1).
func SaveCRMUploadedFile(teamID int64, originalName string, r io.Reader, size int64) (string, error) {
	if size > uploads.MaxGenericUploadSize {
		return "", uploads.ErrFileTooLarge
	}

	dir := crmUploadDir(teamID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	filename := hex.EncodeToString(suffix) + "_" + uploads.SanitizeUploadName(originalName)

	dest, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	defer dest.Close()

	limited := io.LimitReader(r, uploads.MaxGenericUploadSize+1)
	n, err := io.Copy(dest, limited)
	if err != nil {
		os.Remove(filepath.Join(dir, filename))
		return "", err
	}
	if n > uploads.MaxGenericUploadSize {
		os.Remove(filepath.Join(dir, filename))
		return "", uploads.ErrFileTooLarge
	}

	return filename, nil
}

func crmUploadURL(teamSlug, filename string) string {
	return "/crm/" + teamSlug + "/uploads/" + filename
}
