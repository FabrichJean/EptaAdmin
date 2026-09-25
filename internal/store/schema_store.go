package store

import (
	"database/sql"
	"eptaadmin/internal/i18n"
	"errors"
)

const (
	ColumnTypeText     = "text"
	ColumnTypeLongText = "long_text" // free-form, multi-line — a textarea, not an <input>
	ColumnTypeNumber   = "number"
	ColumnTypeBoolean  = "boolean"
	ColumnTypeImage    = "image"    // a URL, normally produced by uploading an image file
	ColumnTypeFile     = "file"     // a URL to an uploaded file of any kind (document, archive...) — like image, but no thumbnail
	ColumnTypeMarkdown = "markdown" // long_text detected/declared as markdown source — edited in a dedicated write+preview modal
	ColumnTypeJSON     = "json"     // an arbitrary nested object/array — e.g. an imported structure with no flat equivalent
)

var validColumnTypes = map[string]bool{
	ColumnTypeText:     true,
	ColumnTypeLongText: true,
	ColumnTypeNumber:   true,
	ColumnTypeBoolean:  true,
	ColumnTypeImage:    true,
	ColumnTypeFile:     true,
	ColumnTypeMarkdown: true,
	ColumnTypeJSON:     true,
}

func IsValidColumnType(t string) bool {
	return validColumnTypes[t]
}

func ColumnTypeLabel(lang, t string) string {
	switch t {
	case ColumnTypeNumber:
		return i18n.T(lang, "type.number")
	case ColumnTypeBoolean:
		return i18n.T(lang, "type.boolean")
	case ColumnTypeLongText:
		return i18n.T(lang, "type.long_text")
	case ColumnTypeImage:
		return i18n.T(lang, "type.image")
	case ColumnTypeFile:
		return i18n.T(lang, "type.file")
	case ColumnTypeMarkdown:
		return i18n.T(lang, "type.markdown")
	case ColumnTypeJSON:
		return i18n.T(lang, "type.json")
	default:
		return i18n.T(lang, "type.text")
	}
}

var ErrColumnExists = errors.New("cette colonne existe déjà")
var ErrTableColumnExists = errors.New("cette colonne existe déjà")

// TableColumn is one field of a table's declared schema — the server-side
// source of truth for which columns exist, their order and their type,
// rather than columns being guessed from whatever happens to be in the
// JSON file. A DataSource is only ever a folder grouping Tables (see
// table_store.go), so this is scoped to a table, not a data source.
type TableColumn struct {
	ID          int64
	TableID     int64
	Key         string
	Type        string
	Position    int
	Description string
}

func (c *TableColumn) TypeLabel(lang string) string {
	return ColumnTypeLabel(lang, c.Type)
}

func (s *Store) ListTableColumns(tableID int64) ([]*TableColumn, error) {
	rows, err := s.db.Query(
		`SELECT id, table_id, key, type, position, description FROM table_columns WHERE table_id = ? ORDER BY position`,
		tableID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*TableColumn
	for rows.Next() {
		c := &TableColumn{}
		if err := rows.Scan(&c.ID, &c.TableID, &c.Key, &c.Type, &c.Position, &c.Description); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddTableColumn appends a new column to a table's schema, at the next
// available position.
func (s *Store) AddTableColumn(tableID int64, key, colType, description string) (*TableColumn, error) {
	var maxPos sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MAX(position) FROM table_columns WHERE table_id = ?`, tableID,
	).Scan(&maxPos); err != nil {
		return nil, err
	}
	position := 0
	if maxPos.Valid {
		position = int(maxPos.Int64) + 1
	}

	res, err := s.db.Exec(
		`INSERT INTO table_columns (table_id, key, type, position, description) VALUES (?, ?, ?, ?, ?)`,
		tableID, key, colType, position, description,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrTableColumnExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &TableColumn{ID: id, TableID: tableID, Key: key, Type: colType, Position: position, Description: description}, nil
}

func (s *Store) UpdateTableColumnDescription(tableID int64, key, description string) error {
	_, err := s.db.Exec(
		`UPDATE table_columns SET description = ? WHERE table_id = ? AND key = ?`,
		description, tableID, key,
	)
	return err
}

// RenameTableColumn changes a column's key in the schema only — the
// caller is responsible for also renaming that field across every record
// in the table's record store (see RecordStore.RenameField), since the
// two must stay in sync.
func (s *Store) RenameTableColumn(tableID int64, oldKey, newKey string) error {
	_, err := s.db.Exec(
		`UPDATE table_columns SET key = ? WHERE table_id = ? AND key = ?`,
		newKey, tableID, oldKey,
	)
	if err != nil && isUniqueConstraintErr(err) {
		return ErrTableColumnExists
	}
	return err
}

// DeleteTableColumn removes a column from the schema. The caller is
// responsible for also removing its values from the JSON store.
func (s *Store) DeleteTableColumn(tableID int64, key string) error {
	_, err := s.db.Exec(
		`DELETE FROM table_columns WHERE table_id = ? AND key = ?`,
		tableID, key,
	)
	return err
}
