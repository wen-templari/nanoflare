package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestSQLiteManagerD1Methods(t *testing.T) {
	manager, err := NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute("db-test", nanoflare.DBQueryRequest{
		Method:     "exec",
		Statements: []nanoflare.DBStatementRequest{{SQL: `CREATE TABLE notes (id integer primary key, body text);`}},
	}); err != nil {
		t.Fatal(err)
	}
	insert, err := manager.Execute("db-test", nanoflare.DBQueryRequest{
		Method:     "run",
		Statements: []nanoflare.DBStatementRequest{{SQL: `INSERT INTO notes (body) VALUES (?)`, Params: []any{"hello"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(insert.Results) != 1 || !insert.Results[0].Meta.ChangedDB || insert.Results[0].Meta.RowsWritten != 1 {
		t.Fatalf("insert response = %#v", insert)
	}
	first, err := manager.Execute("db-test", nanoflare.DBQueryRequest{
		Method:      "first",
		FirstColumn: "body",
		Statements:  []nanoflare.DBStatementRequest{{SQL: `SELECT body FROM notes WHERE id = ?`, Params: []any{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.First != "hello" {
		t.Fatalf("first = %#v, want hello", first.First)
	}
	raw, err := manager.Execute("db-test", nanoflare.DBQueryRequest{
		Method:      "raw",
		ColumnNames: true,
		Statements:  []nanoflare.DBStatementRequest{{SQL: `SELECT body FROM notes ORDER BY id`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Raw) != 2 || raw.Raw[1][0] != "hello" {
		t.Fatalf("raw = %#v", raw.Raw)
	}
}

func TestSQLiteManagerAppliesMigrationOnce(t *testing.T) {
	manager, err := NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.ApplyMigration("db-migrate", "001_create.sql", `CREATE TABLE applied (id integer);`)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied {
		t.Fatalf("first migration was not applied")
	}
	second, err := manager.ApplyMigration("db-migrate", "001_create.sql", `CREATE TABLE should_not_run (id integer);`)
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied {
		t.Fatalf("second migration was applied twice")
	}
}

func TestLitestreamRestoreRunsOnlyWhenDatabaseMissing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "litestream")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := NewLitestreamSupervisor(true, fake, "")
	manager, err := NewSQLiteManager(dir, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute("db-litestream", nanoflare.DBQueryRequest{
		Method:     "exec",
		Statements: []nanoflare.DBStatementRequest{{SQL: `CREATE TABLE t (id integer);`}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute("db-litestream", nanoflare.DBQueryRequest{
		Method:     "exec",
		Statements: []nanoflare.DBStatementRequest{{SQL: `INSERT INTO t VALUES (1);`}},
	}); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(calls)
	if strings.Count(got, "restore\n") != 1 || !strings.Contains(got, filepath.Join(dir, "db-litestream.sqlite")) {
		t.Fatalf("litestream calls = %q", got)
	}
}
