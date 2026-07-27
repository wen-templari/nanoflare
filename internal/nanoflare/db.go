package nanoflare

import (
	"bytes"
	"encoding/json"
	"sort"
)

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
	Columns []string         `json:"-"`
}

// MarshalJSON preserves SQLite's result-column order in the encoded row
// objects. JavaScript observes JSON object insertion order through Object.keys,
// while encoding/json sorts map keys by default.
func (r D1Result) MarshalJSON() ([]byte, error) {
	results, err := marshalD1Rows(r.Results, r.Columns)
	if err != nil {
		return nil, err
	}
	type wireResult struct {
		Success bool            `json:"success"`
		Meta    D1Meta          `json:"meta"`
		Results json.RawMessage `json:"results"`
	}
	return json.Marshal(wireResult{Success: r.Success, Meta: r.Meta, Results: results})
}

func marshalD1Rows(rows []map[string]any, columns []string) ([]byte, error) {
	if rows == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			buf.WriteByte(',')
		}
		if len(columns) == 0 {
			encoded, err := json.Marshal(row)
			if err != nil {
				return nil, err
			}
			buf.Write(encoded)
			continue
		}

		buf.WriteByte('{')
		written := 0
		seen := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			if _, ok := seen[column]; ok {
				continue
			}
			value, ok := row[column]
			if !ok {
				continue
			}
			if err := writeD1RowField(&buf, column, value, written > 0); err != nil {
				return nil, err
			}
			written++
			seen[column] = struct{}{}
		}
		extraColumns := make([]string, 0, len(row)-written)
		for column := range row {
			if _, ok := seen[column]; !ok {
				extraColumns = append(extraColumns, column)
			}
		}
		sort.Strings(extraColumns)
		for _, column := range extraColumns {
			if err := writeD1RowField(&buf, column, row[column], written > 0); err != nil {
				return nil, err
			}
			written++
		}
		buf.WriteByte('}')
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func writeD1RowField(buf *bytes.Buffer, column string, value any, comma bool) error {
	nameJSON, err := json.Marshal(column)
	if err != nil {
		return err
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if comma {
		buf.WriteByte(',')
	}
	buf.Write(nameJSON)
	buf.WriteByte(':')
	buf.Write(valueJSON)
	return nil
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
