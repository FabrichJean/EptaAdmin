package main

import (
	"encoding/json"
	"fmt"
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

// ColumnStore is the on-disk shape of a data source: each column is its own
// independent, ordered list of values. Columns are not required to have the
// same length, and there is no assumption that index i of one column
// relates in any way to index i of another — they are unrelated lists that
// merely happen to be grouped under the same data source.
type ColumnStore map[string][]any

// LoadColumnStore reads a data source's JSON file. A missing file is an
// empty store rather than an error, since a freshly registered data source
// has nothing written to disk yet. It also transparently migrates the
// older row-based format (a JSON array of objects) the first time it's
// read, since that format assumed a row-to-row relation between columns
// that this app no longer imposes.
func LoadColumnStore(path string) (ColumnStore, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ColumnStore{}, nil
	}
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ColumnStore{}, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var rows []map[string]any
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("le fichier JSON n'est pas reconnu: %w", err)
		}
		return columnStoreFromRows(rows), nil
	}

	var cs ColumnStore
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("le fichier JSON n'est pas un objet de colonnes valide: %w", err)
	}
	if cs == nil {
		cs = ColumnStore{}
	}
	return cs, nil
}

// columnStoreFromRows converts the legacy row-based format into independent
// columns. A column's resulting list simply skips rows that didn't have
// that key — there is no attempt to preserve positional alignment between
// columns, since that alignment is exactly what this model discards.
func columnStoreFromRows(rows []map[string]any) ColumnStore {
	cs := ColumnStore{}
	for _, row := range rows {
		for k, v := range row {
			cs[k] = append(cs[k], v)
		}
	}
	return cs
}

// SaveColumnStore writes the store back atomically (write to a temp file,
// then rename) so a crash mid-write never corrupts the data source.
func SaveColumnStore(path string, cs ColumnStore) error {
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
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
	Key      string
	Type     string // "text" | "number" | "boolean"; empty for unmanaged
	Managed  bool
	Editable bool
	Values   []any
}

// BuildColumns lays out the schema columns first (in their declared order),
// then appends any additional keys present in the store but not in the
// schema, so nothing in the underlying JSON is ever hidden.
func BuildColumns(schemaCols []*DataSourceColumn, cs ColumnStore) []Column {
	cols := make([]Column, 0, len(schemaCols))
	known := map[string]bool{}
	for _, c := range schemaCols {
		cols = append(cols, Column{Key: c.Key, Type: c.Type, Managed: true, Editable: true, Values: cs[c.Key]})
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

// CoerceTyped validates and converts a user-submitted string to match a
// column's declared schema type, rejecting values that don't fit rather
// than silently guessing.
func CoerceTyped(colType, raw string) (any, error) {
	switch colType {
	case ColumnTypeNumber:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q n'est pas un nombre valide", raw)
		}
		return n, nil
	case ColumnTypeBoolean:
		if raw != "true" && raw != "false" {
			return nil, fmt.Errorf("%q n'est pas un booléen valide (true/false)", raw)
		}
		return raw == "true", nil
	default:
		return raw, nil
	}
}
