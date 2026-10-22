package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestSQLiteIsolationPolicyAndEngineAttachmentLimit(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewSQLiteManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(id, query string) (nanoflare.DBQueryResponse, error) {
		return manager.Execute(id, nanoflare.DBQueryRequest{Method: "exec", Statements: []nanoflare.DBStatementRequest{{SQL: query}}})
	}
	if _, err = run("victim", `CREATE TABLE private(value TEXT);INSERT INTO private VALUES ('victim-secret')`); err != nil {
		t.Fatal(err)
	}
	if _, err = run("tenant", `CREATE TABLE own(value TEXT);INSERT INTO own VALUES ('allowed')`); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []string{
		"ATTACH DATABASE '" + filepath.Join(dir, "victim.sqlite") + "' AS stolen",
		"SELECT 1; ATTACH DATABASE '" + filepath.Join(dir, "victim.sqlite") + "' AS stolen",
		"VACUUM INTO '" + filepath.Join(dir, "escaped.sqlite") + "'",
		`PRAGMA writable_schema=ON`,
		`SELECT "load_extension"('/tmp/nonexistent')`,
	} {
		if _, err = run("tenant", attack); !errors.Is(err, nanoflare.ErrSQLPolicy) {
			t.Fatalf("unsafe query result: %v", err)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "escaped.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("VACUUM created a file outside the database operation")
	}
	db, err := manager.open("tenant")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := scopedSQLiteConnection(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	// Bypass the lexical layer in this test to verify the actual connection limit.
	if _, err = conn.ExecContext(context.Background(), "ATTACH DATABASE ? AS stolen", filepath.Join(dir, "victim.sqlite")); err == nil {
		t.Fatal("engine allowed an attached database")
	}
	conn.Close()
	if _, err = manager.ApplyMigration("tenant", "unsafe-migration", `PRAGMA writable_schema=ON`); !errors.Is(err, nanoflare.ErrSQLPolicy) {
		t.Fatalf("migration bypassed policy: %v", err)
	}
	response, err := manager.Execute("tenant", nanoflare.DBQueryRequest{Method: "first", FirstColumn: "value", Statements: []nanoflare.DBStatementRequest{{SQL: `SELECT value FROM own`}}})
	if err != nil || response.First != "allowed" {
		t.Fatalf("authorized query: %#v %v", response, err)
	}
}
func TestSQLiteQueryContextInterruptsRecursiveWork(t *testing.T) {
	manager, err := NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = manager.ExecuteContext(ctx, "bounded", nanoflare.DBQueryRequest{Method: "first", Statements: []nanoflare.DBStatementRequest{{SQL: `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n) SELECT sum(x) FROM n`}}})
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("query did not respect cancellation: %v (%s)", err, time.Since(started))
	}
	response, err := manager.Execute("bounded", nanoflare.DBQueryRequest{Method: "first", FirstColumn: "answer", Statements: []nanoflare.DBStatementRequest{{SQL: `SELECT 42 AS answer`}}})
	if err != nil || response.First != int64(42) {
		t.Fatalf("connection failed after interruption: %#v %v", response, err)
	}
}

func TestSQLiteResultAndAdmissionBounds(t *testing.T) {
	manager, err := NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10001) SELECT x FROM n`,
		`SELECT hex(zeroblob(2200000)) AS oversized`,
	} {
		_, err := manager.Execute("bounded", nanoflare.DBQueryRequest{Method: "all", Statements: []nanoflare.DBStatementRequest{{SQL: query}}})
		if !errors.Is(err, nanoflare.ErrSQLPolicy) {
			t.Fatalf("result limit: %v", err)
		}
	}
	// Aggregate limits span the entire batch, and the surrounding transaction rolls back.
	_, err = manager.Execute("bounded", nanoflare.DBQueryRequest{Method: "exec", Statements: []nanoflare.DBStatementRequest{{SQL: `CREATE TABLE batch_guard(x)`}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Execute("bounded", nanoflare.DBQueryRequest{Method: "batch", Statements: []nanoflare.DBStatementRequest{
		{SQL: `INSERT INTO batch_guard VALUES(1)`},
		{SQL: `SELECT hex(zeroblob(1100000)) AS a`},
		{SQL: `SELECT hex(zeroblob(1100000)) AS b`},
	}})
	if !errors.Is(err, nanoflare.ErrSQLPolicy) {
		t.Fatalf("batch aggregate limit: %v", err)
	}
	response, err := manager.Execute("bounded", nanoflare.DBQueryRequest{Method: "first", FirstColumn: "n", Statements: []nanoflare.DBStatementRequest{{SQL: `SELECT count(*) AS n FROM batch_guard`}}})
	if err != nil || response.First != int64(0) {
		t.Fatalf("failed batch committed: %#v %v", response, err)
	}
	for range cap(manager.queries) {
		manager.queries <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = manager.ExecuteContext(ctx, "bounded", nanoflare.DBQueryRequest{Statements: []nanoflare.DBStatementRequest{{SQL: `SELECT 1`}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queue failed to observe context: %v", err)
	}
	for range cap(manager.queries) {
		<-manager.queries
	}
}
