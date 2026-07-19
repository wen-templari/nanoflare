package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDurationMillisecondsKeepsSubMillisecondPrecision(t *testing.T) {
	if got := durationMilliseconds(500 * time.Microsecond); got != 0.5 {
		t.Fatalf("durationMilliseconds(500us) = %v, want 0.5", got)
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

func TestLitestreamGeneratedConfigTracksOpenedDatabases(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	configPath := filepath.Join(dir, "litestream.yml")
	fake := filepath.Join(dir, "litestream")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := NewLitestreamSupervisor(true, fake, "")
	if err := supervisor.UseGeneratedConfig(configPath, LitestreamReplicaConfig{
		URLPrefix:       "s3://backups/nanoflare",
		Endpoint:        "http://minio:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		ForcePathStyle:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSQLiteManager(dir, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute("db-generated", nanoflare.DBQueryRequest{
		Method:     "exec",
		Statements: []nanoflare.DBStatementRequest{{SQL: `CREATE TABLE t (id integer);`}},
	}); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	gotConfig := string(config)
	for _, want := range []string{
		`path: "` + filepath.Join(dir, "db-generated.sqlite") + `"`,
		`url: "s3://backups/nanoflare/db-generated.sqlite"`,
		`endpoint: "http://minio:9000"`,
		`access-key-id: "access"`,
		`secret-access-key: "secret"`,
		`force-path-style: true`,
	} {
		if !strings.Contains(gotConfig, want) {
			t.Fatalf("generated config missing %q:\n%s", want, gotConfig)
		}
	}
	var calls []byte
	for i := 0; i < 20; i++ {
		calls, err = os.ReadFile(logPath)
		if err == nil && strings.Contains(string(calls), "replicate\n-config\n"+configPath) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gotCalls := string(calls)
	if !strings.Contains(gotCalls, "restore\n-if-db-not-exists\n-if-replica-exists\n-config\n"+configPath) || !strings.Contains(gotCalls, "replicate\n-config\n"+configPath) {
		t.Fatalf("litestream calls = %q", gotCalls)
	}
}
