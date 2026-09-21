package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// dataRoot is where every workspace's managed JSON files live. Configurable
// via EPTAADMIN_DATA_DIR for deployments that want it elsewhere.
func dataRoot() string {
	if dir := os.Getenv("EPTAADMIN_DATA_DIR"); dir != "" {
		return dir
	}
	return "data"
}

// defaultWorkspaceStorageLimit caps how much disk space a single
// workspace's data source files and uploaded images may occupy combined —
// override via EPTAADMIN_WORKSPACE_STORAGE_LIMIT (bytes).
const defaultWorkspaceStorageLimit int64 = 400 * 1024 * 1024

func workspaceStorageLimit() int64 {
	if v := os.Getenv("EPTAADMIN_WORKSPACE_STORAGE_LIMIT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultWorkspaceStorageLimit
}

// workspaceStorageUsage sums the size of every file currently stored under
// a workspace's data directory — its data source JSON files and uploaded
// images combined, the same folder newDataSourcePath and uploadDir write
// into. A workspace with nothing written yet (directory doesn't exist) uses
// zero bytes rather than erroring.
func workspaceStorageUsage(workspaceID int64) (int64, error) {
	dir := filepath.Join(dataRoot(), "ws_"+strconv.FormatInt(workspaceID, 10))
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return total, nil
}

var ErrWorkspaceStorageLimitExceeded = errors.New("limite de stockage du workspace dépassée")

// EnsureWorkspaceStorageWithinLimit reports ErrWorkspaceStorageLimitExceeded
// if writing newSize bytes would push a workspace over its storage cap.
// replacePath is the file about to be overwritten (its current size is
// subtracted first, so re-saving a data source doesn't double-count its own
// previous content) — pass "" for a brand new file, e.g. an upload.
func EnsureWorkspaceStorageWithinLimit(workspaceID int64, replacePath string, newSize int64) error {
	var replaceSize int64
	if info, err := os.Stat(replacePath); err == nil {
		replaceSize = info.Size()
	}
	usage, err := workspaceStorageUsage(workspaceID)
	if err != nil {
		return err
	}
	if usage-replaceSize+newSize > workspaceStorageLimit() {
		return ErrWorkspaceStorageLimitExceeded
	}
	return nil
}

// formatBytesHuman renders a byte count for display (sidebar storage
// gauge, etc.) using binary (1024-based) units, matching how the storage
// limit itself is typically expressed (e.g. 1 GiB).
func formatBytesHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// newDataSourcePath computes (and creates the directory for) the on-disk
// location of a data source's JSON file. The frontend never sees or
// chooses this path — only the server does (spec2 §3/§5).
func newDataSourcePath(workspaceID int64, name string) (string, error) {
	dir := filepath.Join(dataRoot(), "ws_"+strconv.FormatInt(workspaceID, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := slugify(name)
	if base == "" {
		base = "data"
	}
	return filepath.Join(dir, base+".json"), nil
}

// Record is one real, whole item in a data source — a set of named field
// values. A record needn't have every declared field set: an absent key
// simply means that field is empty for this record (rendered as nil in the
// aligned per-field view — see RecordStore.Column).
type Record map[string]any

// RecordStore is the on-disk shape of a data source: an ordered list of
// records. Unlike the old independent-per-column-list model, index i really
// is "the same item" across every field — Column(key)[i] and
// Column(otherKey)[i] always come from the same Record.
type RecordStore []Record

// Keys returns every field name that appears in at least one record,
// sorted for deterministic output (JSON/CSV export, the public API...).
func (rs RecordStore) Keys() []string {
	seen := map[string]bool{}
	var keys []string
	for _, rec := range rs {
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// HasField reports whether any record has ever had this key set — used to
// distinguish "this field has no values yet" from "this field doesn't
// exist" (the public API's 404 for an unknown column).
func (rs RecordStore) HasField(key string) bool {
	for _, rec := range rs {
		if _, ok := rec[key]; ok {
			return true
		}
	}
	return false
}

// Column returns one field's values, aligned to every record (nil for a
// record that doesn't have this field) — the same shape the old
// map[string][]any model exposed directly, but now a real, guaranteed
// alignment rather than an accident of independent lists.
func (rs RecordStore) Column(key string) []any {
	out := make([]any, len(rs))
	for i, rec := range rs {
		out[i] = rec[key]
	}
	return out
}

// ToColumnsMap projects every field into its own aligned list — the shape
// the public API and JSON export hand external callers, so nothing
// consuming this app's data (the SDK, an export re-imported elsewhere) has
// to change just because the internal storage is now record-based.
func (rs RecordStore) ToColumnsMap() map[string][]any {
	keys := rs.Keys()
	out := make(map[string][]any, len(keys))
	for _, k := range keys {
		out[k] = rs.Column(k)
	}
	return out
}

// AppendField sets value for key on the record that should receive it: the
// last record, unless it already has key set, in which case a new
// trailing record is started instead. This is what makes filling in one
// field at a time (the existing "+ Ajouter" UI, one click per field) land
// in the same shared record as the other fields entered right before it,
// rather than each field starting its own, unrelated record.
func (rs RecordStore) AppendField(key string, value any) (RecordStore, int) {
	if len(rs) == 0 {
		rs = append(rs, Record{})
	} else if _, exists := rs[len(rs)-1][key]; exists {
		rs = append(rs, Record{})
	}
	idx := len(rs) - 1
	rs[idx][key] = value
	return rs, idx
}

// UpdateField sets key on the record at index, which must already exist.
func (rs RecordStore) UpdateField(key string, index int, value any) error {
	if index < 0 || index >= len(rs) {
		return errRecordIndexOutOfRange
	}
	rs[index][key] = value
	return nil
}

// DeleteField removes key from the record at index, returning its prior
// value. The record itself is kept in place (possibly now with no fields
// at all) rather than removed, so every other field's alignment to the
// remaining records is never disturbed by an unrelated field's delete.
func (rs RecordStore) DeleteField(key string, index int) (any, error) {
	if index < 0 || index >= len(rs) {
		return nil, errRecordIndexOutOfRange
	}
	old := rs[index][key]
	delete(rs[index], key)
	return old, nil
}

// DeleteColumn removes key from every record at once (dropping a field
// from the schema entirely), returning its previous aligned values.
func (rs RecordStore) DeleteColumn(key string) []any {
	old := rs.Column(key)
	for _, rec := range rs {
		delete(rec, key)
	}
	return old
}

// SetColumn overwrites key's aligned values across records, growing the
// store with blank records if values is longer than it — used to restore a
// column's exact prior values when reverting its deletion.
func (rs RecordStore) SetColumn(key string, values []any) RecordStore {
	for len(rs) < len(values) {
		rs = append(rs, Record{})
	}
	for i, v := range values {
		if v != nil {
			rs[i][key] = v
		}
	}
	return rs
}

// MoveRecord moves the whole record at position from to position to. A
// field's value can no longer be reordered in isolation — reordering one
// of a record's fields necessarily means reordering the record itself,
// since every other field travels along with it by definition now.
func (rs RecordStore) MoveRecord(from, to int) (RecordStore, error) {
	if from < 0 || from >= len(rs) {
		return rs, errRecordIndexOutOfRange
	}
	v := rs[from]
	rs = append(rs[:from], rs[from+1:]...)
	if to < 0 {
		to = 0
	}
	if to > len(rs) {
		to = len(rs)
	}
	rs = append(rs[:to], append(RecordStore{v}, rs[to:]...)...)
	return rs, nil
}

var errRecordIndexOutOfRange = errors.New("index out of range")

// LoadRecordStore reads a data source's JSON file. A missing file is an
// empty store rather than an error, since a freshly registered data source
// has nothing written to disk yet. It also transparently migrates the
// older independent-columns format (a JSON object of {field: [values]},
// with no guaranteed alignment between fields) the first time it's read,
// since that's exactly the property this format restores.
func LoadRecordStore(path string) (RecordStore, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RecordStore{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseRecordStoreJSON(data)
}

// ParseRecordStoreJSON parses the bytes of a data source's JSON — either
// the row-array shape this app now writes, or the legacy columnar-object
// shape — into a RecordStore. Shared by LoadRecordStore (reading a data
// source's own file) and data source import (reading an uploaded file), so
// both tolerate the same two shapes.
func ParseRecordStoreJSON(data []byte) (RecordStore, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return RecordStore{}, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var records RecordStore
		if err := json.Unmarshal(data, &records); err != nil {
			return nil, fmt.Errorf("le fichier JSON n'est pas reconnu: %w", err)
		}
		if records == nil {
			records = RecordStore{}
		}
		return records, nil
	}

	var cs map[string][]any
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("le fichier JSON n'est pas un objet de colonnes valide: %w", err)
	}
	return recordsFromColumns(cs), nil
}

// recordsFromColumns transposes the legacy independent-columns shape into
// real, aligned records — record i takes index i from every column that
// has one, nil-padding any column shorter than the longest.
func recordsFromColumns(cs map[string][]any) RecordStore {
	maxLen := 0
	for _, values := range cs {
		if len(values) > maxLen {
		for k, v := range row {
			cs[k] = append(cs[k], v)
		}
	}
	return cs
}

// SaveColumnStore writes the store back atomically (write to a temp file,
// then rename) so a crash mid-write never corrupts the data source. The
// parent directory is recreated if missing, so an out-of-band removal of a
// workspace's data folder doesn't permanently break writes to it.
func SaveColumnStore(path string, cs ColumnStore) error {
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Column is one displayable column panel. Managed columns come from the
// data source's declared schema (data_source_columns) and are editable and
// typed. Unmanaged columns are keys found in the JSON that were never
// declared — e.g. data written outside the app — shown read-only for
// transparency instead of silently hidden.
type Column struct {
	Key         string
	Type        string // "text" | "number" | "boolean"; empty for unmanaged
	Managed     bool
	Editable    bool
	Values      []any
	Description string
}

// BuildColumns lays out the schema columns first (in their declared order),
// then appends any additional keys present in the store but not in the
// schema, so nothing in the underlying JSON is ever hidden.
func BuildColumns(schemaCols []*DataSourceColumn, cs ColumnStore) []Column {
	cols := make([]Column, 0, len(schemaCols))
	known := map[string]bool{}
	for _, c := range schemaCols {
		cols = append(cols, Column{Key: c.Key, Type: c.Type, Managed: true, Editable: true, Values: cs[c.Key], Description: c.Description})
		known[c.Key] = true
	}

	extraKeys := make([]string, 0)
	for k := range cs {
		if !known[k] {
			extraKeys = append(extraKeys, k)
		}
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		// Extra columns are shown read-only: they have no declared type, so
		// editing them would silently guess one again. Use "+ Column" to
		// adopt one under the declared schema.
		cols = append(cols, Column{Key: k, Managed: false, Editable: false, Values: cs[k]})
	}
	return cols
}

// InferSchemaFromColumns guesses an initial schema (key + type) from
// existing data. Used once, the first time a data source with pre-existing
// values is opened, to bootstrap its schema — after that, the schema is
// the source of truth, not the data.
func InferSchemaFromColumns(cs ColumnStore) []struct{ Key, Type string } {
	keys := make([]string, 0, len(cs))
	for k := range cs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]struct{ Key, Type string }, 0, len(keys))
	for _, k := range keys {
		t := ColumnTypeText
		for _, v := range cs[k] {
			if v != nil {
				t = inferType(v)
				break
			}
		}
		out = append(out, struct{ Key, Type string }{Key: k, Type: t})
	}
	return out
}

func inferType(v any) string {
	switch v.(type) {
	case bool:
		return ColumnTypeBoolean
	case float64:
		return ColumnTypeNumber
	default:
		return ColumnTypeText
	}
}

// looksLikeImage heuristically flags a string value as an image reference:
// either one of our own uploaded-file URLs, a data URI, or a URL/path
// ending in a common image extension.
func looksLikeImage(s string) bool {
	if strings.Contains(s, "/uploads/") || strings.HasPrefix(s, "data:image/") {
		return true
	}
	lower := strings.ToLower(s)
	for ext := range allowedImageExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// ValueType reports the practical type of an already-stored value, so the
// UI can show a per-value type badge and pre-select the right widget when
// editing — since type now lives on each value, not on the column.
func ValueType(v any) string {
	switch val := v.(type) {
	case bool:
		return ColumnTypeBoolean
	case float64:
		return ColumnTypeNumber
	case string:
		if looksLikeImage(val) {
			return ColumnTypeImage
		}
		if strings.Contains(val, "\n") {
			return ColumnTypeLongText
		}
		return ColumnTypeText
	default:
		return ColumnTypeText
	}
}

// FormatValue renders a raw JSON value as a display string.
func FormatValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case bool:
		if val {
			return "true"
		}
		return "false"
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case []any:
		parts := make([]string, len(val))
		for i, item := range val {
			parts[i] = FormatValue(item)
		}
		return strings.Join(parts, ", ")
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

// SearchHit is one column value that matched a search query. Index is the
// value's position within its column's list, so the UI can scroll straight
// to it (e.g. .column-panel[data-column=Column] .value-row[data-index=Index]).
type SearchHit struct {
	Column string
	Index  int
	Value  string
}

// SearchColumnStore scans every value of every column for a case-insensitive
// substring match, returning at most limit hits (0 means unlimited). Values
// are compared via FormatValue so e.g. numbers and booleans are searchable
// as their displayed text.
func SearchColumnStore(cs ColumnStore, query string, limit int) []SearchHit {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	keys := make([]string, 0, len(cs))
	for k := range cs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var hits []SearchHit
	for _, k := range keys {
		for i, v := range cs[k] {
			text := FormatValue(v)
			if strings.Contains(strings.ToLower(text), query) {
				hits = append(hits, SearchHit{Column: k, Index: i, Value: text})
				if limit > 0 && len(hits) >= limit {
					return hits
				}
			}
		}
	}
	return hits
}

// CoerceTyped validates and converts a user-submitted string to match a
// column's declared schema type, rejecting values that don't fit rather
// than silently guessing.
func CoerceTyped(lang, colType, raw string) (any, error) {
	switch colType {
	case ColumnTypeNumber:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf(T(lang, "datasource.invalid_number"), raw)
		}
		return n, nil
	case ColumnTypeBoolean:
		if raw != "true" && raw != "false" {
			return nil, fmt.Errorf(T(lang, "datasource.invalid_boolean"), raw)
		}
		return raw == "true", nil
	default:
		return raw, nil
	}
}
