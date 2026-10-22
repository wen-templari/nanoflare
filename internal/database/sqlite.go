package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	sqliteDriver "modernc.org/sqlite"
	sqliteLib "modernc.org/sqlite/lib"
)

type SQLiteManager struct {
	dir        string
	litestream *LitestreamSupervisor
	mu         sync.Mutex
	dbs        map[string]*sql.DB
	bookmarks  map[string]int64
	restoreMu  sync.RWMutex
	queries    chan struct{}
}

func NewSQLiteManager(dir string, litestream *LitestreamSupervisor) (*SQLiteManager, error) {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "nanoflare-db")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	manager := &SQLiteManager{
		dir:        dir,
		queries:    make(chan struct{}, 32),
		litestream: litestream,
		dbs:        make(map[string]*sql.DB),
		bookmarks:  make(map[string]int64),
	}
	// Existing databases must be registered before the supervisor starts. On a
	// process restart no query may run immediately, but their replicas still
	// need a daemon for status and time-travel recovery.
	if litestream != nil && litestream.Enabled() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sqlite") {
				continue
			}
			if err := litestream.ConfigureDatabase(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	return manager, nil
}

func (m *SQLiteManager) Execute(databaseID string, request nanoflare.DBQueryRequest) (nanoflare.DBQueryResponse, error) {
	return m.ExecuteContext(context.Background(), databaseID, request)
}
func (m *SQLiteManager) ExecuteContext(ctx context.Context, databaseID string, request nanoflare.DBQueryRequest) (nanoflare.DBQueryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.admitQuery(ctx); err != nil {
		return nanoflare.DBQueryResponse{}, err
	}
	defer m.releaseQuery()
	if len(request.Statements) > 100 {
		return nanoflare.DBQueryResponse{}, nanoflare.ErrSQLPolicy
	}
	for _, statement := range request.Statements {
		if _, err := nanoflare.InspectSQL(statement.SQL); err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
	}
	m.restoreMu.RLock()
	defer m.restoreMu.RUnlock()
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

	conn, err := scopedSQLiteConnection(ctx, db)
	if err != nil {
		return nanoflare.DBQueryResponse{}, err
	}
	defer conn.Close()
	started := time.Now()
	method := strings.TrimSpace(request.Method)
	if method == "" {
		method = "run"
	}
	switch method {
	case "exec":
		result, err := m.exec(ctx, conn, databaseID, request.Statements[0].SQL, started)
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		return nanoflare.DBQueryResponse{Exec: &result, Bookmark: m.nextBookmark(databaseID)}, nil
	case "batch":
		results, err := m.batch(ctx, conn, databaseID, request.Statements)
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		return nanoflare.DBQueryResponse{Results: results, Bookmark: m.nextBookmark(databaseID)}, nil
	case "raw", "first", "run", "all":
		result, err := m.run(ctx, conn, databaseID, request.Statements[0])
		if err != nil {
			return nanoflare.DBQueryResponse{}, err
		}
		switch method {
		case "raw":
			raw := rowsToRaw(result.Results, result.Columns)
			if request.ColumnNames {
				raw = append([][]any{columnNames(result.Columns)}, raw...)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.admitQuery(ctx); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	defer m.releaseQuery()
	if strings.TrimSpace(migrationSQL) != "" {
		if _, err := nanoflare.InspectSQL(migrationSQL); err != nil {
			return nanoflare.DBMigrationResult{}, err
		}
	}
	m.restoreMu.RLock()
	defer m.restoreMu.RUnlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return nanoflare.DBMigrationResult{}, errors.New("migration name is required")
	}
	db, err := m.open(databaseID)
	if err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	conn, err := scopedSQLiteConnection(ctx, db)
	if err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS nanoflare_db_migrations (name text PRIMARY KEY, applied_at text NOT NULL)`); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT name FROM nanoflare_db_migrations WHERE name = ?`, name).Scan(&existing)
	if err == nil {
		return nanoflare.DBMigrationResult{Name: name, Applied: false}, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nanoflare.DBMigrationResult{}, err
	}
	if strings.TrimSpace(migrationSQL) != "" {
		if _, err := tx.ExecContext(ctx, migrationSQL); err != nil {
			return nanoflare.DBMigrationResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nanoflare_db_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return nanoflare.DBMigrationResult{}, err
	}
	_ = m.nextBookmark(databaseID)
	return nanoflare.DBMigrationResult{Name: name, Applied: true}, nil
}

func (m *SQLiteManager) Stats(databaseID string) (nanoflare.DatabaseRuntimeStats, error) {
	m.restoreMu.RLock()
	defer m.restoreMu.RUnlock()
	databaseID = strings.TrimSpace(databaseID)
	if databaseID == "" {
		return nanoflare.DatabaseRuntimeStats{}, nanoflare.ErrDatabaseNotFound
	}
	db, err := m.open(databaseID)
	if err != nil {
		return nanoflare.DatabaseRuntimeStats{}, err
	}
	var tableCount int64
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'nanoflare_db_migrations'`).Scan(&tableCount); err != nil {
		return nanoflare.DatabaseRuntimeStats{}, err
	}
	return nanoflare.DatabaseRuntimeStats{
		StorageBytes: databaseFileSize(m.path(databaseID)),
		TableCount:   tableCount,
	}, nil
}

func (m *SQLiteManager) ReplicationStatus(databaseID string) (nanoflare.DatabaseReplicationStatus, error) {
	if m.litestream == nil {
		return nanoflare.DatabaseReplicationStatus{State: "disabled", RecoveryHours: 168}, nil
	}
	return m.litestream.Status(m.path(databaseID))
}

func (m *SQLiteManager) Restore(databaseID string, timestamp *time.Time) (nanoflare.RestoreDatabaseResult, error) {
	m.restoreMu.Lock()
	defer m.restoreMu.Unlock()
	if m.litestream == nil || !m.litestream.Enabled() {
		return nanoflare.RestoreDatabaseResult{}, errors.New("Litestream is not enabled")
	}
	path := m.path(databaseID)
	if err := m.litestream.EnsureDatabase(path); err != nil {
		return nanoflare.RestoreDatabaseResult{}, err
	}
	temporary := path + ".restore"
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(temporary + suffix)
	}
	if err := m.litestream.RestoreTo(path, temporary, timestamp); err != nil {
		return nanoflare.RestoreDatabaseResult{}, err
	}
	m.mu.Lock()
	if db := m.dbs[databaseID]; db != nil {
		_ = db.Close()
	}
	delete(m.dbs, databaseID)
	m.mu.Unlock()
	backup := path + ".pre-restore"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nanoflare.RestoreDatabaseResult{}, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Rename(backup, path)
		return nanoflare.RestoreDatabaseResult{}, err
	}
	_ = os.Remove(backup)
	return nanoflare.RestoreDatabaseResult{RestoredAt: time.Now().UTC(), Timestamp: timestamp}, nil
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
	if m.litestream != nil {
		if err := m.litestream.EnsureDatabase(m.path(databaseID)); err != nil {
			m.mu.Lock()
			if m.dbs[databaseID] == db {
				delete(m.dbs, databaseID)
			}
			m.mu.Unlock()
			db.Close()
			return nil, err
		}
	}
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

func (m *SQLiteManager) batch(ctx context.Context, db *sql.Conn, databaseID string, statements []nanoflare.DBStatementRequest) ([]nanoflare.D1Result, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	results := make([]nanoflare.D1Result, 0, len(statements))
	budget := &sqlResultBudget{}
	for _, statement := range statements {
		result, err := runStatement(ctx, tx, m.path(databaseID), statement, budget)
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

func (m *SQLiteManager) run(ctx context.Context, db *sql.Conn, databaseID string, statement nanoflare.DBStatementRequest) (nanoflare.D1Result, error) {
	return runStatement(ctx, db, m.path(databaseID), statement, &sqlResultBudget{})
}

func (m *SQLiteManager) exec(ctx context.Context, db *sql.Conn, databaseID, query string, started time.Time) (nanoflare.D1ExecResult, error) {
	if strings.TrimSpace(query) == "" {
		return nanoflare.D1ExecResult{}, errors.New("SQL query is required")
	}
	if _, err := db.ExecContext(ctx, query); err != nil {
		return nanoflare.D1ExecResult{}, err
	}
	return nanoflare.D1ExecResult{Count: statementCount(query), Duration: durationMilliseconds(time.Since(started))}, nil
}

type statementRunner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func runStatement(ctx context.Context, runner statementRunner, dbPath string, statement nanoflare.DBStatementRequest, budget *sqlResultBudget) (nanoflare.D1Result, error) {
	started := time.Now()
	if strings.TrimSpace(statement.SQL) == "" {
		return nanoflare.D1Result{}, errors.New("SQL query is required")
	}
	rows, err := runner.QueryContext(ctx, statement.SQL, normalizeParams(statement.Params)...)
	if err != nil {
		return nanoflare.D1Result{}, err
	}
	resultRows, columns, err := scanRows(rows, budget)
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
	writeStatement := looksLikeWrite(statement.SQL)
	if !writeStatement {
		changes = 0
	}
	return nanoflare.D1Result{
		Success: true,
		Columns: columns,
		Meta: nanoflare.D1Meta{
			ServedBy:        "nanoflare.db",
			ServedByPrimary: true,
			Duration:        durationMilliseconds(time.Since(started)),
			Changes:         changes,
			LastRowID:       lastRowID,
			ChangedDB:       writeStatement,
			SizeAfter:       size,
			RowsRead:        int64(len(resultRows)),
			RowsWritten:     changes,
		},
		Results: resultRows,
	}, nil
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
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

type sqlResultBudget struct{ rows, bytes int }

func scanRows(rows *sql.Rows, budget *sqlResultBudget) ([]map[string]any, []string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		if budget.rows >= 10000 {
			return nil, nil, nanoflare.ErrSQLPolicy
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeSQLiteValue(values[i])
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, nil, err
		}
		budget.bytes += len(encoded)
		budget.rows++
		if budget.bytes > 4<<20 {
			return nil, nil, nanoflare.ErrSQLPolicy
		}
		result = append(result, row)
	}
	return result, columns, rows.Err()
}

func normalizeSQLiteValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func rowsToRaw(rows []map[string]any, columns []string) [][]any {
	raw := make([][]any, 0, len(rows))
	for _, row := range rows {
		item := make([]any, 0, len(columns))
		for _, column := range columns {
			item = append(item, row[column])
		}
		raw = append(raw, item)
	}
	return raw
}

func columnNames(columns []string) []any {
	names := make([]any, len(columns))
	for i, column := range columns {
		names[i] = column
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

func statementCount(query string) int { count, _ := nanoflare.InspectSQL(query); return count }

func looksLikeWrite(sql string) bool {
	sql = strings.ToUpper(strings.TrimSpace(sql))
	for _, prefix := range []string{"INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "REPLACE", "VACUUM", "PRAGMA"} {
		if strings.HasPrefix(sql, prefix) {
			return true
		}
	}
	return false
}

func scopedSQLiteConnection(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	limits := map[int]int{sqliteLib.SQLITE_LIMIT_ATTACHED: 0, sqliteLib.SQLITE_LIMIT_SQL_LENGTH: 100 << 10, sqliteLib.SQLITE_LIMIT_LENGTH: 10 << 20, sqliteLib.SQLITE_LIMIT_EXPR_DEPTH: 100, sqliteLib.SQLITE_LIMIT_TRIGGER_DEPTH: 16, sqliteLib.SQLITE_LIMIT_WORKER_THREADS: 2}
	for id, value := range limits {
		if _, err = sqliteDriver.Limit(conn, id, value); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA trusted_schema=OFF; PRAGMA busy_timeout=500;"); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Bound concurrent database work across tenant files, including time queued.
func (m *SQLiteManager) admitQuery(ctx context.Context) error {
	select {
	case m.queries <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *SQLiteManager) releaseQuery() { <-m.queries }
