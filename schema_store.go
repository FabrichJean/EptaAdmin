package main

import (
	"database/sql"
	"errors"
)

const (
	ColumnTypeText     = "text"
	ColumnTypeLongText = "long_text" // free-form, multi-line — a textarea, not an <input>
	ColumnTypeNumber   = "number"
	ColumnTypeBoolean  = "boolean"
	ColumnTypeImage    = "image" // a URL, normally produced by uploading a file
)

var validColumnTypes = map[string]bool{
	ColumnTypeText:     true,
	ColumnTypeLongText: true,
	ColumnTypeNumber:   true,
	ColumnTypeBoolean:  true,
	ColumnTypeImage:    true,
}

func IsValidColumnType(t string) bool {
	return validColumnTypes[t]
}

func ColumnTypeLabel(lang, t string) string {
	switch t {
	case ColumnTypeNumber:
		return T(lang, "type.number")
	case ColumnTypeBoolean:
		return T(lang, "type.boolean")
	case ColumnTypeLongText:
		return T(lang, "type.long_text")
	case ColumnTypeImage:
		return T(lang, "type.image")
	default:
		return T(lang, "type.text")
	}
}

var ErrColumnExists = errors.New("cette colonne existe déjà")

// DataSourceColumn is one field of a data source's declared schema — the
// server-side source of truth for which columns exist, their order and
// their type, rather than columns being guessed from whatever happens to
// be in the JSON file.
type DataSourceColumn struct {
	ID           int64
	DataSourceID int64
	Key          string
	Type         string
	Position     int
	Description  string
}

func (c *DataSourceColumn) TypeLabel(lang string) string {
	return ColumnTypeLabel(lang, c.Type)
}

func (s *Store) ListDataSourceColumns(dataSourceID int64) ([]*DataSourceColumn, error) {
	rows, err := s.db.Query(
		`SELECT id, data_source_id, key, type, position, description FROM data_source_columns WHERE data_source_id = ? ORDER BY position`,
		dataSourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DataSourceColumn
	for rows.Next() {
		c := &DataSourceColumn{}
		if err := rows.Scan(&c.ID, &c.DataSourceID, &c.Key, &c.Type, &c.Position, &c.Description); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddDataSourceColumn appends a new column to a data source's schema, at
// the next available position.
func (s *Store) AddDataSourceColumn(dataSourceID int64, key, colType, description string) (*DataSourceColumn, error) {
	var maxPos sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MAX(position) FROM data_source_columns WHERE data_source_id = ?`, dataSourceID,
	).Scan(&maxPos); err != nil {
		return nil, err
	}
	position := 0
	if maxPos.Valid {
		position = int(maxPos.Int64) + 1
	}

	res, err := s.db.Exec(
		`INSERT INTO data_source_columns (data_source_id, key, type, position, description) VALUES (?, ?, ?, ?, ?)`,
		dataSourceID, key, colType, position, description,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrColumnExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &DataSourceColumn{ID: id, DataSourceID: dataSourceID, Key: key, Type: colType, Position: position, Description: description}, nil
}

func (s *Store) UpdateDataSourceColumnDescription(dataSourceID int64, key, description string) error {
	_, err := s.db.Exec(
		`UPDATE data_source_columns SET description = ? WHERE data_source_id = ? AND key = ?`,
		description, dataSourceID, key,
	)
	return err
}

// DeleteDataSourceColumn removes a column from the schema. The caller is
// responsible for also removing its values from the JSON store.
func (s *Store) DeleteDataSourceColumn(dataSourceID int64, key string) error {
	_, err := s.db.Exec(
		`DELETE FROM data_source_columns WHERE data_source_id = ? AND key = ?`,
		dataSourceID, key,
	)
	return err
}

// ColumnPosition is one column card's saved (x, y) on the "free layout"
// canvas (see datasource_table.html) — a shared, per-data-source display
// preference, not per-viewer, so every workspace member sees the same
// arrangement on any device.
type ColumnPosition struct {
	Key string
	X   int
	Y   int
}

func (s *Store) ListColumnPositions(dataSourceID int64) ([]*ColumnPosition, error) {
	rows, err := s.db.Query(
		`SELECT column_key, pos_x, pos_y FROM column_canvas_positions WHERE data_source_id = ?`,
		dataSourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*ColumnPosition
	for rows.Next() {
		p := &ColumnPosition{}
		if err := rows.Scan(&p.Key, &p.X, &p.Y); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetColumnPosition saves (or moves) one column's card position, upserting
// so repeated drags of the same card just update its row.
func (s *Store) SetColumnPosition(dataSourceID int64, key string, x, y int) error {
	_, err := s.db.Exec(
		`INSERT INTO column_canvas_positions (data_source_id, column_key, pos_x, pos_y) VALUES (?, ?, ?, ?)
		 ON CONFLICT(data_source_id, column_key) DO UPDATE SET pos_x = excluded.pos_x, pos_y = excluded.pos_y`,
		dataSourceID, key, x, y,
	)
	return err
}
