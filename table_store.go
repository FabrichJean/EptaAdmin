package main

import (
	"database/sql"
	"errors"
	"strconv"
	"time"
)

var ErrTableExists = errors.New("une table avec ce nom existe déjà dans ce data source")

// Table is a real, independent SQL-like table: it owns its own column
// schema (see TableColumn) and its own on-disk record store. A DataSource
// is only ever a folder grouping several Tables — see the secondary
// sidebar's tree (data source > table) and jsondata.go's RecordStore for
// what a table's own rows/columns look like.
type Table struct {
	ID           int64
	DataSourceID int64
	Name         string
	Slug         string
	Type         string
	StoragePath  string
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// uniqueTableSlug computes a slug for name that isn't already used by
// another table anywhere in the workspace (not just within the same data
// source) — table slugs must be workspace-unique so the public API can
// keep addressing them as a flat "/datasources/{slug}" path, exactly as
// before this data source/table split.
func (s *Store) uniqueTableSlug(workspaceID int64, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "table"
	}
	slug := base
	for suffix := 2; ; suffix++ {
		var exists int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM tables t JOIN data_sources d ON t.data_source_id = d.id WHERE d.workspace_id = ? AND t.slug = ?`,
			workspaceID, slug,
		).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return slug, nil
		}
		slug = base + "-" + strconv.Itoa(suffix)
	}
}

// CreateTable registers a new table under a data source. Like
// CreateDataSource, the server alone decides the on-disk path.
func (s *Store) CreateTable(dataSourceID, workspaceID int64, name string) (*Table, error) {
	storagePath, err := newDataSourcePath(workspaceID, name)
	if err != nil {
		return nil, err
	}
	slug, err := s.uniqueTableSlug(workspaceID, name)
	if err != nil {
		return nil, err
	}

	res, err := s.db.Exec(
		`INSERT INTO tables (data_source_id, name, slug, storage_path) VALUES (?, ?, ?, ?)`,
		dataSourceID, name, slug, storagePath,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrTableExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetTable(id)
}

const tableColumns = `id, data_source_id, name, slug, type, storage_path, version, created_at, updated_at`

func scanTable(row *sql.Row) (*Table, error) {
	t := &Table{}
	err := row.Scan(&t.ID, &t.DataSourceID, &t.Name, &t.Slug, &t.Type, &t.StoragePath, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) GetTable(id int64) (*Table, error) {
	row := s.db.QueryRow(`SELECT `+tableColumns+` FROM tables WHERE id = ?`, id)
	return scanTable(row)
}

// GetTableInDataSource looks up a table by slug, scoped to one data
// source — used by the admin UI, which always navigates via a known data
// source.
func (s *Store) GetTableInDataSource(dataSourceID int64, slug string) (*Table, error) {
	row := s.db.QueryRow(`SELECT `+tableColumns+` FROM tables WHERE data_source_id = ? AND slug = ?`, dataSourceID, slug)
	return scanTable(row)
}

// GetTableByWorkspaceSlug looks up a table by slug across every data
// source in a workspace — this is what keeps the public API's
// "/workspaces/{slug}/datasources/{dsSlug}" URL shape working unchanged
// for the SDK: externally, "dsSlug" always meant "the thing with columns
// and rows", which is now a Table, not a DataSource.
func (s *Store) GetTableByWorkspaceSlug(workspaceID int64, slug string) (*Table, error) {
	row := s.db.QueryRow(
		`SELECT t.id, t.data_source_id, t.name, t.slug, t.type, t.storage_path, t.version, t.created_at, t.updated_at
		 FROM tables t JOIN data_sources d ON t.data_source_id = d.id
		 WHERE d.workspace_id = ? AND t.slug = ?`,
		workspaceID, slug,
	)
	return scanTable(row)
}

func (s *Store) ListTablesByDataSource(dataSourceID int64) ([]*Table, error) {
	rows, err := s.db.Query(`SELECT `+tableColumns+` FROM tables WHERE data_source_id = ? ORDER BY created_at`, dataSourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Table
	for rows.Next() {
		t := &Table{}
		if err := rows.Scan(&t.ID, &t.DataSourceID, &t.Name, &t.Slug, &t.Type, &t.StoragePath, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTablesByWorkspace returns every table across every data source in a
// workspace — used by the public API's data source list endpoint and by
// global search, both of which operate on the flat, workspace-wide set of
// tables rather than walking data sources one at a time.
func (s *Store) ListTablesByWorkspace(workspaceID int64) ([]*Table, error) {
	rows, err := s.db.Query(
		`SELECT t.id, t.data_source_id, t.name, t.slug, t.type, t.storage_path, t.version, t.created_at, t.updated_at
		 FROM tables t JOIN data_sources d ON t.data_source_id = d.id
		 WHERE d.workspace_id = ? ORDER BY t.created_at`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Table
	for rows.Next() {
		t := &Table{}
		if err := rows.Scan(&t.ID, &t.DataSourceID, &t.Name, &t.Slug, &t.Type, &t.StoragePath, &t.Version, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RenameTable changes a table's display name — its slug (and therefore
// every existing SDK/API link to it) never changes, only the name shown
// in the admin UI.
func (s *Store) RenameTable(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE tables SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, name, id)
	if err != nil && isUniqueConstraintErr(err) {
		return ErrTableExists
	}
	return err
}

// DeleteTable removes a table and, via ON DELETE CASCADE, its columns and
// activity log entries. The caller is responsible for also removing its
// on-disk record store file.
func (s *Store) DeleteTable(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tables WHERE id = ?`, id)
	return err
}

// BumpTableVersion increments a table's version, but only if it currently
// matches expectedVersion — the same optimistic-concurrency guard
// BumpDataSourceVersion used to provide when a data source held its own
// records directly.
func (s *Store) BumpTableVersion(id int64, expectedVersion int) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE tables SET version = version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND version = ?`,
		id, expectedVersion,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
