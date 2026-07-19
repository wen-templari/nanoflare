package nanoflare

type DBExecutor interface {
	Execute(databaseID string, request DBQueryRequest) (DBQueryResponse, error)
	ApplyMigration(databaseID, name, sql string) (DBMigrationResult, error)
	Stats(databaseID string) (DatabaseRuntimeStats, error)
	Delete(databaseID string) error
	RestoreMissing(databaseID string) error
}

type DBQueryRequest struct {
	Statements  []DBStatementRequest `json:"statements"`
	Method      string               `json:"method"`
	ColumnNames bool                 `json:"column_names,omitempty"`
	FirstColumn string               `json:"first_column,omitempty"`
}

type DBStatementRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

type DBQueryResponse struct {
	Results  []D1Result    `json:"results,omitempty"`
	Raw      [][]any       `json:"raw,omitempty"`
	First    any           `json:"first,omitempty"`
	Exec     *D1ExecResult `json:"exec,omitempty"`
	Bookmark string        `json:"bookmark,omitempty"`
}

type D1Result struct {
	Success bool             `json:"success"`
	Meta    D1Meta           `json:"meta"`
	Results []map[string]any `json:"results"`
}

type D1Meta struct {
	ServedBy        string  `json:"served_by"`
	ServedByPrimary bool    `json:"served_by_primary"`
	Duration        float64 `json:"duration"`
	Changes         int64   `json:"changes"`
	LastRowID       int64   `json:"last_row_id"`
	ChangedDB       bool    `json:"changed_db"`
	SizeAfter       int64   `json:"size_after"`
	RowsRead        int64   `json:"rows_read"`
	RowsWritten     int64   `json:"rows_written"`
}

type D1ExecResult struct {
	Count    int     `json:"count"`
	Duration float64 `json:"duration"`
}

type DBMigrationResult struct {
	Name    string `json:"name"`
	Applied bool   `json:"applied"`
}

type DatabaseRuntimeStats struct {
	StorageBytes int64 `json:"storage_bytes"`
	TableCount   int64 `json:"table_count"`
}
