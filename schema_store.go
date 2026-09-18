package main

import (
	"database/sql"
	"errors"
)

const (
	ColumnTypeText    = "text"
	ColumnTypeNumber  = "number"
	ColumnTypeBoolean = "boolean"
)

var validColumnTypes = map[string]bool{
	ColumnTypeText:    true,
	ColumnTypeNumber:  true,
	ColumnTypeBoolean: true,
}

func IsValidColumnType(t string) bool {
	return validColumnTypes[t]
}

func ColumnTypeLabel(t string) string {
	switch t {
	case ColumnTypeNumber:
		return "Nombre"
	case ColumnTypeBoolean:
		return "Booléen"
	default:
		return "Texte"
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
}

func (c *DataSourceColumn) TypeLabel() string {
	return ColumnTypeLabel(c.Type)
}

func (s *Store) ListDataSourceColumns(dataSourceID int64) ([]*DataSourceColumn, error) {
	rows, err := s.db.Query(
		`SELECT id, data_source_id, key, type, position FROM data_source_columns WHERE data_source_id = ? ORDER BY position`,
		dataSourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*DataSourceColumn
	for rows.Next() {
		c := &DataSourceColumn{}
		if err := rows.Scan(&c.ID, &c.DataSourceID, &c.Key, &c.Type, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddDataSourceColumn appends a new column to a data source's schema, at
// the next available position.
func (s *Store) AddDataSourceColumn(dataSourceID int64, key, colType string) (*DataSourceColumn, error) {
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
		`INSERT INTO data_source_columns (data_source_id, key, type, position) VALUES (?, ?, ?, ?)`,
		dataSourceID, key, colType, position,
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
	return &DataSourceColumn{ID: id, DataSourceID: dataSourceID, Key: key, Type: colType, Position: position}, nil
}
