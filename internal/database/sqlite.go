package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	_ "modernc.org/sqlite"
)

type SQLiteManager struct {
	dir        string
	litestream *LitestreamSupervisor
	mu         sync.Mutex
	dbs        map[string]*sql.DB
	bookmarks  map[string]int64
}

func NewSQLiteManager(dir string, litestream *LitestreamSupervisor) (*SQLiteManager, error) {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "nanoflare-db")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &SQLiteManager{
		dir:        dir,
		litestream: litestream,
		dbs:        make(map[string]*sql.DB),
		bookmarks:  make(map[string]int64),
	}, nil
}

func (m *SQLiteManager) Execute(databaseID string, request nanoflare.DBQueryRequest) (nanoflare.DBQueryResponse, error) {
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return nanoflare.DBQueryResponse{}, nanoflare.ErrDatabaseNotFound
	}
	if len(request.Statements) == 0 {
		return nanoflare.DBQueryResponse{}, errors.New("at least one SQL statement is required")
	}
	db, err := m.open(databaseID)
	if err != nil {
		return nanoflare.DBQueryResponse{}, err
	}
	started := time.Now()
	method := strings.TrimSpace(request.Method)
	if method == "" {
		method = "run"
	}
	switch method {
	case "exec":
		result, err := m.exec(context.Background(), db, databaseID, request.Statements[0].SQL, started)
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		return nanoflare.DBQueryResponse{Exec: &result, Bookmark: m.nextBookmark(databaseID)}, nil
	case "batch":
		results, err := m.batch(context.Background(), db, databaseID, request.Statements)
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		return nanoflare.DBQueryResponse{Results: results, Bookmark: m.nextBookmark(databaseID)}, nil
	case "raw", "first", "run", "all":
		result, err := m.run(context.Background(), db, databaseID, request.Statements[0])
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		switch method {
		case "raw":
			raw := rowsToRaw(result.Results)
			if request.ColumnNames {
				raw = append([][]any{columnNames(result.Results)}, raw...)
			}
			return nanoflare.DBQueryResponse{Raw: raw, Bookmark: m.nextBookmark(databaseID)}, nil
		case "first":
			var first any
			if len(result.Results) > 0 {
				first = result.Results[0]
				if request.FirstColumn != "" {
					value, ok := result.Results[0][request.FirstColumn]
					if !ok {
						return nanoflare.DBQueryResponse{}, fmt.Errorf("D1_ERROR: column %q does not exist", request.FirstColumn)
					}
					first = value
				}
			}
			return nanoflare.DBQueryResponse{First: first, Bookmark: m.nextBookmark(databaseID)}, nil
		default:
			return nanoflare.DBQueryResponse{Results: []nanoflare.D1Result{result}, Bookmark: m.nextBookmark(databaseID)}, nil
		}
	default:
		return nanoflare.DBQueryResponse{}, fmt.Errorf("unsupported db method %q", method)
	}
}

