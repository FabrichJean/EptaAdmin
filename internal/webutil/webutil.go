// Package webutil holds small HTTP helpers and shared write-serialization
// state used across every domain (table grid, tracking, visual, CRM, ...).
package webutil

import (
	"encoding/json"
	"net/http"
	"sync"
)

// DataWriteMu serializes read-modify-write-then-bump-version sequences
// across all tables. A single process-wide lock is enough here since the
// SQLite connection pool is already capped at 1 (see internal/store), this
// just extends that same "one writer at a time" guarantee to the on-disk
// JSON file, closing the race between the version check and the write.
var DataWriteMu sync.Mutex

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