func (m *SQLiteManager) ApplyMigration(databaseID, name, migrationSQL string) (nanoflare.DBMigrationResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nanoflare.DBMigrationResult{}, errors.New("migration name is required")
	}
	db, err := m.open(databaseID)
	if err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS nanoflare_db_migrations (name text PRIMARY KEY, applied_at text NOT NULL)`); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	var existing string
	err = tx.QueryRow(`SELECT name FROM nanoflare_db_migrations WHERE name = ?`, name).Scan(&existing)
	if err == nil {
		return nanoflare.DBMigrationResult{Name: name, Applied: false}, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nanoflare.DBMigrationResult{}, err
	}
	if strings.TrimSpace(migrationSQL) != "" {
		if _, err := tx.Exec(migrationSQL); err != nil {
			return nanoflare.DBMigrationResult{}, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO nanoflare_db_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	_ = m.nextBookmark(databaseID)
	return nanoflare.DBMigrationResult{Name: name, Applied: true}, nil
}

func (m *SQLiteManager) RestoreMissing(databaseID string) error {
	if _, err := os.Stat(m.path(databaseID)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if m.litestream != nil {
		return m.litestream.Restore(m.path(databaseID))
	}
	return nil
}

func (m *SQLiteManager) Delete(databaseID string) error {
	m.mu.Lock()
	db := m.dbs[databaseID]
	delete(m.dbs, databaseID)
	delete(m.bookmarks, databaseID)
	m.mu.Unlock()
	if db != nil {
		_ = db.Close()
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(m.path(databaseID) + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *SQLiteManager) open(databaseID string) (*sql.DB, error) {
	m.mu.Lock()
	if db := m.dbs[databaseID]; db != nil {
		m.mu.Unlock()
		return db, nil
	}
	m.mu.Unlock()
	if err := m.RestoreMissing(databaseID); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", m.path(databaseID))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	m.mu.Lock()
	if existing := m.dbs[databaseID]; existing != nil {
		m.mu.Unlock()
		db.Close()
		return existing, nil
	}
	m.dbs[databaseID] = db
	m.mu.Unlock()
	return db, nil
}

func (m *SQLiteManager) path(databaseID string) string {
	return filepath.Join(m.dir, databaseID+".sqlite")
}

func (m *SQLiteManager) nextBookmark(databaseID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bookmarks[databaseID]++
	return fmt.Sprintf("%s:%d", databaseID, m.bookmarks[databaseID])
}

func (m *SQLiteManager) batch(ctx context.Context, db *sql.DB, databaseID string, statements []nanoflare.DBStatementRequest) ([]nanoflare.D1Result, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	results := make([]nanoflare.D1Result, 0, len(statements))
	for _, statement := range statements {
		result, err := runStatement(ctx, tx, m.path(databaseID), statement)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func (m *SQLiteManager) run(ctx context.Context, db *sql.DB, databaseID string, statement nanoflare.DBStatementRequest) (nanoflare.D1Result, error) {
	return runStatement(ctx, db, m.path(databaseID), statement)
}

func (m *SQLiteManager) exec(ctx context.Context, db *sql.DB, databaseID, query string, started time.Time) (nanoflare.D1ExecResult, error) {
	if strings.TrimSpace(query) == "" {
		return nanoflare.D1ExecResult{}, errors.New("SQL query is required")
	}
	if _, err := db.ExecContext(ctx, query); err != nil {
		return nanoflare.D1ExecResult{}, err
	}
	return nanoflare.D1ExecResult{Count: statementCount(query), Duration: float64(time.Since(started).Milliseconds())}, nil
}

type statementRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func runStatement(ctx context.Context, runner statementRunner, dbPath string, statement nanoflare.DBStatementRequest) (nanoflare.D1Result, error) {
	started := time.Now()
	if strings.TrimSpace(statement.SQL) == "" {
		return nanoflare.D1Result{}, errors.New("SQL query is required")
	}
	rows, err := runner.QueryContext(ctx, statement.SQL, normalizeParams(statement.Params)...)
	if err != nil {
		return nanoflare.D1Result{}, err
	}
	resultRows, err := scanRows(rows)
	closeErr := rows.Close()
	if err != nil {
		return nanoflare.D1Result{}, err
	}
	if closeErr != nil {
		return nanoflare.D1Result{}, closeErr
	}
	var lastRowID, changes int64
	_ = runner.QueryRowContext(ctx, `SELECT last_insert_rowid(), changes()`).Scan(&lastRowID, &changes)
	size := databaseFileSize(dbPath)
	changed := changes > 0 || looksLikeWrite(statement.SQL)
	return nanoflare.D1Result{
		Success: true,
		Meta: nanoflare.D1Meta{
			ServedBy:        "nanoflare.db",
			ServedByPrimary: true,
			Duration:        float64(time.Since(started).Milliseconds()),
			Changes:         changes,
			LastRowID:       lastRowID,
			ChangedDB:       changed,
			SizeAfter:       size,
			RowsRead:        int64(len(resultRows)),
			RowsWritten:     changes,
		},
		Results: resultRows,
	}, nil
}

func normalizeParams(params []any) []any {
	out := make([]any, 0, len(params))
	for _, param := range params {
		switch value := param.(type) {
		case float64:
			if value == float64(int64(value)) {
				out = append(out, int64(value))
			} else {
				out = append(out, value)
			}
		default:
			out = append(out, value)
		}
	}
	return out
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeSQLiteValue(values[i])
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func normalizeSQLiteValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func rowsToRaw(rows []map[string]any) [][]any {
	names := columnNames(rows)
	raw := make([][]any, 0, len(rows))
	for _, row := range rows {
		item := make([]any, 0, len(names))
		for _, name := range names {
			item = append(item, row[name.(string)])
		}
		raw = append(raw, item)
	}
	return raw
}

func columnNames(rows []map[string]any) []any {
	if len(rows) == 0 {
		return []any{}
	}
	names := make([]any, 0, len(rows[0]))
	for name := range rows[0] {
		names = append(names, name)
	}
	return names
}

func databaseFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func statementCount(sql string) int {
	count := 0
	for _, part := range strings.Split(sql, ";") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	if count == 0 {
		return 1
	}
	return count
}

func looksLikeWrite(sql string) bool {
	sql = strings.ToUpper(strings.TrimSpace(sql))
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "REPLACE", "VACUUM", "PRAGMA"} {
		if strings.HasPrefix(sql, prefix) {
			return true
		}
	}
	return false
}
